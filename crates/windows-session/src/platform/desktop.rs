//! A logon token alone does not mean the user desktop is ready. During first
//! login, lock and UAC, the input desktop may still be Winlogon/secure desktop.
//! Read readiness only: never switch desktops or acquire access to secure UI.
use super::*;
use windows_sys::Win32::System::StationsAndDesktops::*;

fn object_name(handle: HANDLE) -> io::Result<String> {
    if handle.is_null() {
        return Err(denied("Windows desktop object is unavailable"));
    }
    let mut name = [0u16; 256];
    let mut needed = 0;
    // SAFETY: borrowed desktop/window-station handle, bounded aligned UTF-16
    // storage; neither this function nor its callers close borrowed handles.
    check(unsafe {
        GetUserObjectInformationW(
            handle,
            UOI_NAME,
            name.as_mut_ptr().cast(),
            std::mem::size_of_val(&name) as u32,
            &mut needed,
        )
    })?;
    let count = needed as usize / 2;
    if needed % 2 != 0 || !(2..=name.len()).contains(&count) || name[count - 1] != 0 {
        return Err(denied("invalid Windows desktop object name"));
    }
    let name = String::from_utf16(&name[..count - 1]).map_err(io::Error::other)?;
    if name.contains(['\0', '\\']) {
        return Err(denied("invalid Windows desktop object name"));
    }
    Ok(name)
}

/// Require the current thread's normal interactive desktop to be receiving
/// input. A disconnected, locked or not-yet-ready desktop is unavailable, not
/// permission to capture a different user's or a secure desktop instead.
pub fn ensure_input_desktop() -> io::Result<()> {
    let identity = current_identity()?;
    if identity.session_id == 0 || identity.is_system() {
        return Err(denied("Session 0 has no user input desktop"));
    }
    // SAFETY: these return borrowed handles of this process/current thread.
    let station = unsafe { GetProcessWindowStation() };
    let desktop = unsafe { GetThreadDesktop(GetCurrentThreadId()) };
    if !object_name(station)?.eq_ignore_ascii_case("WinSta0")
        || !object_name(desktop)?.eq_ignore_ascii_case("Default")
    {
        return Err(denied("worker is not attached to the normal user desktop"));
    }
    let mut receives_input: i32 = 0;
    let mut needed = 0;
    // SAFETY: UOI_IO returns a BOOL into the correctly sized aligned buffer.
    check(unsafe {
        GetUserObjectInformationW(
            desktop,
            UOI_IO,
            std::ptr::addr_of_mut!(receives_input).cast(),
            size_of::<i32>() as u32,
            &mut needed,
        )
    })?;
    if needed != size_of::<i32>() as u32 || receives_input == 0 {
        return Err(denied("user input desktop is locked or not ready"));
    }
    Ok(())
}

/// UIA rectangles and captured monitor bounds are physical pixels. The usual
/// SetCursorPos API instead accepts the calling thread's DPI-virtualized space;
/// using it here shifts clicks away from their targets on scaled desktops.
pub fn move_cursor_physical(x: i32, y: i32) -> io::Result<()> {
    ensure_input_desktop()?;
    // SAFETY: value-only coordinates, no pointers; access is limited to the
    // already validated current user's normal input desktop. No DPI setting or
    // desktop is changed, and negative multi-monitor origins remain valid.
    check(unsafe { windows_sys::Win32::UI::WindowsAndMessaging::SetPhysicalCursorPos(x, y) })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn desktop_object_names_are_bounded_and_borrowed() {
        // SAFETY: borrowed handles, never closed or made inheritable here.
        let station = unsafe { GetProcessWindowStation() };
        let desktop = unsafe { GetThreadDesktop(GetCurrentThreadId()) };
        assert!(!object_name(station).unwrap().is_empty());
        assert!(!object_name(desktop).unwrap().is_empty());
        assert!(!object_name(station).unwrap().is_empty());
    }

    #[test]
    fn input_desktop_never_admits_session_zero_or_invalid_handles() {
        assert!(object_name(null_mut()).is_err());
        if current_identity().unwrap().session_id == 0 {
            assert_eq!(
                ensure_input_desktop().unwrap_err().kind(),
                io::ErrorKind::PermissionDenied
            );
            assert_eq!(
                move_cursor_physical(0, 0).unwrap_err().kind(),
                io::ErrorKind::PermissionDenied
            );
        }
    }
}
