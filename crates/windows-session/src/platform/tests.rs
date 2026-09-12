use super::*;
use std::sync::atomic::{AtomicU64, Ordering};

pub(super) fn name() -> String {
    static COUNTER: AtomicU64 = AtomicU64::new(1);
    format!(
        "vca{:08x}{:04x}",
        std::process::id(),
        COUNTER.fetch_add(1, Ordering::Relaxed)
    )
}

#[test]
fn authenticated_pipe_preserves_identity_and_round_trips_bytes() {
    let identity = current_identity().unwrap();
    let name = name();
    let listener = PipeListener::bind(&name, &identity.sid, Duration::from_secs(3)).unwrap();
    let expected = identity.clone();
    let helper = thread::spawn(move || -> io::Result<()> {
        let mut pipe = listener.accept(&expected)?;
        // No client impersonation may remain on the helper dispatch thread.
        let mut token = null_mut();
        let opened = unsafe { OpenThreadToken(GetCurrentThread(), TOKEN_QUERY, 1, &mut token) };
        if opened != 0 {
            drop(own(token)?);
            return Err(denied("helper thread retained client impersonation"));
        }
        if unsafe { GetLastError() } != ERROR_NO_TOKEN {
            return Err(io::Error::last_os_error());
        }
        let mut bytes = [0; 5];
        pipe.read_exact(&mut bytes)?;
        assert_eq!(&bytes, b"hello");
        pipe.write_all(b"world")?;
        let mut ack = [0];
        pipe.read_exact(&mut ack)?;
        Ok(())
    });
    let mut pipe = LocalPipe::connect_process(
        &name,
        &identity,
        Duration::from_secs(3),
        Some(unsafe { GetCurrentProcess() }),
    )
    .unwrap();
    pipe.write_all(b"hello").unwrap();
    let mut bytes = [0; 5];
    pipe.read_exact(&mut bytes).unwrap();
    assert_eq!(&bytes, b"world");
    pipe.write_all(&[1]).unwrap();
    helper.join().unwrap().unwrap();
}

#[test]
fn pipe_process_pin_rejects_different_process_with_same_logon_identity() {
    use std::os::windows::process::CommandExt;
    let identity = current_identity().unwrap();
    let name = name();
    let listener = PipeListener::bind(&name, &identity.sid, Duration::from_secs(2)).unwrap();
    let mut child = std::process::Command::new(std::env::current_exe().unwrap())
        .args(["--ignored", "--exact", "platform::tests::pipe_pin_child"])
        .env("VC_WORKSPACE_PIPE_PIN_FIXTURE", "isolated")
        .creation_flags(CREATE_NO_WINDOW)
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .spawn()
        .unwrap();
    let expected = identity.clone();
    let helper = thread::spawn(move || listener.accept(&expected).is_err());
    let result = LocalPipe::connect_process(
        &name,
        &identity,
        Duration::from_secs(2),
        Some(child.as_raw_handle()),
    );
    let _ = child.kill();
    child.wait().unwrap();
    assert!(helper.join().unwrap());
    assert_eq!(
        result.err().unwrap().kind(),
        io::ErrorKind::PermissionDenied
    );
}

#[test]
#[ignore = "bounded child process used only by the pipe process-pin test"]
fn pipe_pin_child() {
    assert_eq!(
        std::env::var("VC_WORKSPACE_PIPE_PIN_FIXTURE").as_deref(),
        Ok("isolated")
    );
    thread::sleep(Duration::from_secs(5));
}

#[test]
fn pipe_name_squatting_cannot_create_a_second_helper() {
    let identity = current_identity().unwrap();
    let name = name();
    let _first = PipeListener::bind(&name, &identity.sid, Duration::from_secs(2)).unwrap();
    assert!(PipeListener::bind(&name, &identity.sid, Duration::from_secs(2)).is_err());
}

#[test]
fn persistent_listener_keeps_name_and_recovers_after_idle_timeout() {
    let identity = current_identity().unwrap();
    let name = name();
    let mut listener = PipeListener::bind(&name, &identity.sid, Duration::from_secs(2)).unwrap();
    let error = listener
        .serve_once(
            &identity,
            Duration::from_millis(100),
            |_| -> io::Result<()> {
                panic!("absent client must not reach the callback");
            },
        )
        .unwrap_err();
    assert_eq!(error.kind(), io::ErrorKind::TimedOut);
    assert!(PipeListener::bind(&name, &identity.sid, Duration::from_secs(1)).is_err());
    let expected = identity.clone();
    let server_name = name.clone();
    let helper = thread::spawn(move || {
        for index in 0u8..12 {
            listener
                .serve_once(&expected, Duration::from_secs(2), |pipe| {
                    let mut request = [0];
                    pipe.read_exact(&mut request)?;
                    assert_eq!(request, [index]);
                    pipe.write_all(&vec![index; 256 * 1024])?;
                    let mut ack = [0];
                    pipe.read_exact(&mut ack)?;
                    assert_eq!(ack, [2]);
                    Ok(())
                })
                .unwrap();
            assert!(
                PipeListener::bind(&server_name, &expected.sid, Duration::from_secs(1)).is_err()
            );
        }
    });
    let mut previous = None;
    for index in 0u8..12 {
        let mut pipe = LocalPipe::connect(&name, &identity, Duration::from_secs(2)).unwrap();
        pipe.write_all(&[index]).unwrap();
        let mut response = vec![0; 256 * 1024];
        pipe.read_exact(&mut response).unwrap();
        assert!(response.iter().all(|byte| *byte == index));
        pipe.write_all(&[2]).unwrap();
        // Retain the client handle until the next connection. The server must
        // explicitly disconnect it, not wait for the client to close its handle.
        previous = Some(pipe);
    }
    helper.join().unwrap();
    drop(previous);
}

#[test]
fn persistent_listener_recovers_after_rejected_and_stalled_clients() {
    let identity = current_identity().unwrap();
    let name = name();
    let mut listener = PipeListener::bind(&name, &identity.sid, Duration::from_secs(2)).unwrap();
    let expected = identity.clone();
    let (ready, phases) = std::sync::mpsc::channel();
    let helper = thread::spawn(move || {
        let mut wrong = expected.clone();
        wrong.authentication_id ^= 1;
        let error = listener
            .serve_once(&wrong, Duration::from_secs(2), |_| -> io::Result<()> {
                panic!("unauthenticated client must not reach the callback");
            })
            .unwrap_err();
        assert_eq!(error.kind(), io::ErrorKind::PermissionDenied);
        ready.send(()).unwrap();
        let error = listener
            .serve_once(&expected, Duration::from_millis(200), |pipe| {
                pipe.read_exact(&mut [0])
            })
            .unwrap_err();
        assert_eq!(error.kind(), io::ErrorKind::TimedOut);
        ready.send(()).unwrap();
        listener
            .serve_once(&expected, Duration::from_secs(2), |pipe| {
                let mut request = [0];
                pipe.read_exact(&mut request)?;
                assert_eq!(request, [7]);
                pipe.write_all(&[8])?;
                pipe.read_exact(&mut request)?;
                assert_eq!(request, [2]);
                Ok(())
            })
            .unwrap();
    });
    assert!(LocalPipe::connect(&name, &identity, Duration::from_secs(2)).is_err());
    phases.recv_timeout(Duration::from_secs(3)).unwrap();
    let stalled = LocalPipe::connect(&name, &identity, Duration::from_secs(2)).unwrap();
    phases.recv_timeout(Duration::from_secs(3)).unwrap();
    let mut pipe = LocalPipe::connect(&name, &identity, Duration::from_secs(2)).unwrap();
    pipe.write_all(&[7]).unwrap();
    let mut response = [0];
    pipe.read_exact(&mut response).unwrap();
    assert_eq!(response, [8]);
    pipe.write_all(&[2]).unwrap();
    helper.join().unwrap();
    drop(stalled);
}

#[test]
fn authenticated_pipe_starts_a_separate_bounded_exchange_phase() {
    let identity = current_identity().unwrap();
    let name = name();
    let mut listener = PipeListener::bind(&name, &identity.sid, Duration::from_secs(2)).unwrap();
    let expected = identity.clone();
    let helper = thread::spawn(move || {
        listener
            .serve_once(&expected, Duration::from_millis(500), |pipe| {
                assert!(pipe.reset_timeout(Duration::ZERO).is_err());
                assert!(pipe.reset_timeout(Duration::from_secs(31)).is_err());
                // Deliberately let the original I/O phase expire; no operation is
                // in flight while a subsequent phase receives its own fresh cap.
                thread::sleep(Duration::from_millis(600));
                pipe.reset_timeout(Duration::from_secs(1))?;
                pipe.write_all(b"fresh")?;
                let mut ack = [0];
                pipe.read_exact(&mut ack)?;
                assert_eq!(ack, [2]);
                Ok(())
            })
            .unwrap();
        assert!(listener.is_usable());
    });
    let mut pipe = LocalPipe::connect(&name, &identity, Duration::from_secs(3)).unwrap();
    let mut response = [0; 5];
    pipe.read_exact(&mut response).unwrap();
    assert_eq!(&response, b"fresh");
    pipe.write_all(&[2]).unwrap();
    helper.join().unwrap();
}

#[test]
fn wrong_server_logon_is_rejected_before_sending_action_data() {
    let identity = current_identity().unwrap();
    let name = name();
    let listener = PipeListener::bind(&name, &identity.sid, Duration::from_secs(1)).unwrap();
    let expected = identity.clone();
    let helper = thread::spawn(move || listener.accept(&expected).is_err());
    let mut impostor = identity;
    impostor.authentication_id ^= 1;
    assert_eq!(
        LocalPipe::connect(&name, &impostor, Duration::from_secs(1))
            .err()
            .unwrap()
            .kind(),
        io::ErrorKind::PermissionDenied
    );
    assert!(helper.join().unwrap());
}

#[test]
fn wrong_client_logon_is_rejected_and_never_exposes_a_stream() {
    let identity = current_identity().unwrap();
    let name = name();
    let listener = PipeListener::bind(&name, &identity.sid, Duration::from_secs(1)).unwrap();
    let mut wrong_client = identity.clone();
    wrong_client.authentication_id ^= 1;
    let helper = thread::spawn(move || listener.accept(&wrong_client).err().unwrap().kind());
    assert!(LocalPipe::connect(&name, &identity, Duration::from_secs(1)).is_err());
    assert_eq!(helper.join().unwrap(), io::ErrorKind::PermissionDenied);
}

#[test]
fn absent_client_and_stalled_read_have_bounded_deadlines() {
    let identity = current_identity().unwrap();
    let listener = PipeListener::bind(&name(), &identity.sid, Duration::from_millis(150)).unwrap();
    let started = Instant::now();
    assert_eq!(
        listener.accept(&identity).err().unwrap().kind(),
        io::ErrorKind::TimedOut
    );
    assert!(started.elapsed() < Duration::from_secs(2));

    let name = name();
    let listener = PipeListener::bind(&name, &identity.sid, Duration::from_millis(500)).unwrap();
    let expected = identity.clone();
    let helper = thread::spawn(move || {
        let mut pipe = listener.accept(&expected).unwrap();
        pipe.read_exact(&mut [0u8]).unwrap_err().kind()
    });
    let _pipe = LocalPipe::connect(&name, &identity, Duration::from_secs(2)).unwrap();
    assert_eq!(helper.join().unwrap(), io::ErrorKind::TimedOut);
}

#[test]
fn full_pipe_write_cancels_without_leaving_inflight_buffers() {
    let identity = current_identity().unwrap();
    let name = name();
    let listener = PipeListener::bind(&name, &identity.sid, Duration::from_secs(2)).unwrap();
    let expected = identity.clone();
    let helper = thread::spawn(move || {
        let _pipe = listener.accept(&expected).unwrap();
        thread::sleep(Duration::from_millis(800));
    });
    let mut pipe = LocalPipe::connect(&name, &identity, Duration::from_millis(300)).unwrap();
    assert_eq!(
        pipe.write_all(&vec![1; 1024 * 1024]).unwrap_err().kind(),
        io::ErrorKind::TimedOut
    );
    helper.join().unwrap();
}

#[test]
fn wts_does_not_select_an_unrelated_logged_on_user() {
    let identity = current_identity().unwrap();
    if identity.is_system() {
        assert_eq!(identity, Identity::local_system());
        assert_eq!(
            logged_on_identity("S-1-5-21-4294967295-4294967295-4294967295-1001")
                .unwrap_err()
                .kind(),
            io::ErrorKind::NotFound
        );
    } else {
        assert_eq!(
            logged_on_identity(&identity.sid).unwrap_err().kind(),
            io::ErrorKind::PermissionDenied
        );
    }
}
