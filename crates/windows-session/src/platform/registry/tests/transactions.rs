use super::*;

fn initialized() -> Option<Fixture> {
    let fixture = Fixture::new()?;
    fixture.0.initialize(USER, SID).unwrap();
    fixture.0.initialize(OTHER, OTHER_SID).unwrap();
    fixture.0.write(USER, SID, b"old-first").unwrap();
    fixture.0.write(OTHER, OTHER_SID, b"old-second").unwrap();
    Some(fixture)
}

fn assert_old(fixture: &Fixture) {
    assert_eq!(fixture.0.read(USER, SID).unwrap(), b"old-first");
    assert_eq!(fixture.0.read(OTHER, OTHER_SID).unwrap(), b"old-second");
}

#[test]
fn registry_transaction_publishes_all_users_and_fence_together() {
    let Some(fixture) = initialized() else { return };
    let mut update = fixture.0.begin_update().unwrap();
    assert_eq!(update.previous().unwrap(), None);
    let mut old = update.existing_authorities().unwrap();
    old.sort();
    assert_eq!(old, vec![b"old-first".to_vec(), b"old-second".to_vec()]);
    update
        .stage(Some((USER, SID)), b"new-active", b"revoked")
        .unwrap();
    assert_old(&fixture); // Ordinary Helper reads cannot see uncommitted data.
    assert_eq!(update.previous().unwrap().unwrap(), b"new-active");
    update.commit().unwrap();
    assert_eq!(fixture.0.read(USER, SID).unwrap(), b"new-active");
    assert_eq!(fixture.0.read(OTHER, OTHER_SID).unwrap(), b"revoked");
    let mut update = fixture.0.begin_update().unwrap();
    assert_eq!(update.previous().unwrap().unwrap(), b"new-active");
    update
        .stage(None, b"new-tombstone", b"new-tombstone")
        .unwrap();
    update.commit().unwrap();
    assert_eq!(fixture.0.read(USER, SID).unwrap(), b"new-tombstone");
    assert_eq!(fixture.0.read(OTHER, OTHER_SID).unwrap(), b"new-tombstone");
    assert_eq!(
        fixture
            .0
            .begin_update()
            .unwrap()
            .previous()
            .unwrap()
            .unwrap(),
        b"new-tombstone"
    );
}

#[test]
fn registry_transaction_drop_and_incomplete_stage_roll_back() {
    let Some(fixture) = initialized() else { return };
    {
        let mut update = fixture.0.begin_update().unwrap();
        update
            .stage(Some((USER, SID)), b"uncommitted", b"revoked")
            .unwrap();
    }
    assert_old(&fixture);
    assert_eq!(fixture.0.begin_update().unwrap().previous().unwrap(), None);
    assert!(fixture.0.begin_update().unwrap().commit().is_err());
    for target in [Some((USER, OTHER_SID)), Some(("vca0123456789ad", SID))] {
        let mut update = fixture.0.begin_update().unwrap();
        assert!(update.stage(target, b"bad", b"revoked").is_err());
        assert!(update.commit().is_err());
        assert_old(&fixture);
    }
    for value in [Vec::new(), vec![0; MAX_VALUE + 1]] {
        let mut update = fixture.0.begin_update().unwrap();
        assert!(update.stage(None, &value, b"revoked").is_err());
        assert!(update.commit().is_err());
    }
    assert_old(&fixture);
    let mut update = fixture.0.begin_update().unwrap();
    update.stage(None, b"first-stage", b"first-stage").unwrap();
    assert!(update
        .stage(None, b"second-stage", b"second-stage")
        .is_err());
    assert!(update.commit().is_err());
    assert_old(&fixture);
}

#[test]
fn registry_transaction_fence_stays_private_and_commit_rechecks_identity() {
    let Some(fixture) = initialized() else { return };
    let mut update = fixture.0.begin_update().unwrap();
    update
        .stage(Some((USER, SID)), b"new-active", b"revoked")
        .unwrap();
    {
        let _revert = restrict_thread_to(SID);
        assert_eq!(fixture.0.read(USER, SID).unwrap(), b"old-first");
        assert!(open(
            HKEY_LOCAL_MACHINE,
            &format!(r"SOFTWARE\{}", fixture.0.root),
            KEY_READ
        )
        .is_err());
        assert_eq!(
            update.commit().unwrap_err().kind(),
            io::ErrorKind::PermissionDenied
        );
    }
    assert_old(&fixture);
    assert_eq!(fixture.0.begin_update().unwrap().previous().unwrap(), None);
}

#[test]
fn registry_transaction_rejects_malformed_fence_without_resetting_it() {
    let Some(fixture) = initialized() else { return };
    let root = fixture.0.root(false).unwrap();
    // A corrupted existing fence is not an absent fence or a fresh install.
    let malformed = wide("wrong");
    status(unsafe {
        RegSetValueExW(
            root.0,
            wide("Fence").as_ptr(),
            0,
            REG_SZ,
            malformed.as_ptr().cast(),
            (malformed.len() * 2) as u32,
        )
    })
    .unwrap();
    let original = read_value(&root, "Fence").unwrap();
    assert_eq!(original.0, REG_SZ);
    {
        let update = fixture.0.begin_update().unwrap();
        assert!(update.previous().is_err());
    }
    assert_eq!(read_value(&root, "Fence").unwrap(), original);
    assert_old(&fixture);
}

#[test]
fn registry_transaction_reserves_writer_before_reading_fence() {
    let Some(fixture) = initialized() else { return };
    let mut first = fixture.0.begin_update().unwrap();
    assert_eq!(first.previous().unwrap(), None);
    // Even before staging, another publisher must not be allowed to read the
    // same old fence and later overwrite a newer revoke based on that read.
    let started = Instant::now();
    match fixture.0.begin_update() {
        Err(error) => assert_eq!(
            error.raw_os_error(),
            Some(ERROR_TRANSACTIONAL_CONFLICT as i32)
        ),
        Ok(_) => panic!("two publishers acquired the ordering fence"),
    }
    assert!(started.elapsed() < Duration::from_secs(2));
    first.stage(None, b"revoked", b"revoked").unwrap();
    first.commit().unwrap();
    let next = fixture.0.begin_update().unwrap();
    assert_eq!(next.previous().unwrap().unwrap(), b"revoked");
}

#[test]
fn registry_transaction_timeout_rolls_back_and_allows_recovery() {
    let Some(fixture) = initialized() else { return };
    let mut update = fixture.0.begin_update().unwrap();
    update
        .stage(None, b"must-not-publish", b"must-not-publish")
        .unwrap();
    thread::sleep(Duration::from_millis(5500));
    assert!(update.commit().is_err());
    assert_old(&fixture);
    let mut next = fixture.0.begin_update().unwrap();
    assert_eq!(next.previous().unwrap(), None);
    next.stage(None, b"recovered", b"recovered").unwrap();
    next.commit().unwrap();
    assert_eq!(fixture.0.read(USER, SID).unwrap(), b"recovered");
}

#[test]
fn registry_transaction_process_exit_rolls_back_without_rust_drop() {
    let Some(fixture) = initialized() else { return };
    let mut input = (fixture.0.root.len() as u32).to_be_bytes().to_vec();
    input.extend_from_slice(fixture.0.root.as_bytes());
    let arguments = [
        "--exact",
        "platform::registry::tests::transactions::fixture_exit_during_transaction",
        "--ignored",
        "--nocapture",
        "--test-threads=1",
    ]
    .map(Into::into);
    let result = crate::platform::worker::run_worker(
        &std::env::current_exe().unwrap(),
        &arguments,
        &input,
        Duration::from_secs(4),
        8192,
    )
    .unwrap();
    assert_eq!(
        result.exit_code,
        73,
        "{}",
        String::from_utf8_lossy(&result.stdout)
    );
    assert_old(&fixture);
    assert_eq!(fixture.0.begin_update().unwrap().previous().unwrap(), None);
}

#[test]
#[ignore = "explicit subprocess exit fixture, parent owns registry cleanup"]
fn fixture_exit_during_transaction() {
    let mut size = [0; 4];
    io::stdin().read_exact(&mut size).unwrap();
    let size = u32::from_be_bytes(size) as usize;
    assert!(size < 128);
    let mut root = vec![0; size];
    io::stdin().read_exact(&mut root).unwrap();
    let root = String::from_utf8(root).unwrap();
    let suffix = root.strip_prefix("VCWorkspace.ComputerV2.Test.").unwrap();
    assert_eq!(suffix.len(), 32);
    assert!(suffix
        .bytes()
        .all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c)));
    let store = AuthorityRegistry { root };
    store.root(false).unwrap(); // Must exist and satisfy the production ACL checks.
    assert_eq!(store.read(USER, SID).unwrap(), b"old-first");
    let mut update = store.begin_update().unwrap();
    update
        .stage(Some((OTHER, OTHER_SID)), b"uncommitted", b"revoked")
        .unwrap();
    std::process::exit(73); // Deliberately bypasses Drop; KTM must undo the writes.
}
