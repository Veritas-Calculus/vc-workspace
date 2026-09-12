//! Opt-in profile acceptance, separate from the SAM-only suite. These fixtures
//! create/load/delete only their newly allocated SID's exact local Profile.
use super::super::tests::{password as secret, Fixture};
use super::*;
use std::{mem::ManuallyDrop, path::PathBuf};
use vc_workspace_guest_lifecycle::Zeroizing;
use windows_sys::Win32::{Security::Cryptography::*, System::GroupPolicy::PI_NOUI, UI::Shell::*};
mod rights;

const PROFILE_ROOT: &str = r"SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList";

fn profile_key(sid: &str) -> io::Result<Option<Key>> {
    assert!(valid_account_sid(sid) && sid != SYSTEM);
    let mut key = null_mut();
    let result = unsafe {
        RegOpenKeyExW(
            HKEY_LOCAL_MACHINE,
            wide(&format!(r"{PROFILE_ROOT}\{sid}")).as_ptr(),
            REG_OPTION_OPEN_LINK,
            KEY_READ | KEY_WOW64_64KEY,
            &mut key,
        )
    };
    if result == ERROR_FILE_NOT_FOUND {
        return Ok(None);
    }
    status(result)?;
    Ok(Some(Key(key)))
}

struct ProfileFixture {
    account: ManuallyDrop<Fixture>,
    path: PathBuf,
    rights_before: std::cell::RefCell<Option<rights::Snapshot>>,
}
impl ProfileFixture {
    fn probe_executable(&self) -> io::Result<PathBuf> {
        let path = self.path.join("vcw-profile-probe.exe");
        let mut source = std::fs::File::open(std::env::current_exe()?)?;
        let mut destination = std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&path)?;
        std::io::copy(&mut source, &mut destination)?;
        destination.sync_all()?;
        Ok(path)
    }
    fn new() -> Self {
        assert_eq!(
            std::env::var("VC_WORKSPACE_NATIVE_PROFILE_FIXTURE").as_deref(),
            Ok("isolated")
        );
        let mut account = Fixture::new_namespace(Namespace::Native);
        let owned = account.provision();
        assert!(profile_key(&owned.sid).unwrap().is_none());
        let mut directory = [0u16; 1024];
        let mut size = directory.len() as u32;
        check(unsafe { GetProfilesDirectoryW(directory.as_mut_ptr(), &mut size) }).unwrap();
        let length = directory.iter().position(|c| *c == 0).unwrap();
        let root = PathBuf::from(String::from_utf16(&directory[..length]).unwrap());
        assert!(root.is_absolute());
        let path = root.join(&account.name);
        assert!(!path.try_exists().unwrap(), "refuse existing profile path");
        eprintln!(
            "owned profile fixture: {} {} {}",
            account.name,
            owned.sid,
            path.display()
        );
        Self {
            account: ManuallyDrop::new(account),
            path,
            rights_before: std::cell::RefCell::new(None),
        }
    }
    fn f(&self) -> &Fixture {
        &self.account
    }
    fn close_retained(&self) -> io::Result<AgentAccount> {
        let _gate = account_gate(&self.account.name)?;
        let key = self.account.store.key(&self.account.name, false)?;
        self.account
            .store
            .disable_login_locked(&key, &self.account.name)
    }
    fn deny_logons(&self) -> io::Result<()> {
        let _gate = account_gate(&self.account.name)?;
        let key = self.account.store.key(&self.account.name, false)?;
        let actual = self.account.store.bound(&key, &self.account.name)?;
        let policy = rights::Policy::open()?;
        if actual.account.sid != *self.account.sid.as_ref().unwrap()
            || self.rights_before.borrow().is_some()
            || policy.direct(&actual.account.sid)?.is_some()
        {
            return Err(denied("fixture policy ownership or baseline mismatch"));
        }
        *self.rights_before.borrow_mut() = Some(policy.snapshot()?);
        policy.deny(&actual.account.sid)
    }
    fn release_denials(&self) -> io::Result<()> {
        let Some(before) = self.rights_before.borrow().clone() else {
            return Ok(());
        };
        let _gate = account_gate(&self.account.name)?;
        let key = self.account.store.key(&self.account.name, false)?;
        let actual = self.account.store.bound(&key, &self.account.name)?;
        if actual.account.sid != *self.account.sid.as_ref().unwrap() {
            return Err(denied("fixture SID changed during policy cleanup"));
        }
        let policy = rights::Policy::open()?;
        policy.remove_fixture(&actual.account.sid)?;
        if policy.snapshot()? != before {
            return Err(denied("LSA policy baseline changed"));
        }
        *self.rights_before.borrow_mut() = None;
        eprintln!("independent LSA enumeration restored original deny rights and account baseline");
        Ok(())
    }
    fn issue(&self, password: &str) {
        self.issue_until(password, unix_seconds().unwrap() + 3600);
    }
    fn issue_until(&self, password: &str, expires: u64) {
        let raw = super::tests::payload(
            self.f(),
            Operation::Issue,
            1,
            "conn_profile_test_first",
            expires,
            Some(password),
        );
        super::tests::store(self.f())
            .apply_credential(&self.account.name, &Request::parse(&raw).unwrap())
            .unwrap();
    }
    fn session(&self, password: &str) -> ProfileSession {
        let password = Zeroizing::new(wide(password));
        let mut token = null_mut();
        check(unsafe {
            LogonUserW(
                wide(&self.account.name).as_ptr(),
                wide(".").as_ptr(),
                password.as_ptr(),
                LOGON32_LOGON_INTERACTIVE,
                LOGON32_PROVIDER_DEFAULT,
                &mut token,
            )
        })
        .unwrap();
        let token = own(token).unwrap();
        assert_eq!(
            token_identity(token.as_raw_handle()).unwrap().sid,
            *self.account.sid.as_ref().unwrap()
        );
        let mut username = wide(&self.account.name);
        let mut info = PROFILEINFOW {
            dwSize: size_of::<PROFILEINFOW>() as u32,
            dwFlags: PI_NOUI,
            lpUserName: username.as_mut_ptr(),
            ..Default::default()
        };
        check(unsafe { LoadUserProfileW(token.as_raw_handle(), &mut info) }).unwrap();
        let session = ProfileSession {
            token,
            profile: info.hProfile,
        };
        let mut directory = [0u16; 1024];
        let mut size = directory.len() as u32;
        check(unsafe {
            GetUserProfileDirectoryW(
                session.token.as_raw_handle(),
                directory.as_mut_ptr(),
                &mut size,
            )
        })
        .unwrap();
        let length = directory.iter().position(|c| *c == 0).unwrap();
        assert!(String::from_utf16(&directory[..length])
            .unwrap()
            .eq_ignore_ascii_case(self.path.to_str().unwrap()));
        assert!(profile_key(self.account.sid.as_ref().unwrap())
            .unwrap()
            .is_some());
        session
    }
    fn cleanup(&self) -> io::Result<()> {
        require_system()?;
        self.release_denials()?;
        let sid = self.account.sid.as_ref().unwrap();
        let actual = self.account.store.verify(&self.account.name)?;
        if actual.sid != *sid || !sessions_for_sid(sid)?.is_empty() {
            return Err(denied(
                "profile fixture identity changed or still interactive",
            ));
        }
        if let Some(key) = profile_key(sid)? {
            let (kind, bytes) = read_value(&key, "ProfileImagePath")?;
            if (kind != REG_SZ && kind != REG_EXPAND_SZ) || bytes.len() % 2 != 0 {
                return Err(denied("invalid profile fixture path record"));
            }
            let text: Vec<u16> = bytes
                .chunks_exact(2)
                .map(|b| u16::from_le_bytes([b[0], b[1]]))
                .take_while(|c| *c != 0)
                .collect();
            let text = String::from_utf16(&text).map_err(io::Error::other)?;
            if !text.eq_ignore_ascii_case(self.path.to_str().unwrap()) {
                return Err(denied("profile fixture path was replaced"));
            }
            drop(key);
            check(unsafe {
                DeleteProfileW(
                    wide(sid).as_ptr(),
                    wide(self.path.to_str().unwrap()).as_ptr(),
                    null(),
                )
            })?;
        }
        if self.path.try_exists()? || profile_key(sid)?.is_some() {
            return Err(denied("profile fixture removal incomplete"));
        }
        Ok(())
    }
}

#[test]
#[ignore = "experimental isolated SID-only logon rights and DPAPI alternative"]
fn denied_logons_password_change_preserves_profile_across_logons() {
    let f = ProfileFixture::new();
    let first = secret();
    f.issue(&first);
    let logon = f.session(&first);
    let data = b"DPAPI preservation while all password logon types are denied";
    let protected = logon.crypto(data, true).unwrap();
    drop(logon);
    f.deny_logons().unwrap();
    let blocked = |password: &str| {
        for kind in [
            LOGON32_LOGON_INTERACTIVE,
            LOGON32_LOGON_NETWORK,
            LOGON32_LOGON_NETWORK_CLEARTEXT,
            LOGON32_LOGON_BATCH,
            LOGON32_LOGON_SERVICE,
        ] {
            let secret = Zeroizing::new(wide(password));
            let mut token = null_mut();
            let ok = unsafe {
                LogonUserW(
                    wide(&f.account.name).as_ptr(),
                    wide(".").as_ptr(),
                    secret.as_ptr(),
                    kind,
                    LOGON32_PROVIDER_DEFAULT,
                    &mut token,
                )
            };
            let error = unsafe { GetLastError() };
            if ok != 0 {
                drop(own(token).unwrap());
                panic!("denied fixture logon succeeded: {kind}");
            }
            assert_eq!(
                error, ERROR_LOGON_TYPE_NOT_GRANTED,
                "logon failure must prove policy enforcement, kind={kind}"
            );
        }
    };
    blocked(&first);
    let next = secret();
    {
        let _gate = account_gate(&f.account.name).unwrap();
        let key = f.account.key();
        f.account.store.bound(&key, &f.account.name).unwrap();
        password::change(&f.account.name, &first, &next).unwrap();
    }
    blocked(&next);
    f.release_denials().unwrap();
    let fresh = f.session(&next);
    assert_eq!(&*fresh.crypto(&protected, false).unwrap(), data);
    drop(fresh);
    let next_logon = f.session(&next);
    assert_eq!(&*next_logon.crypto(&protected, false).unwrap(), data);
}
impl Drop for ProfileFixture {
    fn drop(&mut self) {
        if let Err(error) = self.cleanup() {
            // Retain the exact SAM/journal identity if Profile cleanup failed.
            // Do not orphan its SID by allowing the account fixture to delete it.
            // A failed fixture must not leave even its random password usable.
            // Bound rechecks refuse any SID/ownership replacement; no reset.
            if let Err(close_error) = self.close_retained() {
                eprintln!("retained profile account could not be disabled: {close_error}");
            }
            eprintln!(
                "retain owned profile {} {}: {error}",
                self.account.name,
                self.path.display()
            );
            assert!(
                std::thread::panicking(),
                "owned profile fixture did not clean up"
            );
            return;
        }
        unsafe { ManuallyDrop::drop(&mut self.account) };
    }
}

struct ProfileSession {
    token: OwnedHandle,
    profile: HANDLE,
}
impl Drop for ProfileSession {
    fn drop(&mut self) {
        if unsafe { UnloadUserProfile(self.token.as_raw_handle(), self.profile) } == 0 {
            eprintln!(
                "owned profile failed to unload: {}",
                io::Error::last_os_error()
            );
            assert!(std::thread::panicking(), "owned profile unload failed");
        }
    }
}
struct Impersonation;
impl Drop for Impersonation {
    fn drop(&mut self) {
        assert_ne!(
            unsafe { RevertToSelf() },
            0,
            "fixture impersonation did not revert"
        );
    }
}
impl ProfileSession {
    fn migrate_keys(&self, executable: &std::path::Path, previous: &str) -> io::Result<(u32, u32)> {
        let identity = token_identity(self.token.as_raw_handle())?;
        #[derive(Serialize)]
        struct Input<'a> {
            sid: &'a str,
            luid: u64,
            previous: &'a str,
        }
        let raw = Zeroizing::new(
            serde_json::to_vec(&Input {
                sid: &identity.sid,
                luid: identity.authentication_id,
                previous,
            })
            .map_err(|_| denied("invalid migration input"))?,
        );
        let mut framed = Zeroizing::new((raw.len() as u32).to_le_bytes().to_vec());
        framed.extend_from_slice(&raw);
        let arguments: Vec<_> = [
            "--exact",
            "tests::native_profile_key_child",
            "--ignored",
            "--nocapture",
            "--test-threads=1",
        ]
        .into_iter()
        .map(std::ffi::OsString::from)
        .collect();
        let output = crate::platform::worker::run_profile_worker(
            self.token.as_raw_handle(),
            executable,
            &arguments,
            &framed,
        )?;
        if output.exit_code != 0 {
            return Err(denied("primary-user migration child failed"));
        }
        let text = std::str::from_utf8(&output.stdout)
            .map_err(|_| denied("invalid profile child output"))?;
        let marker = text
            .find("VCW_PROFILE_RESULT ")
            .ok_or_else(|| denied("missing profile child receipt"))?;
        let line = text[marker + 19..]
            .lines()
            .next()
            .ok_or_else(|| denied("missing profile child result"))?;
        let result: serde_json::Value =
            serde_json::from_str(line).map_err(|_| denied("invalid profile child result"))?;
        if result["sid"] != identity.sid || result["luid"] != identity.authentication_id {
            return Err(denied("profile child identity changed"));
        }
        let migrated = result["migrated"]
            .as_u64()
            .and_then(|v| u32::try_from(v).ok())
            .ok_or_else(|| denied("invalid migration count"))?;
        let unmatched = result["unmatched"]
            .as_u64()
            .and_then(|v| u32::try_from(v).ok())
            .ok_or_else(|| denied("invalid migration count"))?;
        Ok((migrated, unmatched))
    }

    fn crypto(&self, input: &[u8], protect: bool) -> io::Result<Zeroizing<Vec<u8>>> {
        check(unsafe { ImpersonateLoggedOnUser(self.token.as_raw_handle()) })?;
        let _revert = Impersonation;
        let mut input = Zeroizing::new(input.to_vec());
        let blob = CRYPT_INTEGER_BLOB {
            cbData: input.len() as u32,
            pbData: input.as_mut_ptr(),
        };
        let mut output = CRYPT_INTEGER_BLOB::default();
        let ok = if protect {
            unsafe {
                CryptProtectData(
                    &blob,
                    null(),
                    null(),
                    null(),
                    null(),
                    CRYPTPROTECT_UI_FORBIDDEN,
                    &mut output,
                )
            }
        } else {
            unsafe {
                CryptUnprotectData(
                    &blob,
                    null_mut(),
                    null(),
                    null(),
                    null(),
                    CRYPTPROTECT_UI_FORBIDDEN,
                    &mut output,
                )
            }
        };
        check(ok)?;
        let _allocation = LocalAllocation(output.pbData.cast());
        if output.pbData.is_null() || output.cbData == 0 || output.cbData > 16384 {
            return Err(denied("invalid DPAPI fixture output"));
        }
        let bytes =
            unsafe { std::slice::from_raw_parts_mut(output.pbData, output.cbData as usize) };
        let result = Zeroizing::new(bytes.to_vec());
        for byte in bytes {
            unsafe { std::ptr::write_volatile(byte, 0) };
        }
        Ok(result)
    }
}

#[test]
#[ignore = "explicit isolated SYSTEM Profile protection and cleanup fixture"]
fn existing_profile_guard_preserves_data_and_allows_revoke_and_expiry() {
    use super::super::tests::password_works;
    use vc_workspace_guest_lifecycle::native::Phase;
    for expire in [false, true] {
        let f = ProfileFixture::new();
        let original = secret();
        let deadline = unix_seconds().unwrap() + if expire { 20 } else { 3600 };
        f.issue_until(&original, deadline);
        let logon = f.session(&original);
        let data = b"Existing Profile must survive refused resets and revocation";
        let protected = logon.crypto(data, true).unwrap();
        let native = super::tests::store(f.f());
        let before = native.observe(&f.account.name).unwrap();
        let retire = super::tests::payload(
            f.f(),
            Operation::Retire,
            2,
            "conn_profile_test_first",
            deadline,
            None,
        );
        assert_eq!(
            native
                .apply_credential(&f.account.name, &Request::parse(&retire).unwrap())
                .unwrap_err()
                .to_string(),
            "Native profile requires DPAPI-safe credential lifecycle"
        );
        assert_eq!(native.observe(&f.account.name).unwrap(), before);
        assert_eq!(&*logon.crypto(&protected, false).unwrap(), data);
        drop(logon);
        if expire {
            while unix_seconds().unwrap() <= deadline {
                thread::sleep(Duration::from_millis(50));
            }
            assert_eq!(native.reconcile_expired().unwrap(), 1);
        } else {
            let revoke = super::tests::payload(
                f.f(),
                Operation::Revoke,
                2,
                "conn_profile_test_first",
                0,
                None,
            );
            native
                .apply_credential(&f.account.name, &Request::parse(&revoke).unwrap())
                .unwrap();
        }
        let closed = native.observe(&f.account.name).unwrap();
        assert_eq!(closed.disabled, Some(true));
        assert_eq!(closed.lifecycle.as_ref().unwrap().phase, Phase::Revoked);
        assert!(!password_works(f.f(), &original));
        let next_secret = secret();
        let issue = super::tests::payload(
            f.f(),
            Operation::Issue,
            closed.lifecycle.as_ref().unwrap().revision + 1,
            "conn_profile_test_second",
            unix_seconds().unwrap() + 3600,
            Some(&next_secret),
        );
        assert_eq!(
            native
                .apply_credential(&f.account.name, &Request::parse(&issue).unwrap())
                .unwrap_err()
                .to_string(),
            "Native profile requires DPAPI-safe credential lifecycle"
        );
        assert_eq!(native.observe(&f.account.name).unwrap(), closed);
        // Test-only observation: reopen this fresh SID without changing its
        // password to prove neither guard nor revocation destroyed its data.
        // This is NOT a production reissue or desktop lifecycle acceptance.
        {
            let _gate = account_gate(&f.account.name).unwrap();
            f.account
                .store
                .enable_locked(
                    &f.account.key(),
                    &f.account.name,
                    (unix_seconds().unwrap() + 3600) as u32,
                )
                .unwrap();
        }
        let fresh = f.session(&original);
        assert_eq!(&*fresh.crypto(&protected, false).unwrap(), data);
        assert!(f.close_retained().unwrap().disabled);
        assert!(!password_works(f.f(), &original));
        assert_eq!(&*fresh.crypto(&protected, false).unwrap(), data);
        drop(fresh);
        assert_eq!(native.reconcile_expired().unwrap(), 1);
        assert_eq!(
            native.observe(&f.account.name).unwrap().disabled,
            Some(true)
        );
        eprintln!("existing Profile guard and encrypted data preserved; local expiry={expire}");
    }
}

#[test]
#[ignore = "experimental isolated Profile/DPAPI expiry alternative; not production acceptance"]
fn expired_account_password_change_preserves_profile_across_logons() {
    let f = ProfileFixture::new();
    let first = secret();
    f.issue(&first);
    let first_logon = f.session(&first);
    let data = b"Account-expiry password change alternative";
    let protected = first_logon.crypto(data, true).unwrap();
    drop(first_logon);
    let next = secret();
    {
        let _gate = account_gate(&f.account.name).unwrap();
        let key = f.account.key();
        f.account.store.bound(&key, &f.account.name).unwrap();
        let expiry = USER_INFO_1017 {
            usri1017_acct_expires: 1,
        };
        status(unsafe {
            NetUserSetInfo(
                null(),
                wide(&f.account.name).as_ptr(),
                1017,
                (&expiry as *const USER_INFO_1017).cast(),
                null_mut(),
            )
        })
        .unwrap();
        assert!(!super::super::tests::password_works(f.f(), &first));
        password::change(&f.account.name, &first, &next).unwrap();
        f.account
            .store
            .enable_locked(
                &key,
                &f.account.name,
                (unix_seconds().unwrap() + 3600) as u32,
            )
            .unwrap();
    }
    let next_logon = f.session(&next);
    assert_eq!(&*next_logon.crypto(&protected, false).unwrap(), data);
    drop(next_logon);
    let fresh = f.session(&next);
    assert_eq!(&*fresh.crypto(&protected, false).unwrap(), data);
}

pub(crate) fn migrate_in_user_process() -> io::Result<()> {
    #[derive(Deserialize)]
    #[serde(deny_unknown_fields)]
    struct Input {
        sid: String,
        luid: u64,
        previous: vc_workspace_guest_lifecycle::Password,
    }
    let mut length = [0u8; 4];
    let mut input = std::io::stdin().lock();
    input.read_exact(&mut length)?;
    let length = u32::from_le_bytes(length) as usize;
    if length == 0 || length > 4096 {
        return Err(denied("invalid profile input length"));
    }
    let mut raw = Zeroizing::new(vec![0u8; length]);
    input.read_exact(&mut raw)?;
    let request: Input =
        serde_json::from_slice(&raw).map_err(|_| denied("invalid profile input"))?;
    let identity = current_identity()?;
    if identity.is_system()
        || identity.sid != request.sid
        || identity.authentication_id != request.luid
        || !valid_account_sid(&identity.sid)
    {
        return Err(denied("profile migration requires the actual primary user"));
    }
    let mut thread_token = null_mut();
    if unsafe { OpenThreadToken(GetCurrentThread(), TOKEN_QUERY, 1, &mut thread_token) } != 0 {
        drop(own(thread_token)?);
        return Err(denied("profile child must not impersonate"));
    }
    if unsafe { GetLastError() } != ERROR_NO_TOKEN {
        return Err(io::Error::last_os_error());
    }
    let previous = Zeroizing::new(wide(request.previous.expose()));
    let (mut migrated, mut unmatched) = (0, 0);
    check(unsafe {
        CryptUpdateProtectedState(
            null_mut(),
            previous.as_ptr(),
            0,
            &mut migrated,
            &mut unmatched,
        )
    })?;
    println!(
        "VCW_PROFILE_RESULT {}",
        serde_json::json!({"sid":identity.sid,"luid":identity.authentication_id,"migrated":migrated,"unmatched":unmatched})
    );
    Ok(())
}

#[test]
#[ignore = "explicit isolated SYSTEM Profile/DPAPI fixture"]
fn password_change_and_key_migration_preserve_profile_across_logons() {
    for disabled in [false, true] {
        let f = ProfileFixture::new();
        let first = secret();
        f.issue(&first);
        let first_logon = f.session(&first);
        let old_luid = token_identity(first_logon.token.as_raw_handle())
            .unwrap()
            .authentication_id;
        let message = b"VC Workspace profile persistence acceptance";
        let protected = first_logon.crypto(message, true).unwrap();
        assert_eq!(&*first_logon.crypto(&protected, false).unwrap(), message);
        let state = super::tests::store(f.f()).observe(&f.account.name).unwrap();
        let deadline = state.lifecycle.as_ref().unwrap().expires_unix_seconds;
        let retirement = super::tests::payload(
            f.f(),
            Operation::Retire,
            2,
            "conn_profile_test_first",
            deadline,
            None,
        );
        assert!(super::tests::store(f.f())
            .apply_credential(&f.account.name, &Request::parse(&retirement).unwrap())
            .is_err());
        assert_eq!(
            super::tests::store(f.f()).observe(&f.account.name).unwrap(),
            state
        );
        assert_eq!(&*first_logon.crypto(&protected, false).unwrap(), message);
        drop(first_logon);
        let next = secret();
        let gate = account_gate(&f.account.name).unwrap();
        let key = f.account.key();
        if disabled {
            f.account
                .store
                .disable_login_locked(&key, &f.account.name)
                .unwrap();
        }
        let sid = f
            .account
            .store
            .bound(&key, &f.account.name)
            .unwrap()
            .account
            .sid;
        password::change(&f.account.name, &first, &next).unwrap();
        assert_eq!(
            f.account
                .store
                .bound(&key, &f.account.name)
                .unwrap()
                .account
                .sid,
            sid
        );
        if disabled {
            f.account
                .store
                .enable_locked(
                    &key,
                    &f.account.name,
                    (unix_seconds().unwrap() + 3600) as u32,
                )
                .unwrap();
        }
        drop(gate);
        let second_logon = f.session(&next);
        assert_ne!(
            token_identity(second_logon.token.as_raw_handle())
                .unwrap()
                .authentication_id,
            old_luid
        );
        let current_message = b"Data created with the new Windows credential";
        let current_protected = if disabled {
            let before = second_logon.crypto(&protected, false);
            eprintln!(
                "disabled-account change decryptable before migration={}",
                before.is_ok()
            );
            // Include a current-password key: the old password cannot migrate
            // that key, but its data must remain readable. The API's failed
            // count is not itself proof of loss (see its documented remarks).
            let current = second_logon.crypto(current_message, true).unwrap();
            let probe = f.probe_executable().unwrap();
            let (migrated, failed) = second_logon.migrate_keys(&probe, &first).unwrap();
            assert!(migrated > 0, "no old DPAPI master key migrated");
            eprintln!("DPAPI key migration: migrated={migrated} unmatched={failed}");
            assert_eq!(
                &*second_logon.crypto(&current, false).unwrap(),
                current_message
            );
            Some(current)
        } else {
            None
        };
        let same_logon = second_logon.crypto(&protected, false);
        eprintln!(
            "old DPAPI data readable in migration logon={}",
            same_logon.is_ok()
        );
        if !disabled {
            assert_eq!(&*same_logon.unwrap(), message);
        }
        drop(second_logon);
        let third_logon = f.session(&next);
        assert_eq!(&*third_logon.crypto(&protected, false).unwrap(), message);
        if let Some(current) = &current_protected {
            assert_eq!(
                &*third_logon.crypto(current, false).unwrap(),
                current_message
            );
        }
        eprintln!("profile DPAPI old-password change passed; disabled during change={disabled}");
    }
}
