//! Test-only local LSA experiment. Never changes a group or adopts an existing
//! policy account. The parent proves the fresh SAM SID before each mutation.
use super::*;
use std::collections::{BTreeMap, BTreeSet};
use windows_sys::Win32::Security::Authentication::Identity as lsa;

const DENIALS: [&str; 5] = [
    "SeDenyInteractiveLogonRight",
    "SeDenyRemoteInteractiveLogonRight",
    "SeDenyNetworkLogonRight",
    "SeDenyBatchLogonRight",
    "SeDenyServiceLogonRight",
];
pub(super) type Snapshot = BTreeMap<String, BTreeSet<String>>;
pub(super) struct Policy(lsa::LSA_HANDLE);
struct Memory(*mut c_void);
impl Drop for Memory {
    fn drop(&mut self) {
        if !self.0.is_null() {
            unsafe { lsa::LsaFreeMemory(self.0) };
        }
    }
}
impl Drop for Policy {
    fn drop(&mut self) {
        unsafe { lsa::LsaClose(self.0) };
    }
}
fn result(code: i32) -> io::Result<()> {
    status(unsafe { lsa::LsaNtStatusToWinError(code) })
}
fn text(value: &mut [u16]) -> lsa::LSA_UNICODE_STRING {
    lsa::LSA_UNICODE_STRING {
        Length: ((value.len() - 1) * 2) as u16,
        MaximumLength: (value.len() * 2) as u16,
        Buffer: value.as_mut_ptr(),
    }
}
fn sid(value: &str) -> io::Result<LocalAllocation> {
    if !valid_account_sid(value) || value == SYSTEM {
        return Err(denied("invalid fixture policy SID"));
    }
    let mut pointer = null_mut();
    check(unsafe { ConvertStringSidToSidW(wide(value).as_ptr(), &mut pointer) })?;
    Ok(LocalAllocation(pointer))
}
impl Policy {
    pub(super) fn open() -> io::Result<Self> {
        require_system()?;
        let attributes = lsa::LSA_OBJECT_ATTRIBUTES {
            Length: size_of::<lsa::LSA_OBJECT_ATTRIBUTES>() as u32,
            ..Default::default()
        };
        let mut handle = 0;
        result(unsafe {
            lsa::LsaOpenPolicy(
                null(),
                &attributes,
                (lsa::POLICY_LOOKUP_NAMES
                    | lsa::POLICY_CREATE_ACCOUNT
                    | lsa::POLICY_VIEW_LOCAL_INFORMATION) as u32,
                &mut handle,
            )
        })?;
        Ok(Self(handle))
    }
    pub(super) fn direct(&self, account: &str) -> io::Result<Option<BTreeSet<String>>> {
        require_system()?;
        let sid = sid(account)?;
        let mut rights = null_mut();
        let mut count = 0;
        let code =
            unsafe { lsa::LsaEnumerateAccountRights(self.0, sid.0, &mut rights, &mut count) };
        let memory = Memory(rights.cast());
        if code as u32 == 0xc0000034 {
            // STATUS_OBJECT_NAME_NOT_FOUND: no LSA account.
            return Ok(None);
        }
        result(code)?;
        if count > 64 || (count != 0 && memory.0.is_null()) {
            return Err(denied("invalid fixture LSA rights list"));
        }
        let mut names = BTreeSet::new();
        for index in 0..count as usize {
            let item = unsafe { &*rights.add(index) };
            if item.Length == 0
                || item.Length > 256
                || item.Length % 2 != 0
                || item.Buffer.is_null()
                || item.MaximumLength < item.Length
            {
                return Err(denied("invalid fixture LSA right name"));
            }
            let name = String::from_utf16(unsafe {
                std::slice::from_raw_parts(item.Buffer, item.Length as usize / 2)
            })
            .map_err(|_| denied("invalid LSA text"))?;
            if !names.insert(name) {
                return Err(denied("duplicate LSA right"));
            }
        }
        Ok(Some(names))
    }
    pub(super) fn snapshot(&self) -> io::Result<Snapshot> {
        require_system()?;
        let mut snapshot = Snapshot::new();
        for right in std::iter::once(None).chain(DENIALS.into_iter().map(Some)) {
            let mut name = wide(right.unwrap_or(""));
            let name = text(&mut name);
            let mut buffer = null_mut();
            let mut count = 0;
            let code = unsafe {
                lsa::LsaEnumerateAccountsWithUserRight(
                    self.0,
                    if right.is_some() { &name } else { null() },
                    &mut buffer,
                    &mut count,
                )
            };
            let memory = Memory(buffer);
            let mut sids = BTreeSet::new();
            if code as u32 != 0x8000001a {
                // STATUS_NO_MORE_ENTRIES
                result(code)?;
                if count > 4096 || (count != 0 && memory.0.is_null()) {
                    return Err(denied("invalid LSA account list"));
                }
                for index in 0..count as usize {
                    let row =
                        unsafe { &*buffer.cast::<lsa::LSA_ENUMERATION_INFORMATION>().add(index) };
                    if !sids.insert(sid_string(row.Sid)?) {
                        return Err(denied("duplicate LSA account"));
                    }
                }
            }
            snapshot.insert(right.unwrap_or("all_accounts").to_owned(), sids);
        }
        Ok(snapshot)
    }
    pub(super) fn deny(&self, account: &str) -> io::Result<()> {
        require_system()?;
        if self.direct(account)?.is_some() {
            return Err(denied("refuse pre-existing LSA policy account"));
        }
        let sid = sid(account)?;
        let mut names: Vec<_> = DENIALS.iter().map(|name| wide(name)).collect();
        let rights: Vec<_> = names.iter_mut().map(|name| text(name)).collect();
        result(unsafe {
            lsa::LsaAddAccountRights(self.0, sid.0, rights.as_ptr(), rights.len() as u32)
        })?;
        if self.direct(account)? != Some(DENIALS.iter().map(|s| s.to_string()).collect()) {
            return Err(denied("fixture deny rights did not converge"));
        }
        Ok(())
    }
    pub(super) fn remove_fixture(&self, account: &str) -> io::Result<()> {
        require_system()?;
        if let Some(current) = self.direct(account)? {
            if !current.iter().all(|name| DENIALS.contains(&name.as_str())) {
                return Err(denied("fixture LSA account gained unrelated rights"));
            }
            let sid = sid(account)?;
            // The caller proved there was no LSA account before this fixture.
            // AllRights removes this new LSA object, not its SAM identity.
            result(unsafe { lsa::LsaRemoveAccountRights(self.0, sid.0, true, null(), 0) })?;
        }
        if self.direct(account)?.is_some() {
            return Err(denied("fixture LSA object remains"));
        }
        Ok(())
    }
}
