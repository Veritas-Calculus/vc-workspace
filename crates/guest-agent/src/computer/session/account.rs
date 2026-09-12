//! Root ownership records prevent adopting an unrelated pre-existing account.
use super::linux::{
    initialize, initialize_control, require_root, root_directory, root_file, trusted_root_path,
    user_uid, BASE,
};
use super::*;
use rustix::fs::{flock, FlockOperation, Mode, OFlags};
use std::os::unix::fs::{FileTypeExt, MetadataExt, OpenOptionsExt, PermissionsExt};

mod lease;
mod native;
pub(crate) use lease::birth::{
    configured as login_birth_configured, login_client, register as register_login, PAM_PREFIX,
};
pub(crate) use lease::reconcile_expired;
pub(super) use lease::{bind_authority, validate_action_account};
pub(crate) use native::{
    configured as native_login_configured, policy as native_pam_policy,
    register as native_pam_register, run as native_run,
};

#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Account {
    username: String,
    uid: u32,
}

fn local_passwd(username: &str) -> io::Result<Option<Vec<String>>> {
    let result = Command::new("/usr/bin/getent")
        .args(["-s", "files", "passwd", username])
        .output()?;
    if result.status.code() == Some(2) {
        return Ok(None);
    }
    let raw = String::from_utf8(result.stdout).map_err(io::Error::other)?;
    let parts: Vec<String> = raw.trim().split(':').map(str::to_owned).collect();
    if !result.status.success() || parts.len() != 7 || parts[0] != username {
        return Err(denied("local account lookup failed"));
    }
    Ok(Some(parts))
}

fn owned_account(username: &str, directory: &Path) -> io::Result<Option<Account>> {
    let bound = read_account(username, directory)?;
    let Some(parts) = local_passwd(username)? else {
        if bound.is_some() {
            return Err(denied(
                "bound account was deleted; automatic re-adoption is forbidden",
            ));
        }
        return Ok(None);
    };
    let uid = user_uid(username)?;
    if let Some(account) = bound {
        if account.username == username && account.uid == uid {
            return Ok(Some(account));
        }
        return Err(denied("managed account UID has changed"));
    }
    // Recover only our own interrupted useradd. A root-owned random GECOS
    // marker is written before useradd and compared before recording the UID.
    let pending = private_record(&directory.join("account.pending"))
        .map_err(|_| denied("existing account is not platform managed"))?;
    let expected = String::from_utf8(pending).map_err(io::Error::other)?;
    if parts[4] != expected || parts[5] != format!("/home/{username}") {
        return Err(denied("existing account is not platform managed"));
    }
    let account = Account {
        username: username.into(),
        uid,
    };
    persist_record(
        directory,
        "account.json",
        &serde_json::to_vec(&account).map_err(io::Error::other)?,
    )?;
    fs::remove_file(directory.join("account.pending"))?;
    fs::File::open(directory)?.sync_all()?;
    Ok(Some(account))
}

fn private_record(path: &Path) -> io::Result<Vec<u8>> {
    account_record(path, false)
}

fn account_record(path: &Path, readable: bool) -> io::Result<Vec<u8>> {
    trusted_root_path(
        path.parent()
            .ok_or_else(|| denied("invalid account path"))?,
    )?;
    let file = fs::File::from(rustix::fs::open(
        path,
        OFlags::RDONLY | OFlags::NOFOLLOW | OFlags::NONBLOCK | OFlags::CLOEXEC,
        Mode::empty(),
    )?);
    let meta = file.metadata()?;
    if !meta.is_file()
        || meta.uid() != 0
        || meta.nlink() != 1
        || !(meta.mode() & 0o777 == 0o600 || (readable && meta.mode() & 0o777 == 0o644))
        || meta.len() > 4096
    {
        return Err(denied("unsafe account record"));
    }
    let mut raw = Vec::new();
    file.take(4097).read_to_end(&mut raw)?;
    if raw.len() > 4096 {
        return Err(denied("oversized account record"));
    }
    Ok(raw)
}

fn read_account(username: &str, directory: &Path) -> io::Result<Option<Account>> {
    let raw = match private_record(&directory.join("account.json")) {
        Ok(raw) => raw,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error),
    };
    let account: Account =
        serde_json::from_slice(&raw).map_err(|_| denied("invalid account ownership record"))?;
    if account.username != username || account.uid < 1000 {
        return Err(denied("invalid account ownership identity"));
    }
    Ok(Some(account))
}

// Never unlink or replace this inode. The kernel releases the gate on process
// exit; abandoned intent is recovered by the local reconciler, not replayed.
fn gate(directory: &Path) -> io::Result<fs::File> {
    trusted_root_path(directory)?;
    let file = fs::File::from(rustix::fs::open(
        directory.join("account.lock"),
        OFlags::RDWR | OFlags::CREATE | OFlags::NOFOLLOW | OFlags::NONBLOCK | OFlags::CLOEXEC,
        Mode::RUSR | Mode::WUSR,
    )?);
    let meta = file.metadata()?;
    if !meta.is_file() || meta.uid() != 0 || meta.nlink() != 1 || meta.mode() & 0o777 != 0o600 {
        return Err(denied("unsafe account gate"));
    }
    flock(&file, FlockOperation::NonBlockingLockExclusive)?;
    // No writer sharing this gate can still own these exact non-secret
    // temporary snapshots. Never follow links or sweep other namespaces.
    let mut removed = false;
    for (index, entry) in fs::read_dir(directory)?.enumerate() {
        if index >= 1024 {
            return Err(denied("account directory limit exceeded"));
        }
        let entry = entry?;
        let name = entry.file_name();
        let Some(nonce) = name
            .to_str()
            .and_then(|n| n.strip_prefix(".account-"))
            .and_then(|n| n.strip_suffix(".tmp"))
        else {
            continue;
        };
        if nonce.len() != 32
            || !nonce
                .bytes()
                .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
        {
            continue;
        }
        account_record(&entry.path(), true)?;
        fs::remove_file(entry.path())?;
        removed = true;
    }
    if removed {
        fs::File::open(directory)?.sync_all()?;
    }
    Ok(file)
}

fn persist_record(directory: &Path, name: &str, raw: &[u8]) -> io::Result<()> {
    persist_account_record(directory, name, raw, false)
}

// Only the typed, non-secret lifecycle is readable by the unprivileged Helper.
// Ownership, pending provision markers and the account gate remain root-only.
fn persist_account_record(
    directory: &Path,
    name: &str,
    raw: &[u8],
    readable: bool,
) -> io::Result<()> {
    if raw.is_empty() || raw.len() > 4096 {
        return Err(denied("invalid account record length"));
    }
    match account_record(&directory.join(name), readable) {
        Ok(_) => (),
        Err(error) if error.kind() == io::ErrorKind::NotFound => (),
        Err(error) => return Err(error),
    }
    let mut nonce = [0u8; 16];
    getrandom::fill(&mut nonce).map_err(io::Error::other)?;
    let label: String = nonce.iter().map(|b| format!("{b:02x}")).collect();
    let path = directory.join(format!(".account-{label}.tmp"));
    let mut file = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(&path)?;
    let result = (|| {
        file.write_all(raw)?;
        if readable {
            file.set_permissions(fs::Permissions::from_mode(0o644))?;
        }
        file.sync_all()?;
        fs::rename(&path, directory.join(name))?;
        fs::File::open(directory)?.sync_all()
    })();
    let _ = fs::remove_file(path);
    result
}

// Credential writers inherit a duplicate of the locked open-file description.
// If their dispatcher dies, its already-running OS writer still owns the gate
// until that writer exits. Never pass this descriptor to an unprivileged user
// session or to long-lived Helpers. The dispatcher is single-threaded here.
fn checked_status(guard: &fs::File, program: &str, args: &[&str]) -> io::Result<()> {
    let result = root_account_command(guard, program, args)?;
    if !result.success() {
        return Err(io::Error::other("managed account command failed"));
    }
    Ok(())
}

fn inherited_gate(guard: &fs::File) -> io::Result<rustix::fd::OwnedFd> {
    let inherited = rustix::io::fcntl_dupfd_cloexec(guard, 3)?;
    rustix::io::fcntl_setfd(&inherited, rustix::io::FdFlags::empty())?;
    Ok(inherited)
}

fn root_account_command(
    guard: &fs::File,
    program: &str,
    args: &[&str],
) -> io::Result<std::process::ExitStatus> {
    let inherited = inherited_gate(guard)?;
    let result = Command::new(program)
        .args(args)
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()?;
    drop(inherited);
    Ok(result)
}

pub(super) fn run(command: &str, username: &str) -> io::Result<()> {
    require_root()?;
    let native = command == "computer-v2-native-account-provision";
    if !managed_name(username) || !username.starts_with(if native { "vcw" } else { "vca" }) {
        return Err(denied("Agent managed account required"));
    }
    if command == "computer-v2-account-lease" {
        return lease::apply_stdin(username, false);
    }
    if command == "computer-v2-account-login" {
        return lease::apply_stdin(username, true);
    }
    if command == "computer-v2-account-inspect" {
        return lease::inspect(username);
    }
    initialize_control()?;
    let directory = Path::new(BASE).join("users").join(username);
    root_directory(
        directory
            .to_str()
            .ok_or_else(|| denied("invalid managed directory"))?,
        0o755,
    )?;
    let guard = gate(&directory)?;
    if matches!(
        command,
        "computer-v2-account-enable" | "computer-v2-account-disable"
    ) {
        lease::reject_legacy(&directory)?;
    }
    let mut account = owned_account(username, &directory)?;
    match command {
        "computer-v2-account-provision" | "computer-v2-native-account-provision" => {
            if account.is_none() {
                let home = Path::new("/home").join(username);
                trusted_root_path(Path::new("/home"))?;
                if fs::symlink_metadata(&home).is_ok() {
                    return Err(denied("existing home is not platform managed"));
                }
                let marker = directory.join("account.pending");
                if !marker.exists() {
                    let mut nonce = [0u8; 16];
                    getrandom::fill(&mut nonce).map_err(io::Error::other)?;
                    let label: String = nonce.iter().map(|b| format!("{b:02x}")).collect();
                    let mut file = fs::OpenOptions::new()
                        .write(true)
                        .create_new(true)
                        .mode(0o600)
                        .open(&marker)?;
                    write!(
                        file,
                        "VCWorkspace-{}-{label}",
                        if native { "Native" } else { "Agent" }
                    )?;
                    file.sync_all()?;
                    fs::File::open(&directory)?.sync_all()?;
                }
                let marker =
                    String::from_utf8(root_file(&marker, 4096)?).map_err(io::Error::other)?;
                checked_status(
                    &guard,
                    "/usr/sbin/useradd",
                    &[
                        "--create-home",
                        "--shell",
                        "/bin/bash",
                        "--expiredate",
                        "1",
                        "--comment",
                        &marker,
                        username,
                    ],
                )?;
                account = owned_account(username, &directory)?;
            }
            let account = account.ok_or_else(|| denied("managed account not found"))?;
            if native
                && fs::symlink_metadata("/dev/dri/renderD128")
                    .is_ok_and(|m| m.file_type().is_char_device())
            {
                let group = Command::new("/usr/bin/getent")
                    .args(["-s", "files", "group", "render"])
                    .stdout(Stdio::null())
                    .stderr(Stdio::null())
                    .status()?;
                match group.code() {
                    Some(0) => {
                        if !lease::verify_identity(&account)? {
                            return Err(denied("bound Native account disappeared"));
                        }
                        // Preserve Native render-node access without granting
                        // display-master/video, TLS-key or administrator access.
                        checked_status(
                            &guard,
                            "/usr/sbin/usermod",
                            &["--append", "--groups", "render", username],
                        )?;
                    }
                    Some(2) => (),
                    _ => return Err(denied("Native render group lookup failed")),
                }
            }
            let home = Path::new("/home").join(username);
            let meta = fs::symlink_metadata(&home)?;
            if !meta.is_dir() || meta.uid() != account.uid {
                return Err(denied("managed home ownership mismatch"));
            }
            fs::set_permissions(home, fs::Permissions::from_mode(0o700))?;
            initialize(username)?;
            // All writes below happen as the target UID. A user-created link
            // in its Home can never make this root dispatcher overwrite files.
            let script = format!(
                r#"set -eu
umask 077
mkdir -p .config/autostart
printf 'exec dbus-run-session -- startxfce4\n' >.xsession
printf '[Desktop Entry]\nType=Application\nName=VC Workspace Agent Session\nExec=/usr/local/sbin/vc-workspace-guest-agent computer-v2-helper --guest-user {username}\nOnlyShowIn=XFCE;\nNoDisplay=true\nX-GNOME-Autostart-enabled=true\n' >.config/autostart/vc-workspace-computer.desktop
"#
            );
            let status = Command::new("/usr/sbin/runuser")
                .args(["-u", username, "--", "/bin/sh", "-c", &script])
                .current_dir(format!("/home/{username}"))
                .stdout(Stdio::null())
                .stderr(Stdio::null())
                .status()?;
            if !status.success() {
                return Err(io::Error::other("managed session initialization failed"));
            }
            Ok(())
        }
        "computer-v2-account-enable" => {
            account.ok_or_else(|| denied("managed account not found"))?;
            checked_status(
                &guard,
                "/usr/sbin/usermod",
                &["--unlock", "--expiredate", "", username],
            )
        }
        "computer-v2-account-disable" => {
            let Some(account) = account else {
                return Ok(());
            };
            checked_status(
                &guard,
                "/usr/sbin/usermod",
                &["--lock", "--expiredate", "1", username],
            )?;
            let _ = Command::new("/usr/bin/loginctl")
                .args(["terminate-user", username])
                .stdout(Stdio::null())
                .stderr(Stdio::null())
                .status();
            let uid = account.uid.to_string();
            let killed = Command::new("/usr/bin/pkill")
                .args(["-KILL", "-u", &uid])
                .stdout(Stdio::null())
                .stderr(Stdio::null())
                .status()?;
            if !matches!(killed.code(), Some(0 | 1)) {
                return Err(io::Error::other("managed process termination failed"));
            }
            for _ in 0..20 {
                let status = Command::new("/usr/bin/pgrep")
                    .args(["-u", &uid])
                    .stdout(Stdio::null())
                    .stderr(Stdio::null())
                    .status()?;
                if status.code() == Some(1) {
                    return Ok(());
                }
                thread::sleep(Duration::from_millis(50));
            }
            Err(io::Error::other("managed user processes still exist"))
        }
        _ => Err(denied("unsupported Agent account command")),
    }
}
