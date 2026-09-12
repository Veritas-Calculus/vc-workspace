use super::*;
use std::ffi::OsString;
use vc_workspace_windows_session::{
    current_identity, ensure_input_desktop, local_account_sid, logged_on_identity, run_worker,
    AgentAccounts, AuthorityRegistry, HelperLaunch, LocalPipe, NativeAccounts, PipeListener,
};

#[derive(Deserialize, Serialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
enum Message {
    Session,
    Action { action: BoundAction },
    Stop { target: SessionKey },
}

fn require_system() -> io::Result<()> {
    if current_identity()? != Identity::local_system() {
        return Err(denied("Windows dispatch requires LocalSystem Session 0"));
    }
    Ok(())
}

fn current_key(username: &str, instance: &str) -> io::Result<SessionKey> {
    let identity = current_identity()?;
    if local_account_sid(username)? != identity.sid {
        return Err(denied("Helper must run as its managed local account"));
    }
    key_for_identity(username, &identity, instance)
}

fn authorize(key: &SessionKey, request: &Request) -> io::Result<()> {
    let raw = AuthorityRegistry::default().read(&key.username, &key.sid)?;
    authorize_bytes(key, request, &raw, unix_millis())
}

fn authorize_interactive(key: &SessionKey, request: &Request) -> io::Result<()> {
    authorize(key, request)?;
    ensure_input_desktop()
}

fn exchange(
    username: &str,
    expected: &Identity,
    message: &Message,
    timeout: Duration,
) -> io::Result<serde_json::Value> {
    require_system()?;
    let pipe = LocalPipe::connect(username, expected, timeout)?;
    exchange_pipe(pipe, message)
}

fn exchange_pipe(mut pipe: LocalPipe, message: &Message) -> io::Result<serde_json::Value> {
    let mut frame = Vec::new();
    encode_frame(&mut frame, message)?;
    if frame.len() > MAX_REQUEST + 4 {
        return Err(denied("Windows request frame exceeds limit"));
    }
    pipe.write_all(&frame)?;
    let response = decode_frame(&mut pipe, MAX_RESPONSE_BYTES)?;
    // Complete receipt before the persistent server disconnects this client.
    pipe.write_all(&[2])?;
    Ok(response)
}

fn discover(username: &str) -> io::Result<SessionKey> {
    require_system()?;
    let sid = local_account_sid(username)?;
    let identity = logged_on_identity(&sid)?;
    AuthorityRegistry::default().check_registration(username, &sid)?;
    let response = exchange(
        username,
        &identity,
        &Message::Session,
        Duration::from_secs(3),
    )?;
    validate_announcement(username, &identity, response)?.ready_target()
}

fn start_helper(username: &str) -> io::Result<Announcement> {
    let mut launch = HelperLaunch::begin(username)?;
    let mut pipe = match launch.connect(Duration::from_millis(300)) {
        Ok(pipe) => pipe,
        Err(error) if error.kind() == io::ErrorKind::TimedOut => {
            launch.start()?;
            launch.connect(Duration::from_secs(5))?
        }
        Err(error) => return Err(error),
    };
    pipe.reset_timeout(Duration::from_secs(3))?;
    let target = validate_announcement(
        username,
        launch.identity(),
        exchange_pipe(pipe, &Message::Session)?,
    )?;
    launch.commit()?;
    Ok(target)
}

fn worker(username: &str, instance: &str) -> io::Result<()> {
    let key = current_key(username, instance)?;
    // Windows job stdin remains open until exit. Read one length-delimited
    // payload, not EOF; only the authenticated Helper invokes this fixed worker.
    let raw = decode_frame(&mut io::stdin(), MAX_REQUEST - 4)?;
    let action: BoundAction = serde_json::from_value(raw).map_err(io::Error::other)?;
    let request = validate_binding(&key, &action)?;
    authorize_interactive(&key, &request)?;
    let output = super::super::super::platform::execute(&request, || {
        authorize_interactive(&key, &request)
            .map_err(|_| "desktop control authority or input desktop has changed".into())
    });
    authorize_interactive(&key, &request)?;
    let mut response = failure(&request.request_id, "");
    match output {
        Ok(output) => {
            response.ok = true;
            response.error = None;
            match output {
                Output::Screenshot(value) => response.screenshot = Some(value),
                Output::Accessibility(value) => response.accessibility = Some(value),
                Output::Input => response.input = Some(InputResult { applied: true }),
            }
        }
        Err(error) => response = failure(&request.request_id, &error),
    }
    encode_frame(&mut io::stdout(), &response)
}

fn isolated_action(
    key: &SessionKey,
    action: &BoundAction,
    request: &Request,
) -> io::Result<serde_json::Value> {
    let frame = worker_frame(action)?;
    let remaining = request.expires_unix_ms.saturating_sub(unix_millis());
    if remaining <= 0 {
        return Err(denied("Windows action expired before worker startup"));
    }
    let arguments: Vec<OsString> = [
        "computer-v2-worker",
        "--guest-user",
        &key.username,
        "--instance-id",
        &key.instance_id,
    ]
    .into_iter()
    .map(OsString::from)
    .collect();
    let output = run_worker(
        &env::current_exe()?,
        &arguments,
        &frame,
        Duration::from_millis((remaining as u64).min(action.timeout_ms)),
        MAX_RESPONSE_BYTES,
    )?;
    if output.exit_code != 0 {
        return Err(denied("Windows action worker failed"));
    }
    worker_reply(&output.stdout, &request.request_id)
}

fn helper(username: &str) -> io::Result<()> {
    let mut nonce = [0u8; 32];
    getrandom::fill(&mut nonce).map_err(io::Error::other)?;
    let instance: String = nonce.iter().map(|byte| format!("{byte:02x}")).collect();
    let key = current_key(username, &instance)?;
    AuthorityRegistry::default().check_registration(username, &key.sid)?;
    let system = Identity::local_system();
    let mut listener = PipeListener::bind(username, &system.sid, Duration::from_secs(30))?;
    let mut replies = ReplayCache::default();
    loop {
        let mut stopping = false;
        let result = listener.serve_once(&system, Duration::from_secs(30), |pipe| {
            // The idle wait is not part of an arriving request's I/O budget.
            pipe.reset_timeout(Duration::from_secs(2))?;
            let message: Message = serde_json::from_value(decode_frame(pipe, MAX_REQUEST)?)
                .map_err(io::Error::other)?;
            pipe.reset_timeout(Duration::from_secs(20))?;
            let response = match message {
                Message::Session => {
                    if current_key(username, &instance)? != key {
                        return Err(denied("Windows Helper identity changed"));
                    }
                    AuthorityRegistry::default().check_registration(username, &key.sid)?;
                    serde_json::to_value(Announcement {
                        schema_version: VERSION,
                        target: key.clone(),
                        input_ready: ensure_input_desktop().is_ok(),
                    })
                    .map_err(io::Error::other)?
                }
                Message::Action { action } => handle_action(
                    &key,
                    &action,
                    &mut replies,
                    |request| authorize_interactive(&key, request),
                    |request| isolated_action(&key, &action, request),
                )?,
                Message::Stop { target } => {
                    // A SYSTEM lifecycle command, not an Agent computer tool.
                    // Never stop a replacement on a stale/lost-result retry.
                    if target != key || current_key(username, &instance)? != key {
                        return Err(denied("Windows Helper stop target changed"));
                    }
                    stopping = true;
                    serde_json::json!({"schema_version": VERSION, "stopped": true, "target": key})
                }
            };
            encode_frame(pipe, &response)?;
            let mut ack = [0];
            pipe.read_exact(&mut ack)?;
            if ack != [2] {
                return Err(denied("invalid Windows response receipt"));
            }
            Ok(())
        });
        if stopping {
            return result;
        }
        if !listener.is_usable() {
            return Err(denied("Windows Helper pipe cleanup failed"));
        }
        if result.is_err() {
            // Never log action data or turn an invalid peer into a tight loop.
            thread::sleep(Duration::from_millis(25));
        }
    }
}

fn read_input() -> io::Result<Vec<u8>> {
    let mut raw = Vec::new();
    io::stdin()
        .take(MAX_REQUEST as u64 + 1)
        .read_to_end(&mut raw)?;
    if raw.is_empty() || raw.len() > MAX_REQUEST {
        return Err(denied("Windows command input exceeds limit"));
    }
    Ok(raw)
}

pub fn run(command: &str, arguments: &[String]) -> io::Result<()> {
    if command == "computer-v2-accounts-reconcile" && arguments.is_empty() {
        let converged = vc_workspace_windows_session::reconcile_accounts()?;
        println!(
            "{}",
            serde_json::json!({"schema_version":1,"expired_or_disabled_accounts":converged})
        );
        return Ok(());
    }
    if command == "computer-v2-capabilities" && arguments.is_empty() {
        // Keep deployment auto-selection closed until the account and real RDP
        // lifecycle is implemented and accepted, not just this opt-in CLI path.
        println!("{{\"schema_version\":2,\"session_transport\":\"unavailable\",\"experimental_session_transport\":\"named_pipe_sid\",\"session_identity\":\"sid_wts_luid\",\"authority_store\":\"protected_registry\",\"experimental_account_lifecycle\":\"system_owned_sam_v1\",\"experimental_account_fence\":\"lease_epoch_login_generation_v1\",\"computer_actions\":false,\"unattended_login\":false}}");
        return Ok(());
    }
    if command == "computer-v2-authority" && arguments.is_empty() {
        require_system()?;
        let raw = read_input()?;
        let (bound, authority) = parse_authority(&raw, unix_millis())?;
        let registry = AuthorityRegistry::default();
        let mut update = registry.begin_update()?;
        if let Some(previous) = update.previous()? {
            validate_authority_transition(&previous, &bound, &authority)?;
        } else {
            validate_unfenced_authorities(&update.existing_authorities()?, &bound, &authority)?;
        }
        if let Some(target) = &bound.target {
            // Revalidate while holding the registry transaction's write intent,
            // not before waiting behind another publication or revocation.
            if discover(&target.username)? != *target {
                return Err(denied(
                    "Windows Helper instance changed before authorization",
                ));
            }
        }
        let desired = serde_json::to_vec(&bound).map_err(io::Error::other)?;
        let revoked = if authority.state == "revoked" {
            desired.clone()
        } else {
            serde_json::to_vec(&serde_json::json!({"schema_version":2,"target":null,
                "authority":{"schema_version":1,"lease_id":authority.lease_id,
                "control_epoch":authority.control_epoch,"state":"revoked","expires_unix_ms":0}}))
            .map_err(io::Error::other)?
        };
        update.stage(
            bound
                .target
                .as_ref()
                .map(|target| (target.username.as_str(), target.sid.as_str())),
            &desired,
            &revoked,
        )?;
        // Discovery/registry work consumed time. Do not acknowledge an active
        // publication whose deadline elapsed while it was being prepared.
        parse_authority(&raw, unix_millis())?;
        return update.commit();
    }
    if arguments.len() < 2 || arguments[0] != "--guest-user" || !managed_name(&arguments[1]) {
        return Err(denied("managed Windows guest account required"));
    }
    let username = &arguments[1];
    if command == "computer-v2-helper" && arguments.len() == 2 {
        return helper(username);
    }
    if command == "computer-v2-worker" && arguments.len() == 4 && arguments[2] == "--instance-id" {
        return worker(username, &arguments[3]);
    }
    require_system()?;
    if command == "computer-v2-dispatch" && arguments.len() == 4 && arguments[2] == "--request-id" {
        let action: BoundAction =
            serde_json::from_slice(&read_input()?).map_err(io::Error::other)?;
        let request = validate_binding(&action.target, &action)?;
        let sid = local_account_sid(username)?;
        let identity = logged_on_identity(&sid)?;
        if key_for_identity(username, &identity, &action.target.instance_id)? != action.target
            || request.request_id != arguments[3]
        {
            return Err(denied("Windows dispatch target changed"));
        }
        let key = action.target.clone();
        authorize(&key, &request)?;
        let response = exchange(
            username,
            &identity,
            &Message::Action { action },
            Duration::from_secs(20),
        )?;
        // The helper also rechecks. Root never returns a late observation after
        // SYSTEM has replaced the local authority while the IPC was in flight.
        authorize(&key, &request)?;
        if response["request_id"] != request.request_id
            || response["schema_version"] != SCHEMA_VERSION
            || !response["ok"].is_boolean()
        {
            return Err(denied("invalid Windows Helper response"));
        }
        println!("{}", qga_json(&response)?);
        return Ok(());
    }
    if arguments.len() != 2 {
        return Err(denied("unknown Windows session command options"));
    }
    match command {
        "computer-v2-native-account-provision" => {
            NativeAccounts::default().provision(username).map(|_| ())
        }
        "computer-v2-native-account-credential" => {
            let raw = vc_workspace_guest_lifecycle::Zeroizing::new(read_input()?);
            let request = vc_workspace_guest_lifecycle::native::Request::parse(&raw)?;
            let receipt = NativeAccounts::default().apply_credential(username, &request)?;
            println!(
                "{}",
                serde_json::to_string(&receipt).map_err(io::Error::other)?
            );
            Ok(())
        }
        "computer-v2-native-account-inspect" => {
            let account = NativeAccounts::default().observe(username)?;
            let sessions: Vec<_> = account.sessions.iter().map(Identity::binding_id).collect();
            // This experimental adapter observes SAM/WTS, not every possible
            // process/token birth path. Do not fabricate Linux closure fields.
            println!(
                "{}",
                serde_json::json!({"schema_version":1,"observation_version":1,
                "username":account.username,"sid":account.sid,"exists":account.disabled.is_some(),
                "disabled":account.disabled,"expires_unix_seconds":account.expires_unix_seconds,
                "sessions":sessions,"lifecycle":account.lifecycle})
            );
            Ok(())
        }
        "computer-v2-account-lease" => {
            let raw = vc_workspace_guest_lifecycle::Zeroizing::new(read_input()?);
            let request = vc_workspace_guest_lifecycle::Request::parse(&raw)?;
            let receipt = AgentAccounts::default().apply_lease(username, &request)?;
            println!(
                "{}",
                serde_json::to_string(&receipt).map_err(io::Error::other)?
            );
            Ok(())
        }
        "computer-v2-account-provision" => {
            let account = AgentAccounts::default().provision(username)?;
            AuthorityRegistry::default().initialize(username, &account.sid)
        }
        "computer-v2-account-enable" => {
            #[derive(Deserialize)]
            #[serde(deny_unknown_fields)]
            struct LoginWindow {
                expires_unix_seconds: u32,
            }
            let window: LoginWindow =
                serde_json::from_slice(&read_input()?).map_err(io::Error::other)?;
            AgentAccounts::default()
                .enable(username, window.expires_unix_seconds)
                .map(|_| ())
        }
        "computer-v2-account-disable" => AgentAccounts::default().disable(username).map(|_| ()),
        "computer-v2-account-inspect" => {
            let account = AgentAccounts::default().observe(username)?;
            let sessions: Vec<_> = account.sessions.iter().map(Identity::binding_id).collect();
            println!(
                "{}",
                serde_json::json!({"schema_version":1,"observation_version":1,
                "username":account.username,"sid":account.sid,
                "exists":account.disabled.is_some(),"disabled":account.disabled,
                "expires_unix_seconds":account.expires_unix_seconds,"sessions":sessions,
                "lifecycle":account.lifecycle})
            );
            Ok(())
        }
        "computer-v2-helper-start" => {
            println!(
                "{}",
                serde_json::to_value(start_helper(username)?).map_err(io::Error::other)?
            );
            Ok(())
        }
        "computer-v2-helper-stop" => {
            let target: SessionKey =
                serde_json::from_slice(&read_input()?).map_err(io::Error::other)?;
            let account = AgentAccounts::default().verify(username)?;
            let identity = logged_on_identity(&account.sid)?;
            if key_for_identity(username, &identity, &target.instance_id)? != target {
                return Err(denied("Windows Helper stop target changed"));
            }
            let response = exchange(
                username,
                &identity,
                &Message::Stop {
                    target: target.clone(),
                },
                Duration::from_secs(5),
            )?;
            if response
                != serde_json::json!({"schema_version":VERSION,"stopped":true,"target":target})
            {
                return Err(denied("invalid Windows Helper stop receipt"));
            }
            println!("{}", qga_json(&response)?);
            Ok(())
        }
        "computer-v2-init" => {
            AuthorityRegistry::default().initialize(username, &local_account_sid(username)?)
        }
        "computer-v2-session" => {
            println!(
                "{}",
                serde_json::to_value(Announcement {
                    schema_version: VERSION,
                    target: discover(username)?,
                    input_ready: true,
                })
                .map_err(io::Error::other)?
            );
            Ok(())
        }
        "computer-v2-inspect" => {
            let sid = local_account_sid(username)?;
            let identity = logged_on_identity(&sid)?;
            let ready = discover(username).is_ok_and(|target| {
                target.sid == sid && target.session_id == identity.binding_id()
            });
            println!(
                "{}",
                serde_json::json!({"schema_version":2,"username":username,
                "sid":sid,"session_id":identity.binding_id(),"helper_ready":ready})
            );
            Ok(())
        }
        _ => Err(io::Error::new(
            io::ErrorKind::Unsupported,
            "unsupported Windows session command",
        )),
    }
}
