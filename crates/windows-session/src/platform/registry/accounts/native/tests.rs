use super::super::tests::{password, password_works, Fixture};
use super::*;
use vc_workspace_guest_lifecycle::{native::Phase, Zeroizing};

pub(super) fn store(f: &Fixture) -> NativeAccounts {
    NativeAccounts {
        inner: AgentAccounts {
            root: f.store.root.clone(),
            namespace: Namespace::Native,
        },
    }
}

pub(super) fn payload(
    f: &Fixture,
    operation: Operation,
    revision: u64,
    connection: &str,
    expiry: u64,
    secret: Option<&str>,
) -> Zeroizing<Vec<u8>> {
    Zeroizing::new(
        serde_json::to_vec(&serde_json::json!({
            "schema_version":1,"identity":{"username":f.name,"uid":0,"sid":f.sid.as_ref().unwrap()},
            "operation":operation,"revision":revision,"connection_id":connection,
            "expires_unix_seconds":expiry,"password":secret
        }))
        .unwrap(),
    )
}

fn apply(
    f: &Fixture,
    operation: Operation,
    revision: u64,
    connection: &str,
    expiry: u64,
    secret: Option<&str>,
) -> io::Result<Fence> {
    let raw = payload(f, operation, revision, connection, expiry, secret);
    store(f).apply_credential(&f.name, &Request::parse(&raw)?)
}

const FIRST: &str = "conn_native_test_first";
const SECOND: &str = "conn_native_test_second";

#[test]
#[ignore = "explicit isolated SYSTEM Native account lifecycle fixture"]
fn native_retirement_rotates_password_without_disabling_retained_account() {
    let mut f = Fixture::new_namespace(Namespace::Native);
    let owned = f.provision();
    let expiry = unix_seconds().unwrap() + 3600;
    let first = password();
    let issued = apply(&f, Operation::Issue, 1, FIRST, expiry, Some(&first)).unwrap();
    assert!(password_works(&f, &first));
    assert_eq!(store(&f).reconcile_expired().unwrap(), 0);
    assert!(apply(&f, Operation::Issue, 1, FIRST, expiry, Some(&first)).is_err());
    let retired = apply(&f, Operation::Retire, 2, FIRST, expiry, None).unwrap();
    assert_eq!(retired.phase, Phase::Retired);
    assert!(!password_works(&f, &first));
    let state = store(&f).observe(&f.name).unwrap();
    assert_eq!(state.sid, owned.sid);
    assert_eq!(state.disabled, Some(false));
    assert_eq!(state.expires_unix_seconds, Some(expiry as u32));
    assert_eq!(store(&f).reconcile_expired().unwrap(), 0);
    assert_eq!(
        apply(&f, Operation::Retire, 2, FIRST, expiry, None).unwrap(),
        retired
    );
    let next_secret = password();
    let next = apply(&f, Operation::Issue, 3, SECOND, expiry, Some(&next_secret)).unwrap();
    assert!(password_works(&f, &next_secret));
    assert!(apply(&f, Operation::Retire, 2, FIRST, expiry, None).is_err());
    assert!(apply(&f, Operation::Revoke, 2, FIRST, 0, None).is_err());
    assert_eq!(store(&f).observe(&f.name).unwrap().lifecycle, Some(next));
    let closed = apply(&f, Operation::Revoke, 4, SECOND, 0, None).unwrap();
    assert_eq!(closed.phase, Phase::Revoked);
    assert!(!password_works(&f, &next_secret));
    assert_eq!(store(&f).observe(&f.name).unwrap().disabled, Some(true));
    assert_eq!(
        apply(&f, Operation::Revoke, 4, SECOND, 0, None).unwrap(),
        closed
    );
    assert!(apply(
        &f,
        Operation::Issue,
        issued.revision,
        FIRST,
        expiry,
        Some(&first)
    )
    .is_err());
}

#[test]
#[ignore = "explicit isolated SYSTEM Native account lifecycle fixture"]
fn native_namespaces_do_not_adopt_agent_or_unowned_native_accounts() {
    let mut agent = Fixture::new_namespace(Namespace::Agent);
    let a = agent.provision();
    assert!(store(&agent).provision(&agent.name).is_err());
    assert_eq!(agent.store.verify(&agent.name).unwrap(), a);
    let mut foreign = Fixture::new_namespace(Namespace::Native);
    let external = foreign.external();
    assert!(store(&foreign).provision(&foreign.name).is_err());
    assert!(store(&foreign).observe(&foreign.name).is_err());
    assert!(store(&foreign).reconcile_expired().is_err());
    assert_eq!(local(&foreign.name).unwrap().unwrap().account, external);
    let mut f = Fixture::new_namespace(Namespace::Native);
    f.provision();
    let agent_store = AgentAccounts {
        root: f.store.root.clone(),
        namespace: Namespace::Agent,
    };
    assert!(agent_store.provision(&f.name).is_err());
    assert!(agent_store.observe(&f.name).is_err());
    assert!(agent_store.disable(&f.name).is_err());
    assert!(f
        .store
        .enable(&f.name, (unix_seconds().unwrap() + 3600) as u32)
        .is_err());
    let key = f.key();
    write_value(&key, "Lifecycle", b"{}").unwrap();
    assert!(store(&f).observe(&f.name).is_err());
    assert!(apply(
        &f,
        Operation::Issue,
        1,
        FIRST,
        unix_seconds().unwrap() + 3600,
        Some(&password())
    )
    .is_err());
    assert!(store(&f).reconcile_expired().is_err());
    assert!(local(&f.name).unwrap().unwrap().account.disabled);
    assert_eq!(read_value(&key, "Lifecycle").unwrap().1, b"{}");
}

#[test]
#[ignore = "explicit isolated SYSTEM Native account lifecycle fixture"]
fn native_gate_identity_and_corrupt_history_fail_closed() {
    let mut f = Fixture::new_namespace(Namespace::Native);
    f.provision();
    let secret = password();
    let expiry = unix_seconds().unwrap() + 3600;
    let raw = payload(&f, Operation::Issue, 1, FIRST, expiry, Some(&secret));
    let request = Request::parse(&raw).unwrap();
    let gate = account_gate(&f.name).unwrap();
    assert!(store(&f).apply_credential(&f.name, &request).is_err());
    assert!(store(&f).observe(&f.name).is_err());
    drop(gate);
    check(unsafe { ImpersonateSelf(SecurityImpersonation) }).unwrap();
    let rejected = store(&f).apply_credential(&f.name, &request).is_err();
    check(unsafe { RevertToSelf() }).unwrap();
    assert!(rejected);
    let mut wrong = Request::parse(&raw).unwrap();
    wrong.identity.sid = "S-1-5-21-1-2-3-1999".into();
    assert!(store(&f).apply_credential(&f.name, &wrong).is_err());
    store(&f).apply_credential(&f.name, &request).unwrap();
    write_value(&f.key(), VALUE, b"{}").unwrap();
    assert!(store(&f).observe(&f.name).is_err());
    assert!(apply(&f, Operation::Revoke, 2, FIRST, 0, None).is_err());
    assert!(store(&f).reconcile_expired().is_err());
    assert!(local(&f.name).unwrap().unwrap().account.disabled);
    assert_eq!(read_value(&f.key(), VALUE).unwrap().1, b"{}");
}

#[test]
#[ignore = "explicit isolated SYSTEM Native account lifecycle fixture"]
fn native_deleted_sid_tombstone_never_rebinds_a_replacement() {
    let mut f = Fixture::new_namespace(Namespace::Native);
    let original = f.provision();
    f.delete_account();
    f.sid = Some(original.sid.clone());
    apply(&f, Operation::Revoke, 2, FIRST, 0, None).unwrap();
    let absent = store(&f).observe(&f.name).unwrap();
    assert_eq!(absent.sid, original.sid);
    assert_eq!(absent.disabled, None);
    assert!(absent.sessions.is_empty());
    assert!(store(&f).provision(&f.name).is_err());
    let replacement = f.external();
    assert_ne!(replacement.sid, original.sid);
    assert!(store(&f).provision(&f.name).is_err());
    assert!(store(&f).observe(&f.name).is_err());
    assert!(store(&f).reconcile_expired().is_err());
    assert!(apply(&f, Operation::Revoke, 3, FIRST, 0, None).is_err());
    assert_eq!(local(&f.name).unwrap().unwrap().account, replacement);
}

#[test]
#[ignore = "explicit isolated SYSTEM Native account lifecycle fixture"]
fn native_local_expiry_terminalizes_without_resurrecting_old_credentials() {
    let mut f = Fixture::new_namespace(Namespace::Native);
    f.provision();
    let secret = password();
    let expiry = unix_seconds().unwrap() + 3;
    apply(&f, Operation::Issue, 1, FIRST, expiry, Some(&secret)).unwrap();
    while unix_seconds().unwrap() <= expiry {
        thread::sleep(Duration::from_millis(100));
    }
    assert_eq!(store(&f).reconcile_expired().unwrap(), 1);
    let state = store(&f).observe(&f.name).unwrap();
    assert_eq!(state.disabled, Some(true));
    let fence = state.lifecycle.unwrap();
    assert_eq!(fence.phase, Phase::Revoked);
    assert_eq!(fence.revision, 1);
    let expiry = unix_seconds().unwrap() + 3600;
    assert!(apply(&f, Operation::Issue, 1, FIRST, expiry, Some(&secret)).is_err());
    apply(&f, Operation::Issue, 2, SECOND, expiry, Some(&secret)).unwrap();
    assert!(password_works(&f, &secret));
    assert_eq!(store(&f).reconcile_expired().unwrap(), 0);
    apply(&f, Operation::Revoke, 3, SECOND, 0, None).unwrap();
}

pub(crate) fn crash_during_credential() {
    use std::io::Read;
    require_system().unwrap();
    assert_eq!(
        std::env::var("VC_WORKSPACE_NATIVE_ACCOUNT_FIXTURE").as_deref(),
        Ok("isolated")
    );
    let root = std::env::var("VCW_NATIVE_CRASH_ROOT").unwrap();
    let suffix = root
        .strip_prefix("VCWorkspace.NativeAccountsV1.Test.")
        .unwrap();
    assert!(suffix.len() == 32 && suffix.bytes().all(|b| b.is_ascii_hexdigit()));
    let point = std::env::var("VCW_NATIVE_CRASH_POINT")
        .unwrap()
        .parse::<usize>()
        .unwrap();
    assert!(point == 1 || point == 2);
    let mut raw = Zeroizing::new(Vec::new());
    std::io::stdin().take(4097).read_to_end(&mut raw).unwrap();
    let request = Request::parse(&raw).unwrap();
    assert_eq!(request.identity.username, format!("vcw{}", &suffix[..12]));
    let native = NativeAccounts {
        inner: AgentAccounts {
            root,
            namespace: Namespace::Native,
        },
    };
    let mut reached = 0;
    native
        .apply_checkpoints(&request.identity.username, &request, || {
            reached += 1;
            if reached == point {
                unsafe { ExitProcess(86) };
            }
        })
        .unwrap();
    panic!("requested Native process-exit checkpoint was not reached");
}

#[test]
#[ignore = "explicit isolated SYSTEM Native account lifecycle fixture"]
fn native_process_exit_recovers_pending_issue_and_retirement() {
    use std::io::Write;
    use std::process::{Command, Stdio};
    for (operation, point) in [
        (Operation::Issue, 1),
        (Operation::Issue, 2),
        (Operation::Retire, 1),
        (Operation::Retire, 2),
    ] {
        let mut f = Fixture::new_namespace(Namespace::Native);
        f.provision();
        let secret = password();
        let expiry = unix_seconds().unwrap() + 3600;
        let revision = if operation == Operation::Retire {
            apply(&f, Operation::Issue, 1, FIRST, expiry, Some(&secret)).unwrap();
            2
        } else {
            1
        };
        let raw = payload(
            &f,
            operation,
            revision,
            FIRST,
            expiry,
            if operation == Operation::Issue {
                Some(&secret)
            } else {
                None
            },
        );
        let mut child = Command::new(std::env::current_exe().unwrap())
            .args([
                "--exact",
                "tests::native_account_crash_child",
                "--ignored",
                "--test-threads=1",
            ])
            .env("VCW_NATIVE_CRASH_ROOT", &f.store.root)
            .env("VCW_NATIVE_CRASH_POINT", point.to_string())
            .stdin(Stdio::piped())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .unwrap();
        child.stdin.take().unwrap().write_all(&raw).unwrap();
        let until = Instant::now() + Duration::from_secs(10);
        let code = loop {
            if let Some(status) = child.try_wait().unwrap() {
                break status.code();
            }
            if Instant::now() >= until {
                child.kill().unwrap();
                child.wait().unwrap();
                panic!("Native crash fixture deadline");
            }
            thread::sleep(Duration::from_millis(25));
        };
        assert_eq!(code, Some(86));
        let state = store(&f).observe(&f.name).unwrap();
        assert_eq!(
            state.disabled,
            Some(operation == Operation::Issue && point == 1)
        );
        assert_eq!(
            state.lifecycle.unwrap().phase,
            if operation == Operation::Issue {
                Phase::Issuing
            } else {
                Phase::Retiring
            }
        );
        assert!(store(&f)
            .apply_credential(&f.name, &Request::parse(&raw).unwrap())
            .is_err());
        assert_eq!(store(&f).reconcile_expired().unwrap(), 1);
        let stopped = store(&f).observe(&f.name).unwrap();
        assert_eq!(stopped.disabled, Some(true));
        let fence = stopped.lifecycle.unwrap();
        assert_eq!(fence.phase, Phase::Revoked);
        assert_eq!(fence.revision, revision);
        assert!(stopped.sessions.is_empty());
        assert!(store(&f)
            .apply_credential(&f.name, &Request::parse(&raw).unwrap())
            .is_err());
    }
}
