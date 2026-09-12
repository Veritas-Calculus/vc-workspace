use super::*;
use windows_sys::Wdk::System::Registry::NtDeleteKey;

mod transactions;

const USER: &str = "vca0123456789ab";
const OTHER: &str = "vca0123456789ac";
const SID: &str = "S-1-5-21-1111-2222-3333-1001";
const OTHER_SID: &str = "S-1-5-21-1111-2222-3333-1002";

fn delete_fixture_key(path: &str) -> io::Result<()> {
    let mut raw = null_mut();
    // Open the exact fixture key itself, including a dangling registry link.
    // RegDeleteKeyEx follows links and can delete their targets instead. Native
    // handle-based deletion does not follow links and is deliberately nonrecursive.
    let code = unsafe {
        RegOpenKeyExW(
            HKEY_LOCAL_MACHINE,
            wide(path).as_ptr(),
            REG_OPTION_OPEN_LINK,
            DELETE | KEY_WOW64_64KEY,
            &mut raw,
        )
    };
    if code == ERROR_FILE_NOT_FOUND {
        return Ok(());
    }
    status(code)?;
    let key = Key(raw);
    let result = unsafe { NtDeleteKey(key.0) };
    if result < 0 {
        return status(unsafe { RtlNtStatusToDosError(result) });
    }
    Ok(())
}

struct Fixture(AuthorityRegistry);
impl Fixture {
    fn new() -> Option<Self> {
        let mut nonce = [0; 16];
        getrandom::fill(&mut nonce).unwrap();
        let nonce: String = nonce.iter().map(|b| format!("{b:02x}")).collect();
        let store = AuthorityRegistry {
            root: format!("VCWorkspace.ComputerV2.Test.{nonce}"),
        };
        if !current_identity().unwrap().is_system() {
            // Standard CI users exercise the non-elevation boundary. The full
            // machine-registry branch requires the explicit SYSTEM PVE run.
            assert_eq!(
                store.initialize(USER, SID).unwrap_err().kind(),
                io::ErrorKind::PermissionDenied
            );
            assert_eq!(
                store.write(USER, SID, b"active").unwrap_err().kind(),
                io::ErrorKind::PermissionDenied
            );
            return None;
        }
        assert!(matches!(store.root(false), Err(error) if error.kind() == io::ErrorKind::NotFound));
        Some(Self(store))
    }
    fn key(&self, username: &str) -> Key {
        let root = self.0.root(false).unwrap();
        open(root.0, username, KEY_ALL_ACCESS).unwrap()
    }
}
impl Drop for Fixture {
    fn drop(&mut self) {
        // Nonrecursive deletion of only the exact names created by these tests.
        // No production key, existing user account or filesystem path is touched.
        let mut errors = Vec::new();
        for leaf in [USER, OTHER] {
            let path = format!(r"SOFTWARE\{}\{leaf}", self.0.root);
            if let Err(error) = delete_fixture_key(&path) {
                errors.push(error);
            }
        }
        let path = format!(r"SOFTWARE\{}", self.0.root);
        if let Err(error) = delete_fixture_key(&path) {
            errors.push(error);
        }
        if !errors.is_empty() {
            eprintln!("registry test cleanup failed: {} {errors:?}", self.0.root);
            assert!(
                std::thread::panicking(),
                "registry fixture must clean up its keys"
            );
        }
    }
}

#[test]
fn registry_is_private_sid_bound_and_initialization_never_reactivates() {
    let Some(fixture) = Fixture::new() else {
        return;
    };
    fixture.0.initialize(USER, SID).unwrap();
    fixture.0.check_registration(USER, SID).unwrap();
    assert!(fixture.0.read(USER, SID).is_err()); // absent = denied
    fixture
        .0
        .write(USER, SID, br#"{"state":"active"}"#)
        .unwrap();
    assert_eq!(fixture.0.read(USER, SID).unwrap(), br#"{"state":"active"}"#);
    fixture
        .0
        .write(USER, SID, br#"{"state":"revoked"}"#)
        .unwrap();
    fixture.0.initialize(USER, SID).unwrap();
    assert_eq!(
        fixture.0.read(USER, SID).unwrap(),
        br#"{"state":"revoked"}"#
    );
    for operation in [
        fixture.0.read(USER, OTHER_SID).map(|_| ()),
        fixture.0.write(USER, OTHER_SID, b"active"),
        fixture.0.initialize(USER, OTHER_SID),
    ] {
        assert_eq!(
            operation.unwrap_err().kind(),
            io::ErrorKind::PermissionDenied
        );
    }
    assert_eq!(fixture.0.users().unwrap(), vec![(USER.into(), SID.into())]);
}

#[test]
fn registry_rejects_wrong_sid_metadata_and_unsafe_acl_without_repairing_it() {
    let Some(fixture) = Fixture::new() else {
        return;
    };
    fixture.0.initialize(USER, SID).unwrap();
    let key = fixture.key(USER);
    write_value(&key, "SID", OTHER_SID.as_bytes()).unwrap();
    assert!(fixture.0.initialize(USER, SID).is_err());
    assert!(fixture.0.write(USER, SID, b"active").is_err());
    assert!(fixture.0.read(USER, SID).is_err());
    assert!(fixture.0.users().is_err());
    assert_eq!(read_value(&key, "SID").unwrap().1, OTHER_SID.as_bytes());
    write_value(&key, "SID", SID.as_bytes()).unwrap();
    let sddl = wide(&format!("O:SYD:P(A;;KA;;;SY)(A;;KA;;;{SID})"));
    let mut raw = null_mut();
    check(unsafe {
        ConvertStringSecurityDescriptorToSecurityDescriptorW(sddl.as_ptr(), 1, &mut raw, null_mut())
    })
    .unwrap();
    let bad = LocalAllocation(raw);
    status(unsafe { RegSetKeySecurity(key.0, DACL_SECURITY_INFORMATION, bad.0) }).unwrap();
    assert!(fixture.0.initialize(USER, SID).is_err());
    assert!(fixture.0.write(USER, SID, b"active").is_err());
    assert!(fixture.0.read(USER, SID).is_err());
    assert!(fixture.0.users().is_err());
    assert!(fixture.0.begin_update().is_err());
    // It must not silently chmod an unsafe existing record into a trusted one.
    assert!(verify_security(&key, Some(SID)).is_err());
}

#[test]
fn registry_rejects_links_before_reading_or_writing_the_target() {
    let Some(fixture) = Fixture::new() else {
        return;
    };
    fixture.0.initialize(USER, SID).unwrap();
    fixture.0.write(USER, SID, b"original").unwrap();
    let root = fixture.0.root(false).unwrap();
    let descriptor = security(Some(SID)).unwrap();
    let attributes = SECURITY_ATTRIBUTES {
        nLength: size_of::<SECURITY_ATTRIBUTES>() as u32,
        lpSecurityDescriptor: descriptor.0,
        bInheritHandle: 0,
    };
    let mut raw = null_mut();
    let mut disposition = 0;
    status(unsafe {
        RegCreateKeyExW(
            root.0,
            wide(OTHER).as_ptr(),
            0,
            null(),
            REG_OPTION_CREATE_LINK,
            KEY_ALL_ACCESS | KEY_WOW64_64KEY,
            &attributes,
            &mut raw,
            &mut disposition,
        )
    })
    .unwrap();
    let link = Key(raw);
    assert_eq!(disposition, REG_CREATED_NEW_KEY);
    let target: Vec<u16> = format!(r"\REGISTRY\MACHINE\SOFTWARE\{}\{USER}", fixture.0.root)
        .encode_utf16()
        .collect();
    status(unsafe {
        RegSetValueExW(
            link.0,
            wide("SymbolicLinkValue").as_ptr(),
            0,
            REG_LINK,
            target.as_ptr().cast(),
            (target.len() * 2) as u32,
        )
    })
    .unwrap();
    assert!(fixture.0.initialize(OTHER, SID).is_err());
    assert!(fixture.0.read(OTHER, SID).is_err());
    assert!(fixture.0.write(OTHER, SID, b"changed").is_err());
    assert!(fixture.0.users().is_err());
    assert!(fixture.0.begin_update().is_err());
    assert_eq!(fixture.0.read(USER, SID).unwrap(), b"original");
    delete_fixture_key(&format!(r"SOFTWARE\{}\{OTHER}", fixture.0.root)).unwrap();
    drop(link);
    // The cleanup path must remove only the link, never its target or authority.
    assert_eq!(fixture.0.read(USER, SID).unwrap(), b"original");
    assert_eq!(fixture.0.users().unwrap(), vec![(USER.into(), SID.into())]);
}

pub(in crate::platform::registry) struct RevertImpersonation;
impl Drop for RevertImpersonation {
    fn drop(&mut self) {
        // Do not continue fixture cleanup in an unknown security context.
        if unsafe { RevertToSelf() } == 0 {
            std::process::abort();
        }
    }
}

pub(in crate::platform::registry) fn restrict_thread_to(sid: &str) -> RevertImpersonation {
    let mut raw = null_mut();
    check(unsafe { ConvertStringSidToSidW(wide(sid).as_ptr(), &mut raw) }).unwrap();
    let sid = LocalAllocation(raw);
    let restriction = SID_AND_ATTRIBUTES {
        Sid: sid.0,
        Attributes: 0,
    };
    let mut raw = null_mut();
    check(unsafe {
        OpenProcessToken(GetCurrentProcess(), TOKEN_DUPLICATE | TOKEN_QUERY, &mut raw)
    })
    .unwrap();
    let original = own(raw).unwrap();
    let mut raw = null_mut();
    check(unsafe {
        CreateRestrictedToken(
            original.as_raw_handle(),
            DISABLE_MAX_PRIVILEGE,
            0,
            null(),
            0,
            null(),
            1,
            &restriction,
            &mut raw,
        )
    })
    .unwrap();
    let token = own(raw).unwrap();
    check(unsafe { ImpersonateLoggedOnUser(token.as_raw_handle()) }).unwrap();
    RevertImpersonation
}

#[test]
fn registry_kernel_acl_limits_reader_and_rejects_impersonated_updates() {
    let Some(fixture) = Fixture::new() else {
        return;
    };
    fixture.0.initialize(USER, SID).unwrap();
    fixture.0.initialize(OTHER, OTHER_SID).unwrap();
    fixture.0.write(USER, SID, b"first").unwrap();
    fixture.0.write(OTHER, OTHER_SID, b"second").unwrap();
    {
        // Restricting SIDs require a second kernel access check, even though the
        // original process is SYSTEM. These are not two real logged-on users.
        let _revert = restrict_thread_to(SID);
        assert_eq!(fixture.0.read(USER, SID).unwrap(), b"first");
        assert!(matches!(fixture.0.read(OTHER, OTHER_SID), Err(error)
            if error.kind() == io::ErrorKind::PermissionDenied));
        assert!(matches!(open(HKEY_LOCAL_MACHINE,
            &format!(r"SOFTWARE\{}\{USER}", fixture.0.root), KEY_SET_VALUE), Err(error)
            if error.kind() == io::ErrorKind::PermissionDenied));
        for operation in [
            fixture.0.initialize(USER, SID),
            fixture.0.write(USER, SID, b"forged"),
            fixture.0.users().map(|_| ()),
            fixture.0.begin_update().map(|_| ()),
        ] {
            assert_eq!(
                operation.unwrap_err().kind(),
                io::ErrorKind::PermissionDenied
            );
        }
    }
    assert_eq!(fixture.0.read(USER, SID).unwrap(), b"first");
    assert_eq!(fixture.0.read(OTHER, OTHER_SID).unwrap(), b"second");
}

#[test]
fn registry_values_are_bounded_typed_and_do_not_leak_handles() {
    let Some(fixture) = Fixture::new() else {
        return;
    };
    fixture.0.initialize(USER, SID).unwrap();
    let key = fixture.key(USER);
    for value in [Vec::new(), vec![0; MAX_VALUE + 1]] {
        assert!(fixture.0.write(USER, SID, &value).is_err());
    }
    // A trusted writer fixture deliberately creates malformed values. Readers
    // still fail closed rather than treating registry text as binary authority.
    let oversized = vec![0u8; MAX_VALUE + 1];
    status(unsafe {
        RegSetValueExW(
            key.0,
            wide("Authority").as_ptr(),
            0,
            REG_BINARY,
            oversized.as_ptr(),
            oversized.len() as u32,
        )
    })
    .unwrap();
    assert!(fixture.0.read(USER, SID).is_err());
    status(unsafe {
        RegSetValueExW(
            key.0,
            wide("Authority").as_ptr(),
            0,
            REG_SZ,
            c"wrong".as_ptr().cast(),
            6,
        )
    })
    .unwrap();
    assert!(fixture.0.read(USER, SID).is_err());
    let mut before = 0;
    check(unsafe { GetProcessHandleCount(GetCurrentProcess(), &mut before) }).unwrap();
    for _ in 0..100 {
        fixture.0.write(USER, SID, b"bounded").unwrap();
        assert_eq!(fixture.0.read(USER, SID).unwrap(), b"bounded");
    }
    let mut after = 0;
    check(unsafe { GetProcessHandleCount(GetCurrentProcess(), &mut after) }).unwrap();
    assert!(
        after <= before + 2,
        "registry handles leaked: {before} -> {after}"
    );
}

#[test]
fn registry_snapshot_replacement_never_returns_partial_bytes() {
    let Some(fixture) = Fixture::new() else {
        return;
    };
    fixture.0.initialize(USER, SID).unwrap();
    let small = vec![b'a'; 1024];
    let large = vec![b'z'; 60_000];
    fixture.0.write(USER, SID, &small).unwrap();
    thread::scope(|scope| {
        scope.spawn(|| {
            for index in 0..100 {
                fixture
                    .0
                    .write(USER, SID, if index % 2 == 0 { &large } else { &small })
                    .unwrap();
            }
        });
        let mut read = 0;
        for _ in 0..100 {
            match fixture.0.read(USER, SID) {
                Ok(value) => {
                    assert!(value == small || value == large);
                    read += 1;
                }
                Err(error) => assert_eq!(error.kind(), io::ErrorKind::WouldBlock),
            }
        }
        assert!(read > 0);
    });
}

#[test]
fn registry_rejects_namespace_and_system_identity_confusion() {
    let Some(fixture) = Fixture::new() else {
        return;
    };
    for name in [
        "vdi",
        "vca0123456789AB",
        "vca0123456789/a",
        r"vca0123456789\a",
    ] {
        assert!(fixture.0.initialize(name, SID).is_err());
    }
    for sid in [
        SYSTEM,
        "S-1-5-32-544",
        "S-1-5-21-1-2-3-500",
        "S-1-5-21-1-2-3-1001)(A;;KA;;;WD)",
    ] {
        assert!(fixture.0.initialize(USER, sid).is_err());
    }
    assert!(fixture.0.users().unwrap().is_empty());
}
