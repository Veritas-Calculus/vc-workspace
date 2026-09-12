//! Local account write fence. Shadow's expiry is day-granular; the root Agent
//! reaper separately closes precise lease deadlines and interrupted intents.
use super::*;
use vc_workspace_guest_lifecycle::{
    plan, AccountIdentity, Fence, Operation, Phase, Request, Zeroizing,
};

pub(super) mod birth;
mod display;

const SNAPSHOT: &str = "account-lifecycle.json";

pub(super) fn identity(account: &Account) -> AccountIdentity {
    AccountIdentity {
        username: account.username.clone(),
        uid: account.uid,
        sid: String::new(),
    }
}

fn read_fence(directory: &Path, account: &Account) -> io::Result<Option<Fence>> {
    match account_record(&directory.join(SNAPSHOT), true) {
        Ok(raw) => Fence::parse(&raw, &identity(account)).map(Some),
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(None),
        Err(error) => Err(error),
    }
}

pub(super) fn reject_legacy(directory: &Path) -> io::Result<()> {
    match fs::symlink_metadata(directory.join(SNAPSHOT)) {
        Ok(_) => Err(denied(
            "versioned account requires a bound lifecycle request",
        )),
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(()),
        Err(error) => Err(error),
    }
}

fn persist(directory: &Path, fence: &Fence) -> io::Result<()> {
    fence.validate()?;
    persist_account_record(
        directory,
        SNAPSHOT,
        &serde_json::to_vec(fence).map_err(io::Error::other)?,
        true,
    )
}

fn read_action_fence(key: &SessionKey) -> io::Result<Fence> {
    let raw = account_record(
        &Path::new(BASE)
            .join("users")
            .join(&key.username)
            .join(SNAPSHOT),
        true,
    )?;
    Fence::parse(
        &raw,
        &AccountIdentity {
            username: key.username.clone(),
            uid: key.uid,
            sid: key.sid.clone(),
        },
    )
}

fn matches_authority(fence: &Fence, authority: &Authority, generation: Option<u64>) -> bool {
    fence.phase == Phase::Sealed
        && fence.login_live(now())
        && generation == Some(fence.login_generation)
        && fence.lease_id == authority.lease_id
        && fence.control_epoch == authority.control_epoch as u64
        && fence.expires_unix_seconds == (authority.expires_unix_ms / 1000) as u64
}

pub(crate) fn validate_action_account(
    key: &SessionKey,
    authority: &Authority,
    generation: Option<u64>,
) -> io::Result<()> {
    if !key.username.starts_with("vca") {
        return if generation.is_none() {
            Ok(())
        } else {
            Err(denied("unexpected Agent login version"))
        };
    }
    if !matches_authority(&read_action_fence(key)?, authority, generation) {
        return Err(denied("Agent account login authority changed"));
    }
    Ok(())
}

// Hold the root-only account gate until the authority snapshot is committed.
// Input workers independently re-read this same journal before each operation
// and input chunk; pending/revoked writes therefore deny old grants immediately
// on their next validation, even when account mutation is interrupted.
pub(crate) fn bind_authority(
    key: &SessionKey,
    authority: &Authority,
) -> io::Result<Option<(fs::File, u64)>> {
    if !key.username.starts_with("vca") {
        return Ok(None);
    }
    let (directory, guard, account) = bound(&key.username)?;
    if account.uid != key.uid || !key.sid.is_empty() {
        return Err(denied("Agent identity changed"));
    }
    let fence = read_fence(&directory, &account)?
        .ok_or_else(|| denied("versioned Agent login required"))?;
    // Pre-upgrade private journals cannot silently authorize a Helper which
    // cannot read them. Their migration path is explicit revoke/new login.
    let meta = fs::symlink_metadata(directory.join(SNAPSHOT))?;
    if meta.mode() & 0o777 != 0o644
        || !matches_authority(&fence, authority, Some(fence.login_generation))
    {
        return Err(denied("Agent account is not ready for input authority"));
    }
    live_state(&account, &fence)?;
    Ok(Some((guard, fence.login_generation)))
}

// A deleted bound username is not permission to kill a newly assigned UID.
// Only the files backend is considered; directory identities are never adopted.
pub(super) fn verify_identity(account: &Account) -> io::Result<bool> {
    let output = Command::new("/usr/bin/getent")
        .args(["-s", "files", "passwd"])
        .output()?;
    if !output.status.success() {
        return Err(denied("local identity enumeration failed"));
    }
    let raw =
        String::from_utf8(output.stdout).map_err(|_| denied("invalid local identity encoding"))?;
    let mut found = false;
    for line in raw.lines() {
        let fields: Vec<_> = line.split(':').collect();
        if fields.len() != 7 {
            return Err(denied("invalid local identity record"));
        }
        let uid: u32 = fields[2].parse().map_err(|_| denied("invalid local UID"))?;
        if fields[0] == account.username {
            if found || uid != account.uid || fields[5] != format!("/home/{}", account.username) {
                return Err(denied("bound local identity changed"));
            }
            found = true;
        } else if uid == account.uid {
            return Err(denied("bound UID was reassigned or shared"));
        }
    }
    Ok(found)
}

#[derive(Serialize)]
pub(super) struct LoginState {
    pub(super) exists: bool,
    pub(super) disabled: bool,
    pub(super) expiry_day: Option<u64>,
}

pub(super) fn login_state(account: &Account) -> io::Result<LoginState> {
    if !verify_identity(account)? {
        return Ok(LoginState {
            exists: false,
            disabled: true,
            expiry_day: None,
        });
    }
    let result = Command::new("/usr/bin/getent")
        .args(["-s", "files", "shadow", &account.username])
        .output()?;
    let raw = Zeroizing::new(result.stdout);
    let text = std::str::from_utf8(&raw).map_err(|_| denied("invalid shadow encoding"))?;
    let fields: Vec<_> = text.trim_end().split(':').collect();
    if !result.status.success() || fields.len() != 9 || fields[0] != account.username {
        return Err(denied("local shadow lookup failed"));
    }
    let expiry_day = if fields[7].is_empty() {
        None
    } else {
        Some(
            fields[7]
                .parse::<u64>()
                .map_err(|_| denied("invalid shadow expiry"))?,
        )
    };
    let disabled = fields[1].is_empty()
        || fields[1].starts_with(['!', '*'])
        || expiry_day.is_some_and(|day| day <= now() / 86400);
    Ok(LoginState {
        exists: true,
        disabled,
        expiry_day,
    })
}

pub(super) fn now() -> u64 {
    unix_millis().max(0) as u64 / 1000
}

fn shadow_day(fence: &Fence) -> u64 {
    fence.expires_unix_seconds.div_ceil(86400)
}

fn live_state(account: &Account, fence: &Fence) -> io::Result<()> {
    let state = login_state(account)?;
    if !state.exists
        || state.disabled
        || state.expiry_day != Some(shadow_day(fence))
        || !fence.login_live(now())
    {
        return Err(denied("account login window did not converge"));
    }
    Ok(())
}

pub(super) fn disable_login(account: &Account, guard: &fs::File) -> io::Result<()> {
    if verify_identity(account)? {
        checked_status(
            guard,
            "/usr/sbin/usermod",
            &["--lock", "--expiredate", "1", &account.username],
        )?;
    }
    if !login_state(account)?.disabled {
        return Err(denied("account login did not close"));
    }
    Ok(())
}

pub(super) fn processes_absent(account: &Account) -> io::Result<bool> {
    verify_identity(account)?;
    let status = Command::new("/usr/bin/pgrep")
        .args(["-u", &account.uid.to_string()])
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()?;
    match status.code() {
        Some(0) => Ok(false),
        Some(1) => Ok(true),
        _ => Err(denied("account process observation failed")),
    }
}

pub(super) fn disable(account: &Account, guard: &fs::File) -> io::Result<()> {
    disable_login(account, guard)?;
    birth::drain(
        &Path::new(BASE).join("users").join(&account.username),
        account,
    )?;
    verify_identity(account)?;
    // Do not queue an asynchronous logind terminate-user job which could act
    // after a later login. This synchronous UID writer retains our gate even
    // if its dispatcher dies; observations below and the sweep check closure.
    let status = root_account_command(
        guard,
        "/usr/bin/pkill",
        &["-KILL", "-u", &account.uid.to_string()],
    )?;
    if !matches!(status.code(), Some(0 | 1)) {
        return Err(denied("account process termination failed"));
    }
    // logind's default user-stop delay is 10 seconds. Wait within the bounded
    // caller budget; an outstanding job remains unacknowledged and the timer
    // retries observation. UID absence alone is not a completed cleanup.
    let deadline = Instant::now() + Duration::from_secs(15);
    loop {
        if processes_absent(account)?
            && birth::absent(
                &Path::new(BASE).join("users").join(&account.username),
                account,
            )?
        {
            display::cleanup(account.uid)?;
            return Ok(());
        }
        if Instant::now() >= deadline {
            break;
        }
        thread::sleep(Duration::from_millis(100));
    }
    Err(denied(
        "account process domain or user-manager jobs remain; revocation is not acknowledged",
    ))
}

pub(super) fn set_password(account: &Account, guard: &fs::File, password: &str) -> io::Result<()> {
    if !verify_identity(account)? {
        return Err(denied("bound account was deleted"));
    }
    let inherited = inherited_gate(guard)?;
    let mut child = Command::new("/usr/sbin/chpasswd")
        .stdin(Stdio::piped())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()?;
    drop(inherited);
    // Fixed username, bounded printable password, no argv/environment/file.
    let input = Zeroizing::new(format!("{}:{password}\n", account.username));
    let written = child
        .stdin
        .take()
        .ok_or_else(|| denied("password input unavailable"))?
        .write_all(input.as_bytes());
    let status = child.wait()?;
    written?;
    if !status.success() {
        return Err(denied("account credential mutation failed"));
    }
    Ok(())
}

pub(super) fn retire(account: &Account, guard: &fs::File) -> io::Result<()> {
    let mut nonce = Zeroizing::new([0u8; 32]);
    getrandom::fill(nonce.as_mut()).map_err(io::Error::other)?;
    let mut secret = Zeroizing::new(String::from("Vcw1!"));
    use std::fmt::Write;
    for byte in nonce.iter() {
        write!(secret, "{byte:02x}").map_err(io::Error::other)?;
    }
    set_password(account, guard, &secret)
}

fn bound(username: &str) -> io::Result<(PathBuf, fs::File, Account)> {
    require_root()?;
    if !managed_name(username) || !username.starts_with("vca") {
        return Err(denied("Agent account required"));
    }
    let directory = Path::new(BASE).join("users").join(username);
    let guard = gate(&directory)?;
    let account =
        read_account(username, &directory)?.ok_or_else(|| denied("pre-bound account required"))?;
    verify_identity(&account)?;
    Ok((directory, guard, account))
}

pub(super) fn apply_stdin(username: &str, login: bool) -> io::Result<()> {
    let mut raw = Zeroizing::new(Vec::new());
    io::stdin()
        .take(vc_workspace_guest_lifecycle::MAX_BYTES as u64 + 1)
        .read_to_end(&mut raw)?;
    let request = Request::parse(&raw)?;
    let fence = apply_inner(username, &request, login, || {})?;
    println!("{}", qga_json(&fence)?);
    Ok(())
}

// Static checkpoints are available to opt-in test binaries only. The release
// CLI has no fault-injection flag, caller path, or configurable OS executable.
fn apply_inner(
    username: &str,
    request: &Request,
    login: bool,
    mut checkpoint: impl FnMut(),
) -> io::Result<Fence> {
    if login && request.operation != Operation::Open {
        return Err(denied("login requires a fresh open intent"));
    }
    let (directory, guard, account) = bound(username)?;
    let mut birth_guard = Some(birth::lock(&directory)?);
    if login && !birth::configured() {
        return Err(denied(
            "PAM login birth protection must be installed before login",
        ));
    }
    if request.identity != identity(&account) {
        return Err(denied("account request identity mismatch"));
    }
    let previous = read_fence(&directory, &account)?;
    let mut next = plan(previous.as_ref(), request, now())?;
    if login {
        next.completed.phase = Phase::Sealed;
    }
    if request.operation == Operation::Seal {
        live_state(
            &account,
            previous
                .as_ref()
                .ok_or_else(|| denied("missing login intent"))?,
        )?;
    } else if request.operation == Operation::Open && !verify_identity(&account)? {
        return Err(denied("bound account was deleted"));
    }
    if next.observe_only {
        return Ok(next.completed);
    }
    persist(&directory, &next.pending)?;
    checkpoint();
    let result = (|| {
        match request.operation {
            Operation::Open => {
                disable(&account, &guard)?;
                set_password(
                    &account,
                    &guard,
                    request
                        .password
                        .as_ref()
                        .ok_or_else(|| denied("missing login credential"))?
                        .expose(),
                )?;
                if !verify_identity(&account)? {
                    return Err(denied("bound account was deleted"));
                }
                checked_status(
                    &guard,
                    "/usr/sbin/usermod",
                    &[
                        "--unlock",
                        "--expiredate",
                        &shadow_day(&next.completed).to_string(),
                        username,
                    ],
                )?;
                if login {
                    live_state(&account, &next.completed)?;
                    // PAM registration takes only the birth gate, never the
                    // account gate held by this dispatcher. Freeze it again
                    // before retiring credentials and publishing sealed.
                    drop(birth_guard.take());
                    start_xorg(&account, &guard, request)?;
                    birth_guard = Some(birth::lock(&directory)?);
                    birth::one_live(&directory, &account, &next.completed)?;
                    retire(&account, &guard)?;
                }
            }
            Operation::Seal => retire(&account, &guard)?,
            Operation::Revoke => disable(&account, &guard)?,
        }
        checkpoint();
        if request.operation != Operation::Revoke {
            live_state(&account, &next.completed)?;
        }
        persist(&directory, &next.completed)?;
        Ok(next.completed)
    })();
    if result.is_err() {
        if birth_guard.is_none() {
            birth_guard = birth::lock(&directory).ok();
        }
        if birth_guard.is_some() {
            let _ = persist(&directory, &next.pending.revoked());
            let _ = disable(&account, &guard);
        }
    }
    result
}

fn start_xorg(account: &Account, guard: &fs::File, request: &Request) -> io::Result<()> {
    if !verify_identity(account)? {
        return Err(denied("bound account was deleted"));
    }
    let password = request
        .password
        .as_ref()
        .ok_or_else(|| denied("missing login credential"))?;
    let inherited = inherited_gate(guard)?;
    let mut child = Command::new(env::current_exe()?)
        .args([
            "computer-v2-login-client",
            "--guest-user",
            &account.username,
            "--parent-pid",
            &std::process::id().to_string(),
        ])
        .stdin(Stdio::piped())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()?;
    drop(inherited);
    let input = Zeroizing::new(format!("{}\n", password.expose()));
    let written = child
        .stdin
        .take()
        .ok_or_else(|| denied("login input unavailable"))?
        .write_all(input.as_bytes());
    if written.is_err() {
        let _ = child.kill();
        let _ = child.wait();
        return Err(denied("local login input failed"));
    }
    let deadline = Instant::now() + Duration::from_secs(20);
    loop {
        match child.try_wait() {
            Ok(Some(status)) if status.success() => return Ok(()),
            Ok(Some(_)) => return Err(denied("local Xorg login failed")),
            Ok(None) if Instant::now() < deadline && now() < request.expires_unix_seconds => (),
            _ => {
                let _ = child.kill();
                let _ = child.wait();
                // A timeout cannot prove sesman did not accept authentication.
                // Pending/revoked state and the local sweep retain cleanup.
                return Err(denied("local Xorg login deadline"));
            }
        }
        thread::sleep(Duration::from_millis(25));
    }
}

pub(super) fn inspect(username: &str) -> io::Result<()> {
    require_root()?;
    if !managed_name(username) || !username.starts_with("vca") {
        return Err(denied("Agent account required"));
    }
    initialize_control()?;
    let directory = Path::new(BASE).join("users").join(username);
    root_directory(
        directory
            .to_str()
            .ok_or_else(|| denied("invalid account directory"))?,
        0o755,
    )?;
    let _guard = gate(&directory)?;
    let _birth = birth::lock(&directory)?;
    if read_account(username, &directory)?.is_none() {
        reject_legacy(&directory)?;
        if local_passwd(username)?.is_some() {
            return Err(denied("existing account is not platform managed"));
        }
        println!(
            "{}",
            qga_json(
                &serde_json::json!({"schema_version":1,"username":username,"identity":null,
            "account":{"exists":false,"disabled":true,"expiry_day":null},"processes_absent":null,"login_writers_absent":null,"lifecycle":null})
            )?
        );
        return Ok(());
    }
    let account =
        read_account(username, &directory)?.ok_or_else(|| denied("account disappeared"))?;
    verify_identity(&account)?;
    let lifecycle = read_fence(&directory, &account)?;
    let state = login_state(&account)?;
    let absent = processes_absent(&account)?;
    println!(
        "{}",
        qga_json(
            &serde_json::json!({"schema_version":1,"username":username,"identity":identity(&account),
        "account":state,"processes_absent":absent,"login_writers_absent":birth::absent(&directory, &account)?,"lifecycle":lifecycle})
        )?
    );
    Ok(())
}

fn reconcile(username: &str) -> io::Result<()> {
    let (directory, guard, account) = bound(username)?;
    let _birth = birth::lock(&directory)?;
    let fence = match read_fence(&directory, &account) {
        Ok(Some(fence)) => fence,
        Ok(None) => return Ok(()),
        Err(error) => {
            // Keep corrupt evidence. Only a validated immutable local identity
            // may be closed; never invent an epoch or success receipt.
            let _ = disable(&account, &guard);
            return Err(error);
        }
    };
    if live_state(&account, &fence).is_ok() {
        return Ok(());
    }
    persist(&directory, &fence.revoked())?;
    disable(&account, &guard)
}

pub(crate) fn reconcile_expired() -> io::Result<()> {
    require_root()?;
    let users = Path::new(BASE).join("users");
    match fs::symlink_metadata(&users) {
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(()),
        Err(error) => return Err(error),
        Ok(_) => (),
    }
    trusted_root_path(&users)?;
    let mut failed = false;
    for (index, entry) in fs::read_dir(users)?.enumerate() {
        if index >= 1024 {
            return Err(denied("account sweep limit exceeded"));
        }
        let entry = entry?;
        let name = entry.file_name();
        let Some(name) = name.to_str() else { continue };
        if !managed_name(name) {
            continue;
        }
        if name.starts_with("vcw") {
            if super::native::reconcile(name).is_err() {
                failed = true;
            }
            continue;
        }
        // Unmanaged Helpers have no lifecycle record and are never touched.
        match fs::symlink_metadata(entry.path().join(SNAPSHOT)) {
            Err(error) if error.kind() == io::ErrorKind::NotFound => continue,
            Err(_) => {
                failed = true;
                continue;
            }
            Ok(_) => (),
        }
        if reconcile(name).is_err() {
            failed = true;
        }
    }
    if failed {
        Err(denied("one or more account fences could not be reconciled"))
    } else {
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn input_authority_requires_exact_sealed_login_generation() {
        let mut fence = Fence {
            schema_version: 1,
            identity: AccountIdentity {
                username: "vca1234567890ab".into(),
                uid: 1001,
                sid: String::new(),
            },
            lease_id: "lease_input_bound".into(),
            control_epoch: 7,
            login_generation: 2,
            expires_unix_seconds: now() + 60,
            phase: Phase::Sealed,
        };
        let authority: Authority = serde_json::from_value(serde_json::json!({"schema_version":1,
            "lease_id":fence.lease_id,"control_epoch":7,"state":"active",
            "expires_unix_ms":fence.expires_unix_seconds*1000+999}))
        .unwrap();
        assert!(matches_authority(&fence, &authority, Some(2)));
        for generation in [None, Some(0), Some(1), Some(3)] {
            assert!(!matches_authority(&fence, &authority, generation));
        }
        for phase in [Phase::Opening, Phase::Open, Phase::Sealing, Phase::Revoked] {
            fence.phase = phase;
            assert!(!matches_authority(&fence, &authority, Some(2)));
        }
        fence.phase = Phase::Sealed;
        fence.control_epoch += 1;
        assert!(!matches_authority(&fence, &authority, Some(2)));
        fence.control_epoch -= 1;
        fence.lease_id.push('x');
        assert!(!matches_authority(&fence, &authority, Some(2)));
        fence.lease_id.pop();
        fence.expires_unix_seconds += 1;
        assert!(!matches_authority(&fence, &authority, Some(2)));
        fence.expires_unix_seconds = now();
        assert!(!matches_authority(&fence, &authority, Some(2)));
    }

    #[test]
    #[ignore = "test-only child process; disposable Linux account fixture required"]
    fn account_lease_crash_child() {
        assert_eq!(env::var("VC_WORKSPACE_DISPOSABLE_TEST").as_deref(), Ok("1"));
        assert!(Path::new("/.dockerenv").is_file());
        require_root().unwrap();
        let mut raw = Zeroizing::new(Vec::new());
        io::stdin().take(4097).read_to_end(&mut raw).unwrap();
        let request = Request::parse(&raw).unwrap();
        let stop: usize = env::var("VC_WORKSPACE_ACCOUNT_TEST_CHECKPOINT")
            .unwrap()
            .parse()
            .unwrap();
        assert!(matches!(stop, 1 | 2));
        let mut index = 0;
        apply_inner(&request.identity.username, &request, false, || {
            index += 1;
            if index == stop {
                std::process::exit(86);
            }
        })
        .unwrap();
        panic!("crash checkpoint was not reached");
    }
}
