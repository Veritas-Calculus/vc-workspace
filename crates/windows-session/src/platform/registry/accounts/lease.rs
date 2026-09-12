//! Durable ordering at the actual SAM mutation boundary, not at the QGA caller.
use super::*;
use vc_workspace_guest_lifecycle::{plan, AccountIdentity, Fence, Operation, Request, Zeroizing};

const VALUE: &str = "Lifecycle";

fn account_identity(name: &str, sid: &str) -> AccountIdentity {
    AccountIdentity {
        username: name.into(),
        uid: 0,
        sid: sid.into(),
    }
}

pub(super) fn read_fence(key: &Key, name: &str, sid: &str) -> io::Result<Option<Fence>> {
    match read_value(key, VALUE) {
        Err(error) if error.raw_os_error() == Some(ERROR_FILE_NOT_FOUND as i32) => Ok(None),
        Ok((REG_BINARY, bytes)) => Fence::parse(&bytes, &account_identity(name, sid)).map(Some),
        Ok(_) => Err(denied("invalid account lifecycle journal type")),
        Err(error) => Err(error),
    }
}

fn persist(key: &Key, fence: &Fence) -> io::Result<()> {
    fence.validate()?;
    write_value(
        key,
        VALUE,
        &serde_json::to_vec(fence).map_err(io::Error::other)?,
    )?;
    // Registry and SAM are NOT one transaction. Persist intent first; a crash
    // leaves opening/sealing, which the local reconciler can only revoke.
    status(unsafe { RegFlushKey(key.0) })
}

pub(super) fn reject_legacy(key: &Key) -> io::Result<()> {
    match read_value(key, VALUE) {
        Err(error) if error.raw_os_error() == Some(ERROR_FILE_NOT_FOUND as i32) => Ok(()),
        Ok(_) => Err(denied(
            "versioned account requires a bound lifecycle request",
        )),
        Err(error) => Err(error),
    }
}

pub(super) fn set_password(name: &str, password: &str) -> io::Result<()> {
    let mut secret = Zeroizing::new(wide(password));
    let info = USER_INFO_1003 {
        usri1003_password: secret.as_mut_ptr(),
    };
    // Fixed local SAM, never a server/domain or arbitrary field supplied by a
    // request. Do not return diagnostics containing the supplied credential.
    status(unsafe {
        NetUserSetInfo(
            null(),
            wide(name).as_ptr(),
            1003,
            (&info as *const USER_INFO_1003).cast(),
            null_mut(),
        )
    })
}

pub(super) fn retired_password() -> io::Result<Zeroizing<String>> {
    let mut nonce = Zeroizing::new([0u8; 32]);
    getrandom::fill(nonce.as_mut()).map_err(io::Error::other)?;
    let mut value = Zeroizing::new(String::from("Vcw1!"));
    use std::fmt::Write;
    for byte in nonce.iter() {
        write!(value, "{byte:02x}").map_err(io::Error::other)?;
    }
    Ok(value)
}

impl AgentAccounts {
    pub fn apply_lease(&self, name: &str, request: &Request) -> io::Result<Fence> {
        self.apply_lease_checkpoints(name, request, || {})
    }

    // A statically supplied checkpoint permits real process-exit tests at the
    // actual intent/SAM boundaries. No runtime flag or protocol fault injection.
    pub(super) fn apply_lease_checkpoints(
        &self,
        name: &str,
        request: &Request,
        mut checkpoint: impl FnMut(),
    ) -> io::Result<Fence> {
        agent_name(name)?;
        let _gate = account_gate(name)?;
        let key = self.key(name, false)?;
        let sid = self
            .record(&key, name)?
            .and_then(|r| r.sid)
            .ok_or_else(|| denied("account lifecycle requires a pre-bound SID"))?;
        if request.identity != account_identity(name, &sid) {
            return Err(denied("account lifecycle identity mismatch"));
        }
        if local(name)?.is_some_and(|a| a.account.sid != sid) {
            return Err(denied("account lifecycle SAM identity changed"));
        }
        let previous = read_fence(&key, name, &sid)?;
        let next = plan(previous.as_ref(), request, unix_seconds()?)?;
        if request.operation != Operation::Revoke {
            let actual = self.bound(&key, name)?;
            if request.operation == Operation::Seal
                && (actual.account.disabled
                    || u64::from(actual.expires_unix_seconds) != request.expires_unix_seconds)
            {
                return Err(denied("login window changed before credential retirement"));
            }
        }
        if next.observe_only {
            return Ok(next.completed);
        }
        persist(&key, &next.pending)?;
        checkpoint();
        let result = (|| {
            match request.operation {
                Operation::Open => {
                    // Terminate an older logon before installing a fresh one-use
                    // credential. Old epochs cannot reach any of these writes.
                    self.disable_locked(name)?;
                    self.bound(&key, name)?;
                    set_password(
                        name,
                        request
                            .password
                            .as_ref()
                            .ok_or_else(|| denied("missing login credential"))?
                            .expose(),
                    )?;
                    self.enable_locked(&key, name, request.expires_unix_seconds as u32)?;
                }
                Operation::Seal => {
                    let secret = retired_password()?;
                    self.bound(&key, name)?;
                    set_password(name, &secret)?;
                }
                Operation::Revoke => {
                    self.disable_locked(name)?;
                }
            }
            checkpoint();
            if request.operation != Operation::Revoke {
                let actual = self.bound(&key, name)?;
                if actual.account.disabled
                    || u64::from(actual.expires_unix_seconds) != request.expires_unix_seconds
                    || !next.completed.login_live(unix_seconds()?)
                {
                    return Err(denied("account login window failed to converge"));
                }
            }
            persist(&key, &next.completed)?;
            Ok(next.completed)
        })();
        if result.is_err() {
            // Best-effort immediate close, backed by durable pending/revoked
            // state if this process or an OS API fails. Never roll back epoch.
            let _ = persist(&key, &next.pending.revoked());
            // Do not run another 30-second WTS dispatch cycle after an earlier
            // failure. Close login now; the durable local sweep owns teardown.
            let _ = self.disable_login_locked(&key, name);
        }
        result
    }
}

pub(super) fn revoke_record(key: &Key, fence: &Fence) -> io::Result<()> {
    persist(key, &fence.revoked())
}
