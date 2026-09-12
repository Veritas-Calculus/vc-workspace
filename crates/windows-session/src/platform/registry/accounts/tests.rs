use super::*;
use vc_workspace_guest_lifecycle::{Operation, Phase, Request, Zeroizing};
use windows_sys::Wdk::System::Registry::NtDeleteKey;

fn lease_payload(
    f: &Fixture,
    operation: Operation,
    epoch: u64,
    expiry: u64,
    password: Option<&str>,
) -> Zeroizing<Vec<u8>> {
    Zeroizing::new(
        serde_json::to_vec(&serde_json::json!({"schema_version":1,
        "identity":{"username":f.name,"uid":0,"sid":f.sid.as_ref().unwrap()},
        "lease_id":format!("lease_test_{epoch}"),"control_epoch":epoch,"login_generation":1,
        "expires_unix_seconds":expiry,"operation":operation,"password":password}))
        .unwrap(),
    )
}

fn apply(
    f: &Fixture,
    operation: Operation,
    epoch: u64,
    expiry: u64,
    password: Option<&str>,
) -> io::Result<vc_workspace_guest_lifecycle::Fence> {
    let payload = lease_payload(f, operation, epoch, expiry, password);
    f.store.apply_lease(&f.name, &Request::parse(&payload)?)
}

pub(super) fn password() -> Zeroizing<String> {
    Zeroizing::new(format!("Vcw1!{}", nonce()))
}

// Network logon verifies the actual SAM password without loading a Profile,
// creating a WTS desktop or granting this test an interactive user session.
pub(super) fn password_works(f: &Fixture, password: &str) -> bool {
    let secret = Zeroizing::new(wide(password));
    let mut token = null_mut();
    if unsafe {
        LogonUserW(
            wide(&f.name).as_ptr(),
            wide(".").as_ptr(),
            secret.as_ptr(),
            LOGON32_LOGON_NETWORK,
            LOGON32_PROVIDER_DEFAULT,
            &mut token,
        )
    } == 0
    {
        return false;
    }
    let token = own(token).unwrap();
    assert_eq!(
        token_identity(token.as_raw_handle()).unwrap().sid,
        *f.sid.as_ref().unwrap()
    );
    true
}

#[test]
#[ignore = "explicit isolated SYSTEM account lifecycle fixture"]
fn lease_fence_rejects_late_mutations_and_retires_bootstrap_password() {
    let mut f = Fixture::new();
    f.provision();
    let expiry = unix_seconds().unwrap() + 3600;
    let first_password = password();
    let delayed = lease_payload(&f, Operation::Open, 10, expiry, Some(&first_password));
    apply(&f, Operation::Open, 10, expiry, Some(&first_password)).unwrap();
    assert!(password_works(&f, &first_password));
    let current_password = password();
    let current = apply(&f, Operation::Open, 11, expiry, Some(&current_password)).unwrap();
    assert!(password_works(&f, &current_password));
    assert!(f
        .store
        .apply_lease(&f.name, &Request::parse(&delayed).unwrap())
        .is_err());
    assert!(apply(&f, Operation::Seal, 10, expiry, None).is_err());
    assert!(apply(&f, Operation::Revoke, 10, 0, None).is_err());
    assert!(f.store.enable(&f.name, expiry as u32).is_err());
    assert!(f.store.disable(&f.name).is_err());
    assert_eq!(f.store.observe(&f.name).unwrap().lifecycle, Some(current));
    assert!(password_works(&f, &current_password));
    let sealed = apply(&f, Operation::Seal, 11, expiry, None).unwrap();
    assert_eq!(sealed.phase, Phase::Sealed);
    assert!(!password_works(&f, &current_password));
    assert_eq!(
        apply(&f, Operation::Seal, 11, expiry, None).unwrap(),
        sealed
    );
    assert!(apply(&f, Operation::Open, 11, expiry, Some(&current_password)).is_err());
    apply(&f, Operation::Revoke, 11, 0, None).unwrap();
    assert!(f.store.observe(&f.name).unwrap().disabled.unwrap());
    assert!(apply(&f, Operation::Seal, 11, expiry, None).is_err());
    assert!(apply(&f, Operation::Open, 11, expiry, Some(&current_password)).is_err());
    let next = apply(&f, Operation::Open, 12, expiry, Some(&first_password)).unwrap();
    assert!(apply(&f, Operation::Revoke, 11, 0, None).is_err());
    assert_eq!(f.store.observe(&f.name).unwrap().lifecycle, Some(next));
    assert!(password_works(&f, &first_password));
    apply(&f, Operation::Revoke, 12, 0, None).unwrap();
}

pub(crate) fn crash_during_lease() {
    use std::io::Read;
    require_system().unwrap();
    let root = std::env::var("VCW_ACCOUNT_CRASH_ROOT").unwrap();
    let suffix = root
        .strip_prefix("VCWorkspace.AgentAccountsV2.Test.")
        .unwrap();
    assert!(suffix.len() == 32 && suffix.bytes().all(|b| b.is_ascii_hexdigit()));
    let point = std::env::var("VCW_ACCOUNT_CRASH_POINT")
        .unwrap()
        .parse::<usize>()
        .unwrap();
    assert!(point == 1 || point == 2);
    let mut raw = Zeroizing::new(Vec::new());
    std::io::stdin().take(4097).read_to_end(&mut raw).unwrap();
    let request = Request::parse(&raw).unwrap();
    assert_eq!(request.identity.username, format!("vca{}", &suffix[..12]));
    let store = AgentAccounts {
        root,
        namespace: Namespace::Agent,
    };
    let mut reached = 0;
    store
        .apply_lease_checkpoints(&request.identity.username, &request, || {
            reached += 1;
            if reached == point {
                unsafe { ExitProcess(86) };
            }
        })
        .unwrap();
    panic!("requested process-exit checkpoint was not reached");
}

#[test]
#[ignore = "explicit isolated SYSTEM account lifecycle fixture"]
fn lease_fence_crash_recovery_closes_uncommitted_sam_changes() {
    use std::io::Write;
    use std::process::{Command, Stdio};
    let mut f = Fixture::new();
    f.provision();
    let expiry = unix_seconds().unwrap() + 3600;
    let secret = password();
    for (epoch, operation, point, enabled) in [
        (10, Operation::Open, 1, false),
        (11, Operation::Open, 2, true),
        (12, Operation::Seal, 2, true),
    ] {
        if operation == Operation::Seal {
            apply(&f, Operation::Open, epoch, expiry, Some(&secret)).unwrap();
        }
        let payload = lease_payload(
            &f,
            operation,
            epoch,
            expiry,
            if operation == Operation::Open {
                Some(&secret)
            } else {
                None
            },
        );
        let mut child = Command::new(std::env::current_exe().unwrap())
            .args([
                "--exact",
                "tests::account_lease_crash_child",
                "--ignored",
                "--test-threads=1",
            ])
            .env("VCW_ACCOUNT_CRASH_ROOT", &f.store.root)
            .env("VCW_ACCOUNT_CRASH_POINT", point.to_string())
            .stdin(Stdio::piped())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .unwrap();
        child.stdin.take().unwrap().write_all(&payload).unwrap();
        let until = Instant::now() + Duration::from_secs(10);
        let code = loop {
            if let Some(status) = child.try_wait().unwrap() {
                break status.code();
            }
            if Instant::now() >= until {
                child.kill().unwrap();
                child.wait().unwrap();
                panic!("crash fixture deadline");
            }
            thread::sleep(Duration::from_millis(25));
        };
        assert_eq!(code, Some(86));
        let pending = f.store.observe(&f.name).unwrap();
        assert_eq!(pending.disabled, Some(!enabled));
        assert_eq!(
            pending.lifecycle.as_ref().unwrap().phase,
            if operation == Operation::Open {
                Phase::Opening
            } else {
                Phase::Sealing
            }
        );
        assert!(f.store.lock_enabled(&f.name).is_err());
        assert!(f
            .store
            .apply_lease(&f.name, &Request::parse(&payload).unwrap())
            .is_err());
        assert_eq!(f.store.reconcile_expired().unwrap(), 1);
        let stopped = f.store.observe(&f.name).unwrap();
        assert_eq!(stopped.disabled, Some(true));
        assert_eq!(stopped.lifecycle.as_ref().unwrap().phase, Phase::Revoked);
        assert!(stopped.sessions.is_empty());
        assert!(apply(&f, Operation::Open, epoch, expiry, Some(&secret)).is_err());
    }
}

#[test]
#[ignore = "explicit isolated SYSTEM account lifecycle fixture"]
fn lease_fence_rejects_rebound_corrupt_and_concurrent_mutations() {
    let mut f = Fixture::new();
    f.provision();
    let secret = password();
    let expiry = unix_seconds().unwrap() + 3600;
    let payload = lease_payload(&f, Operation::Open, 10, expiry, Some(&secret));
    let gate = account_gate(&f.name).unwrap();
    assert!(f
        .store
        .apply_lease(&f.name, &Request::parse(&payload).unwrap())
        .is_err());
    drop(gate);
    check(unsafe { ImpersonateSelf(SecurityImpersonation) }).unwrap();
    let rejected = f
        .store
        .apply_lease(&f.name, &Request::parse(&payload).unwrap())
        .is_err();
    check(unsafe { RevertToSelf() }).unwrap();
    assert!(rejected);
    let mut wrong = Request::parse(&payload).unwrap();
    wrong.identity.sid = "S-1-5-21-1-2-3-1999".into();
    assert!(f.store.apply_lease(&f.name, &wrong).is_err());
    let fence = apply(&f, Operation::Open, 10, expiry, Some(&secret)).unwrap();
    let before = local(&f.name).unwrap().unwrap().account;
    write_value(&f.key(), "Lifecycle", b"{}").unwrap();
    assert!(apply(&f, Operation::Open, 11, expiry, Some(&secret)).is_err());
    assert!(apply(&f, Operation::Revoke, 11, 0, None).is_err());
    assert!(f.store.enable(&f.name, expiry as u32).is_err());
    assert!(f.store.disable(&f.name).is_err());
    assert_eq!(local(&f.name).unwrap().unwrap().account, before);
    assert!(f.store.reconcile_expired().is_err());
    assert!(local(&f.name).unwrap().unwrap().account.disabled);
    assert_eq!(read_value(&f.key(), "Lifecycle").unwrap().1, b"{}");
    write_value(&f.key(), "Lifecycle", &serde_json::to_vec(&fence).unwrap()).unwrap();
    f.delete_account();
    // A known deleted account can still persist a newer revocation tombstone.
    f.sid = Some(fence.identity.sid.clone());
    apply(&f, Operation::Revoke, 11, 0, None).unwrap();
    let replacement = f.external();
    assert_ne!(replacement.sid, fence.identity.sid);
    assert!(f
        .store
        .apply_lease(&f.name, &Request::parse(&payload).unwrap())
        .is_err());
    assert!(apply(&f, Operation::Revoke, 12, 0, None).is_err());
    assert_eq!(local(&f.name).unwrap().unwrap().account, replacement);
}

#[test]
#[ignore = "explicit isolated SYSTEM account lifecycle fixture"]
fn lease_expiry_terminalizes_the_version_before_account_cleanup() {
    let mut f = Fixture::new();
    f.provision();
    let secret = password();
    let expiry = unix_seconds().unwrap() + 3;
    apply(&f, Operation::Open, 10, expiry, Some(&secret)).unwrap();
    while unix_seconds().unwrap() <= expiry {
        thread::sleep(Duration::from_millis(100));
    }
    assert_eq!(f.store.reconcile_expired().unwrap(), 1);
    let state = f.store.observe(&f.name).unwrap();
    assert_eq!(state.disabled, Some(true));
    assert_eq!(state.lifecycle.unwrap().phase, Phase::Revoked);
    let next_expiry = unix_seconds().unwrap() + 3600;
    assert!(apply(&f, Operation::Open, 10, next_expiry, Some(&secret)).is_err());
    apply(&f, Operation::Open, 11, next_expiry, Some(&secret)).unwrap();
    assert_eq!(f.store.reconcile_expired().unwrap(), 0);
    assert!(password_works(&f, &secret));
    apply(&f, Operation::Revoke, 11, 0, None).unwrap();
}

fn logoff_identity(authentication_id: u64) -> Identity {
    Identity {
        sid: "S-1-5-21-1-2-3-1001".into(),
        session_id: 2,
        authentication_id,
    }
}

#[test]
fn asynchronous_logoff_dispatches_once_per_exact_incarnation() {
    let identity = logoff_identity(1);
    let mut requested = Vec::new();
    let mut dispatched = Vec::new();
    for _ in 0..3 {
        dispatch_logoffs_once(
            &[identity.clone(), identity.clone()],
            &mut requested,
            |id| {
                dispatched.push(id.clone());
                Ok(true)
            },
        )
        .unwrap();
    }
    assert_eq!(dispatched, vec![identity.clone()]);
    let renewed = logoff_identity(2);
    let mut different_sid = identity.clone();
    different_sid.sid = "S-1-5-21-1-2-3-1002".into();
    let mut different_session = identity.clone();
    different_session.session_id = 3;
    dispatch_logoffs_once(
        &[
            renewed.clone(),
            different_sid.clone(),
            different_session.clone(),
        ],
        &mut requested,
        |id| {
            dispatched.push(id.clone());
            Ok(true)
        },
    )
    .unwrap();
    assert_eq!(
        dispatched,
        vec![identity, renewed, different_sid, different_session]
    );
}

#[test]
fn failed_or_raced_logoff_dispatch_is_not_acknowledged() {
    let sessions = [logoff_identity(1)];
    let mut requested = Vec::new();
    dispatch_logoffs_once(&sessions, &mut requested, |_| Ok(false)).unwrap();
    assert!(requested.is_empty());
    let error = dispatch_logoffs_once(&sessions, &mut requested, |_| {
        Err(io::Error::from_raw_os_error(ERROR_ACCESS_DENIED as i32))
    })
    .unwrap_err();
    assert_eq!(error.raw_os_error(), Some(ERROR_ACCESS_DENIED as i32));
    assert!(requested.is_empty());
    dispatch_logoffs_once(&sessions, &mut requested, |_| Ok(true)).unwrap();
    assert_eq!(requested, sessions);
}

#[test]
fn logoff_incarnation_tracking_is_bounded_without_repeating_known_dispatches() {
    let mut requested: Vec<_> = (0..1024).map(logoff_identity).collect();
    dispatch_logoffs_once(&[logoff_identity(0)], &mut requested, |_| {
        panic!("must not repeat an acknowledged asynchronous logoff")
    })
    .unwrap();
    assert!(
        dispatch_logoffs_once(&[logoff_identity(1024)], &mut requested, |_| {
            panic!("must not exceed the incarnation bound")
        })
        .is_err()
    );
    assert_eq!(requested.len(), 1024);
}

#[test]
fn only_explicit_missing_logon_errors_are_absence() {
    for code in [ERROR_NO_TOKEN, ERROR_CTX_WINSTATION_NOT_FOUND] {
        assert!(logon_disappeared(&io::Error::from_raw_os_error(
            code as i32
        )));
    }
    for code in [ERROR_ACCESS_DENIED, ERROR_INVALID_HANDLE, ERROR_TIMEOUT] {
        assert!(!logon_disappeared(&io::Error::from_raw_os_error(
            code as i32
        )));
    }
    assert!(!logon_disappeared(&io::Error::other(
        "no logged-on session"
    )));
}

#[test]
fn login_window_requires_a_future_bounded_deadline() {
    assert!(!login_window_open(0, 0));
    assert!(!login_window_open(100, 100));
    assert!(!login_window_open(99, 100));
    assert!(!login_window_open(u32::MAX, 100));
    assert!(login_window_open(101, 100));
    assert!(!login_window_open(u32::MAX - 1, u64::MAX));
}

fn fixture_expiry(name: &str, expiry: u32) {
    let info = USER_INFO_1017 {
        usri1017_acct_expires: expiry,
    };
    status(unsafe {
        NetUserSetInfo(
            null(),
            wide(name).as_ptr(),
            1017,
            (&info as *const USER_INFO_1017).cast(),
            null_mut(),
        )
    })
    .unwrap();
}

#[test]
#[ignore = "explicit isolated SYSTEM account lifecycle fixture"]
fn account_observation_retains_deleted_sid_and_reports_real_sam_expiry() {
    let mut fixture = Fixture::new();
    assert!(fixture.store.observe(&fixture.name).is_err());
    let owned = fixture.provision();
    let state = fixture.store.observe(&fixture.name).unwrap();
    assert_eq!(state.username, fixture.name);
    assert_eq!(state.sid, owned.sid);
    assert_eq!(state.disabled, Some(true));
    assert!(state.expires_unix_seconds.is_some());
    assert!(state.sessions.is_empty());
    let expiry = (unix_seconds().unwrap() + 3600) as u32;
    fixture.store.enable(&fixture.name, expiry).unwrap();
    let state = fixture.store.observe(&fixture.name).unwrap();
    assert_eq!(state.disabled, Some(false));
    assert_eq!(state.expires_unix_seconds, Some(expiry));
    assert!(state.sessions.is_empty());
    fixture.delete_account();
    let state = fixture.store.observe(&fixture.name).unwrap();
    assert_eq!(state.sid, owned.sid);
    assert_eq!(state.disabled, None);
    assert_eq!(state.expires_unix_seconds, None);
    assert!(state.sessions.is_empty());
    let replacement = fixture.external();
    assert_ne!(replacement.sid, owned.sid);
    assert!(fixture.store.observe(&fixture.name).is_err());
    assert_eq!(local(&fixture.name).unwrap().unwrap().account, replacement);
}

#[test]
#[ignore = "explicit isolated SYSTEM account lifecycle fixture"]
fn account_observation_never_adopts_unowned_or_unbound_namesakes() {
    let mut fixture = Fixture::new();
    let external = fixture.external();
    assert!(fixture.store.observe(&fixture.name).is_err());
    let pending = Record {
        schema_version: 1,
        username: fixture.name.clone(),
        nonce: nonce(),
        sid: None,
    };
    write_record(&fixture.key(), &pending).unwrap();
    assert!(fixture.store.observe(&fixture.name).is_err());
    assert_eq!(local(&fixture.name).unwrap().unwrap().account, external);
    assert!(fixture
        .store
        .record(&fixture.key(), &fixture.name)
        .unwrap()
        .unwrap()
        .sid
        .is_none());
}

#[test]
#[ignore = "explicit isolated SYSTEM account lifecycle fixture"]
fn expired_account_reconciliation_preserves_sid_and_rechecks_renewal_under_gate() {
    let mut f = Fixture::new();
    assert_eq!(f.store.reconcile_expired().unwrap(), 0);
    let original = f.provision();
    let future = (unix_seconds().unwrap() + 3600) as u32;
    f.store.enable(&f.name, future).unwrap();
    assert_eq!(f.store.reconcile_expired().unwrap(), 0);
    fixture_expiry(&f.name, 1);
    assert!(f.store.lock_enabled(&f.name).is_err());
    let gate = account_gate(&f.name).unwrap();
    assert!(f.store.reconcile_expired().is_err());
    assert!(!local(&f.name).unwrap().unwrap().account.disabled);
    drop(gate);
    // A renewal completed before the next tick must not be disabled using the
    // previous observation, even though this account was previously expired.
    f.store.enable(&f.name, future).unwrap();
    assert_eq!(f.store.reconcile_expired().unwrap(), 0);
    fixture_expiry(&f.name, 1);
    assert_eq!(f.store.reconcile_expired().unwrap(), 1);
    assert_eq!(f.store.verify(&f.name).unwrap(), original);
    assert_eq!(f.store.reconcile_expired().unwrap(), 1);
    f.store.enable(&f.name, future).unwrap();
    fixture_expiry(&f.name, u32::MAX);
    assert!(f.store.lock_enabled(&f.name).is_err());
    assert_eq!(f.store.reconcile_expired().unwrap(), 1);
    assert_eq!(f.store.verify(&f.name).unwrap(), original);
}

#[test]
#[ignore = "explicit isolated SYSTEM account lifecycle fixture"]
fn expiry_scan_does_not_adopt_unowned_or_replaced_accounts() {
    let mut foreign = Fixture::new();
    let external = foreign.external();
    assert_eq!(foreign.store.reconcile_expired().unwrap(), 0);
    assert_eq!(local(&foreign.name).unwrap().unwrap().account, external);
    let mut owned = Fixture::new();
    let original = owned.provision();
    owned.delete_account();
    assert_eq!(owned.store.reconcile_expired().unwrap(), 1);
    let replacement = owned.external();
    assert_ne!(original.sid, replacement.sid);
    assert!(owned.store.reconcile_expired().is_err());
    assert_eq!(local(&owned.name).unwrap().unwrap().account, replacement);
}

fn nonce() -> String {
    let mut value = [0; 16];
    getrandom::fill(&mut value).unwrap();
    value.iter().map(|b| format!("{b:02x}")).collect()
}

pub(super) struct Fixture {
    pub(super) store: AgentAccounts,
    pub(super) name: String,
    comment: String,
    pub(super) sid: Option<String>,
}
impl Fixture {
    fn new() -> Self {
        Self::new_namespace(Namespace::Agent)
    }

    pub(super) fn new_namespace(namespace: Namespace) -> Self {
        assert_eq!(
            std::env::var("VC_WORKSPACE_NATIVE_ACCOUNT_FIXTURE").as_deref(),
            Ok("isolated")
        );
        require_system().expect("mutating account fixture requires SYSTEM");
        let marker = nonce();
        let fixture = Self {
            store: AgentAccounts {
                namespace,
                root: format!(
                    "{}.Test.{marker}",
                    match namespace {
                        Namespace::Agent => ACCOUNT_ROOT,
                        Namespace::Native => native::ROOT,
                    }
                ),
            },
            name: format!(
                "{}{}",
                match namespace {
                    Namespace::Agent => "vca",
                    Namespace::Native => "vcw",
                },
                &marker[..12]
            ),
            comment: format!("VCWAccountTest-{marker}"),
            sid: None,
        };
        assert!(local(&fixture.name).unwrap().is_none());
        eprintln!(
            "owned account fixture: {} {}",
            fixture.name, fixture.store.root
        );
        fixture
    }
    pub(super) fn provision(&mut self) -> AgentAccount {
        let account = self.store.provision(&self.name).unwrap();
        self.sid = Some(account.sid.clone());
        account
    }
    pub(super) fn external(&mut self) -> AgentAccount {
        create_local(&self.name, &self.comment).unwrap();
        let account = local(&self.name).unwrap().unwrap().account;
        self.sid = Some(account.sid.clone());
        account
    }
    pub(super) fn key(&self) -> Key {
        self.store.key(&self.name, true).unwrap()
    }
    pub(super) fn delete_account(&mut self) {
        let actual = local(&self.name).unwrap().unwrap();
        assert_eq!(Some(&actual.account.sid), self.sid.as_ref());
        // These fixtures never log on or create a profile. Refuse deletion if
        // an unexpected interactive session appeared under the owned SID.
        assert!(sessions_for_sid(&actual.account.sid).unwrap().is_empty());
        status(unsafe { NetUserDel(null(), wide(&self.name).as_ptr()) }).unwrap();
        self.sid = None;
    }
}

#[test]
#[ignore = "creates an isolated local account; explicit SYSTEM acceptance only"]
fn helper_gate_requires_owned_enabled_account_and_serializes_disable() {
    let mut fixture = Fixture::new();
    assert!(fixture.store.lock_enabled(&fixture.name).is_err());
    let owned = fixture.provision();
    assert!(fixture.store.lock_enabled(&fixture.name).is_err());
    let expiry = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_secs() as u32
        + 3600;
    fixture.store.enable(&fixture.name, expiry).unwrap();
    let (account, gate) = fixture.store.lock_enabled(&fixture.name).unwrap();
    assert_eq!(account.sid, owned.sid);
    assert!(!account.disabled);
    assert!(fixture.store.disable(&fixture.name).is_err());
    drop(gate);
    assert!(
        fixture
            .store
            .disable(&fixture.name)
            .unwrap()
            .unwrap()
            .disabled
    );
    assert!(fixture.store.lock_enabled(&fixture.name).is_err());
}
impl Fixture {
    fn cleanup(&self) -> io::Result<()> {
        if let Some(actual) = local(&self.name)? {
            // Also recover a fixture assertion that failed immediately after
            // creation but before the test retained the resulting SID.
            let ours = self.sid.as_ref() == Some(&actual.account.sid)
                || actual.comment == self.comment
                || self
                    .store
                    .record(&self.store.key(&self.name, false)?, &self.name)?
                    .is_some_and(|r| {
                        r.sid.as_ref() == Some(&actual.account.sid)
                            || (r.sid.is_none()
                                && actual.account.disabled
                                && actual.comment
                                    == format!("{}{}", self.store.namespace.marker(), r.nonce))
                    });
            if !ours || !sessions_for_sid(&actual.account.sid)?.is_empty() {
                return Err(denied("refuse unrelated or logged-on account cleanup"));
            }
            status(unsafe { NetUserDel(null(), wide(&self.name).as_ptr()) })?;
        }
        for path in [
            format!(r"SOFTWARE\{}\{}", self.store.root, self.name),
            format!(r"SOFTWARE\{}", self.store.root),
        ] {
            let mut raw = null_mut();
            let code = unsafe {
                RegOpenKeyExW(
                    HKEY_LOCAL_MACHINE,
                    wide(&path).as_ptr(),
                    REG_OPTION_OPEN_LINK,
                    DELETE | KEY_WOW64_64KEY,
                    &mut raw,
                )
            };
            if code == ERROR_FILE_NOT_FOUND {
                continue;
            }
            status(code)?;
            let key = Key(raw);
            let code = unsafe { NtDeleteKey(key.0) };
            if code < 0 {
                return Err(io::Error::from_raw_os_error(
                    unsafe { RtlNtStatusToDosError(code) } as i32,
                ));
            }
        }
        Ok(())
    }
}
impl Drop for Fixture {
    fn drop(&mut self) {
        if let Err(error) = self.cleanup() {
            eprintln!(
                "retain exact fixture {} {} after cleanup failure: {error}",
                self.name, self.store.root
            );
            assert!(std::thread::panicking(), "account fixture did not clean up");
        }
    }
}

#[test]
fn account_journal_rejects_foreign_names_sids_and_schema() {
    let name = "vca0123456789ab";
    let record = Record {
        schema_version: 1,
        username: name.into(),
        nonce: "a".repeat(32),
        sid: None,
    };
    let valid = serde_json::to_value(&record).unwrap();
    assert!(parse_record(&serde_json::to_vec(&valid).unwrap(), name).is_ok());
    for (key, value) in [
        ("username", serde_json::json!("vca0123456789ac")),
        ("schema_version", serde_json::json!(2)),
        ("sid", serde_json::json!("S-1-5-18")),
        ("sid", serde_json::json!("S-1-5-21-1-2-3-500")),
        ("nonce", serde_json::json!("A".repeat(32))),
        ("extra", serde_json::json!(1)),
    ] {
        let mut bad = valid.clone();
        bad[key] = value;
        assert!(parse_record(&serde_json::to_vec(&bad).unwrap(), name).is_err());
    }
    for bad in [
        "vdi",
        "vcw0123456789ab",
        "vca0123456789AB",
        "vca0123456789/.",
    ] {
        assert!(agent_name(bad).is_err());
    }
}

#[test]
fn wts_listeners_and_session_zero_are_not_user_logons() {
    assert!(!session_may_own_token(65536, WTSListen));
    assert!(!session_may_own_token(0, WTSListen));
    for state in [
        WTSActive,
        WTSConnected,
        WTSDisconnected,
        WTSDown,
        WTSInit,
        WTSReset,
        WTSIdle,
        WTSConnectQuery,
        WTSShadow,
    ] {
        assert!(session_may_own_token(1, state));
        assert!(!session_may_own_token(0, state));
    }
}

#[test]
fn account_lifecycle_requires_non_impersonating_system() {
    let store = AgentAccounts::default();
    let name = "vca0123456789ab";
    if !current_identity().unwrap().is_system() {
        assert_eq!(
            store.provision(name).unwrap_err().kind(),
            io::ErrorKind::PermissionDenied
        );
        assert_eq!(
            store.verify(name).unwrap_err().kind(),
            io::ErrorKind::PermissionDenied
        );
        assert_eq!(
            store.observe(name).unwrap_err().kind(),
            io::ErrorKind::PermissionDenied
        );
        assert_eq!(
            store.enable(name, 0).unwrap_err().kind(),
            io::ErrorKind::PermissionDenied
        );
        assert_eq!(
            store.disable(name).unwrap_err().kind(),
            io::ErrorKind::PermissionDenied
        );
        return;
    }
    check(unsafe { ImpersonateSelf(SecurityImpersonation) }).unwrap();
    let denied_all = store.provision(name).is_err()
        && store.verify(name).is_err()
        && store.observe(name).is_err()
        && store.disable(name).is_err();
    check(unsafe { RevertToSelf() }).unwrap();
    assert!(denied_all);
}

#[test]
#[ignore = "explicit isolated SYSTEM account lifecycle fixture"]
fn owned_account_lifecycle_retains_sid_and_never_reactivates_by_provision() {
    let mut f = Fixture::new();
    let account = f.provision();
    assert!(account.disabled);
    assert_eq!(f.store.verify(&f.name).unwrap(), account);
    assert!(f.store.enable(&f.name, 0).is_err());
    assert!(f.store.enable(&f.name, u32::MAX).is_err());
    let expiry = (SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_secs()
        + 120) as u32;
    let enabled = f.store.enable(&f.name, expiry).unwrap();
    assert!(!enabled.disabled);
    assert_eq!(enabled.sid, account.sid);
    assert_eq!(f.provision(), enabled);
    assert_eq!(f.store.disable(&f.name).unwrap(), Some(account.clone()));
    assert_eq!(f.provision(), account);
    assert_eq!(f.store.disable(&f.name).unwrap(), Some(account.clone()));
    let mut groups = null_mut();
    let (mut read, mut total) = (0, 0);
    status(unsafe {
        NetUserGetLocalGroups(
            null(),
            wide(&f.name).as_ptr(),
            0,
            0,
            &mut groups,
            MAX_PREFERRED_LENGTH,
            &mut read,
            &mut total,
        )
    })
    .unwrap();
    let _groups = NetBuffer(groups);
    assert_eq!(read, total);
    assert_eq!(read, 2, "new account should have only Users and RDP Users");
}

#[test]
#[ignore = "explicit isolated SYSTEM account lifecycle fixture"]
fn existing_unowned_account_is_never_adopted_disabled_or_changed() {
    let mut f = Fixture::new();
    let original = f.external();
    assert!(f.store.provision(&f.name).is_err());
    assert!(f.store.verify(&f.name).is_err());
    assert!(f.store.disable(&f.name).is_err());
    assert!(f.store.enable(&f.name, 0).is_err());
    let actual = local(&f.name).unwrap().unwrap();
    assert_eq!(actual.account, original);
    assert_eq!(actual.comment, f.comment);
    assert!(f.store.record(&f.key(), &f.name).unwrap().is_none());
}

#[test]
#[ignore = "explicit isolated SYSTEM account lifecycle fixture"]
fn interrupted_creation_recovers_only_matching_disabled_account() {
    let mut f = Fixture::new();
    let mut record = Record {
        schema_version: 1,
        username: f.name.clone(),
        nonce: nonce(),
        sid: None,
    };
    write_record(&f.key(), &record).unwrap();
    create_local(&f.name, &format!("{MARKER_PREFIX}{}", record.nonce)).unwrap();
    let original = local(&f.name).unwrap().unwrap().account;
    f.sid = Some(original.sid.clone());
    assert_eq!(f.provision(), original);
    record.nonce = nonce();
    write_record(&f.key(), &record).unwrap();
    assert!(
        f.store.provision(&f.name).is_err(),
        "wrong creation marker was adopted"
    );
    assert!(f.store.disable(&f.name).is_err());
}

#[test]
#[ignore = "explicit isolated SYSTEM account lifecycle fixture"]
fn deleted_or_replaced_owned_account_is_not_recreated_or_rebound() {
    let mut f = Fixture::new();
    let original = f.provision();
    f.delete_account();
    assert!(f.store.provision(&f.name).is_err());
    assert!(local(&f.name).unwrap().is_none());
    let replacement = f.external();
    assert_ne!(original.sid, replacement.sid);
    assert!(f.store.provision(&f.name).is_err());
    assert!(f.store.verify(&f.name).is_err());
    assert!(f.store.disable(&f.name).is_err());
    assert_eq!(local(&f.name).unwrap().unwrap().account, replacement);
    assert_eq!(
        f.store.record(&f.key(), &f.name).unwrap().unwrap().sid,
        Some(original.sid)
    );
}

#[test]
#[ignore = "explicit isolated SYSTEM account lifecycle fixture"]
fn account_gate_is_exclusive_and_released_after_handle_close() {
    let mut f = Fixture::new();
    let gate = account_gate(&f.name).unwrap();
    assert!(f.store.provision(&f.name).is_err());
    assert!(f.store.observe(&f.name).is_err());
    assert!(local(&f.name).unwrap().is_none());
    drop(gate);
    f.provision();
}

#[test]
#[ignore = "explicit isolated SYSTEM account lifecycle fixture"]
fn unsafe_account_journal_is_rejected_without_acl_repair() {
    let mut f = Fixture::new();
    let original = f.provision();
    let key = f.key();
    let unsafe_acl = security(Some(&original.sid)).unwrap();
    status(unsafe { RegSetKeySecurity(key.0, DACL_SECURITY_INFORMATION, unsafe_acl.0) }).unwrap();
    assert!(f.store.provision(&f.name).is_err());
    assert!(f.store.verify(&f.name).is_err());
    assert!(f.store.observe(&f.name).is_err());
    assert!(f.store.disable(&f.name).is_err());
    assert!(verify_security(&key, None).is_err());
    assert!(f.store.reconcile_expired().is_err());
    // Restore only the test-owned key so exact, nonrecursive cleanup can run.
    let original_acl = security(None).unwrap();
    status(unsafe { RegSetKeySecurity(key.0, DACL_SECURITY_INFORMATION, original_acl.0) }).unwrap();
}

#[test]
#[ignore = "explicit isolated SYSTEM account lifecycle fixture"]
fn account_journal_is_kernel_private_even_to_its_account() {
    let mut f = Fixture::new();
    let account = f.provision();
    let path = format!(r"SOFTWARE\{}\{}", f.store.root, f.name);
    {
        let _revert = super::super::tests::restrict_thread_to(&account.sid);
        for rights in [KEY_READ, KEY_SET_VALUE] {
            assert!(matches!(open(HKEY_LOCAL_MACHINE, &path, rights), Err(error)
                if error.kind() == io::ErrorKind::PermissionDenied));
        }
        assert_eq!(
            f.store.verify(&f.name).unwrap_err().kind(),
            io::ErrorKind::PermissionDenied
        );
    }
    assert_eq!(f.store.verify(&f.name).unwrap(), account);
}

#[test]
#[ignore = "explicit isolated SYSTEM account lifecycle fixture"]
fn failed_bootstrap_cleanup_is_idempotent_without_creating_an_account() {
    let mut f = Fixture::new();
    assert_eq!(f.store.disable(&f.name).unwrap(), None);
    assert!(local(&f.name).unwrap().is_none());
    let record = Record {
        schema_version: 1,
        username: f.name.clone(),
        nonce: nonce(),
        sid: None,
    };
    write_record(&f.key(), &record).unwrap();
    assert_eq!(f.store.disable(&f.name).unwrap(), None);
    assert!(local(&f.name).unwrap().is_none());
    create_local(&f.name, &format!("{MARKER_PREFIX}{}", record.nonce)).unwrap();
    let original = local(&f.name).unwrap().unwrap().account;
    f.sid = Some(original.sid.clone());
    assert_eq!(f.store.disable(&f.name).unwrap(), Some(original.clone()));
    assert_eq!(f.store.verify(&f.name).unwrap(), original);
    f.delete_account();
    assert_eq!(f.store.disable(&f.name).unwrap(), None);
    assert_eq!(
        f.store.record(&f.key(), &f.name).unwrap().unwrap().sid,
        Some(original.sid)
    );
    assert!(f.store.provision(&f.name).is_err());
}
