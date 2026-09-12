//! SID-bound Native SAM/WTS adapter. Retiring credentials preserves the desktop;
//! revocation closes SAM login and waits for the exact SID's WTS logons.
//! This is not a claim that all other token/process creation paths are fenced.
//! Password resets have not been accepted for persistent DPAPI/user secrets;
//! this adapter must stay off the default broker until that boundary is solved.
use super::*;
use vc_workspace_guest_lifecycle::{
    native::{plan, Fence, Operation, Request},
    AccountIdentity,
};

pub(super) const ROOT: &str = "VCWorkspace.NativeAccountsV1";
const VALUE: &str = "NativeLifecycle";
mod password;
mod secret;
pub use super::AgentAccount as NativeAccount;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct NativeAccountObservation {
    pub username: String,
    pub sid: String,
    pub disabled: Option<bool>,
    pub expires_unix_seconds: Option<u32>,
    pub sessions: Vec<Identity>,
    pub lifecycle: Option<Fence>,
}

/// A separate protected ownership root: never adopt an existing `vcw` account
/// from the old PowerShell path, an Agent journal, or a matching username.
pub struct NativeAccounts {
    inner: AgentAccounts,
}

impl Default for NativeAccounts {
    fn default() -> Self {
        Self {
            inner: AgentAccounts {
                root: ROOT.into(),
                namespace: Namespace::Native,
            },
        }
    }
}

fn identity(name: &str, sid: &str) -> AccountIdentity {
    AccountIdentity {
        username: name.into(),
        uid: 0,
        sid: sid.into(),
    }
}

fn read_fence(key: &Key, name: &str, sid: &str) -> io::Result<Option<Fence>> {
    // Distinct names, roots and value types prevent interpreting an Agent seal
    // as Native retirement. Even a malformed foreign value is not ignored.
    lease::reject_legacy(key)?;
    match read_value(key, VALUE) {
        Err(error) if error.raw_os_error() == Some(ERROR_FILE_NOT_FOUND as i32) => Ok(None),
        Ok((REG_BINARY, bytes)) => Fence::parse(&bytes, &identity(name, sid)).map(Some),
        Ok(_) => Err(denied("invalid Native lifecycle journal type")),
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
    status(unsafe { RegFlushKey(key.0) })
}

impl NativeAccounts {
    pub fn provision(&self, name: &str) -> io::Result<NativeAccount> {
        Namespace::Native.validate(name)?;
        self.inner.provision(name)
    }

    fn owned_sid(&self, key: &Key, name: &str) -> io::Result<String> {
        let sid = self
            .inner
            .record(key, name)?
            .and_then(|r| r.sid)
            .ok_or_else(|| denied("Native lifecycle requires a pre-bound SID"))?;
        if local(name)?.is_some_and(|actual| actual.account.sid != sid) {
            return Err(denied("Native SAM identity differs from owned SID"));
        }
        Ok(sid)
    }

    pub fn observe(&self, name: &str) -> io::Result<NativeAccountObservation> {
        Namespace::Native.validate(name)?;
        let _gate = account_gate(name)?;
        let key = self.inner.key(name, false)?;
        let sid = self.owned_sid(&key, name)?;
        let before = local(name)?;
        let lifecycle = read_fence(&key, name, &sid)?;
        let sessions = sessions_for_sid(&sid)?;
        let after = local(name)?;
        if account_snapshot(&before) != account_snapshot(&after)
            || self.owned_sid(&key, name)? != sid
            || read_fence(&key, name, &sid)? != lifecycle
        {
            return Err(denied("Native SAM/WTS observation changed"));
        }
        Ok(NativeAccountObservation {
            username: name.into(),
            sid,
            disabled: after.as_ref().map(|a| a.account.disabled),
            expires_unix_seconds: after.as_ref().map(|a| a.expires_unix_seconds),
            sessions,
            lifecycle,
        })
    }

    fn retained(&self, key: &Key, name: &str, fence: &Fence) -> io::Result<()> {
        let actual = self.inner.bound(key, name)?;
        if actual.account.disabled
            || !fence.retained(unix_seconds()?)
            || fence.expires_unix_seconds != u64::from(actual.expires_unix_seconds)
            || actual.expires_unix_seconds == u32::MAX
        {
            return Err(denied("Native retained login window changed"));
        }
        Ok(())
    }

    pub fn apply_credential(&self, name: &str, request: &Request) -> io::Result<Fence> {
        self.apply_checkpoints(name, request, || {})
    }

    fn apply_checkpoints(
        &self,
        name: &str,
        request: &Request,
        mut checkpoint: impl FnMut(),
    ) -> io::Result<Fence> {
        Namespace::Native.validate(name)?;
        let _gate = account_gate(name)?;
        let key = self.inner.key(name, false)?;
        let sid = self.owned_sid(&key, name)?;
        if request.identity != identity(name, &sid)
            || (request.operation != Operation::Revoke
                && request.expires_unix_seconds >= u64::from(u32::MAX))
        {
            return Err(denied("Native credential identity or expiry mismatch"));
        }
        let previous = read_fence(&key, name, &sid)?;
        let next = plan(previous.as_ref(), request, unix_seconds()?)?;
        // The experimental SAM reset adapter is not safe for persistent DPAPI
        // data. Do not damage an existing Profile while its replacement is
        // being accepted; revocation/local expiry still close access normally.
        if request.operation != Operation::Revoke && password::has_profile(&sid)? {
            return Err(denied(
                "Native profile requires DPAPI-safe credential lifecycle",
            ));
        }
        if next.preserve_desktop {
            self.retained(
                &key,
                name,
                previous
                    .as_ref()
                    .ok_or_else(|| denied("retained intent missing"))?,
            )?;
        }
        if next.observe_only {
            return Ok(next.completed);
        }
        persist(&key, &next.pending)?;
        checkpoint();
        let result = (|| {
            match request.operation {
                Operation::Issue => {
                    if next.preserve_desktop {
                        self.inner.disable_login_locked(&key, name)?;
                    } else {
                        self.inner.disable_locked(name)?;
                    }
                    self.inner.bound(&key, name)?;
                    lease::set_password(
                        name,
                        request
                            .password
                            .as_ref()
                            .ok_or_else(|| denied("missing Native password"))?
                            .expose(),
                    )?;
                    self.inner
                        .enable_locked(&key, name, request.expires_unix_seconds as u32)?;
                }
                Operation::Retire => {
                    let secret = lease::retired_password()?;
                    self.inner.bound(&key, name)?;
                    lease::set_password(name, &secret)?;
                }
                Operation::Revoke => {
                    self.inner.disable_locked(name)?;
                }
            }
            checkpoint();
            if request.operation != Operation::Revoke {
                self.retained(&key, name, &next.completed)?;
            } else {
                self.owned_sid(&key, name)?;
                if local(name)?.is_some_and(|a| !a.account.disabled)
                    || !sessions_for_sid(&sid)?.is_empty()
                {
                    return Err(denied("Native SAM/WTS revocation did not converge"));
                }
            }
            persist(&key, &next.completed)?;
            Ok(next.completed)
        })();
        if result.is_err() {
            let _ = persist(&key, &next.pending.revoked());
            // Keep failed work durable without running a second WTS timeout.
            // A later local sweep owns completion, never credential replay.
            let _ = self.inner.disable_login_locked(&key, name);
        }
        result
    }

    pub fn reconcile_expired(&self) -> io::Result<usize> {
        self.inner.reconcile_records(|name| self.expire(name))
    }

    fn expire(&self, name: &str) -> io::Result<bool> {
        Namespace::Native.validate(name)?;
        let _gate = account_gate(name)?;
        let key = self.inner.key(name, false)?;
        let sid = self.owned_sid(&key, name)?;
        match read_fence(&key, name, &sid) {
            Ok(Some(fence)) => {
                if self.retained(&key, name, &fence).is_ok() {
                    return Ok(false);
                }
                persist(&key, &fence.revoked())?;
            }
            Ok(None) => (), // Unversioned owned users may never remain enabled.
            Err(error) => {
                // A corrupt journal cannot authorize login. Do not overwrite
                // unknown version history or touch an unbound/reused SID.
                self.inner.disable_locked(name)?;
                return Err(error);
            }
        }
        self.inner.disable_locked(name)?;
        Ok(true)
    }
}

#[cfg(test)]
pub(crate) mod tests;

#[cfg(test)]
pub(crate) mod profile_tests;
