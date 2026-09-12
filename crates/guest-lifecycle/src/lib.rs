//! Account mutation ordering, independent of the GUI/input authority fence.
//! OS adapters hold an exclusive account gate across read/plan, durable intent,
//! all SAM/shadow mutations, and durable completion. Never erase expired fences.
#![forbid(unsafe_code)]

use serde::{Deserialize, Serialize};
use std::io;
use zeroize::Zeroize;
pub use zeroize::Zeroizing;

pub const MAX_BYTES: usize = 4096;
pub mod native;

fn denied() -> io::Error {
    io::Error::new(
        io::ErrorKind::PermissionDenied,
        "invalid or stale account lifecycle request",
    )
}

#[derive(Clone, Debug, PartialEq, Eq, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct AccountIdentity {
    pub username: String,
    pub uid: u32,
    pub sid: String,
}

impl AccountIdentity {
    pub fn validate(&self) -> io::Result<()> {
        self.validate_for("vca")
    }

    fn validate_for(&self, prefix: &str) -> io::Result<()> {
        let valid_name = self.username.len() == 15
            && self.username.starts_with(prefix)
            && self.username[3..]
                .bytes()
                .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b));
        let valid_sid = || {
            let parts: Vec<_> = self.sid.split('-').collect();
            parts.len() == 8
                && parts[..4] == ["S", "1", "5", "21"]
                && parts[4..]
                    .iter()
                    .all(|p| p.parse::<u32>().is_ok_and(|n| n.to_string() == *p))
                && parts[7].parse::<u32>().is_ok_and(|n| n >= 1000)
        };
        if valid_name
            && ((self.uid >= 1000 && self.sid.is_empty()) || (self.uid == 0 && valid_sid()))
        {
            Ok(())
        } else {
            Err(denied())
        }
    }
}

// Deliberately no Serialize/Debug/Clone. Credentials only enter via bounded
// stdin and are never part of a journal or receipt. Wiping is best effort;
// it does not claim to remove copies held by the JSON parser or OS API.
#[derive(Deserialize)]
#[serde(transparent)]
pub struct Password(String);
impl Password {
    pub fn expose(&self) -> &str {
        &self.0
    }
    fn valid(&self) -> bool {
        (24..=256).contains(&self.0.len()) && self.0.bytes().all(|b| (33..=126).contains(&b))
    }
}
impl Drop for Password {
    fn drop(&mut self) {
        self.0.zeroize();
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Deserialize, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Operation {
    Open,
    Seal,
    Revoke,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Request {
    pub schema_version: u8,
    pub identity: AccountIdentity,
    pub lease_id: String,
    pub control_epoch: u64,
    pub login_generation: u64,
    pub expires_unix_seconds: u64,
    pub operation: Operation,
    pub password: Option<Password>,
}

impl Request {
    pub fn parse(raw: &[u8]) -> io::Result<Self> {
        if raw.is_empty() || raw.len() > MAX_BYTES {
            return Err(denied());
        }
        // Never expose serde's input-derived diagnostic (including secrets).
        let request: Self = serde_json::from_slice(raw).map_err(|_| denied())?;
        request.validate()?;
        Ok(request)
    }

    fn validate(&self) -> io::Result<()> {
        self.identity.validate()?;
        if self.schema_version != 1
            || !valid_lease(&self.lease_id, self.control_epoch)
            || !valid_generation(self.login_generation)
            || match self.operation {
                Operation::Open => {
                    !self.password.as_ref().is_some_and(Password::valid)
                        || !valid_expiry(self.expires_unix_seconds)
                }
                Operation::Seal => {
                    self.password.is_some() || !valid_expiry(self.expires_unix_seconds)
                }
                Operation::Revoke => self.password.is_some() || self.expires_unix_seconds != 0,
            }
        {
            return Err(denied());
        }
        Ok(())
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Deserialize, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Phase {
    Opening,
    Open,
    Sealing,
    Sealed,
    Revoked,
}

#[derive(Clone, Debug, PartialEq, Eq, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Fence {
    pub schema_version: u8,
    pub identity: AccountIdentity,
    pub lease_id: String,
    pub control_epoch: u64,
    pub login_generation: u64,
    pub expires_unix_seconds: u64,
    pub phase: Phase,
}

fn valid_lease(lease: &str, epoch: u64) -> bool {
    epoch > 0
        && epoch <= i64::MAX as u64
        && (7..=128).contains(&lease.len())
        && lease.starts_with("lease_")
        && lease
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
}

fn valid_expiry(expiry: u64) -> bool {
    expiry > 0 && expiry < u32::MAX as u64
}

fn valid_generation(generation: u64) -> bool {
    generation > 0 && generation <= i64::MAX as u64
}

impl Fence {
    pub fn parse(raw: &[u8], identity: &AccountIdentity) -> io::Result<Self> {
        if raw.is_empty() || raw.len() > MAX_BYTES {
            return Err(denied());
        }
        let fence: Self = serde_json::from_slice(raw).map_err(|_| denied())?;
        fence.validate()?;
        if &fence.identity != identity {
            return Err(denied());
        }
        Ok(fence)
    }

    pub fn validate(&self) -> io::Result<()> {
        self.identity.validate()?;
        if self.schema_version != 1
            || !valid_lease(&self.lease_id, self.control_epoch)
            || !valid_generation(self.login_generation)
            || if self.phase == Phase::Revoked {
                self.expires_unix_seconds != 0
            } else {
                !valid_expiry(self.expires_unix_seconds)
            }
        {
            return Err(denied());
        }
        Ok(())
    }

    pub fn login_live(&self, now: u64) -> bool {
        matches!(self.phase, Phase::Open | Phase::Sealed) && self.expires_unix_seconds > now
    }

    pub fn revoked(&self) -> Self {
        Self {
            phase: Phase::Revoked,
            expires_unix_seconds: 0,
            ..self.clone()
        }
    }
}

pub struct Plan {
    pub pending: Fence,
    pub completed: Fence,
    /// Sealing an already sealed exact lease only returns its existing receipt.
    /// Revocation is converged again under the gate; opening is never replayed.
    pub observe_only: bool,
}

/// Caller must re-read the durable record under its OS gate immediately before
/// calling this. Neither wall-clock expiry nor a missing SAM account resets it.
pub fn plan(previous: Option<&Fence>, request: &Request, now: u64) -> io::Result<Plan> {
    request.validate()?;
    if request.operation != Operation::Revoke
        && (request.expires_unix_seconds <= now
            || request.expires_unix_seconds.saturating_sub(now) > 28_800)
    {
        return Err(denied());
    }
    if let Some(old) = previous {
        old.validate()?;
        if old.identity != request.identity
            || request.control_epoch < old.control_epoch
            || (request.control_epoch == old.control_epoch && request.lease_id != old.lease_id)
            || (request.control_epoch == old.control_epoch
                && request.login_generation < old.login_generation)
        {
            return Err(denied());
        }
    }
    let (pending_phase, complete_phase, observe_only) = match request.operation {
        Operation::Open => {
            // A new login is allowed within a live lease, but cannot revive a
            // revoked/expired or interrupted lease, or change its deadline.
            // Lease revocation remains terminal even with a higher generation.
            if previous.is_some_and(|old| {
                request.control_epoch == old.control_epoch
                    && (request.login_generation <= old.login_generation
                        || !old.login_live(now)
                        || request.expires_unix_seconds != old.expires_unix_seconds)
            }) {
                return Err(denied());
            }
            (Phase::Opening, Phase::Open, false)
        }
        Operation::Seal => {
            let old = previous.ok_or_else(denied)?;
            if request.control_epoch != old.control_epoch
                || request.login_generation != old.login_generation
                || request.expires_unix_seconds != old.expires_unix_seconds
                || !matches!(old.phase, Phase::Open | Phase::Sealed)
            {
                return Err(denied());
            }
            (Phase::Sealing, Phase::Sealed, old.phase == Phase::Sealed)
        }
        Operation::Revoke => (Phase::Revoked, Phase::Revoked, false),
    };
    let pending = Fence {
        schema_version: 1,
        identity: request.identity.clone(),
        lease_id: request.lease_id.clone(),
        control_epoch: request.control_epoch,
        login_generation: request.login_generation,
        expires_unix_seconds: request.expires_unix_seconds,
        phase: pending_phase,
    };
    let completed = Fence {
        phase: complete_phase,
        ..pending.clone()
    };
    Ok(Plan {
        pending,
        completed,
        observe_only,
    })
}

#[cfg(test)]
mod tests;
