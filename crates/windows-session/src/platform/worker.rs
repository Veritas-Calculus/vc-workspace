//! Bounded same-user process trees. A job is a lifetime/resource boundary, not
//! a sandbox against a user who can ask unrelated system services to run code.
use super::*;
use std::{
    ffi::{OsStr, OsString},
    os::windows::ffi::OsStrExt,
    path::{Component, Path, Prefix},
    sync::mpsc,
};
use windows_sys::Win32::System::Environment::{CreateEnvironmentBlock, DestroyEnvironmentBlock};
use windows_sys::Win32::System::JobObjects::*;

#[derive(Debug)]
pub struct WorkerOutput {
    pub exit_code: u32,
    pub stdout: Vec<u8>,
}

fn invalid(message: &str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidInput, message)
}

// Windows CRT quoting, not shell quoting. CreateProcess gets an explicit local
// .exe; neither PATH lookup, cmd.exe expansion nor a batch-file fallback occurs.
fn quote(value: &OsStr, output: &mut Vec<u16>) -> io::Result<()> {
    output.push(b'"' as u16);
    let mut slashes = 0;
    for character in value.encode_wide() {
        if character == 0 {
            return Err(invalid("NUL in worker argument"));
        }
        if character == b'\\' as u16 {
            slashes += 1;
            continue;
        }
        if character == b'"' as u16 {
            output.extend(std::iter::repeat_n(b'\\' as u16, slashes * 2 + 1));
        } else {
            output.extend(std::iter::repeat_n(b'\\' as u16, slashes));
        }
        slashes = 0;
        output.push(character);
    }
    output.extend(std::iter::repeat_n(b'\\' as u16, slashes * 2));
    output.push(b'"' as u16);
    Ok(())
}

pub(super) fn command_line(
    executable: &Path,
    arguments: &[OsString],
) -> io::Result<(Vec<u16>, Vec<u16>)> {
    // Bound allocation before quoting; the post-quote cap below also accounts
    // for doubled backslashes and embedded quotes.
    if std::iter::once(executable.as_os_str())
        .chain(arguments.iter().map(OsString::as_os_str))
        .any(|value| value.encode_wide().take(8193).count() > 8192)
    {
        return Err(invalid("worker argument exceeds limit"));
    }
    if !executable.is_absolute()
        || !matches!(executable.components().next(), Some(Component::Prefix(prefix))
            if matches!(prefix.kind(), Prefix::Disk(_) | Prefix::VerbatimDisk(_)))
        || !executable
            .extension()
            .is_some_and(|extension| extension.eq_ignore_ascii_case("exe"))
        || executable.components().any(|component| {
            matches!(component,
            Component::Normal(name) if name.encode_wide().any(|c| c == b':' as u16 || c == 0))
        })
        || arguments.len() > 32
    {
        return Err(invalid(
            "explicit local worker executable and bounded arguments required",
        ));
    }
    let application: Vec<_> = executable
        .as_os_str()
        .encode_wide()
        .chain(Some(0))
        .collect();
    let mut command = Vec::new();
    quote(executable.as_os_str(), &mut command)?;
    for argument in arguments {
        command.push(b' ' as u16);
        quote(argument, &mut command)?;
        if command.len() > 8192 {
            return Err(invalid("worker command line exceeds limit"));
        }
    }
    if command.len() > 8192 {
        return Err(invalid("worker command line exceeds limit"));
    }
    command.push(0);
    Ok((application, command))
}

struct Attributes(Vec<usize>);
impl Attributes {
    fn handles(handles: &[HANDLE]) -> io::Result<Self> {
        let mut bytes = 0;
        // SAFETY: documented size discovery with null attribute storage.
        unsafe { InitializeProcThreadAttributeList(null_mut(), 1, 0, &mut bytes) };
        if bytes == 0 || bytes > 65_536 {
            return Err(io::Error::other("invalid process attribute size"));
        }
        let mut storage = vec![0usize; bytes.div_ceil(size_of::<usize>())];
        // SAFETY: pointer-aligned storage is at least the reported size.
        check(unsafe {
            InitializeProcThreadAttributeList(storage.as_mut_ptr().cast(), 1, 0, &mut bytes)
        })?;
        let mut attributes = Self(storage);
        // SAFETY: handles remain live through CreateProcess; caller supplies
        // only real inheritable stdin/stdout/stderr handles, never a job/token.
        check(unsafe {
            UpdateProcThreadAttribute(
                attributes.0.as_mut_ptr().cast(),
                0,
                PROC_THREAD_ATTRIBUTE_HANDLE_LIST as usize,
                handles.as_ptr().cast(),
                std::mem::size_of_val(handles),
                null_mut(),
                null(),
            )
        })?;
        Ok(attributes)
    }
}
impl Drop for Attributes {
    fn drop(&mut self) {
        // SAFETY: successfully initialized list, storage remains live.
        unsafe { DeleteProcThreadAttributeList(self.0.as_mut_ptr().cast()) };
    }
}

// Unlike anonymous pipes, the parent endpoints support cancellable overlapped
// I/O. Names are private/unpredictable and both ends open before spawning. Only
// the synchronous child end is inherited. Parent never FlushFileBuffers-waits.
fn stdio_pair(input: bool, deadline: Instant) -> io::Result<(LocalPipe, OwnedHandle)> {
    let identity = current_identity()?;
    let descriptor = descriptor(&identity.sid, &identity.sid)?;
    let mut nonce = [0u8; 32];
    getrandom::fill(&mut nonce).map_err(io::Error::other)?;
    let nonce: String = nonce.iter().map(|b| format!("{b:02x}")).collect();
    let name = wide(&format!(r"\\.\pipe\VCWorkspace.WorkerV2.{nonce}"));
    let mut attributes = SECURITY_ATTRIBUTES {
        nLength: size_of::<SECURITY_ATTRIBUTES>() as u32,
        lpSecurityDescriptor: descriptor.0,
        bInheritHandle: 0,
    };
    // SAFETY: valid explicit descriptor and unique local byte-pipe name.
    let parent = own(unsafe {
        CreateNamedPipeW(
            name.as_ptr(),
            (if input {
                PIPE_ACCESS_OUTBOUND
            } else {
                PIPE_ACCESS_INBOUND
            }) | FILE_FLAG_OVERLAPPED
                | FILE_FLAG_FIRST_PIPE_INSTANCE,
            PIPE_TYPE_BYTE | PIPE_READMODE_BYTE | PIPE_WAIT | PIPE_REJECT_REMOTE_CLIENTS,
            1,
            4096,
            4096,
            0,
            &attributes,
        )
    })?;
    attributes.bInheritHandle = 1;
    // SAFETY: own process opens the other endpoint synchronously. SQOS cannot
    // grant an impersonation token to any pipe server even on a name collision.
    let child = own(unsafe {
        CreateFileW(
            name.as_ptr(),
            if input { GENERIC_READ } else { GENERIC_WRITE },
            0,
            &attributes,
            OPEN_EXISTING,
            SECURITY_SQOS_PRESENT | SECURITY_IDENTIFICATION,
            null_mut(),
        )
    })?;
    let parent = LocalPipe {
        handle: parent,
        deadline,
    };
    let event = event()?;
    // SAFETY: valid initialized overlapped event, drained before releasing it.
    let mut overlapped: OVERLAPPED = unsafe { zeroed() };
    overlapped.hEvent = event.as_raw_handle();
    let ok = unsafe { ConnectNamedPipe(parent.handle.as_raw_handle(), &mut overlapped) };
    let code = if ok == 0 {
        unsafe { GetLastError() }
    } else {
        0
    };
    if code != ERROR_PIPE_CONNECTED {
        finish_io(&parent, &mut overlapped, ok, code)?;
    }
    Ok((parent, child))
}

struct Worker {
    job: OwnedHandle,
    process: OwnedHandle,
}
impl Worker {
    fn terminate(&self) -> io::Result<()> {
        // SAFETY: handles belong to this worker only. Explicit termination also
        // catches descendants that outlive a successfully exited main worker.
        check(unsafe { TerminateJobObject(self.job.as_raw_handle(), 1) })?;
        let deadline = Instant::now() + Duration::from_secs(2);
        loop {
            let mut accounting: JOBOBJECT_BASIC_ACCOUNTING_INFORMATION = unsafe { zeroed() };
            check(unsafe {
                QueryInformationJobObject(
                    self.job.as_raw_handle(),
                    JobObjectBasicAccountingInformation,
                    (&mut accounting as *mut JOBOBJECT_BASIC_ACCOUNTING_INFORMATION).cast(),
                    size_of::<JOBOBJECT_BASIC_ACCOUNTING_INFORMATION>() as u32,
                    null_mut(),
                )
            })?;
            if accounting.ActiveProcesses == 0 {
                return Ok(());
            }
            if Instant::now() >= deadline {
                return Err(io::Error::new(
                    io::ErrorKind::TimedOut,
                    "worker tree cleanup timed out",
                ));
            }
            thread::sleep(Duration::from_millis(5));
        }
    }
}
impl Drop for Worker {
    fn drop(&mut self) {
        // SAFETY: cleanup includes the suspended/unassigned failure path. The
        // non-inherited job handle also has KILL_ON_JOB_CLOSE as a final guard.
        unsafe {
            TerminateJobObject(self.job.as_raw_handle(), 1);
            TerminateProcess(self.process.as_raw_handle(), 1);
        }
    }
}

fn read_output(mut pipe: LocalPipe, maximum: usize) -> io::Result<Vec<u8>> {
    let mut output = Vec::new();
    let mut buffer = [0u8; 16 * 1024];
    loop {
        let bytes = match pipe.read(&mut buffer) {
            Ok(0) => return Ok(output),
            Ok(bytes) => bytes,
            Err(error) if error.raw_os_error() == Some(ERROR_BROKEN_PIPE as i32) => {
                return Ok(output)
            }
            Err(error) => return Err(error),
        };
        if bytes > maximum.saturating_sub(output.len()) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "worker output exceeds limit",
            ));
        }
        output.extend_from_slice(&buffer[..bytes]);
    }
}

/// Run a trusted local executable with the caller's identity/desktop. The
/// worker starts suspended, is assigned to a non-breakaway job, then resumes.
/// Stdin carries bounded bytes (never command-line secrets); the worker must
/// use a length-delimited protocol, not wait for EOF. Stdin stays open until
/// exit to avoid discarding unread named-pipe bytes. Stdout is captured with a
/// hard cap; stderr is discarded to prevent leaking action text to QGA/logs.
/// All I/O shares the execution deadline; tree cleanup has a separate 2s cap.
pub fn run_worker(
    executable: &Path,
    arguments: &[OsString],
    input: &[u8],
    timeout: Duration,
    maximum_output: usize,
) -> io::Result<WorkerOutput> {
    run_worker_context(executable, arguments, input, timeout, maximum_output, None)
}

#[cfg(test)]
pub(super) fn run_profile_worker(
    token: HANDLE,
    executable: &Path,
    arguments: &[OsString],
    input: &[u8],
) -> io::Result<WorkerOutput> {
    let parent = current_identity()?;
    let user = token_identity(token)?;
    if !parent.is_system() || user.is_system() || user.session_id != parent.session_id {
        return Err(denied(
            "isolated same-session user profile fixture required",
        ));
    }
    run_worker_context(
        executable,
        arguments,
        input,
        Duration::from_secs(15),
        8192,
        Some(token),
    )
}

struct UserEnvironment(*mut c_void);
impl Drop for UserEnvironment {
    fn drop(&mut self) {
        if !self.0.is_null() {
            unsafe { DestroyEnvironmentBlock(self.0) };
        }
    }
}

fn run_worker_context(
    executable: &Path,
    arguments: &[OsString],
    input: &[u8],
    timeout: Duration,
    maximum_output: usize,
    user_token: Option<HANDLE>,
) -> io::Result<WorkerOutput> {
    if input.len() > 65_536
        || !(1..=16 * 1024 * 1024).contains(&maximum_output)
        || timeout < Duration::from_millis(1)
        || timeout > Duration::from_secs(15)
    {
        return Err(invalid("invalid worker input, output or deadline limits"));
    }
    let deadline = Instant::now() + timeout;
    let (application, mut command) = command_line(executable, arguments)?;
    // SAFETY: unnamed non-inheritable job; default security is not exposed by name.
    let job = own(unsafe { CreateJobObjectW(null(), null()) })?;
    let mut limits: JOBOBJECT_EXTENDED_LIMIT_INFORMATION = unsafe { zeroed() };
    limits.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
        | JOB_OBJECT_LIMIT_ACTIVE_PROCESS
        | JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION;
    limits.BasicLimitInformation.ActiveProcessLimit = 16;
    // No BREAKAWAY_OK or SILENT_BREAKAWAY_OK; child processes stay in this job.
    check(unsafe {
        SetInformationJobObject(
            job.as_raw_handle(),
            JobObjectExtendedLimitInformation,
            (&limits as *const JOBOBJECT_EXTENDED_LIMIT_INFORMATION).cast(),
            size_of::<JOBOBJECT_EXTENDED_LIMIT_INFORMATION>() as u32,
        )
    })?;
    let (mut input_pipe, child_input) = stdio_pair(true, deadline)?;
    let (output_pipe, child_output) = stdio_pair(false, deadline)?;
    let attributes = SECURITY_ATTRIBUTES {
        nLength: size_of::<SECURITY_ATTRIBUTES>() as u32,
        lpSecurityDescriptor: null_mut(),
        bInheritHandle: 1,
    };
    // SAFETY: fixed Windows null device; only this output handle is inherited.
    let child_error = own(unsafe {
        CreateFileW(
            wide("NUL").as_ptr(),
            GENERIC_WRITE,
            FILE_SHARE_READ | FILE_SHARE_WRITE,
            &attributes,
            OPEN_EXISTING,
            0,
            null_mut(),
        )
    })?;
    let handles = [
        child_input.as_raw_handle(),
        child_output.as_raw_handle(),
        child_error.as_raw_handle(),
    ];
    let mut attributes = Attributes::handles(&handles)?;
    let mut startup: STARTUPINFOEXW = unsafe { zeroed() };
    startup.StartupInfo.cb = size_of::<STARTUPINFOEXW>() as u32;
    startup.StartupInfo.dwFlags = STARTF_USESTDHANDLES;
    startup.StartupInfo.hStdInput = handles[0];
    startup.StartupInfo.hStdOutput = handles[1];
    startup.StartupInfo.hStdError = handles[2];
    startup.lpAttributeList = attributes.0.as_mut_ptr().cast();
    let mut process: PROCESS_INFORMATION = unsafe { zeroed() };
    let mut environment = UserEnvironment(null_mut());
    if let Some(token) = user_token {
        check(unsafe { CreateEnvironmentBlock(&mut environment.0, token, 0) })?;
    }
    // SAFETY: all buffers and HANDLE_LIST backing memory live through the call;
    // explicit application path, writable command, no shell or elevation.
    let created = if let Some(token) = user_token {
        let directory: Vec<u16> = executable
            .parent()
            .ok_or_else(|| denied("profile probe directory missing"))?
            .as_os_str()
            .encode_wide()
            .chain(Some(0))
            .collect();
        // Profile fixture only: actual primary user token and its own full
        // environment, not SYSTEM plus thread impersonation. The caller has
        // already loaded the exact Profile and keeps it until the child exits.
        unsafe {
            CreateProcessAsUserW(
                token,
                application.as_ptr(),
                command.as_mut_ptr(),
                null(),
                null(),
                1,
                EXTENDED_STARTUPINFO_PRESENT
                    | CREATE_SUSPENDED
                    | CREATE_NO_WINDOW
                    | CREATE_UNICODE_ENVIRONMENT,
                environment.0,
                directory.as_ptr(),
                &startup.StartupInfo,
                &mut process,
            )
        }
    } else {
        unsafe {
            CreateProcessW(
                application.as_ptr(),
                command.as_mut_ptr(),
                null(),
                null(),
                1,
                EXTENDED_STARTUPINFO_PRESENT | CREATE_SUSPENDED | CREATE_NO_WINDOW,
                null(),
                null(),
                &startup.StartupInfo,
                &mut process,
            )
        }
    };
    check(created)?;
    let worker = Worker {
        job,
        process: own(process.hProcess)?,
    };
    let primary_thread = own(process.hThread)?;
    drop((child_input, child_output, child_error, attributes));
    // SAFETY: no worker user code has run. Failure drops/terminates the still
    // suspended process; never resume an uncontained process as a fallback.
    check(unsafe {
        AssignProcessToJobObject(worker.job.as_raw_handle(), worker.process.as_raw_handle())
    })?;
    if Instant::now() >= deadline {
        return Err(io::Error::new(
            io::ErrorKind::TimedOut,
            "worker startup timed out",
        ));
    }
    if unsafe { ResumeThread(primary_thread.as_raw_handle()) } == u32::MAX {
        return Err(io::Error::last_os_error());
    }
    drop(primary_thread);

    thread::scope(|scope| {
        let (output_sender, output_receiver) = mpsc::sync_channel(1);
        scope.spawn(move || {
            let _ = output_sender.send(read_output(output_pipe, maximum_output));
        });
        let (input_sender, input_receiver) = mpsc::sync_channel(1);
        scope.spawn(move || {
            let result = input_pipe.write_all(input);
            // Retain the server endpoint in the receiver; closing it immediately
            // after WriteFile can discard bytes the child has not read yet.
            let _ = input_sender.send((result, input_pipe));
        });
        let mut output = None;
        let mut sent_input = None;
        let result = (|| {
            let exit_code = loop {
                if output.is_none() {
                    if let Ok(result) = output_receiver.try_recv() {
                        output = Some(result?);
                    }
                }
                if sent_input.is_none() {
                    if let Ok((result, pipe)) = input_receiver.try_recv() {
                        result?;
                        sent_input = Some(pipe);
                    }
                }
                let state = unsafe { WaitForSingleObject(worker.process.as_raw_handle(), 0) };
                if state == WAIT_OBJECT_0 {
                    let mut code = 0;
                    check(unsafe {
                        GetExitCodeProcess(worker.process.as_raw_handle(), &mut code)
                    })?;
                    break code;
                }
                if state == WAIT_FAILED {
                    return Err(io::Error::last_os_error());
                }
                if Instant::now() >= deadline {
                    return Err(io::Error::new(
                        io::ErrorKind::TimedOut,
                        "worker action timed out",
                    ));
                }
                thread::sleep(Duration::from_millis(5));
            };
            Ok(exit_code)
        })();
        let cleanup = worker.terminate();
        // Both overlapped reader/writer threads terminate within their own
        // deadlines even if an unexpected process retained an endpoint.
        let exit_code = result?;
        cleanup?;
        if sent_input.is_none() {
            let (result, pipe) = input_receiver
                .recv()
                .map_err(|_| io::Error::other("worker writer failed"))?;
            result?;
            sent_input = Some(pipe);
        }
        let stdout = match output {
            Some(output) => output,
            None => output_receiver
                .recv()
                .map_err(|_| io::Error::other("worker reader failed"))??,
        };
        drop(sent_input);
        Ok(WorkerOutput { exit_code, stdout })
    })
}

#[cfg(test)]
mod tests;
