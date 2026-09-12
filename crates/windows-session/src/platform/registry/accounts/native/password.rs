use super::*;
use vc_workspace_guest_lifecycle::Zeroizing;
use windows_sys::Win32::System::SystemInformation::{ComputerNameNetBIOS, GetComputerNameExW};

pub(super) fn has_profile(sid: &str) -> io::Result<bool> {
    require_system()?;
    if !valid_account_sid(sid) || sid == SYSTEM {
        return Err(denied("invalid Native profile SID"));
    }
    let path = wide(&format!(
        r"SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\{sid}"
    ));
    let mut key = null_mut();
    let result = unsafe {
        RegOpenKeyExW(
            HKEY_LOCAL_MACHINE,
            path.as_ptr(),
            REG_OPTION_OPEN_LINK,
            KEY_READ | KEY_WOW64_64KEY,
            &mut key,
        )
    };
    if result == ERROR_FILE_NOT_FOUND {
        return Ok(false);
    }
    status(result)?;
    drop(Key(key));
    Ok(true)
}

// A local password CHANGE supplies the old secret; never fall back to a reset
// if password policy, identity verification or the change operation fails.
// The lifecycle caller must hold its account gate and check the immutable SID.
#[cfg_attr(not(test), allow(dead_code))] // Enabled only after profile acceptance.
pub(super) fn change(name: &str, previous: &str, next: &str) -> io::Result<()> {
    require_system()?;
    Namespace::Native.validate(name)?;
    let mut computer = [0u16; 256];
    let mut length = computer.len() as u32;
    check(unsafe { GetComputerNameExW(ComputerNameNetBIOS, computer.as_mut_ptr(), &mut length) })?;
    if length == 0 || length as usize >= computer.len() {
        return Err(denied("invalid local SAM computer name"));
    }
    let computer = String::from_utf16(&computer[..length as usize]).map_err(io::Error::other)?;
    let server = wide(&format!(r"\\{computer}"));
    let previous = Zeroizing::new(wide(previous));
    let next = Zeroizing::new(wide(next));
    status(unsafe {
        NetUserChangePassword(
            server.as_ptr(),
            wide(name).as_ptr(),
            previous.as_ptr(),
            next.as_ptr(),
        )
    })
}
