//! Narrow, safe interface to Windows session identity and authenticated local
//! pipes. Win32 unsafe code is confined here; the Guest agent keeps forbidding
//! unsafe code. This is not an unattended Windows login implementation.
#![deny(unsafe_op_in_unsafe_fn)]

use std::io;

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Identity {
    pub sid: String,
    pub session_id: u32,
    /// Logon LUID, not the SID's RID. Distinguishes a reused WTS session number.
    pub authentication_id: u64,
}

impl Identity {
    /// Well-known LocalSystem logon session (LSA 0x0:0x3e7), not a user session.
    pub fn local_system() -> Self {
        Self {
            sid: "S-1-5-18".into(),
            session_id: 0,
            authentication_id: 0x3e7,
        }
    }

    pub fn binding_id(&self) -> String {
        format!(
            "windows:{}:{:016x}",
            self.session_id, self.authentication_id
        )
    }

    pub fn is_system(&self) -> bool {
        self.sid == "S-1-5-18" && self.session_id == 0
    }
}

fn denied(message: &str) -> io::Error {
    io::Error::new(io::ErrorKind::PermissionDenied, message)
}

// Only local/domain account SIDs and LocalSystem are accepted in our generated
// SDDL. Explicit parsing also prevents interpolating SDDL control characters.
pub fn valid_account_sid(sid: &str) -> bool {
    if sid == "S-1-5-18" {
        return true;
    }
    let components: Vec<_> = sid.split('-').collect();
    components.len() == 8
        && components[..4] == ["S", "1", "5", "21"]
        && components[4..].iter().all(|part| {
            part.parse::<u32>()
                .is_ok_and(|value| value.to_string() == *part)
        })
        && components[7].parse::<u32>().is_ok_and(|rid| rid >= 1000)
}

pub fn pipe_name(username: &str, session_id: u32) -> io::Result<String> {
    if username.len() != 15
        || !(username.starts_with("vca") || username.starts_with("vcw"))
        || !username[3..]
            .bytes()
            .all(|b| b.is_ascii_hexdigit() && !b.is_ascii_uppercase())
    {
        return Err(denied("managed account name required"));
    }
    Ok(format!(
        r"\\.\pipe\VCWorkspace.ComputerV2.{username}.{session_id}"
    ))
}

#[cfg(windows)]
mod platform;
#[cfg(windows)]
pub use platform::desktop::{ensure_input_desktop, move_cursor_physical};
#[cfg(windows)]
pub use platform::input::{send_key_chord, send_unicode_text_chunk};
#[cfg(windows)]
pub use platform::launcher::HelperLaunch;
#[cfg(windows)]
pub use platform::registry::accounts::native::{
    NativeAccount, NativeAccountObservation, NativeAccounts,
};
#[cfg(windows)]
pub use platform::registry::accounts::{
    reconcile_accounts, AgentAccount, AgentAccountObservation, AgentAccounts,
};
#[cfg(windows)]
pub use platform::registry::{AuthorityRegistry, AuthorityUpdate};
#[cfg(windows)]
pub use platform::worker::{run_worker, WorkerOutput};
#[cfg(windows)]
pub use platform::{
    current_identity, local_account_sid, logged_on_identity, LocalPipe, PipeListener,
};

#[cfg(test)]
mod tests {
    use super::*;

    #[cfg(windows)]
    #[test]
    #[ignore = "explicit child process exit; parent owns the exact account fixture"]
    fn account_lease_crash_child() {
        platform::registry::accounts::tests::crash_during_lease();
    }

    #[cfg(windows)]
    #[test]
    #[ignore = "explicit child process exit; parent owns the exact Native account fixture"]
    fn native_account_crash_child() {
        platform::registry::accounts::native::tests::crash_during_credential();
    }

    #[cfg(windows)]
    #[test]
    #[ignore = "explicit same-SID primary-user profile child; parent owns its fixture"]
    fn native_profile_key_child() {
        platform::registry::accounts::native::profile_tests::migrate_in_user_process().unwrap();
    }

    #[test]
    fn sid_and_pipe_names_cannot_escape_their_namespaces() {
        assert!(valid_account_sid("S-1-5-21-1-2-4294967295-1000"));
        for bad in [
            "S-1-5-32-544",
            "S-1-5-21-1-2-3-500",
            "S-1-5-21-01-2-3-1000",
            "S-1-5-21-1-2-3-4294967296",
            "S-1-5-18)(A;;GA;;;WD)",
            "s-1-5-18",
        ] {
            assert!(!valid_account_sid(bad), "accepted {bad}");
        }
        assert!(pipe_name("vca0123456789ab", 3).is_ok());
        for bad in [
            "vdi",
            "vca0123456789AB",
            "vca0123456789/a",
            "vcw0123456789a🦀",
        ] {
            assert!(pipe_name(bad, 3).is_err());
        }
    }

    #[test]
    fn binding_changes_with_logon_incarnation() {
        let mut identity = Identity {
            sid: "S-1-5-21-1-2-3-1001".into(),
            session_id: 3,
            authentication_id: 1,
        };
        assert!(!identity.is_system());
        let old = identity.binding_id();
        identity.authentication_id += 1;
        assert_ne!(old, identity.binding_id());
        assert_eq!(identity.binding_id(), "windows:3:0000000000000002");
    }
}
