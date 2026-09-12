use super::*;
use std::os::windows::process::CommandExt;
use std::process::{Child, Command, Stdio};

fn fixture_arguments(fixture: &str) -> Vec<OsString> {
    [
        "--exact".into(),
        format!("platform::worker::tests::{fixture}").into(),
        "--ignored".into(),
        "--nocapture".into(),
        "--test-threads=1".into(),
    ]
    .into()
}

fn fixture(
    fixture: &str,
    input: &[u8],
    timeout: Duration,
    maximum: usize,
) -> io::Result<WorkerOutput> {
    run_worker(
        &std::env::current_exe()?,
        &fixture_arguments(fixture),
        input,
        timeout,
        maximum,
    )
}

#[test]
fn worker_limits_and_executable_paths_reject_unsafe_inputs() {
    let exe = std::env::current_exe().unwrap();
    for path in [
        "worker.exe",
        r"C:worker.exe",
        r"\\server\share\worker.exe",
        r"C:\worker.cmd",
        r"C:\worker.exe:other.exe",
        "C:\\worker\0.exe",
    ] {
        assert!(command_line(Path::new(path), &[]).is_err(), "{path}");
    }
    assert!(command_line(&exe, &["bad\0argument".into()]).is_err());
    assert!(command_line(&exe, &["x".repeat(9000).into()]).is_err());
    assert!(command_line(&exe, &vec!["arg".into(); 33]).is_err());
    for duration in [Duration::ZERO, Duration::from_secs(16)] {
        assert!(run_worker(&exe, &[], &[], duration, 10).is_err());
    }
    for maximum in [0, 16 * 1024 * 1024 + 1] {
        assert!(run_worker(&exe, &[], &[], Duration::from_secs(1), maximum).is_err());
    }
    assert!(run_worker(&exe, &[], &vec![0; 65_537], Duration::from_secs(1), 10).is_err());
}

#[test]
fn worker_stdin_and_quoted_unicode_arguments_round_trip() {
    let payload: Vec<u8> = (0..60_000).map(|index| (index % 251) as u8).collect();
    let mut input = (payload.len() as u32).to_be_bytes().to_vec();
    input.extend_from_slice(&payload);
    let argument = "space 中文 \"quoted\" \\ trailing\\";
    let mut arguments = fixture_arguments("fixture_echo");
    arguments.extend(["--skip".into(), argument.into()]);
    let result = run_worker(
        &std::env::current_exe().unwrap(),
        &arguments,
        &input,
        Duration::from_secs(5),
        100_000,
    )
    .unwrap();
    assert_eq!(result.exit_code, 0);
    assert!(result
        .stdout
        .windows(payload.len())
        .any(|part| part == payload));
    assert!(String::from_utf8_lossy(&result.stdout).contains(&format!("ARG={argument:?}")));
}

#[test]
fn worker_flood_is_rejected_and_next_worker_still_runs() {
    let started = Instant::now();
    let error = fixture("fixture_flood", &[], Duration::from_secs(5), 1024).unwrap_err();
    assert_eq!(error.kind(), io::ErrorKind::InvalidData);
    assert!(started.elapsed() < Duration::from_secs(3));
    let result = fixture("fixture_failure", &[], Duration::from_secs(3), 8192).unwrap();
    assert_eq!(result.exit_code, 23);
    assert!(!String::from_utf8_lossy(&result.stdout).contains("sensitive-worker-stderr"));
}

#[test]
fn worker_blocked_input_and_silent_process_have_deadlines() {
    for input in [Vec::new(), vec![7; 65_536]] {
        let started = Instant::now();
        let error = fixture("fixture_sleep", &input, Duration::from_millis(250), 8192).unwrap_err();
        assert_eq!(error.kind(), io::ErrorKind::TimedOut);
        assert!(started.elapsed() < Duration::from_secs(3));
    }
}

struct ChildGuard(Child);
impl Drop for ChildGuard {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

struct ObservedChild(OwnedHandle);
impl Drop for ObservedChild {
    fn drop(&mut self) {
        // Only exact test-created children reported over our authenticated pipe.
        // Clean up even when an assertion detects a broken job implementation.
        unsafe {
            TerminateProcess(self.0.as_raw_handle(), 1);
        }
    }
}

fn observe_child(name: &str) -> thread::JoinHandle<ObservedChild> {
    let identity = current_identity().unwrap();
    let listener = PipeListener::bind(name, &identity.sid, Duration::from_secs(10)).unwrap();
    thread::spawn(move || {
        let mut pipe = listener.accept(&identity).unwrap();
        let mut pid = [0u8; 4];
        pipe.read_exact(&mut pid).unwrap();
        // Keep the actual process handle, not a PID that could be reused.
        let child = ObservedChild(
            own(unsafe {
                OpenProcess(
                    PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_TERMINATE | SYNCHRONIZE,
                    0,
                    u32::from_be_bytes(pid),
                )
            })
            .unwrap(),
        );
        assert_eq!(
            unsafe { WaitForSingleObject(child.0.as_raw_handle(), 0) },
            WAIT_TIMEOUT
        );
        pipe.write_all(&[1]).unwrap();
        pipe.read_exact(&mut [0]).unwrap();
        child
    })
}

#[test]
fn worker_job_cleans_descendants_on_timeout_and_success_only() {
    let exe = std::env::current_exe().unwrap();
    let sibling = ChildGuard(
        Command::new(&exe)
            .args(fixture_arguments("fixture_sleep"))
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .unwrap(),
    );
    for mode in [b'H', b'E'] {
        let name = super::super::tests::name();
        let observer = observe_child(&name);
        let mut input = vec![mode];
        input.extend_from_slice(name.as_bytes());
        let result = fixture("fixture_tree", &input, Duration::from_secs(3), 8192);
        if mode == b'H' {
            assert_eq!(result.unwrap_err().kind(), io::ErrorKind::TimedOut);
        } else {
            assert_eq!(result.unwrap().exit_code, 0);
        }
        let child = observer.join().unwrap();
        assert_eq!(
            unsafe { WaitForSingleObject(child.0.as_raw_handle(), 2000) },
            WAIT_OBJECT_0
        );
        // A non-job sibling is not killed by cleanup of a worker with the same SID.
        assert_eq!(
            unsafe { WaitForSingleObject(sibling.0.as_raw_handle(), 0) },
            WAIT_TIMEOUT
        );
    }
}

#[test]
fn worker_job_kills_descendants_when_the_owner_process_exits() {
    let name = super::super::tests::name();
    let observer = observe_child(&name);
    let mut owner = ChildGuard(
        Command::new(std::env::current_exe().unwrap())
            .args(fixture_arguments("fixture_owner"))
            .stdin(Stdio::piped())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .unwrap(),
    );
    let mut input = vec![b'H'];
    input.extend_from_slice(name.as_bytes());
    owner.0.stdin.take().unwrap().write_all(&input).unwrap();
    let child = observer.join().unwrap();
    assert_eq!(
        unsafe { WaitForSingleObject(child.0.as_raw_handle(), 0) },
        WAIT_TIMEOUT
    );
    owner.0.kill().unwrap();
    owner.0.wait().unwrap();
    assert_eq!(
        unsafe { WaitForSingleObject(child.0.as_raw_handle(), 2000) },
        WAIT_OBJECT_0
    );
}

#[test]
fn worker_cannot_create_a_breakaway_process() {
    let result = fixture("fixture_breakaway", &[], Duration::from_secs(3), 8192).unwrap();
    assert_eq!(
        result.exit_code,
        0,
        "{}",
        String::from_utf8_lossy(&result.stdout)
    );
}

#[test]
fn worker_does_not_inherit_unlisted_inheritable_handles() {
    let attributes = SECURITY_ATTRIBUTES {
        nLength: size_of::<SECURITY_ATTRIBUTES>() as u32,
        lpSecurityDescriptor: null_mut(),
        bInheritHandle: 1,
    };
    let event = own(unsafe { CreateEventW(&attributes, 1, 0, null()) }).unwrap();
    let input = (event.as_raw_handle() as usize as u64).to_be_bytes();
    let result = fixture(
        "fixture_unlisted_handle",
        &input,
        Duration::from_secs(3),
        8192,
    )
    .unwrap();
    assert_eq!(result.exit_code, 0);
    assert_eq!(
        unsafe { WaitForSingleObject(event.as_raw_handle(), 0) },
        WAIT_TIMEOUT
    );
}

// Only explicitly selected child fixtures run in a subprocess. They create no
// accounts, files, desktop actions or network connections. Job cleanup owns all
// deliberately stalled descendants; the outer unrelated control has a guard.
#[test]
#[ignore = "worker subprocess fixture"]
fn fixture_echo() {
    let mut size = [0u8; 4];
    io::stdin().read_exact(&mut size).unwrap();
    let size = u32::from_be_bytes(size) as usize;
    assert!(size <= 65_532);
    let mut data = vec![0; size];
    io::stdin().read_exact(&mut data).unwrap();
    io::stdout().write_all(&data).unwrap();
    for argument in std::env::args() {
        println!("ARG={argument:?}");
    }
}

#[test]
#[ignore = "worker subprocess fixture"]
fn fixture_flood() {
    io::stdout()
        .write_all(&vec![b'X'; 2 * 1024 * 1024])
        .unwrap();
}

#[test]
#[ignore = "worker subprocess fixture"]
fn fixture_failure() {
    eprintln!("sensitive-worker-stderr");
    std::process::exit(23);
}

#[test]
#[ignore = "worker subprocess fixture"]
fn fixture_sleep() {
    thread::sleep(Duration::from_secs(120));
}

#[test]
#[ignore = "worker subprocess fixture"]
fn fixture_tree() {
    let mut input = [0u8; 16];
    io::stdin().read_exact(&mut input).unwrap();
    let name = std::str::from_utf8(&input[1..]).unwrap();
    let identity = current_identity().unwrap();
    let child = Command::new(std::env::current_exe().unwrap())
        .args(fixture_arguments("fixture_sleep"))
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    let mut pipe = LocalPipe::connect(name, &identity, Duration::from_secs(2)).unwrap();
    pipe.write_all(&child.id().to_be_bytes()).unwrap();
    pipe.read_exact(&mut [0]).unwrap();
    pipe.write_all(&[1]).unwrap();
    if input[0] == b'H' {
        thread::sleep(Duration::from_secs(120));
    }
    // Intentionally leave the child running: only the job may clean it up.
    std::process::exit(0);
}

#[test]
#[ignore = "worker subprocess fixture"]
fn fixture_unlisted_handle() {
    let mut bytes = [0u8; 8];
    io::stdin().read_exact(&mut bytes).unwrap();
    let handle = u64::from_be_bytes(bytes) as usize as HANDLE;
    // Deliberately attempt to signal the parent's inheritable handle. The
    // allowlist must prevent this; a reused numeric handle cannot signal it.
    unsafe {
        SetEvent(handle);
    }
}

#[test]
#[ignore = "worker subprocess fixture"]
fn fixture_owner() {
    let mut input = [0; 16];
    io::stdin().read_exact(&mut input).unwrap();
    let _ = fixture("fixture_tree", &input, Duration::from_secs(15), 8192);
}

#[test]
#[ignore = "worker subprocess fixture"]
fn fixture_breakaway() {
    match Command::new(std::env::current_exe().unwrap())
        .args(fixture_arguments("fixture_sleep"))
        .creation_flags(CREATE_BREAKAWAY_FROM_JOB)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
    {
        Ok(child) => {
            drop(ChildGuard(child));
            panic!("worker escaped its job");
        }
        Err(error) => assert_eq!(error.raw_os_error(), Some(ERROR_ACCESS_DENIED as i32)),
    }
}
