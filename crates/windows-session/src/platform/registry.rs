//! Small, per-user authorization records in the 64-bit machine registry. This
//! authority API never adopts/changes an OS user. The separate accounts module
//! shares only the verified registry primitives, not authority records.
use super::*;
use windows_sys::Win32::System::Registry::*;

const SYSTEM: &str = "S-1-5-18";
const ROOT: &str = "VCWorkspace.ComputerV2";
const MAX_VALUE: usize = 65_536;

mod transaction;
pub use transaction::AuthorityUpdate;
pub mod accounts;

struct Key(HKEY);
impl Drop for Key {
    fn drop(&mut self) {
        // SAFETY: every Key owns a non-predefined handle returned by RegOpen/Create.
        unsafe {
            RegCloseKey(self.0);
        }
    }
}

fn status(code: u32) -> io::Result<()> {
    if code == ERROR_SUCCESS {
        Ok(())
    } else {
        Err(io::Error::from_raw_os_error(code as i32))
    }
}

fn require_system() -> io::Result<()> {
    if !current_identity()?.is_system() {
        return Err(denied("authority updates require LocalSystem Session 0"));
    }
    // A thread carrying another token must not turn a read-only helper context
    // into a SYSTEM write merely because the underlying process is SYSTEM.
    let mut token = null_mut();
    if unsafe { OpenThreadToken(GetCurrentThread(), TOKEN_QUERY, 1, &mut token) } != 0 {
        drop(own(token)?);
        return Err(denied("authority updates cannot run while impersonating"));
    }
    if unsafe { GetLastError() } != ERROR_NO_TOKEN {
        return Err(io::Error::last_os_error());
    }
    Ok(())
}

fn identity(username: &str, sid: &str) -> io::Result<()> {
    pipe_name(username, 0)?;
    if !valid_account_sid(sid) || sid == SYSTEM {
        return Err(denied("authority requires a managed user SID"));
    }
    Ok(())
}

fn security(sid: Option<&str>) -> io::Result<LocalAllocation> {
    let reader = match sid {
        Some(sid) if valid_account_sid(sid) && sid != SYSTEM => format!("(A;;0x00020019;;;{sid})"),
        Some(_) => return Err(denied("invalid authority SID")),
        None => String::new(),
    };
    let text = wide(&format!("O:SYD:P(A;;0x000f003f;;;SY){reader}"));
    let mut descriptor = null_mut();
    // SAFETY: fixed SDDL plus validated account SID, API owns allocation.
    check(unsafe {
        ConvertStringSecurityDescriptorToSecurityDescriptorW(
            text.as_ptr(),
            1,
            &mut descriptor,
            null_mut(),
        )
    })?;
    Ok(LocalAllocation(descriptor))
}

fn read_value(key: &Key, name: &str) -> io::Result<(u32, Vec<u8>)> {
    let name = wide(name);
    // An atomic registry value may change size between the size/read calls.
    // Retry boundedly, not with an unbounded allocation or a partial snapshot.
    for _ in 0..3 {
        let mut bytes = 0;
        let mut kind = 0;
        status(unsafe {
            RegQueryValueExW(
                key.0,
                name.as_ptr(),
                null(),
                &mut kind,
                null_mut(),
                &mut bytes,
            )
        })?;
        if bytes as usize > MAX_VALUE {
            return Err(denied("authority registry value exceeds limit"));
        }
        let mut value = vec![0u8; bytes as usize];
        let code = unsafe {
            RegQueryValueExW(
                key.0,
                name.as_ptr(),
                null(),
                &mut kind,
                value.as_mut_ptr(),
                &mut bytes,
            )
        };
        if code == ERROR_MORE_DATA {
            continue;
        }
        status(code)?;
        if bytes as usize > value.len() {
            return Err(denied("invalid registry value size"));
        }
        value.truncate(bytes as usize);
        return Ok((kind, value));
    }
    Err(io::Error::new(
        io::ErrorKind::WouldBlock,
        "authority changed while reading",
    ))
}

fn no_link(key: &Key) -> io::Result<()> {
    match read_value(key, "SymbolicLinkValue") {
        Err(error) if error.raw_os_error() == Some(ERROR_FILE_NOT_FOUND as i32) => Ok(()),
        // Even a wrongly typed link marker is unexpected in this namespace.
        Ok(_) => Err(denied("registry links are not authority records")),
        Err(error) => Err(error),
    }
}

fn open(parent: HKEY, name: &str, access: u32) -> io::Result<Key> {
    let mut raw = null_mut();
    // SAFETY: caller supplies fixed/validated single components, or the fixed
    // helper read path. OPEN_LINK exposes, rather than follows, the last link.
    status(unsafe {
        RegOpenKeyExW(
            parent,
            wide(name).as_ptr(),
            REG_OPTION_OPEN_LINK,
            access | KEY_WOW64_64KEY,
            &mut raw,
        )
    })?;
    let key = Key(raw);
    no_link(&key)?;
    Ok(key)
}

fn create(parent: HKEY, name: &str, sid: Option<&str>) -> io::Result<Key> {
    if name.is_empty() || name.contains(['\\', '/', '\0']) {
        return Err(denied("single registry component required"));
    }
    match open(parent, name, KEY_ALL_ACCESS) {
        Ok(key) => {
            verify_security(&key, sid)?;
            return Ok(key);
        }
        Err(error) if error.raw_os_error() == Some(ERROR_FILE_NOT_FOUND as i32) => {}
        Err(error) => return Err(error),
    }
    let descriptor = security(sid)?;
    let attributes = SECURITY_ATTRIBUTES {
        nLength: size_of::<SECURITY_ATTRIBUTES>() as u32,
        lpSecurityDescriptor: descriptor.0,
        bInheritHandle: 0,
    };
    let mut raw = null_mut();
    let mut disposition = 0;
    // SAFETY: create one child of an already-open key, with the final protected
    // DACL at creation (never a permissive-then-chmod interval).
    status(unsafe {
        RegCreateKeyExW(
            parent,
            wide(name).as_ptr(),
            0,
            null(),
            REG_OPTION_NON_VOLATILE,
            KEY_ALL_ACCESS | KEY_WOW64_64KEY,
            &attributes,
            &mut raw,
            &mut disposition,
        )
    })?;
    let key = Key(raw);
    if disposition != REG_CREATED_NEW_KEY {
        // A competing creator may have inserted a symbolic link. No values are
        // written through the returned handle; a fresh call will OPEN_LINK-check.
        return Err(io::Error::new(
            io::ErrorKind::AlreadyExists,
            "authority key changed during creation",
        ));
    }
    no_link(&key)?;
    verify_security(&key, sid)?;
    Ok(key)
}

fn verify_security(key: &Key, sid: Option<&str>) -> io::Result<()> {
    let info = OWNER_SECURITY_INFORMATION | DACL_SECURITY_INFORMATION;
    let mut bytes = 0;
    let code = unsafe { RegGetKeySecurity(key.0, info, null_mut(), &mut bytes) };
    if code != ERROR_INSUFFICIENT_BUFFER || bytes == 0 || bytes > 65_536 {
        return Err(denied("invalid authority security descriptor size"));
    }
    let mut buffer = vec![0usize; (bytes as usize).div_ceil(size_of::<usize>())];
    status(unsafe { RegGetKeySecurity(key.0, info, buffer.as_mut_ptr().cast(), &mut bytes) })?;
    let descriptor = buffer.as_mut_ptr().cast();
    let mut owner = null_mut();
    let mut defaulted = 0;
    let mut control = 0;
    let mut revision = 0;
    // SAFETY: API returned a valid, aligned self-relative descriptor, kept live
    // while inspecting the owner/DACL/ACE pointers into its buffer.
    check(unsafe { GetSecurityDescriptorOwner(descriptor, &mut owner, &mut defaulted) })?;
    check(unsafe { GetSecurityDescriptorControl(descriptor, &mut control, &mut revision) })?;
    if owner.is_null() || sid_string(owner)? != SYSTEM || control & SE_DACL_PROTECTED == 0 {
        return Err(denied(
            "authority must be SYSTEM-owned with a protected ACL",
        ));
    }
    let mut present = 0;
    let mut dacl = null_mut();
    check(unsafe {
        GetSecurityDescriptorDacl(descriptor, &mut present, &mut dacl, &mut defaulted)
    })?;
    if present == 0 || dacl.is_null() {
        return Err(denied("authority has no restrictive DACL"));
    }
    let mut size: ACL_SIZE_INFORMATION = unsafe { zeroed() };
    check(unsafe {
        GetAclInformation(
            dacl,
            (&mut size as *mut ACL_SIZE_INFORMATION).cast(),
            size_of::<ACL_SIZE_INFORMATION>() as u32,
            AclSizeInformation,
        )
    })?;
    if size.AceCount != if sid.is_some() { 2 } else { 1 } {
        return Err(denied("unexpected authority ACL entries"));
    }
    let mut system_seen = false;
    let mut reader_seen = false;
    for index in 0..size.AceCount {
        let mut raw = null_mut();
        check(unsafe { GetAce(dacl, index, &mut raw) })?;
        // The kernel validates ACL/ACE structures on write; still check the ACE
        // type/flags/size before treating this as an ACCESS_ALLOWED_ACE SID.
        if raw.is_null() {
            return Err(denied("missing authority ACE"));
        }
        let header = unsafe { &*raw.cast::<ACE_HEADER>() };
        if header.AceType != 0
            || header.AceFlags != 0
            || (header.AceSize as usize) < size_of::<ACCESS_ALLOWED_ACE>() + 4
        {
            return Err(denied("unexpected authority ACL form"));
        }
        let ace = unsafe { &*raw.cast::<ACCESS_ALLOWED_ACE>() };
        let actual = sid_string(std::ptr::addr_of!(ace.SidStart).cast_mut().cast())?;
        if actual == SYSTEM && ace.Mask == KEY_ALL_ACCESS && !system_seen {
            system_seen = true;
        } else if Some(actual.as_str()) == sid && ace.Mask == KEY_READ && !reader_seen {
            reader_seen = true;
        } else {
            return Err(denied("authority grants unexpected registry rights"));
        }
    }
    if !system_seen || (sid.is_some() && !reader_seen) {
        return Err(denied("incomplete authority ACL"));
    }
    Ok(())
}

fn write_value(key: &Key, name: &str, value: &[u8]) -> io::Result<()> {
    if value.is_empty() || value.len() > MAX_VALUE {
        return Err(denied("invalid authority value size"));
    }
    // SAFETY: a single REG_BINARY value is replaced; never a multi-file or
    // partially rewritten JSON snapshot. The parser must still check its schema.
    status(unsafe {
        RegSetValueExW(
            key.0,
            wide(name).as_ptr(),
            0,
            REG_BINARY,
            value.as_ptr(),
            value.len() as u32,
        )
    })
}

fn verify_sid(key: &Key, sid: &str) -> io::Result<()> {
    let (kind, value) = read_value(key, "SID")?;
    if kind != REG_BINARY || value != sid.as_bytes() {
        return Err(denied("authority SID binding changed"));
    }
    Ok(())
}

/// A per-user, SYSTEM-written authorization snapshot. No generic registry path
/// or arbitrary value name is accepted by the public API. The record grants no
/// OS-account ownership and does not itself validate the application's JSON.
#[derive(Clone)]
pub struct AuthorityRegistry {
    root: String,
}
impl Default for AuthorityRegistry {
    fn default() -> Self {
        Self { root: ROOT.into() }
    }
}
impl AuthorityRegistry {
    fn root(&self, create_missing: bool) -> io::Result<Key> {
        let software = open(HKEY_LOCAL_MACHINE, "SOFTWARE", KEY_READ)?;
        let root = if create_missing {
            create(software.0, &self.root, None)?
        } else {
            open(software.0, &self.root, KEY_ALL_ACCESS)?
        };
        verify_security(&root, None)?;
        Ok(root)
    }

    pub fn initialize(&self, username: &str, sid: &str) -> io::Result<()> {
        require_system()?;
        identity(username, sid)?;
        let root = self.root(true)?;
        let key = create(root.0, username, Some(sid))?;
        match read_value(&key, "SID") {
            Err(error) if error.raw_os_error() == Some(ERROR_FILE_NOT_FOUND as i32) => {
                write_value(&key, "SID", sid.as_bytes())?
            }
            _ => verify_sid(&key, sid)?,
        }
        // Missing authority means denied. Initialization never renews a lease,
        // overwrites a tombstone, or supplies a default active authorization.
        Ok(())
    }

    fn user(&self, username: &str, sid: &str) -> io::Result<Key> {
        identity(username, sid)?;
        // Open the exact leaf directly: helpers do not need enumerate/write
        // access to the SYSTEM-only parent. Verify the returned leaf every time.
        let key = open(
            HKEY_LOCAL_MACHINE,
            &format!(r"SOFTWARE\{}\{username}", self.root),
            KEY_READ,
        )?;
        verify_security(&key, Some(sid))?;
        verify_sid(&key, sid)?;
        Ok(key)
    }

    /// Verify the initialized SID/ACL without requiring an active authority.
    /// Helpers may start while revoked, but cannot act until authorized.
    pub fn check_registration(&self, username: &str, sid: &str) -> io::Result<()> {
        self.user(username, sid).map(|_| ())
    }

    pub fn read(&self, username: &str, sid: &str) -> io::Result<Vec<u8>> {
        let key = self.user(username, sid)?;
        let (kind, value) = read_value(&key, "Authority")?;
        if kind != REG_BINARY || value.is_empty() {
            return Err(denied("invalid authority registry type"));
        }
        Ok(value)
    }

    // Only native fixtures may bypass VM-wide publication. Production writers
    // must use begin_update(), including the persistent ordering fence.
    #[cfg(test)]
    pub fn write(&self, username: &str, sid: &str, value: &[u8]) -> io::Result<()> {
        require_system()?;
        identity(username, sid)?;
        let root = self.root(false)?;
        let key = open(root.0, username, KEY_ALL_ACCESS)?;
        verify_security(&key, Some(sid))?;
        verify_sid(&key, sid)?;
        write_value(&key, "Authority", value)
    }

    pub fn begin_update(&self) -> io::Result<AuthorityUpdate> {
        AuthorityUpdate::begin(self)
    }

    /// SYSTEM can enumerate identities for a VM-wide tombstone operation. A
    /// malformed entry fails closed; no link, unknown name or foreign SID is
    /// silently ignored. Callers serialize initialization/activation/revocation.
    pub fn users(&self) -> io::Result<Vec<(String, String)>> {
        require_system()?;
        let root = match self.root(false) {
            Err(error) if error.raw_os_error() == Some(ERROR_FILE_NOT_FOUND as i32) => {
                return Ok(Vec::new())
            }
            result => result?,
        };
        let mut users = Vec::new();
        for index in 0..=1024 {
            let mut name = [0u16; 256];
            let mut length = name.len() as u32;
            // SAFETY: bounded writable subkey-name array; unused outputs null.
            let code = unsafe {
                RegEnumKeyExW(
                    root.0,
                    index,
                    name.as_mut_ptr(),
                    &mut length,
                    null(),
                    null_mut(),
                    null_mut(),
                    null_mut(),
                )
            };
            if code == ERROR_NO_MORE_ITEMS {
                return Ok(users);
            }
            status(code)?;
            if index == 1024 {
                return Err(denied("too many authority records"));
            }
            let name = String::from_utf16(&name[..length as usize]).map_err(io::Error::other)?;
            pipe_name(&name, 0)?;
            let key = open(root.0, &name, KEY_READ)?;
            let (kind, raw) = read_value(&key, "SID")?;
            let sid = String::from_utf8(raw).map_err(io::Error::other)?;
            identity(&name, &sid)?;
            if kind != REG_BINARY {
                return Err(denied("invalid SID registry type"));
            }
            verify_security(&key, Some(&sid))?;
            users.push((name, sid));
        }
        unreachable!()
    }
}

#[cfg(test)]
mod tests;
