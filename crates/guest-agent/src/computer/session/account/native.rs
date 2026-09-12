//! Native credentials share the OS account lock, but never reuse Agent lease
//! semantics: retiring a password must preserve the user's existing desktop.
use super::*;
use lease::{
    birth, disable, disable_login, identity, login_state, now, processes_absent, retire,
    set_password, verify_identity,
};
use vc_workspace_guest_lifecycle::{
    native::{self as protocol, Fence, Operation, Phase, Request},
    Zeroizing,
};

const SNAPSHOT: &str = "native-account-lifecycle.json";
pub(crate) const PAM_PREFIX: &str = "# VC Workspace Native login birth fence v1\nauth requisite /usr/local/lib/security/pam_vcworkspace_native.so\nsession requisite /usr/local/lib/security/pam_vcworkspace_native.so\n";

pub(crate) fn policy() -> &'static str {
    PAM_PREFIX
}
pub(crate) fn configured() -> bool {
    birth::configured()
        && root_file(Path::new("/etc/pam.d/xrdp-sesman"), 65536).is_ok_and(|raw| {
            raw.strip_prefix(birth::PAM_PREFIX.as_bytes())
                .is_some_and(|rest| rest.starts_with(PAM_PREFIX.as_bytes()))
        })
        && fs::symlink_metadata("/usr/local/lib/security/pam_vcworkspace_native.so")
            .is_ok_and(|m| m.is_file() && m.uid() == 0 && m.nlink() == 1 && m.mode() & 0o022 == 0)
}

pub(crate) fn register() -> io::Result<()> {
    birth::register_native()
}

fn read_fence(directory: &Path, account: &Account) -> io::Result<Option<Fence>> {
    match account_record(&directory.join(SNAPSHOT), true) {
        Ok(raw) => Fence::parse(&raw, &identity(account)).map(Some),
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(None),
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

pub(super) fn login_fence(directory: &Path, account: &Account) -> io::Result<Fence> {
    let fence = read_fence(directory, account)?
        .ok_or_else(|| denied("Native credential intent missing"))?;
    if fence.phase != Phase::Issued || !fence.retained(now()) {
        return Err(denied("Native credentials no longer permit login"));
    }
    Ok(fence)
}

fn bound(username: &str) -> io::Result<(PathBuf, fs::File, Account)> {
    require_root()?;
    if !managed_name(username) || !username.starts_with("vcw") {
        return Err(denied("Native account required"));
    }
    let directory = Path::new(BASE).join("users").join(username);
    let guard = gate(&directory)?;
    let account = read_account(username, &directory)?
        .ok_or_else(|| denied("pre-bound Native account required"))?;
    verify_identity(&account)?;
    // This namespace can never silently reinterpret an Agent lifecycle.
    lease::reject_legacy(&directory)?;
    Ok((directory, guard, account))
}

fn live_state(account: &Account, fence: &Fence) -> io::Result<()> {
    let state = login_state(account)?;
    if !fence.retained(now())
        || !state.exists
        || state.disabled
        || state.expiry_day != Some(fence.expires_unix_seconds.div_ceil(86400))
    {
        return Err(denied("Native account window did not converge"));
    }
    Ok(())
}

fn apply_inner(
    username: &str,
    request: &Request,
    enforce_birth: bool,
    mut checkpoint: impl FnMut(),
) -> io::Result<Fence> {
    let (directory, guard, account) = bound(username)?;
    let _birth = birth::lock(&directory)?;
    if request.identity != identity(&account) {
        return Err(denied("Native identity mismatch"));
    }
    if enforce_birth && request.operation != Operation::Revoke && !configured() {
        return Err(denied(
            "Native PAM login protection must be installed before credentials",
        ));
    }
    let previous = read_fence(&directory, &account)?;
    let next = protocol::plan(previous.as_ref(), request, now())?;
    if next.preserve_desktop {
        live_state(
            &account,
            previous
                .as_ref()
                .ok_or_else(|| denied("missing retained desktop window"))?,
        )?;
    }
    if next.observe_only {
        return Ok(next.completed);
    }
    persist(&directory, &next.pending)?;
    checkpoint();
    let result = (|| {
        match request.operation {
            Operation::Issue => {
                if next.preserve_desktop {
                    disable_login(&account, &guard)?;
                } else {
                    disable(&account, &guard)?;
                }
                set_password(
                    &account,
                    &guard,
                    request
                        .password
                        .as_ref()
                        .ok_or_else(|| denied("missing credential"))?
                        .expose(),
                )?;
                checked_status(
                    &guard,
                    "/usr/sbin/usermod",
                    &[
                        "--unlock",
                        "--expiredate",
                        &next
                            .completed
                            .expires_unix_seconds
                            .div_ceil(86400)
                            .to_string(),
                        username,
                    ],
                )?;
            }
            Operation::Retire => retire(&account, &guard)?,
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
        // Pending/failed writes cannot retain a login window. Local recovery
        // preserves the same revision and never generates another credential.
        let _ = persist(&directory, &next.pending.revoked());
        let _ = disable(&account, &guard);
    }
    result
}

pub(crate) fn run(command: &str, username: &str) -> io::Result<()> {
    if command == "computer-v2-native-account-provision" {
        return super::run(command, username);
    }
    if command == "computer-v2-native-account-inspect" {
        let (directory, _guard, account) = bound(username)?;
        let _birth = birth::lock(&directory)?;
        let writers = if configured() {
            Some(birth::absent(&directory, &account)?)
        } else {
            None
        };
        println!(
            "{}",
            qga_json(
                &serde_json::json!({"schema_version":1,"identity":identity(&account),"account":login_state(&account)?,
            "processes_absent":processes_absent(&account)?,"login_writers_absent":writers,"lifecycle":read_fence(&directory,&account)?})
            )?
        );
        return Ok(());
    }
    if command != "computer-v2-native-account-credential" {
        return Err(denied("unsupported Native account command"));
    }
    let mut raw = Zeroizing::new(Vec::new());
    io::stdin()
        .take(vc_workspace_guest_lifecycle::MAX_BYTES as u64 + 1)
        .read_to_end(&mut raw)?;
    let request = Request::parse(&raw)?;
    println!(
        "{}",
        qga_json(&apply_inner(username, &request, true, || {})?)?
    );
    Ok(())
}

pub(super) fn reconcile(username: &str) -> io::Result<()> {
    let path = Path::new(BASE).join("users").join(username).join(SNAPSHOT);
    match fs::symlink_metadata(path) {
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(()),
        Err(error) => return Err(error),
        Ok(_) => (),
    }
    let (directory, guard, account) = bound(username)?;
    let _birth = birth::lock(&directory)?;
    let fence = match read_fence(&directory, &account) {
        Ok(Some(fence)) => fence,
        Ok(None) => return Err(denied("Native fence disappeared")),
        Err(error) => {
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

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    #[ignore = "private Linux shadow contract/crash subprocess; not a PAM acceptance"]
    fn native_account_contract_child() {
        assert_eq!(env::var("VC_WORKSPACE_DISPOSABLE_TEST").as_deref(), Ok("1"));
        assert!(Path::new("/.dockerenv").is_file());
        require_root().unwrap();
        let mut raw = Zeroizing::new(Vec::new());
        io::stdin().take(4097).read_to_end(&mut raw).unwrap();
        let request = Request::parse(&raw).unwrap();
        let stop: usize = env::var("VC_WORKSPACE_NATIVE_TEST_CHECKPOINT")
            .unwrap()
            .parse()
            .unwrap();
        assert!(stop <= 2);
        let mut index = 0;
        let result = apply_inner(&request.identity.username, &request, false, || {
            index += 1;
            if index == stop {
                std::process::exit(86);
            }
        });
        match result {
            Ok(receipt) => println!("NATIVE_RECEIPT={}", qga_json(&receipt).unwrap()),
            Err(_) => std::process::exit(87),
        }
    }
}
