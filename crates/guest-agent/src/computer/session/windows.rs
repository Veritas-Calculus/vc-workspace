//! Windows SID/session-bound protocol. OS-account adoption and unattended login
//! are separate lifecycle responsibilities; the default HTTP gate stays closed.
use super::*;
use sha2::{Digest, Sha256};
use vc_workspace_windows_session::Identity;
#[cfg(target_os = "windows")]
mod runtime;
#[cfg(target_os = "windows")]
pub(super) use runtime::run;

#[derive(Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct Announcement {
    schema_version: u8,
    target: SessionKey,
    input_ready: bool,
}
impl Announcement {
    fn ready_target(self) -> io::Result<SessionKey> {
        if !self.input_ready {
            return Err(denied("Windows user input desktop is locked or not ready"));
        }
        Ok(self.target)
    }
}

fn validate_announcement(
    username: &str,
    identity: &Identity,
    response: serde_json::Value,
) -> io::Result<Announcement> {
    let response: Announcement = serde_json::from_value(response).map_err(io::Error::other)?;
    let expected = key_for_identity(username, identity, &response.target.instance_id)?;
    if response.schema_version != VERSION || response.target != expected {
        return Err(denied(
            "Windows Helper announcement does not match its kernel identity",
        ));
    }
    Ok(response)
}

fn authority_record(raw: &[u8]) -> io::Result<(BoundAuthority, Authority)> {
    if raw.is_empty() || raw.len() > MAX_REQUEST {
        return Err(denied("authority exceeds limit"));
    }
    let bound: BoundAuthority = serde_json::from_slice(raw).map_err(io::Error::other)?;
    let authority: Authority =
        serde_json::from_value(bound.authority.clone()).map_err(io::Error::other)?;
    if bound.schema_version != VERSION
        || authority.schema_version != SCHEMA_VERSION
        || authority.lease_id.len() > 128
        || !authority.lease_id.starts_with("lease_")
        || !authority
            .lease_id
            .bytes()
            .all(|c| c.is_ascii_alphanumeric() || matches!(c, b'_' | b'-'))
        || authority.control_epoch < 1
        || !matches!(authority.state.as_str(), "active" | "revoked")
        || (authority.state == "active"
            && (authority.expires_unix_ms <= 0
                || !bound
                    .target
                    .as_ref()
                    .is_some_and(|key| valid_key(key) && !key.sid.is_empty())))
        || (authority.state == "revoked" && bound.target.is_some())
    {
        return Err(denied("invalid Windows bound authority"));
    }
    Ok((bound, authority))
}

fn parse_authority(raw: &[u8], now: i64) -> io::Result<(BoundAuthority, Authority)> {
    let (bound, authority) = authority_record(raw)?;
    if (authority.state == "active" && authority.expires_unix_ms <= now)
        || (authority.state == "revoked" && authority.expires_unix_ms > now)
    {
        return Err(denied("invalid Windows authority deadline"));
    }
    Ok((bound, authority))
}

// Stored active grants keep their ordering significance after expiry. Parsing
// the previous record using today's activation deadline would erase the fence.
fn validate_authority_transition(
    previous: &[u8],
    next: &BoundAuthority,
    authority: &Authority,
) -> io::Result<()> {
    let (old_bound, old) = authority_record(previous)?;
    if authority.control_epoch < old.control_epoch {
        return Err(denied("Windows authority epoch cannot move backwards"));
    }
    if authority.control_epoch == old.control_epoch && authority.state == "active" {
        if old.state == "revoked" {
            return Err(denied("revoked Windows authority epoch cannot reactivate"));
        }
        let same_user = old_bound
            .target
            .as_ref()
            .zip(next.target.as_ref())
            .is_some_and(|(old, next)| old.username == next.username && old.sid == next.sid);
        if old.lease_id != authority.lease_id
            || !same_user
            || authority.expires_unix_ms < old.expires_unix_ms
        {
            return Err(denied(
                "Windows authority lease, owner or expiry moved backwards",
            ));
        }
    }
    Ok(())
}

fn validate_unfenced_authorities(
    old_records: &[Vec<u8>],
    next: &BoundAuthority,
    authority: &Authority,
) -> io::Result<()> {
    if !old_records.is_empty() && authority.state != "revoked" {
        return Err(denied(
            "unfenced Windows authority must be revoked before activation",
        ));
    }
    for previous in old_records {
        validate_authority_transition(previous, next, authority)?;
    }
    Ok(())
}

fn key_for_identity(username: &str, identity: &Identity, instance: &str) -> io::Result<SessionKey> {
    let key = SessionKey {
        username: username.into(),
        uid: 0,
        sid: identity.sid.clone(),
        session_id: identity.binding_id(),
        instance_id: instance.into(),
    };
    if !valid_key(&key) {
        return Err(denied("managed interactive Windows identity required"));
    }
    Ok(key)
}

fn validate_binding(key: &SessionKey, action: &BoundAction) -> io::Result<Request> {
    if action.schema_version != VERSION
        || action.target != *key
        || !valid_key(key)
        || key.sid.is_empty()
        || !(250..=15_000).contains(&action.timeout_ms)
    {
        return Err(denied("Windows session binding has changed"));
    }
    let request: Request =
        serde_json::from_value(action.request.clone()).map_err(io::Error::other)?;
    if !valid_request_id(&request.request_id)
        || request.expires_unix_ms > unix_millis() + 20_000
        || request.lease_id.len() > 128
        || !request
            .lease_id
            .bytes()
            .all(|c| c.is_ascii_alphanumeric() || matches!(c, b'_' | b'-'))
    {
        return Err(denied("invalid Windows action identity or deadline"));
    }
    validate_request(&request.request_id, &request).map_err(io::Error::other)?;
    Ok(request)
}

fn authorize_bytes(key: &SessionKey, request: &Request, raw: &[u8], now: i64) -> io::Result<()> {
    let (bound, authority) = parse_authority(raw, now)?;
    if bound.target.as_ref() != Some(key)
        || authority.state != "active"
        || authority.lease_id != request.lease_id
        || authority.control_epoch != request.control_epoch
        || request.expires_unix_ms <= now
    {
        return Err(denied("Windows desktop control authority has changed"));
    }
    Ok(())
}

fn handle_action(
    key: &SessionKey,
    action: &BoundAction,
    replies: &mut ReplayCache,
    authorize: impl Fn(&Request) -> io::Result<()>,
    execute: impl FnOnce(&Request) -> io::Result<serde_json::Value>,
) -> io::Result<serde_json::Value> {
    let request = validate_binding(key, action)?;
    // Reauthorize cached observations as well as new input. Reserve before any
    // worker starts; a lost result must not cause a second keystroke/click.
    authorize(&request)?;
    let fingerprint =
        Sha256::digest(serde_json::to_vec(action).map_err(io::Error::other)?).to_vec();
    let response = match replies
        .begin(
            &request.request_id,
            fingerprint,
            request.expires_unix_ms,
            unix_millis(),
        )
        .map_err(io::Error::other)?
    {
        Some(response) => response,
        None => execute(&request).unwrap_or_else(|_| {
            serde_json::to_value(failure(
                &request.request_id,
                "session action failed or timed out",
            ))
            .expect("serializable action failure")
        }),
    };
    // A revocation while the worker ran discards its observation and keeps the
    // replay reservation. It cannot become an unauthenticated cached screenshot.
    authorize(&request)?;
    replies.complete(&request.request_id, &response);
    Ok(response)
}

fn worker_frame(action: &BoundAction) -> io::Result<Vec<u8>> {
    let mut frame = Vec::new();
    encode_frame(&mut frame, action)?;
    if frame.len() > MAX_REQUEST {
        return Err(denied("Windows worker input exceeds limit"));
    }
    Ok(frame)
}

fn worker_reply(raw: &[u8], request_id: &str) -> io::Result<serde_json::Value> {
    if raw.len() > MAX_RESPONSE_BYTES {
        return Err(denied("Windows worker output exceeds limit"));
    }
    let mut reader = io::Cursor::new(raw);
    let response = decode_frame(&mut reader, MAX_RESPONSE_BYTES)?;
    if reader.position() as usize != raw.len()
        || response["request_id"] != request_id
        || response["schema_version"] != SCHEMA_VERSION
        || !response["ok"].is_boolean()
    {
        return Err(denied("invalid Windows worker response"));
    }
    Ok(response)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn windows_helper_liveness_does_not_authorize_a_locked_desktop() {
        let identity = Identity {
            sid: "S-1-5-21-1-2-3-1001".into(),
            session_id: 2,
            authentication_id: 1234,
        };
        let key = key_for_identity("vca0123456789ab", &identity, &"a".repeat(64)).unwrap();
        for ready in [false, true] {
            let value = serde_json::to_value(Announcement {
                schema_version: VERSION,
                target: key.clone(),
                input_ready: ready,
            })
            .unwrap();
            let announcement = validate_announcement(&key.username, &identity, value).unwrap();
            assert_eq!(announcement.target, key);
            assert_eq!(announcement.ready_target().is_ok(), ready);
        }
    }

    #[test]
    fn windows_helper_readiness_cannot_override_its_kernel_binding() {
        let identity = Identity {
            sid: "S-1-5-21-1-2-3-1001".into(),
            session_id: 2,
            authentication_id: 1234,
        };
        let key = key_for_identity("vca0123456789ab", &identity, &"a".repeat(64)).unwrap();
        let value = serde_json::to_value(Announcement {
            schema_version: VERSION,
            target: key.clone(),
            input_ready: true,
        })
        .unwrap();
        for other in [
            Identity {
                session_id: 3,
                ..identity.clone()
            },
            Identity {
                authentication_id: 5678,
                ..identity.clone()
            },
            Identity {
                sid: "S-1-5-21-1-2-3-1002".into(),
                ..identity.clone()
            },
        ] {
            assert!(validate_announcement(&key.username, &other, value.clone()).is_err());
        }
        let mut missing = value.clone();
        missing.as_object_mut().unwrap().remove("input_ready");
        assert!(validate_announcement(&key.username, &identity, missing).is_err());
        for (field, data) in [
            ("schema_version", serde_json::json!(1)),
            ("input_ready", serde_json::json!("true")),
            ("unknown", serde_json::json!(true)),
        ] {
            let mut bad = value.clone();
            bad[field] = data;
            assert!(validate_announcement(&key.username, &identity, bad).is_err());
        }
    }
    use std::cell::Cell;

    fn key() -> SessionKey {
        key_for_identity(
            "vca0123456789ab",
            &Identity {
                sid: "S-1-5-21-1-2-3-1001".into(),
                session_id: 2,
                authentication_id: 18,
            },
            &"a".repeat(64),
        )
        .unwrap()
    }

    fn action() -> BoundAction {
        BoundAction {
            schema_version: VERSION,
            target: key(),
            timeout_ms: 5000,
            request: serde_json::json!({"schema_version":1,"request_id":format!("action_{}", "a".repeat(24)),
                "lease_id":"lease_example","control_epoch":4,"expires_unix_ms":unix_millis()+10_000,
                "operation":"type_text","text":{"value":"fixture input"}}),
        }
    }

    fn active_authority() -> serde_json::Value {
        serde_json::json!({"schema_version":2,"target":key(),"authority":{
            "schema_version":1,"lease_id":"lease_example","control_epoch":4,
            "state":"active","expires_unix_ms":unix_millis()+30_000}})
    }

    #[test]
    fn windows_accepts_urlsafe_lease_ids_for_grants_and_actions() {
        let mut authority = active_authority();
        authority["authority"]["lease_id"] = "lease_a-Z_012".into();
        assert!(parse_authority(&serde_json::to_vec(&authority).unwrap(), unix_millis()).is_ok());
        let mut request = action();
        request.request["lease_id"] = "lease_a-Z_012".into();
        assert!(validate_binding(&key(), &request).is_ok());
        for bad in ["lease_a/b", "lease_a b", "lease_a\n", "lease_é"] {
            authority["authority"]["lease_id"] = bad.into();
            request.request["lease_id"] = bad.into();
            assert!(
                parse_authority(&serde_json::to_vec(&authority).unwrap(), unix_millis()).is_err()
            );
            assert!(validate_binding(&key(), &request).is_err());
        }
    }

    #[test]
    fn windows_helper_identity_rejects_system_and_noninteractive_tokens() {
        assert!(key_for_identity(
            "vca0123456789ab",
            &Identity::local_system(),
            &"a".repeat(64)
        )
        .is_err());
        for (sid, session_id, authentication_id) in [
            ("S-1-5-21-1-2-3-1001", 0, 18),
            ("S-1-5-21-1-2-3-1001", 2, 0),
            ("S-1-5-21-1-2-3-500", 2, 18),
        ] {
            assert!(key_for_identity(
                "vca0123456789ab",
                &Identity {
                    sid: sid.into(),
                    session_id,
                    authentication_id
                },
                &"a".repeat(64)
            )
            .is_err());
        }
        let mut wrong = action();
        wrong.target.instance_id = "b".repeat(64);
        assert!(validate_binding(&key(), &wrong).is_err());
        wrong.target = key();
        wrong.request["request_id"] = "action_invalid".into();
        assert!(validate_binding(&key(), &wrong).is_err());
        wrong = action();
        wrong.request["expires_unix_ms"] = (unix_millis() + 30_000).into();
        assert!(validate_binding(&key(), &wrong).is_err());
        wrong = action();
        wrong.timeout_ms = 15_001;
        assert!(validate_binding(&key(), &wrong).is_err());
    }

    #[test]
    fn windows_authority_checks_lease_epoch_sid_and_helper_incarnation() {
        let key = key();
        let request = validate_binding(&key, &action()).unwrap();
        let active = active_authority();
        assert!(authorize_bytes(
            &key,
            &request,
            &serde_json::to_vec(&active).unwrap(),
            unix_millis()
        )
        .is_ok());
        for (section, field, value) in [
            ("target", "sid", serde_json::json!("S-1-5-21-1-2-3-1002")),
            ("target", "instance_id", serde_json::json!("b".repeat(64))),
            (
                "target",
                "session_id",
                serde_json::json!("windows:3:0000000000000012"),
            ),
            ("authority", "lease_id", serde_json::json!("lease_other")),
            ("authority", "control_epoch", serde_json::json!(5)),
            ("authority", "expires_unix_ms", serde_json::json!(0)),
        ] {
            let mut wrong = active.clone();
            wrong[section][field] = value;
            assert!(authorize_bytes(
                &key,
                &request,
                &serde_json::to_vec(&wrong).unwrap(),
                unix_millis()
            )
            .is_err());
        }
    }

    #[test]
    fn windows_replay_never_reexecutes_input_and_reauthorizes_cached_results() {
        let action = action();
        let mut replies = ReplayCache::default();
        let allowed = Cell::new(true);
        let executions = Cell::new(0);
        let authorize = |_: &Request| {
            if allowed.get() {
                Ok(())
            } else {
                Err(denied("revoked"))
            }
        };
        let execute = |_: &Request| {
            executions.set(executions.get() + 1);
            Ok(serde_json::json!({"fixture":true}))
        };
        let first = handle_action(&key(), &action, &mut replies, authorize, execute).unwrap();
        assert_eq!(
            handle_action(&key(), &action, &mut replies, authorize, execute).unwrap(),
            first
        );
        assert_eq!(executions.get(), 1);
        allowed.set(false);
        assert!(handle_action(&key(), &action, &mut replies, authorize, execute).is_err());
        assert_eq!(executions.get(), 1);
        allowed.set(true);
        let mut changed: BoundAction =
            serde_json::from_value(serde_json::to_value(&action).unwrap()).unwrap();
        changed.request["text"]["value"] = "different input".into();
        assert!(handle_action(&key(), &changed, &mut replies, authorize, execute).is_err());
        assert_eq!(executions.get(), 1);
    }

    #[test]
    fn windows_revocation_during_worker_discards_data_and_preserves_reservation() {
        let action = action();
        let allowed = Cell::new(true);
        let mut replies = ReplayCache::default();
        let authorize = |_: &Request| {
            if allowed.get() {
                Ok(())
            } else {
                Err(denied("revoked"))
            }
        };
        assert!(
            handle_action(&key(), &action, &mut replies, authorize, |_| {
                allowed.set(false);
                Ok(serde_json::json!({"observation":"must not be returned"}))
            })
            .is_err()
        );
        allowed.set(true);
        assert!(
            handle_action(&key(), &action, &mut replies, authorize, |_| {
                panic!("lost result must not reexecute input");
            })
            .is_err()
        );
    }

    #[test]
    fn windows_worker_frames_are_bounded_and_bind_complete_responses() {
        let action = action();
        let frame = worker_frame(&action).unwrap();
        let mut cursor = io::Cursor::new(frame);
        let parsed = decode_frame(&mut cursor, MAX_REQUEST - 4).unwrap();
        assert_eq!(parsed["target"], serde_json::to_value(key()).unwrap());
        let mut oversized = action;
        oversized.request["text"]["value"] = "a".repeat(MAX_REQUEST).into();
        assert!(worker_frame(&oversized).is_err());
        let response =
            serde_json::json!({"schema_version":1,"request_id":"action_fixture","ok":true});
        let mut frame = Vec::new();
        encode_frame(&mut frame, &response).unwrap();
        assert_eq!(worker_reply(&frame, "action_fixture").unwrap(), response);
        assert!(worker_reply(&frame, "action_other").is_err());
        frame.push(0);
        assert!(worker_reply(&frame, "action_fixture").is_err());
        assert!(worker_reply(&[], "action_fixture").is_err());
    }

    fn authority(state: &str) -> serde_json::Value {
        serde_json::json!({"schema_version":2,"target":null,"authority":{
            "schema_version":1,"lease_id":"lease_example","control_epoch":4,
            "state":state,"expires_unix_ms":1000}})
    }

    fn transition(previous: &serde_json::Value, next: &serde_json::Value) -> io::Result<()> {
        let (bound, authority) = parse_authority(&serde_json::to_vec(next).unwrap(), 999)?;
        validate_authority_transition(&serde_json::to_vec(previous).unwrap(), &bound, &authority)
    }

    fn grant() -> serde_json::Value {
        let mut value = authority("active");
        value["target"] = serde_json::to_value(key()).unwrap();
        value
    }

    #[test]
    fn windows_epoch_fence_rejects_late_activation_and_late_revocation() {
        let active = grant();
        let mut revoked = authority("revoked");
        revoked["authority"]["expires_unix_ms"] = 0.into();
        assert!(transition(&active, &revoked).is_ok());
        assert!(transition(&revoked, &revoked).is_ok());
        assert!(transition(&revoked, &active).is_err());
        // A later lease may recover, but neither a delayed old grant nor a
        // delayed old tombstone may overwrite the new epoch.
        let mut newer = active.clone();
        newer["authority"]["control_epoch"] = 5.into();
        newer["authority"]["lease_id"] = "lease_new".into();
        assert!(transition(&revoked, &newer).is_ok());
        assert!(transition(&newer, &active).is_err());
        assert!(transition(&newer, &revoked).is_err());
        // Generic same-epoch tombstones can revoke, never reactivate.
        revoked["authority"]["lease_id"] = "lease_revoked_session_tombstone".into();
        assert!(transition(&active, &revoked).is_ok());
        assert!(transition(&revoked, &active).is_err());
    }

    #[test]
    fn windows_epoch_fence_allows_only_same_owner_lease_refresh() {
        let active = grant();
        assert!(transition(&active, &active).is_ok());
        for (group, field, value) in [
            ("authority", "lease_id", serde_json::json!("lease_other")),
            ("authority", "expires_unix_ms", serde_json::json!(1000)),
            ("target", "username", serde_json::json!("vca0123456789ac")),
            ("target", "sid", serde_json::json!("S-1-5-21-1-2-3-1002")),
        ] {
            // Keep the new deadline valid to isolate expiry rollback from
            // ordinary expiration rejection.
            let mut previous = active.clone();
            previous["authority"]["expires_unix_ms"] = 2000.into();
            let mut changed = previous.clone();
            changed[group][field] = value;
            assert!(transition(&previous, &changed).is_err(), "{field}");
        }
        let mut refreshed = active.clone();
        refreshed["authority"]["expires_unix_ms"] = 2000.into();
        refreshed["target"]["instance_id"] = "b".repeat(64).into();
        refreshed["target"]["session_id"] = "windows:3:0000000000000020".into();
        // Runtime must rediscover this instance inside the publication lock.
        assert!(transition(&active, &refreshed).is_ok());
    }

    #[test]
    fn windows_epoch_fence_preserves_expired_records_and_rejects_corruption() {
        let mut expired = grant();
        expired["authority"]["expires_unix_ms"] = 10.into();
        let active = grant();
        assert!(transition(&expired, &active).is_ok());
        let mut old = active.clone();
        old["authority"]["control_epoch"] = 3.into();
        assert!(transition(&expired, &old).is_err());
        let (bound, authority) =
            parse_authority(&serde_json::to_vec(&active).unwrap(), 999).unwrap();
        for previous in [b"{}".to_vec(), Vec::new(), vec![0; MAX_REQUEST + 1]] {
            assert!(validate_authority_transition(&previous, &bound, &authority).is_err());
        }
        expired["authority"]["expires_unix_ms"] = 0.into();
        assert!(transition(&expired, &active).is_err());
    }

    #[test]
    fn windows_unfenced_migration_requires_revocation_at_least_as_new_as_every_record() {
        let mut active = grant();
        let mut previous = vec![serde_json::to_vec(&active).unwrap()];
        active["authority"]["control_epoch"] = 5.into();
        previous.push(serde_json::to_vec(&active).unwrap());
        let (bound, auth) = parse_authority(&serde_json::to_vec(&active).unwrap(), 999).unwrap();
        assert!(validate_unfenced_authorities(&[], &bound, &auth).is_ok());
        assert!(validate_unfenced_authorities(&previous, &bound, &auth).is_err());
        let mut revoked = authority("revoked");
        revoked["authority"]["expires_unix_ms"] = 0.into();
        let (bound, auth) = parse_authority(&serde_json::to_vec(&revoked).unwrap(), 999).unwrap();
        assert!(validate_unfenced_authorities(&previous, &bound, &auth).is_err());
        revoked["authority"]["control_epoch"] = 5.into();
        let (bound, auth) = parse_authority(&serde_json::to_vec(&revoked).unwrap(), 999).unwrap();
        assert!(validate_unfenced_authorities(&previous, &bound, &auth).is_ok());
        previous.push(b"{}".to_vec());
        assert!(validate_unfenced_authorities(&previous, &bound, &auth).is_err());
    }

    #[test]
    fn windows_authority_requires_the_bound_os_identity_and_current_lease() {
        let mut active = authority("active");
        active["target"] = serde_json::json!({"username":"vca0123456789ab","uid":0,
            "sid":"S-1-5-21-1-2-3-1001","session_id":"windows:2:0000000000000012",
            "instance_id":"a".repeat(64)});
        assert!(parse_authority(&serde_json::to_vec(&active).unwrap(), 999).is_ok());
        assert!(parse_authority(&serde_json::to_vec(&active).unwrap(), 1000).is_err());
        for (field, value) in [
            ("sid", serde_json::json!("S-1-5-18")),
            ("uid", serde_json::json!(1000)),
            ("session_id", serde_json::json!("linux:1000:22::1")),
            ("instance_id", serde_json::json!("wrong")),
        ] {
            let mut bad = active.clone();
            bad["target"][field] = value;
            assert!(parse_authority(&serde_json::to_vec(&bad).unwrap(), 999).is_err());
        }
        let mut revoked = active;
        revoked["authority"]["state"] = "revoked".into();
        assert!(parse_authority(&serde_json::to_vec(&revoked).unwrap(), 1000).is_err());
        revoked["target"] = serde_json::Value::Null;
        assert!(parse_authority(&serde_json::to_vec(&revoked).unwrap(), 1000).is_ok());
    }

    #[test]
    fn windows_authority_rejects_malformed_and_oversized_snapshots() {
        for raw in [Vec::new(), vec![b' '; MAX_REQUEST + 1], b"{}".to_vec()] {
            assert!(parse_authority(&raw, 1000).is_err());
        }
        let baseline = authority("revoked");
        for (field, value) in [
            ("lease_id", serde_json::json!("lease_../wrong")),
            ("control_epoch", serde_json::json!(0)),
            ("state", serde_json::json!("unknown")),
            ("schema_version", serde_json::json!(2)),
            ("expires_unix_ms", serde_json::json!(1001)),
        ] {
            let mut bad = baseline.clone();
            bad["authority"][field] = value;
            assert!(parse_authority(&serde_json::to_vec(&bad).unwrap(), 1000).is_err());
        }
    }
}
