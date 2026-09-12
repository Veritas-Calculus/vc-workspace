//! Start the current Agent image in an EXISTING, unambiguous WTS logon. This
//! does not authenticate a user, create a desktop, or unlock a secure desktop.
use super::*;
use std::{ffi::OsString, mem::size_of_val, os::windows::ffi::OsStrExt};
use windows_sys::Win32::System::{Environment::*, JobObjects::*};

struct Environment(*mut c_void);
impl Drop for Environment {
    fn drop(&mut self) {
        // SAFETY: owned result of CreateEnvironmentBlock.
        unsafe { DestroyEnvironmentBlock(self.0) };
    }
}

struct PendingProcess {
    process: OwnedHandle,
    job: OwnedHandle,
    detached: bool,
}
impl PendingProcess {
    fn detach(&mut self) -> io::Result<()> {
        let limits: JOBOBJECT_EXTENDED_LIMIT_INFORMATION = unsafe { zeroed() };
        check(unsafe {
            SetInformationJobObject(
                self.job.as_raw_handle(),
                JobObjectExtendedLimitInformation,
                (&limits as *const JOBOBJECT_EXTENDED_LIMIT_INFORMATION).cast(),
                size_of_val(&limits) as u32,
            )
        })?;
        self.detached = true;
        Ok(())
    }
}
impl Drop for PendingProcess {
    fn drop(&mut self) {
        if !self.detached {
            // Only the process tree we created, never PID/name-based cleanup.
            // Closing the last job handle also kills on failure/crash.
            unsafe { TerminateJobObject(self.job.as_raw_handle(), 1) };
        }
    }
}

/// Holds the SYSTEM-only account lifecycle gate until readiness is committed.
/// A dropped/failed launch terminates its own pending process tree. A committed
/// Helper survives this short-lived QGA command and ends with its user logon.
/// The caller must exchange/validate the application announcement before commit.
pub struct HelperLaunch {
    username: String,
    identity: Identity,
    _gate: OwnedHandle,
    pending: Option<PendingProcess>,
}
impl Drop for HelperLaunch {
    fn drop(&mut self) {
        // Field declaration order must not release the account gate before
        // rolling back a partially started process tree.
        drop(self.pending.take());
    }
}
impl HelperLaunch {
    pub fn begin(username: &str) -> io::Result<Self> {
        let (account, gate) =
            registry::accounts::AgentAccounts::default().lock_enabled(username)?;
        let identity = logged_on_identity(&account.sid)?;
        validate_identity(&identity)?;
        registry::AuthorityRegistry::default().check_registration(username, &account.sid)?;
        Ok(Self {
            username: username.into(),
            identity,
            _gate: gate,
            pending: None,
        })
    }

    pub fn identity(&self) -> &Identity {
        &self.identity
    }

    /// Probe an existing Helper before starting one. After start, authenticate
    /// not only SID/WTS/LUID but the held kernel process returned by creation.
    pub fn connect(&self, timeout: Duration) -> io::Result<LocalPipe> {
        self.revalidate()?;
        LocalPipe::connect_process(
            &self.username,
            &self.identity,
            timeout,
            self.pending.as_ref().map(|p| p.process.as_raw_handle()),
        )
    }

    fn revalidate(&self) -> io::Result<()> {
        if logged_on_identity(&self.identity.sid)? != self.identity {
            return Err(denied("managed WTS logon changed during Helper startup"));
        }
        if let Some(pending) = &self.pending {
            if unsafe { WaitForSingleObject(pending.process.as_raw_handle(), 0) } != WAIT_TIMEOUT
                || process_identity(pending.process.as_raw_handle())? != self.identity
            {
                return Err(denied("launched Helper exited or changed identity"));
            }
        }
        Ok(())
    }

    pub fn start(&mut self) -> io::Result<()> {
        if self.pending.is_some() {
            return Err(denied("Helper launch already attempted"));
        }
        self.revalidate()?;
        let mut token = null_mut();
        // No LogonUser/SetTokenInformation or active-console fallback: WTS must
        // already own the exact full SID, session number AND logon LUID.
        check(unsafe { WTSQueryUserToken(self.identity.session_id, &mut token) })?;
        let token = own(token)?;
        if token_identity(token.as_raw_handle())? != self.identity {
            return Err(denied("WTS token changed before Helper creation"));
        }
        let mut environment = null_mut();
        // FALSE excludes the SYSTEM service's process environment/secrets.
        check(unsafe { CreateEnvironmentBlock(&mut environment, token.as_raw_handle(), 0) })?;
        let environment = Environment(environment);
        let executable = std::env::current_exe()?;
        let arguments: Vec<OsString> = ["computer-v2-helper", "--guest-user", &self.username]
            .into_iter()
            .map(OsString::from)
            .collect();
        let (application, mut command) = worker::command_line(&executable, &arguments)?;
        let directory: Vec<u16> = executable
            .parent()
            .ok_or_else(|| denied("Agent installation directory required"))?
            .as_os_str()
            .encode_wide()
            .chain(Some(0))
            .collect();
        let job = own(unsafe { CreateJobObjectW(null(), null()) })?;
        let mut limits: JOBOBJECT_EXTENDED_LIMIT_INFORMATION = unsafe { zeroed() };
        limits.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
        check(unsafe {
            SetInformationJobObject(
                job.as_raw_handle(),
                JobObjectExtendedLimitInformation,
                (&limits as *const JOBOBJECT_EXTENDED_LIMIT_INFORMATION).cast(),
                size_of_val(&limits) as u32,
            )
        })?;
        let mut desktop = wide(r"WinSta0\Default");
        let startup = STARTUPINFOW {
            cb: size_of::<STARTUPINFOW>() as u32,
            lpDesktop: desktop.as_mut_ptr(),
            ..unsafe { zeroed() }
        };
        let mut info: PROCESS_INFORMATION = unsafe { zeroed() };
        // Explicit application/working directory/desktop and user environment.
        // No shell, PATH lookup, inherited handles, credentials or console UI.
        check(unsafe {
            CreateProcessAsUserW(
                token.as_raw_handle(),
                application.as_ptr(),
                command.as_mut_ptr(),
                null(),
                null(),
                0,
                CREATE_SUSPENDED | CREATE_UNICODE_ENVIRONMENT | CREATE_NO_WINDOW,
                environment.0,
                directory.as_ptr(),
                &startup,
                &mut info,
            )
        })?;
        let process = own(info.hProcess)?;
        let thread = own(info.hThread)?;
        if unsafe { AssignProcessToJobObject(job.as_raw_handle(), process.as_raw_handle()) } == 0 {
            let error = io::Error::last_os_error();
            // Still suspended: no user code ran outside our rollback boundary.
            unsafe { TerminateProcess(process.as_raw_handle(), 1) };
            return Err(error);
        }
        self.pending = Some(PendingProcess {
            process,
            job,
            detached: false,
        });
        self.revalidate()?;
        if unsafe { ResumeThread(thread.as_raw_handle()) } == u32::MAX {
            return Err(io::Error::last_os_error());
        }
        Ok(())
    }

    pub fn commit(mut self) -> io::Result<()> {
        self.revalidate()?;
        if let Some(pending) = &mut self.pending {
            pending.detach()?;
        }
        Ok(())
    }
}

fn validate_identity(identity: &Identity) -> io::Result<()> {
    if !valid_account_sid(&identity.sid)
        || identity.sid == "S-1-5-18"
        || identity.session_id == 0
        || identity.authentication_id == 0
        || identity.authentication_id == Identity::local_system().authentication_id
    {
        return Err(denied("a real user WTS logon is required"));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pending_launch_rolls_back_but_commit_survives_launcher_handle_close() {
        use std::os::windows::process::CommandExt;
        struct Child(std::process::Child);
        impl Drop for Child {
            fn drop(&mut self) {
                // Always exact held child, including assertion failures. The
                // fixture remains suspended and never executes its test main.
                let _ = self.0.kill();
                unsafe { WaitForSingleObject(self.0.as_raw_handle(), 1000) };
            }
        }
        for detached in [false, true] {
            let child = Child(
                std::process::Command::new(std::env::current_exe().unwrap())
                    .args(["--ignored", "--exact", "platform::tests::pipe_pin_child"])
                    .creation_flags(CREATE_SUSPENDED | CREATE_NO_WINDOW)
                    .stdin(std::process::Stdio::null())
                    .stdout(std::process::Stdio::null())
                    .stderr(std::process::Stdio::null())
                    .spawn()
                    .unwrap(),
            );
            let job = own(unsafe { CreateJobObjectW(null(), null()) }).unwrap();
            let mut limits: JOBOBJECT_EXTENDED_LIMIT_INFORMATION = unsafe { zeroed() };
            limits.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
            check(unsafe {
                SetInformationJobObject(
                    job.as_raw_handle(),
                    JobObjectExtendedLimitInformation,
                    (&limits as *const JOBOBJECT_EXTENDED_LIMIT_INFORMATION).cast(),
                    size_of_val(&limits) as u32,
                )
            })
            .unwrap();
            check(unsafe {
                AssignProcessToJobObject(job.as_raw_handle(), child.0.as_raw_handle())
            })
            .unwrap();
            let mut duplicate = null_mut();
            check(unsafe {
                DuplicateHandle(
                    GetCurrentProcess(),
                    child.0.as_raw_handle(),
                    GetCurrentProcess(),
                    &mut duplicate,
                    0,
                    0,
                    DUPLICATE_SAME_ACCESS,
                )
            })
            .unwrap();
            let mut pending = PendingProcess {
                process: own(duplicate).unwrap(),
                job,
                detached: false,
            };
            if detached {
                pending.detach().unwrap();
            }
            drop(pending);
            assert_eq!(
                unsafe { WaitForSingleObject(child.0.as_raw_handle(), 500) },
                if detached {
                    WAIT_TIMEOUT
                } else {
                    WAIT_OBJECT_0
                }
            );
        }
    }

    #[test]
    fn launch_rejects_system_synthetic_and_invalid_logons() {
        let good = Identity {
            sid: "S-1-5-21-1-2-3-1234".into(),
            session_id: 2,
            authentication_id: 12345,
        };
        assert!(validate_identity(&good).is_ok());
        for bad in [
            Identity::local_system(),
            Identity {
                session_id: 0,
                ..good.clone()
            },
            Identity {
                authentication_id: 0,
                ..good.clone()
            },
            Identity {
                authentication_id: 0x3e7,
                ..good.clone()
            },
            Identity {
                sid: "S-1-5-32-544".into(),
                ..good
            },
        ] {
            assert!(validate_identity(&bad).is_err());
        }
    }
}
