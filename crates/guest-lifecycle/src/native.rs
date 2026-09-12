//! Native credential retirement does not terminate a retained user desktop.
//! This protocol is separate from an Agent lease/login generation. The adapter
//! must hold its OS account gate across intent, mutation and acknowledgement.
use super::{denied, valid_expiry, valid_generation, AccountIdentity, Password, MAX_BYTES};
use serde::{Deserialize, Serialize};
use std::io;

pub fn validate_identity(identity: &AccountIdentity) -> io::Result<()> {
    identity.validate_for("vcw")
}

fn valid_connection(value: &str) -> bool {
    (13..=133).contains(&value.len())
        && value.starts_with("conn_")
        && value
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Deserialize, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Operation {
    Issue,
    Retire,
    Revoke,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Request {
    pub schema_version: u8,
    pub identity: AccountIdentity,
    pub connection_id: String,
    pub revision: u64,
    pub expires_unix_seconds: u64,
    pub operation: Operation,
    pub password: Option<Password>,
}

impl Request {
    pub fn parse(raw: &[u8]) -> io::Result<Self> {
        if raw.is_empty() || raw.len() > MAX_BYTES {
            return Err(denied());
        }
        let request: Self = serde_json::from_slice(raw).map_err(|_| denied())?;
        request.validate()?;
        Ok(request)
    }

    fn validate(&self) -> io::Result<()> {
        validate_identity(&self.identity)?;
        if self.schema_version != 1
            || !valid_connection(&self.connection_id)
            || !valid_generation(self.revision)
            || match self.operation {
                Operation::Issue => {
                    !self.password.as_ref().is_some_and(Password::valid)
                        || !valid_expiry(self.expires_unix_seconds)
                }
                Operation::Retire => {
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
    Issuing,
    Issued,
    Retiring,
    Retired,
    Revoked,
}

#[derive(Clone, Debug, PartialEq, Eq, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Fence {
    pub schema_version: u8,
    pub identity: AccountIdentity,
    pub connection_id: String,
    pub revision: u64,
    pub expires_unix_seconds: u64,
    pub phase: Phase,
}

impl Fence {
    pub fn parse(raw: &[u8], identity: &AccountIdentity) -> io::Result<Self> {
        if raw.is_empty() || raw.len() > MAX_BYTES {
            return Err(denied());
        }
        let value: Self = serde_json::from_slice(raw).map_err(|_| denied())?;
        value.validate()?;
        if &value.identity != identity {
            return Err(denied());
        }
        Ok(value)
    }

    pub fn validate(&self) -> io::Result<()> {
        validate_identity(&self.identity)?;
        if self.schema_version != 1
            || !valid_connection(&self.connection_id)
            || !valid_generation(self.revision)
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

    pub fn retained(&self, now: u64) -> bool {
        matches!(self.phase, Phase::Issued | Phase::Retired) && self.expires_unix_seconds > now
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
    pub observe_only: bool,
    pub preserve_desktop: bool,
}

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
        if old.identity != request.identity || request.revision < old.revision {
            return Err(denied());
        }
    }
    let mut preserve_desktop = false;
    let (pending, completed, observe_only) = match request.operation {
        Operation::Issue => {
            if let Some(old) = previous {
                if request.revision != old.revision + 1
                    || request.connection_id == old.connection_id
                    || !(old.phase == Phase::Revoked
                        || (old.phase == Phase::Retired && old.retained(now)))
                {
                    return Err(denied());
                }
                preserve_desktop = old.phase == Phase::Retired;
            } else if request.revision != 1 {
                return Err(denied());
            }
            (Phase::Issuing, Phase::Issued, false)
        }
        Operation::Retire => {
            let old = previous.ok_or_else(denied)?;
            if request.connection_id != old.connection_id
                || request.expires_unix_seconds != old.expires_unix_seconds
                || !old.retained(now)
                || !((old.phase == Phase::Issued && request.revision == old.revision + 1)
                    || (old.phase == Phase::Retired && request.revision == old.revision))
            {
                return Err(denied());
            }
            preserve_desktop = true;
            (Phase::Retiring, Phase::Retired, old.phase == Phase::Retired)
        }
        Operation::Revoke => {
            if previous.is_some_and(|old| {
                request.revision == old.revision
                    && (request.connection_id != old.connection_id || old.phase != Phase::Revoked)
            }) {
                return Err(denied());
            }
            // A higher revocation may fence an issue that never reached the
            // Guest, including a new connection ID. Exact revoked retries
            // reconverge; they never set passwords or change the connection.
            (Phase::Revoked, Phase::Revoked, false)
        }
    };
    let pending = Fence {
        schema_version: 1,
        identity: request.identity.clone(),
        connection_id: request.connection_id.clone(),
        revision: request.revision,
        expires_unix_seconds: request.expires_unix_seconds,
        phase: pending,
    };
    let completed = Fence {
        phase: completed,
        ..pending.clone()
    };
    Ok(Plan {
        pending,
        completed,
        observe_only,
        preserve_desktop,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    fn request(operation: Operation, revision: u64, connection: &str) -> Request {
        Request::parse(&serde_json::to_vec(&serde_json::json!({
            "schema_version":1,"identity":{"username":"vcw123456abcdef","uid":1001,"sid":""},
            "connection_id":connection,"revision":revision,"expires_unix_seconds":if operation==Operation::Revoke {0}else{200},
            "operation":operation,"password":if operation==Operation::Issue {Some("Vcw1!long-disposable-password")}else{None}
        })).unwrap()).unwrap()
    }
    #[test]
    fn retirement_and_reconnect_preserve_desktop_but_revocation_does_not() {
        let issue = request(Operation::Issue, 1, "conn_example_one");
        let one = plan(None, &issue, 100).unwrap();
        assert!(!one.preserve_desktop);
        assert!(plan(Some(&one.completed), &issue, 100).is_err());
        let retire = request(Operation::Retire, 2, "conn_example_one");
        let two = plan(Some(&one.completed), &retire, 100).unwrap();
        assert!(two.preserve_desktop && !two.observe_only);
        assert!(plan(Some(&two.pending), &retire, 100).is_err());
        assert!(
            plan(Some(&two.completed), &retire, 100)
                .unwrap()
                .observe_only
        );
        let three = plan(
            Some(&two.completed),
            &request(Operation::Issue, 3, "conn_example_two"),
            100,
        )
        .unwrap();
        assert!(three.preserve_desktop);
        let close = request(Operation::Revoke, 4, "conn_example_two");
        let four = plan(Some(&three.pending), &close, 100).unwrap();
        assert!(!four.preserve_desktop);
        assert!(plan(Some(&four.completed), &retire, 100).is_err());
        assert!(plan(Some(&four.completed), &close, 100).is_ok());
        assert!(
            !plan(
                Some(&four.completed),
                &request(Operation::Issue, 5, "conn_example_three"),
                100
            )
            .unwrap()
            .preserve_desktop
        );
    }
    #[test]
    fn invalid_identity_versions_deadlines_and_secret_fields_are_rejected() {
        let issue = request(Operation::Issue, 1, "conn_example_one");
        let issued = plan(None, &issue, 100).unwrap().completed;
        for name in ["vca123456abcdef", "vcw123", "vcw123456ABCDEf", "root"] {
            let mut bad = request(Operation::Issue, 1, "conn_example_one");
            bad.identity.username = name.into();
            assert!(plan(None, &bad, 100).is_err());
        }
        for revision in [0, 2, u64::MAX] {
            let mut bad = request(Operation::Issue, 1, "conn_example_one");
            bad.revision = revision;
            assert!(plan(None, &bad, 100).is_err());
        }
        for expiry in [0, 99, 100, 28_901, u64::MAX] {
            let mut bad = request(Operation::Retire, 2, "conn_example_one");
            bad.expires_unix_seconds = expiry;
            assert!(plan(Some(&issued), &bad, 100).is_err());
        }
        let raw = serde_json::to_vec(&issued).unwrap();
        assert!(Fence::parse(&raw, &issued.identity).is_ok());
        let mut payload = serde_json::to_value(&issued).unwrap();
        payload["password"] = "never persisted".into();
        assert!(Fence::parse(&serde_json::to_vec(&payload).unwrap(), &issued.identity).is_err());
        let mut other = issued.identity.clone();
        other.uid += 1;
        assert!(Fence::parse(&raw, &other).is_err());
    }
    #[test]
    fn expired_retired_window_and_interrupted_issue_cannot_be_reopened() {
        let issued = plan(None, &request(Operation::Issue, 1, "conn_example_one"), 100).unwrap();
        let retired = plan(
            Some(&issued.completed),
            &request(Operation::Retire, 2, "conn_example_one"),
            100,
        )
        .unwrap();
        let mut next = request(Operation::Issue, 3, "conn_example_two");
        next.expires_unix_seconds = 400;
        assert!(plan(Some(&retired.completed), &next, 201).is_err());
        let close = request(Operation::Revoke, 9, "conn_example_one");
        assert!(plan(Some(&issued.pending), &close, 100).is_ok());
        let closed = plan(None, &close, 100).unwrap();
        assert!(plan(
            Some(&closed.completed),
            &request(Operation::Issue, 1, "conn_example_one"),
            100
        )
        .is_err());
    }
    #[test]
    fn higher_revocation_fences_an_undelivered_new_connection() {
        let issued = plan(None, &request(Operation::Issue, 1, "conn_example_one"), 100)
            .unwrap()
            .completed;
        let retired = plan(
            Some(&issued),
            &request(Operation::Retire, 2, "conn_example_one"),
            100,
        )
        .unwrap()
        .completed;
        // DB revision 3 reserved conn_example_two, but Guest never saw issue.
        let close = request(Operation::Revoke, 4, "conn_example_two");
        let revoked = plan(Some(&retired), &close, 100).unwrap().completed;
        assert_eq!(revoked.connection_id, "conn_example_two");
        assert!(plan(Some(&revoked), &close, 100).is_ok());
        assert!(plan(
            Some(&revoked),
            &request(Operation::Revoke, 4, "conn_example_one"),
            100
        )
        .is_err());
        assert!(plan(
            Some(&revoked),
            &request(Operation::Issue, 3, "conn_example_two"),
            100
        )
        .is_err());
        assert!(plan(
            Some(&revoked),
            &request(Operation::Issue, 5, "conn_example_three"),
            100
        )
        .is_ok());
    }
}
