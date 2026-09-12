use super::*;
use serde_json::{json, Value};

fn wire(operation: Operation, epoch: u64) -> Value {
    json!({"schema_version":1,"identity":{"username":"vca0123456789ab","uid":0,"sid":"S-1-5-21-1-2-3-1001"},
        "lease_id":format!("lease_{epoch}"),"control_epoch":epoch,"login_generation":1,
        "expires_unix_seconds":if operation == Operation::Revoke {0} else {3700},
        "operation":operation,"password":if operation == Operation::Open {Some("Vcw1!disposable-not-a-real-secret")} else {None}})
}
fn request(operation: Operation, epoch: u64) -> Request {
    Request::parse(&serde_json::to_vec(&wire(operation, epoch)).unwrap()).unwrap()
}
fn open() -> Fence {
    plan(None, &request(Operation::Open, 10), 100)
        .unwrap()
        .completed
}

#[test]
fn all_phases_reject_delayed_writes_and_identity_or_lease_substitution() {
    for phase in [
        Phase::Opening,
        Phase::Open,
        Phase::Sealing,
        Phase::Sealed,
        Phase::Revoked,
    ] {
        let mut old = open();
        old.phase = phase;
        if phase == Phase::Revoked {
            old.expires_unix_seconds = 0;
        }
        for operation in [Operation::Open, Operation::Seal, Operation::Revoke] {
            assert!(plan(Some(&old), &request(operation, 9), 100).is_err());
            let mut foreign = request(operation, 10);
            foreign.lease_id = "lease_foreign".into();
            assert!(plan(Some(&old), &foreign, 100).is_err());
            foreign.lease_id = old.lease_id.clone();
            foreign.identity.sid = "S-1-5-21-1-2-3-1002".into();
            assert!(plan(Some(&old), &foreign, 100).is_err());
        }
        assert!(plan(Some(&old), &request(Operation::Open, 10), 100).is_err());
        assert!(plan(Some(&old), &request(Operation::Open, 11), 100).is_ok());
        let sealed = plan(Some(&old), &request(Operation::Seal, 10), 100);
        assert_eq!(sealed.is_ok(), matches!(phase, Phase::Open | Phase::Sealed));
        if let Ok(sealed) = sealed {
            assert_eq!(sealed.observe_only, phase == Phase::Sealed);
        }
        assert_eq!(
            plan(Some(&old), &request(Operation::Revoke, 10), 100)
                .unwrap()
                .completed,
            old.revoked()
        );
    }
}

#[test]
fn pending_and_expired_intents_cannot_resurrect_or_replay_credentials() {
    let first = plan(None, &request(Operation::Open, 10), 100).unwrap();
    assert!(!first.pending.login_live(100));
    assert!(first.completed.login_live(100));
    assert!(!first.completed.login_live(3700));
    assert!(plan(Some(&first.pending), &request(Operation::Seal, 10), 100).is_err());
    assert!(plan(Some(&first.pending), &request(Operation::Open, 10), 100).is_err());
    assert!(plan(
        Some(&first.completed.revoked()),
        &request(Operation::Open, 10),
        100
    )
    .is_err());
    // The account's old expiry is NOT a reason to discard its epoch.
    assert!(plan(
        Some(&first.completed),
        &request(Operation::Revoke, 9),
        10000
    )
    .is_err());
    assert!(plan(
        Some(&first.completed),
        &request(Operation::Revoke, 10),
        10000
    )
    .is_ok());
}

#[test]
fn only_open_installs_a_password_and_seal_cannot_refresh_the_login_window() {
    let old = open();
    for operation in [Operation::Seal, Operation::Revoke] {
        let mut bad = wire(operation, 10);
        bad["password"] = json!("Vcw1!disposable-not-a-real-secret");
        assert!(Request::parse(&serde_json::to_vec(&bad).unwrap()).is_err());
    }
    let mut seal = request(Operation::Seal, 10);
    for expiry in [3699, 3701] {
        seal.expires_unix_seconds = expiry;
        assert!(plan(Some(&old), &seal, 100).is_err());
    }
    assert!(plan(None, &request(Operation::Seal, 10), 100).is_err());
    assert!(plan(Some(&old), &request(Operation::Seal, 11), 100).is_err());
    assert!(plan(None, &request(Operation::Open, 10), 3700).is_err());
    let mut long = request(Operation::Open, 10);
    long.expires_unix_seconds = 28901;
    assert!(plan(None, &long, 100).is_err());
    long.expires_unix_seconds = 28900;
    assert!(plan(None, &long, 100).is_ok());
}

#[test]
fn journal_is_bounded_strict_and_keeps_identity_after_expiry() {
    let old = open();
    let raw = serde_json::to_vec(&old).unwrap();
    assert_eq!(Fence::parse(&raw, &old.identity).unwrap(), old);
    for bad in [
        b"".as_slice(),
        b"null",
        b"{}",
        b"{",
        &vec![b' '; MAX_BYTES + 1],
    ] {
        assert!(Fence::parse(bad, &old.identity).is_err());
    }
    for (field, value) in [
        ("schema_version", json!(2)),
        ("control_epoch", json!(0)),
        ("control_epoch", json!(u64::MAX)),
        ("login_generation", json!(0)),
        ("login_generation", json!(u64::MAX)),
        ("lease_id", json!("lease_")),
        ("phase", json!("unknown")),
        ("extra", json!(true)),
        ("expires_unix_seconds", json!(u32::MAX)),
    ] {
        let mut bad = serde_json::to_value(&old).unwrap();
        bad[field] = value;
        assert!(Fence::parse(&serde_json::to_vec(&bad).unwrap(), &old.identity).is_err());
    }
    let mut foreign = old.identity.clone();
    foreign.sid = "S-1-5-21-1-2-3-1002".into();
    assert!(Fence::parse(&raw, &foreign).is_err());
    assert!(plan(None, &request(Operation::Revoke, 10), u64::MAX).is_ok());
}

#[test]
fn reconnect_advances_only_login_generation_with_an_unchanged_live_lease() {
    for phase in [
        Phase::Opening,
        Phase::Open,
        Phase::Sealing,
        Phase::Sealed,
        Phase::Revoked,
    ] {
        let mut old = open();
        old.phase = phase;
        if phase == Phase::Revoked {
            old.expires_unix_seconds = 0;
        }
        let mut reconnect = request(Operation::Open, 10);
        reconnect.login_generation = 2;
        let outcome = plan(Some(&old), &reconnect, 100);
        assert_eq!(
            outcome.is_ok(),
            matches!(phase, Phase::Open | Phase::Sealed)
        );
        if let Ok(new) = outcome {
            for operation in [Operation::Open, Operation::Seal, Operation::Revoke] {
                assert!(plan(Some(&new.completed), &request(operation, 10), 100).is_err());
            }
            let mut seal = request(Operation::Seal, 10);
            seal.login_generation = 2;
            assert!(plan(Some(&new.completed), &seal, 100).is_ok());
            let mut revoke = request(Operation::Revoke, 10);
            revoke.login_generation = 2;
            let revoked = plan(Some(&new.completed), &revoke, 100).unwrap().completed;
            reconnect.login_generation = 3;
            assert!(plan(Some(&revoked), &reconnect, 100).is_err());
        }
        reconnect.expires_unix_seconds = 3800;
        assert!(plan(Some(&old), &reconnect, 100).is_err());
        assert!(plan(Some(&old), &reconnect, 3701).is_err());
    }
    let mut raw = wire(Operation::Open, 10);
    raw.as_object_mut().unwrap().remove("login_generation");
    assert!(Request::parse(&serde_json::to_vec(&raw).unwrap()).is_err());
}

#[test]
fn requests_reject_ambiguous_identity_secrets_and_input_derived_diagnostics() {
    for (field, value) in [
        ("password", json!(null)),
        ("password", json!("short")),
        ("password", json!("x".repeat(257))),
        ("password", json!("\n".repeat(30))),
        ("operation", json!("PRIVATE-SENTINEL")),
        ("control_epoch", json!(-1)),
        ("lease_id", json!("lease_a/b")),
        ("extra", json!(true)),
    ] {
        let mut bad = wire(Operation::Open, 10);
        bad[field] = value;
        let error = Request::parse(&serde_json::to_vec(&bad).unwrap())
            .err()
            .unwrap();
        assert_eq!(
            error.to_string(),
            "invalid or stale account lifecycle request"
        );
    }
    for (uid, sid, name) in [
        (0, "S-1-5-18", "vca0123456789ab"),
        (1000, "S-1-5-21-1-2-3-1001", "vca0123456789ab"),
        (0, "", "vca0123456789ab"),
        (999, "", "vca0123456789ab"),
        (1001, "", "vcw0123456789ab"),
        (0, "S-1-5-21-01-2-3-1001", "vca0123456789ab"),
        (1001, "", "vca0123456789AB"),
    ] {
        assert!(AccountIdentity {
            username: name.into(),
            uid,
            sid: sid.into()
        }
        .validate()
        .is_err());
    }
    assert!(AccountIdentity {
        username: "vca0123456789ab".into(),
        uid: 1001,
        sid: String::new()
    }
    .validate()
    .is_ok());
    let receipt = serde_json::to_string(&open()).unwrap();
    assert!(!receipt.contains("password") && !receipt.contains("disposable"));
    let duplicate = r#"{"schema_version":1,"schema_version":1}"#;
    assert!(Request::parse(duplicate.as_bytes()).is_err());
}
