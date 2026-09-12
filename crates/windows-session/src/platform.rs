use super::{denied, pipe_name, valid_account_sid, Identity};
use std::{
    ffi::c_void,
    io::{self, Read, Write},
    mem::{size_of, zeroed},
    os::windows::io::{AsRawHandle, FromRawHandle, OwnedHandle},
    ptr::{null, null_mut},
    thread,
    time::{Duration, Instant},
};
use windows_sys::Win32::{
    Foundation::*,
    Security::{Authorization::*, *},
    Storage::FileSystem::*,
    System::{Pipes::*, RemoteDesktop::*, Threading::*, WindowsProgramming::*, IO::*},
};

pub(super) mod desktop;
pub(super) mod input;
pub(super) mod launcher;
pub(super) mod registry;
#[cfg(test)]
mod tests;
pub(super) mod worker;

fn wide(value: &str) -> Vec<u16> {
    value.encode_utf16().chain(Some(0)).collect()
}

fn check(ok: i32) -> io::Result<()> {
    if ok == 0 {
        Err(io::Error::last_os_error())
    } else {
        Ok(())
    }
}

// Every handle accepted here is newly returned by Win32 and transferred once.
fn own(handle: HANDLE) -> io::Result<OwnedHandle> {
    if handle.is_null() || handle == INVALID_HANDLE_VALUE {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: callers transfer a valid, uniquely owned, CloseHandle-compatible handle.
    Ok(unsafe { OwnedHandle::from_raw_handle(handle) })
}

struct LocalAllocation(*mut c_void);
impl Drop for LocalAllocation {
    fn drop(&mut self) {
        // SAFETY: this pointer is returned by a documented LocalAlloc-backed API.
        unsafe {
            LocalFree(self.0);
        }
    }
}

fn token_info(token: HANDLE, class: TOKEN_INFORMATION_CLASS) -> io::Result<Vec<usize>> {
    let mut bytes = 0;
    // SAFETY: size discovery passes a null buffer and writable size pointer.
    unsafe {
        GetTokenInformation(token, class, null_mut(), 0, &mut bytes);
    }
    if bytes == 0 || bytes > 65_536 {
        return Err(denied("invalid token information size"));
    }
    // usize storage supplies the alignment needed for Win32 token structures.
    let mut buffer = vec![0usize; (bytes as usize).div_ceil(size_of::<usize>())];
    // SAFETY: buffer is aligned, at least bytes long, and live for the call.
    check(unsafe {
        GetTokenInformation(token, class, buffer.as_mut_ptr().cast(), bytes, &mut bytes)
    })?;
    Ok(buffer)
}

fn sid_string(sid: PSID) -> io::Result<String> {
    let mut text = null_mut();
    // SAFETY: SID comes from a successful GetTokenInformation call.
    check(unsafe { ConvertSidToStringSidW(sid, &mut text) })?;
    let allocation = LocalAllocation(text.cast());
    let mut length = 0;
    // SAFETY: API promises an allocated, NUL-terminated SID string. Windows SID
    // strings are bounded; stop at 184 characters rather than reading endlessly.
    unsafe {
        while length < 184 && *text.add(length) != 0 {
            length += 1;
        }
        if length == 184 {
            return Err(denied("invalid SID string"));
        }
        let result = String::from_utf16(std::slice::from_raw_parts(text, length))
            .map_err(io::Error::other)?;
        drop(allocation);
        Ok(result)
    }
}

fn token_identity(token: HANDLE) -> io::Result<Identity> {
    let user = token_info(token, TokenUser)?;
    let statistics = token_info(token, TokenStatistics)?;
    let session = token_info(token, TokenSessionId)?;
    if user.len() * size_of::<usize>() < size_of::<TOKEN_USER>()
        || statistics.len() * size_of::<usize>() < size_of::<TOKEN_STATISTICS>()
        || session.len() * size_of::<usize>() < size_of::<u32>()
    {
        return Err(denied("incomplete token identity"));
    }
    // SAFETY: buffers are aligned and checked to fit the returned token types.
    unsafe {
        let sid = (*(user.as_ptr().cast::<TOKEN_USER>())).User.Sid;
        let luid = (*(statistics.as_ptr().cast::<TOKEN_STATISTICS>())).AuthenticationId;
        Ok(Identity {
            sid: sid_string(sid)?,
            session_id: *session.as_ptr().cast::<u32>(),
            authentication_id: (u64::from(luid.HighPart as u32) << 32) | u64::from(luid.LowPart),
        })
    }
}

fn process_identity(process: HANDLE) -> io::Result<Identity> {
    let mut token = null_mut();
    // SAFETY: process is a valid borrowed handle; token receives a new handle.
    check(unsafe { OpenProcessToken(process, TOKEN_QUERY, &mut token) })?;
    let token = own(token)?;
    token_identity(token.as_raw_handle())
}

pub fn current_identity() -> io::Result<Identity> {
    // SAFETY: pseudo process handle is borrowed and never closed.
    process_identity(unsafe { GetCurrentProcess() })
}

/// Resolve a managed local account using the OS computer name, never an
/// environment variable or a domain-unqualified username supplied by a caller.
pub fn local_account_sid(username: &str) -> io::Result<String> {
    pipe_name(username, 0)?;
    let mut computer = [0u16; 256];
    let mut length = computer.len() as u32;
    // SAFETY: computer is a writable buffer and length is its size in WCHARs.
    check(unsafe { GetComputerNameW(computer.as_mut_ptr(), &mut length) })?;
    let name = String::from_utf16(&computer[..length as usize]).map_err(io::Error::other)?;
    let account = wide(&format!("{name}\\{username}"));
    let mut sid_bytes = 0;
    let mut domain_chars = 0;
    let mut kind = 0;
    // SAFETY: size discovery for a local, explicitly qualified account name.
    unsafe {
        LookupAccountNameW(
            null(),
            account.as_ptr(),
            null_mut(),
            &mut sid_bytes,
            null_mut(),
            &mut domain_chars,
            &mut kind,
        );
    }
    if !(8..=256).contains(&sid_bytes) || domain_chars > 256 {
        return Err(io::Error::new(
            io::ErrorKind::NotFound,
            "managed local account not found",
        ));
    }
    let mut sid = vec![0usize; (sid_bytes as usize).div_ceil(size_of::<usize>())];
    let mut domain = vec![0u16; domain_chars as usize];
    // SAFETY: aligned SID and domain buffers have the sizes reported by Windows.
    check(unsafe {
        LookupAccountNameW(
            null(),
            account.as_ptr(),
            sid.as_mut_ptr().cast(),
            &mut sid_bytes,
            domain.as_mut_ptr(),
            &mut domain_chars,
            &mut kind,
        )
    })?;
    let sid = sid_string(sid.as_mut_ptr().cast())?;
    if kind != SidTypeUser || !valid_account_sid(&sid) || sid == "S-1-5-18" {
        return Err(denied("managed local user SID required"));
    }
    Ok(sid)
}

struct WtsAllocation(*mut WTS_SESSION_INFOW);
impl Drop for WtsAllocation {
    fn drop(&mut self) {
        // SAFETY: WTSEnumerateSessions allocated this buffer.
        unsafe {
            WTSFreeMemory(self.0.cast());
        }
    }
}

/// Resolve exactly one logged-on session for the full expected account SID.
/// Must run as LocalSystem. Never chooses the foreground/console user, and an
/// ambiguous multi-login state is an error rather than an arbitrary selection.
pub fn logged_on_identity(expected_sid: &str) -> io::Result<Identity> {
    if !valid_account_sid(expected_sid)
        || expected_sid == "S-1-5-18"
        || !current_identity()?.is_system()
    {
        return Err(denied("LocalSystem and a managed account SID required"));
    }
    let mut sessions = null_mut();
    let mut count = 0;
    // SAFETY: local-server enumeration with writable out parameters.
    check(unsafe { WTSEnumerateSessionsW(null_mut(), 0, 1, &mut sessions, &mut count) })?;
    let _allocation = WtsAllocation(sessions);
    if count > 1024 || (count > 0 && sessions.is_null()) {
        return Err(denied("invalid WTS session list"));
    }
    let mut found = None;
    for index in 0..count as usize {
        // SAFETY: index is within the allocated array returned by WTS.
        let session = unsafe { &*sessions.add(index) };
        if session.SessionId == 0
            || ![WTSActive, WTSConnected, WTSDisconnected].contains(&session.State)
        {
            continue;
        }
        let mut token = null_mut();
        // SAFETY: querying a known local WTS session, caller is LocalSystem.
        if unsafe { WTSQueryUserToken(session.SessionId, &mut token) } == 0 {
            let error = io::Error::last_os_error();
            if error.raw_os_error() == Some(ERROR_NO_TOKEN as i32) {
                continue;
            }
            return Err(error);
        }
        let token = own(token)?;
        let identity = token_identity(token.as_raw_handle())?;
        if identity.sid == expected_sid {
            if identity.session_id != session.SessionId || found.is_some() {
                return Err(denied("ambiguous or changed interactive session"));
            }
            found = Some(identity);
        }
    }
    found.ok_or_else(|| {
        io::Error::new(
            io::ErrorKind::NotFound,
            "managed user has no logged-on session",
        )
    })
}

fn descriptor(owner_sid: &str, client_sid: &str) -> io::Result<LocalAllocation> {
    if !valid_account_sid(owner_sid) || !valid_account_sid(client_sid) {
        return Err(denied("invalid pipe account SID"));
    }
    // Only the owner may create a pipe instance. The peer receives explicit
    // data/attribute/READ_CONTROL/SYNCHRONIZE rights, not FILE_APPEND_DATA (which
    // aliases FILE_CREATE_PIPE_INSTANCE). No default Everyone/Anonymous ACE.
    let text = wide(&format!(
        "O:{owner_sid}D:P(A;;GA;;;{owner_sid})(A;;0x00120083;;;{client_sid})"
    ));
    let mut value = null_mut();
    // SAFETY: text is NUL terminated; API allocates the returned descriptor.
    check(unsafe {
        ConvertStringSecurityDescriptorToSecurityDescriptorW(
            text.as_ptr(),
            1,
            &mut value,
            null_mut(),
        )
    })?;
    Ok(LocalAllocation(value))
}

/// One helper instance. FIRST_PIPE_INSTANCE prevents silently attaching to a
/// pre-created pipe; rejecting remote clients keeps the channel strictly local.
pub struct PipeListener {
    pipe: LocalPipe,
    poisoned: bool,
}

impl PipeListener {
    pub fn is_usable(&self) -> bool {
        !self.poisoned
    }

    pub fn bind(username: &str, allowed_client_sid: &str, timeout: Duration) -> io::Result<Self> {
        let identity = current_identity()?;
        let name = wide(&pipe_name(username, identity.session_id)?);
        let descriptor = descriptor(&identity.sid, allowed_client_sid)?;
        let attributes = SECURITY_ATTRIBUTES {
            nLength: size_of::<SECURITY_ATTRIBUTES>() as u32,
            lpSecurityDescriptor: descriptor.0,
            bInheritHandle: 0,
        };
        // SAFETY: all arguments live for the call; the descriptor is copied by
        // Windows. Pipe handle is overlapped, non-inheritable and uniquely owned.
        let handle = own(unsafe {
            CreateNamedPipeW(
                name.as_ptr(),
                PIPE_ACCESS_DUPLEX | FILE_FLAG_OVERLAPPED | FILE_FLAG_FIRST_PIPE_INSTANCE,
                PIPE_TYPE_BYTE | PIPE_READMODE_BYTE | PIPE_WAIT | PIPE_REJECT_REMOTE_CLIENTS,
                1,
                65_536,
                65_536,
                0,
                &attributes,
            )
        })?;
        Ok(Self {
            pipe: LocalPipe::new(handle, timeout)?,
            poisoned: false,
        })
    }

    /// Authenticate the client's identification token before returning a usable
    /// stream. Only a fixed one-byte handshake is read before authentication.
    pub fn accept(mut self, expected: &Identity) -> io::Result<LocalPipe> {
        self.authenticate(expected)?;
        Ok(self.pipe)
    }

    /// Serve one bounded, authenticated exchange while retaining the same pipe
    /// handle between calls. A second listener cannot claim the name during the
    /// idle gap. The deadline bounds pipe I/O; any other callback work must have
    /// its own deadline (e.g. run_worker). The callback must receive a peer acknowledgement after its last
    /// response: disconnect discards unread bytes; FlushFileBuffers is unbounded.
    /// Failed authentication, timeout and callback unwind all disconnect the peer.
    pub fn serve_once<T>(
        &mut self,
        expected: &Identity,
        timeout: Duration,
        operation: impl FnOnce(&mut LocalPipe) -> io::Result<T>,
    ) -> io::Result<T> {
        if self.poisoned {
            return Err(denied("pipe listener cleanup previously failed"));
        }
        if timeout < Duration::from_millis(1) || timeout > Duration::from_secs(30) {
            return Err(denied("pipe deadline must be 1 ms to 30 seconds"));
        }
        self.pipe.deadline = Instant::now() + timeout;
        let mut exchange = PipeExchange {
            listener: self,
            disconnected: false,
        };
        let result = exchange
            .listener
            .authenticate(expected)
            .and_then(|()| operation(&mut exchange.listener.pipe));
        exchange.disconnect()?;
        result
    }

    fn authenticate(&mut self, expected: &Identity) -> io::Result<()> {
        if self.poisoned {
            return Err(denied("pipe listener cleanup previously failed"));
        }
        let event = event()?;
        // SAFETY: zero is the documented initialization for OVERLAPPED.
        let mut overlapped: OVERLAPPED = unsafe { zeroed() };
        overlapped.hEvent = event.as_raw_handle();
        // SAFETY: handle is an overlapped server pipe; OVERLAPPED remains live
        // until finish_io confirms completion (including cancellation).
        let ok = unsafe { ConnectNamedPipe(self.pipe.handle.as_raw_handle(), &mut overlapped) };
        let code = if ok == 0 {
            unsafe { GetLastError() }
        } else {
            0
        };
        if code != ERROR_PIPE_CONNECTED {
            finish_io(&self.pipe, &mut overlapped, ok, code)?;
        }
        let mut handshake = [0u8];
        self.pipe.read_exact(&mut handshake)?;
        if handshake != [2] {
            return Err(denied("invalid pipe handshake"));
        }
        let actual = client_identity(self.pipe.handle.as_raw_handle())?;
        if &actual != expected {
            return Err(denied("pipe client identity changed"));
        }
        self.pipe.write_all(&[2])?;
        Ok(())
    }
}

struct PipeExchange<'a> {
    listener: &'a mut PipeListener,
    disconnected: bool,
}
impl PipeExchange<'_> {
    fn disconnect(&mut self) -> io::Result<()> {
        if self.disconnected {
            return Ok(());
        }
        self.disconnected = true;
        // All I/O above is synchronously drained even after cancellation. Keep
        // the server handle alive, but detach the last client before reusing it.
        if unsafe { DisconnectNamedPipe(self.listener.pipe.handle.as_raw_handle()) } == 0 {
            let code = unsafe { GetLastError() };
            if code != ERROR_PIPE_NOT_CONNECTED {
                self.listener.poisoned = true;
                return Err(io::Error::from_raw_os_error(code as i32));
            }
        }
        Ok(())
    }
}
impl Drop for PipeExchange<'_> {
    fn drop(&mut self) {
        let _ = self.disconnect();
    }
}

fn client_identity(pipe: HANDLE) -> io::Result<Identity> {
    // SECURITY_IDENTIFICATION supplied by our client permits identity queries
    // without granting the helper the ability to act with the SYSTEM token.
    // SAFETY: called only after reading a handshake from this connected pipe.
    check(unsafe { ImpersonateNamedPipeClient(pipe) })?;
    struct Revert;
    impl Drop for Revert {
        fn drop(&mut self) {
            // SAFETY: always undo thread impersonation before returning. Per
            // Microsoft's contract, failure must terminate the process.
            if unsafe { RevertToSelf() } == 0 {
                std::process::abort();
            }
        }
    }
    let revert = Revert;
    let mut token = null_mut();
    // SAFETY: query only the current thread's identification token, OpenAsSelf
    // authorizes opening the handle as the helper, not as the impersonated user.
    let result = check(unsafe { OpenThreadToken(GetCurrentThread(), TOKEN_QUERY, 1, &mut token) });
    drop(revert);
    result?;
    let token = own(token)?;
    let level = token_info(token.as_raw_handle(), TokenImpersonationLevel)?;
    // SAFETY: the aligned GetTokenInformation buffer contains a Win32 enum.
    if level.len() * size_of::<usize>() < size_of::<SECURITY_IMPERSONATION_LEVEL>()
        || unsafe { *level.as_ptr().cast::<SECURITY_IMPERSONATION_LEVEL>() }
            != SecurityIdentification
    {
        return Err(denied("pipe peer must use identification-only security"));
    }
    token_identity(token.as_raw_handle())
}

pub struct LocalPipe {
    handle: OwnedHandle,
    deadline: Instant,
}

impl LocalPipe {
    fn new(handle: OwnedHandle, timeout: Duration) -> io::Result<Self> {
        if timeout < Duration::from_millis(1) || timeout > Duration::from_secs(30) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "pipe deadline must be 1 ms to 30 seconds",
            ));
        }
        Ok(Self {
            handle,
            deadline: Instant::now() + timeout,
        })
    }

    /// Start a bounded I/O phase after authentication, independently of the
    /// idle accept wait. &mut self ensures no I/O is still in flight here.
    pub fn reset_timeout(&mut self, timeout: Duration) -> io::Result<()> {
        if timeout < Duration::from_millis(1) || timeout > Duration::from_secs(30) {
            return Err(denied("pipe deadline must be 1 ms to 30 seconds"));
        }
        self.deadline = Instant::now() + timeout;
        Ok(())
    }

    pub fn connect(username: &str, expected: &Identity, timeout: Duration) -> io::Result<Self> {
        Self::connect_process(username, expected, timeout, None)
    }

    fn connect_process(
        username: &str,
        expected: &Identity,
        timeout: Duration,
        expected_process: Option<HANDLE>,
    ) -> io::Result<Self> {
        let name = wide(&pipe_name(username, expected.session_id)?);
        if !valid_account_sid(&expected.sid)
            || timeout < Duration::from_millis(1)
            || timeout > Duration::from_secs(30)
        {
            return Err(denied("invalid pipe identity or deadline"));
        }
        let deadline = Instant::now() + timeout;
        let handle = loop {
            // SAFETY: fixed local pipe namespace, no handle inheritance. SQOS
            // IDENTIFICATION forbids a target user from impersonating SYSTEM.
            let raw = unsafe {
                CreateFileW(
                    name.as_ptr(),
                    FILE_READ_DATA
                        | FILE_WRITE_DATA
                        | FILE_READ_ATTRIBUTES
                        | READ_CONTROL
                        | SYNCHRONIZE,
                    0,
                    null(),
                    OPEN_EXISTING,
                    FILE_FLAG_OVERLAPPED | SECURITY_SQOS_PRESENT | SECURITY_IDENTIFICATION,
                    null_mut(),
                )
            };
            if raw != INVALID_HANDLE_VALUE {
                break own(raw)?;
            }
            let code = unsafe { GetLastError() };
            if ![ERROR_PIPE_BUSY, ERROR_FILE_NOT_FOUND].contains(&code) {
                return Err(io::Error::from_raw_os_error(code as i32));
            }
            if Instant::now() >= deadline {
                return Err(io::Error::new(
                    io::ErrorKind::TimedOut,
                    "pipe connection timed out",
                ));
            }
            thread::sleep(Duration::from_millis(10));
        };
        let mut process_id = 0;
        // SAFETY: connected local pipe; PID receives the peer process identity.
        check(unsafe { GetNamedPipeServerProcessId(handle.as_raw_handle(), &mut process_id) })?;
        if expected_process.is_some_and(|process| unsafe { GetProcessId(process) } != process_id) {
            return Err(denied("pipe server is not the launched Helper process"));
        }
        let process =
            own(unsafe { OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, process_id) })?;
        if process_identity(process.as_raw_handle())? != *expected {
            return Err(denied("pipe server identity changed"));
        }
        let mut pipe = Self { handle, deadline };
        pipe.write_all(&[2])?;
        let mut ack = [0u8];
        pipe.read_exact(&mut ack)?;
        if ack != [2] {
            return Err(denied("invalid authenticated pipe handshake"));
        }
        Ok(pipe)
    }
}

fn event() -> io::Result<OwnedHandle> {
    // SAFETY: unnamed, non-inheritable, manually-reset event, initially clear.
    own(unsafe { CreateEventW(null(), 1, 0, null()) })
}

fn finish_io(
    pipe: &LocalPipe,
    overlapped: &mut OVERLAPPED,
    ok: i32,
    code: u32,
) -> io::Result<usize> {
    if ok == 0 && code != ERROR_IO_PENDING {
        return Err(io::Error::from_raw_os_error(code as i32));
    }
    let mut transferred = 0;
    if ok == 0 {
        let milliseconds = pipe
            .deadline
            .saturating_duration_since(Instant::now())
            .as_millis()
            .min(30_000) as u32;
        // SAFETY: the event and OVERLAPPED remain live until I/O is complete.
        let status = unsafe { WaitForSingleObject(overlapped.hEvent, milliseconds) };
        if status != WAIT_OBJECT_0 {
            // SAFETY: cancel only this operation, then drain its completion
            // before releasing the event, OVERLAPPED or caller's byte buffer.
            unsafe {
                CancelIoEx(pipe.handle.as_raw_handle(), overlapped);
                GetOverlappedResult(pipe.handle.as_raw_handle(), overlapped, &mut transferred, 1);
            }
            return Err(io::Error::new(
                io::ErrorKind::TimedOut,
                "pipe operation timed out",
            ));
        }
    }
    // SAFETY: operation has completed and its buffers remain alive.
    check(unsafe {
        GetOverlappedResult(pipe.handle.as_raw_handle(), overlapped, &mut transferred, 0)
    })?;
    Ok(transferred as usize)
}

impl Read for LocalPipe {
    fn read(&mut self, buffer: &mut [u8]) -> io::Result<usize> {
        if buffer.is_empty() {
            return Ok(0);
        }
        if Instant::now() >= self.deadline {
            return Err(io::Error::new(
                io::ErrorKind::TimedOut,
                "pipe deadline expired",
            ));
        }
        let event = event()?;
        // SAFETY: Win32 requires zero initialization except hEvent.
        let mut overlapped: OVERLAPPED = unsafe { zeroed() };
        overlapped.hEvent = event.as_raw_handle();
        // SAFETY: buffer is writable for the requested bounded length; it and
        // OVERLAPPED live until finish_io completes or drains cancellation.
        let ok = unsafe {
            ReadFile(
                self.handle.as_raw_handle(),
                buffer.as_mut_ptr(),
                buffer.len().min(65_536) as u32,
                null_mut(),
                &mut overlapped,
            )
        };
        let code = if ok == 0 {
            unsafe { GetLastError() }
        } else {
            0
        };
        if code == ERROR_BROKEN_PIPE {
            return Ok(0);
        }
        finish_io(self, &mut overlapped, ok, code)
    }
}

impl Write for LocalPipe {
    fn write(&mut self, buffer: &[u8]) -> io::Result<usize> {
        if buffer.is_empty() {
            return Ok(0);
        }
        if Instant::now() >= self.deadline {
            return Err(io::Error::new(
                io::ErrorKind::TimedOut,
                "pipe deadline expired",
            ));
        }
        let event = event()?;
        // SAFETY: Win32 requires zero initialization except hEvent.
        let mut overlapped: OVERLAPPED = unsafe { zeroed() };
        overlapped.hEvent = event.as_raw_handle();
        // SAFETY: buffer and OVERLAPPED stay live until completion, including
        // the CancelIoEx/GetOverlappedResult drain on a timeout.
        let ok = unsafe {
            WriteFile(
                self.handle.as_raw_handle(),
                buffer.as_ptr(),
                buffer.len().min(65_536) as u32,
                null_mut(),
                &mut overlapped,
            )
        };
        let code = if ok == 0 {
            unsafe { GetLastError() }
        } else {
            0
        };
        finish_io(self, &mut overlapped, ok, code)
    }
    fn flush(&mut self) -> io::Result<()> {
        // No user-space buffering. FlushFileBuffers could block forever until a
        // peer reads; it is intentionally not used on this bounded IPC channel.
        Ok(())
    }
}
