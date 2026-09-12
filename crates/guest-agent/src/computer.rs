#[cfg(any(target_os = "linux", target_os = "windows"))]
use base64::{engine::general_purpose::STANDARD as BASE64, Engine as _};
use serde::{Deserialize, Serialize};
#[cfg(any(target_os = "linux", target_os = "windows"))]
use sha2::{Digest, Sha256};
#[cfg(not(target_os = "windows"))]
use std::process::{Command, Stdio};
use std::{
    env, fs,
    io::{self, Write},
    path::{Path, PathBuf},
    process::ExitCode,
    thread,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

const SCHEMA_VERSION: u8 = 1;
const MAX_RESPONSE_BYTES: usize = 16 * 1024 * 1024;

mod session;
#[cfg(target_os = "linux")]
pub(crate) use session::reconcile_accounts;

#[derive(Debug, Deserialize)]
struct Request {
    schema_version: u8,
    request_id: String,
    lease_id: String,
    control_epoch: i64,
    expires_unix_ms: i64,
    operation: String,
    screenshot: Option<Screenshot>,
    accessibility: Option<Accessibility>,
    mouse: Option<Mouse>,
    key: Option<KeyInput>,
    text: Option<TextInput>,
}

#[derive(Debug, Deserialize)]
struct Screenshot {
    max_width: u32,
}

#[derive(Debug, Deserialize)]
struct Accessibility {
    max_depth: usize,
    max_nodes: usize,
}

#[derive(Debug, Deserialize)]
struct Mouse {
    action: String,
    #[serde(default)]
    x: i32,
    #[serde(default)]
    y: i32,
    #[serde(default)]
    button: String,
    #[serde(default)]
    delta_x: i32,
    #[serde(default)]
    delta_y: i32,
}

#[derive(Debug, Deserialize)]
struct KeyInput {
    key: String,
    #[serde(default)]
    modifiers: Vec<String>,
}

#[derive(Debug, Deserialize)]
struct TextInput {
    value: String,
    #[serde(default)]
    #[allow(dead_code)]
    sensitive: bool,
}

#[derive(Debug, Deserialize)]
struct Authority {
    schema_version: u8,
    lease_id: String,
    control_epoch: i64,
    state: String,
    expires_unix_ms: i64,
}

#[derive(Debug, Serialize)]
struct Response {
    schema_version: u8,
    request_id: String,
    ok: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    screenshot: Option<ScreenshotResult>,
    #[serde(skip_serializing_if = "Option::is_none")]
    accessibility: Option<AccessibilityResult>,
    #[serde(skip_serializing_if = "Option::is_none")]
    input: Option<InputResult>,
}

#[derive(Debug, Serialize)]
struct ScreenshotResult {
    content_type: &'static str,
    data: String,
    width: u32,
    height: u32,
    sha256: String,
    desktop_bounds: DesktopBounds,
}

#[derive(Debug, Serialize)]
struct DesktopBounds {
    x: i32,
    y: i32,
    width: u32,
    height: u32,
}

#[derive(Debug, Serialize)]
struct AccessibilityResult {
    source: &'static str,
    truncated: bool,
    nodes: Vec<AccessibilityNode>,
}

#[derive(Debug, Serialize)]
struct AccessibilityNode {
    id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    parent_id: Option<String>,
    role: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    value: String,
    x: i32,
    y: i32,
    width: u32,
    height: u32,
    focused: bool,
    enabled: bool,
}

#[derive(Debug, Serialize)]
struct InputResult {
    applied: bool,
}

#[derive(Debug)]
struct CLIConfig {
    state_dir: PathBuf,
    request_id: Option<String>,
    timeout: Duration,
}

pub(crate) fn run_cli(arguments: &[String]) -> Option<ExitCode> {
    if let Some(result) = session::run_cli(arguments) {
        return Some(result);
    }
    let command = arguments.first()?.as_str();
    if !matches!(
        command,
        "computer-helper"
            | "computer-run"
            | "computer-dispatch"
            | "computer-wait"
            | "computer-cleanup"
    ) {
        return None;
    }
    let config = match parse_cli(&arguments[1..]) {
        Ok(config) => config,
        Err(error) => {
            eprintln!("vc-workspace-guest-agent: {error}");
            return Some(ExitCode::from(2));
        }
    };
    let result = match command {
        "computer-helper" => run_helper(&config),
        "computer-run" => run_worker(&config),
        "computer-dispatch" => dispatch(&config),
        "computer-wait" => wait_for_response(&config),
        "computer-cleanup" => cleanup(&config),
        _ => unreachable!(),
    };
    Some(match result {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) if error.kind() == io::ErrorKind::TimedOut => ExitCode::from(124),
        Err(error) => {
            eprintln!("vc-workspace-guest-agent: {error}");
            ExitCode::FAILURE
        }
    })
}

fn parse_cli(arguments: &[String]) -> Result<CLIConfig, String> {
    let mut config = CLIConfig {
        state_dir: default_state_dir(),
        request_id: None,
        timeout: Duration::from_secs(5),
    };
    let mut index = 0;
    while index < arguments.len() {
        match arguments[index].as_str() {
            "--state-dir" => {
                index += 1;
                config.state_dir =
                    PathBuf::from(arguments.get(index).ok_or("--state-dir requires a path")?);
            }
            "--request-id" => {
                index += 1;
                let request_id = arguments
                    .get(index)
                    .ok_or("--request-id requires a value")?;
                if !valid_request_id(request_id) {
                    return Err("--request-id is invalid".into());
                }
                config.request_id = Some(request_id.clone());
            }
            "--timeout-ms" => {
                index += 1;
                let timeout: u64 = arguments
                    .get(index)
                    .ok_or("--timeout-ms requires a number")?
                    .parse()
                    .map_err(|_| "--timeout-ms must be a number")?;
                if !(250..=15_000).contains(&timeout) {
                    return Err("--timeout-ms must be between 250 and 15000".into());
                }
                config.timeout = Duration::from_millis(timeout);
            }
            argument => return Err(format!("unknown computer command argument {argument}")),
        }
        index += 1;
    }
    Ok(config)
}

fn run_helper(config: &CLIConfig) -> io::Result<()> {
    let paths = ComputerPaths::new(&config.state_dir);
    fs::create_dir_all(&paths.requests)?;
    fs::create_dir_all(&paths.responses)?;
    loop {
        match atomic_write(&paths.ready, format!("{}\n", unix_millis()).as_bytes()) {
            Ok(()) => break,
            Err(error) if paths.ready.is_file() => {
                eprintln!(
                    "vc-workspace-guest-agent: preserving existing helper heartbeat after refresh failed: {error}"
                );
                break;
            }
            Err(error) => {
                eprintln!("vc-workspace-guest-agent: initial helper heartbeat failed: {error}");
                thread::sleep(Duration::from_millis(100));
            }
        }
    }
    let mut last_heartbeat = Instant::now();
    let mut heartbeat_error_reported = false;
    loop {
        if last_heartbeat.elapsed() >= Duration::from_secs(5) {
            match atomic_write(&paths.ready, format!("{}\n", unix_millis()).as_bytes()) {
                Ok(()) => {
                    last_heartbeat = Instant::now();
                    heartbeat_error_reported = false;
                }
                Err(error) => {
                    if !heartbeat_error_reported {
                        eprintln!("vc-workspace-guest-agent: helper heartbeat failed: {error}");
                        heartbeat_error_reported = true;
                    }
                    last_heartbeat = Instant::now();
                }
            }
        }
        if let Err(error) = process_pending(&paths) {
            eprintln!("vc-workspace-guest-agent: helper request processing failed: {error}");
            thread::sleep(Duration::from_millis(100));
            continue;
        }
        thread::sleep(Duration::from_millis(50));
    }
}

fn process_pending(paths: &ComputerPaths) -> io::Result<()> {
    let mut requests = fs::read_dir(&paths.requests)?
        .filter_map(Result::ok)
        .filter(|entry| {
            entry
                .file_type()
                .map(|kind| kind.is_file())
                .unwrap_or(false)
        })
        .collect::<Vec<_>>();
    requests.sort_by_key(fs::DirEntry::file_name);
    for entry in requests {
        let Some(filename) = entry.file_name().to_str().map(str::to_owned) else {
            continue;
        };
        let Some(request_id) = filename.strip_suffix(".json") else {
            continue;
        };
        if !valid_request_id(request_id) {
            continue;
        }
        let response_path = paths.response(request_id);
        if response_path.exists() {
            let _ = fs::remove_file(entry.path());
            continue;
        }
        let raw = match fs::read(entry.path()) {
            Ok(raw) => raw,
            Err(_) => continue,
        };
        let request: Request = match serde_json::from_slice(&raw) {
            Ok(request) => request,
            Err(_) => {
                write_response(&response_path, &failure(request_id, "request is invalid"))?;
                let _ = fs::remove_file(entry.path());
                continue;
            }
        };
        let remaining_ms = request.expires_unix_ms.saturating_sub(unix_millis());
        let timeout = Duration::from_millis(remaining_ms.clamp(1, 15_000) as u64);
        if let Err(error) = run_isolated_worker(paths, request_id, timeout) {
            write_response(&response_path, &failure(request_id, &error.to_string()))?;
        }
        let _ = fs::remove_file(entry.path());
    }
    Ok(())
}

fn run_worker(config: &CLIConfig) -> io::Result<()> {
    let request_id = config
        .request_id
        .as_deref()
        .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidInput, "--request-id is required"))?;
    let paths = ComputerPaths::new(&config.state_dir);
    let raw = fs::read(paths.request(request_id))?;
    let response = match serde_json::from_slice(&raw) {
        Ok(request) => execute_request(&paths, request_id, request),
        Err(_) => failure(request_id, "request is invalid"),
    };
    write_response(&paths.response(request_id), &response)
}

#[cfg(target_os = "windows")]
fn run_isolated_worker(
    paths: &ComputerPaths,
    request_id: &str,
    timeout: Duration,
) -> io::Result<()> {
    let arguments = [
        "computer-run".into(),
        "--state-dir".into(),
        paths.state_dir.as_os_str().to_owned(),
        "--request-id".into(),
        request_id.into(),
    ];
    // V1 remains a migration-only protocol. Still terminate the entire Windows
    // worker tree on success/failure/timeout; never leave UIA/input descendants.
    let result = vc_workspace_windows_session::run_worker(
        &env::current_exe()?,
        &arguments,
        &[],
        timeout,
        1024,
    )?;
    if result.exit_code == 0 && paths.response(request_id).is_file() {
        Ok(())
    } else {
        Err(io::Error::other("computer worker failed"))
    }
}

#[cfg(not(target_os = "windows"))]
fn run_isolated_worker(
    paths: &ComputerPaths,
    request_id: &str,
    timeout: Duration,
) -> io::Result<()> {
    let executable = env::current_exe()?;
    let mut child = Command::new(executable)
        .args([
            "computer-run",
            "--state-dir",
            paths.state_dir.to_string_lossy().as_ref(),
            "--request-id",
            request_id,
        ])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::inherit())
        .spawn()?;
    let deadline = Instant::now() + timeout;
    loop {
        if let Some(status) = child.try_wait()? {
            if status.success() && paths.response(request_id).is_file() {
                return Ok(());
            }
            return Err(io::Error::other("computer worker failed"));
        }
        if Instant::now() >= deadline {
            let _ = child.kill();
            let _ = child.wait();
            return Err(io::Error::new(
                io::ErrorKind::TimedOut,
                "computer action timed out",
            ));
        }
        thread::sleep(Duration::from_millis(20));
    }
}

fn dispatch(config: &CLIConfig) -> io::Result<()> {
    let request_id = config
        .request_id
        .as_deref()
        .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidInput, "--request-id is required"))?;
    let paths = ComputerPaths::new(&config.state_dir);
    publish_staged_request(&paths, request_id)?;
    let deadline = Instant::now() + config.timeout;
    while Instant::now() < deadline {
        let response = paths.response(request_id);
        if response.is_file() {
            let raw = fs::read(&response)?;
            if raw.len() > MAX_RESPONSE_BYTES {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidData,
                    "computer response exceeds the transport limit",
                ));
            }
            io::stdout().write_all(&raw)?;
            cleanup_request_files(&paths, request_id)?;
            return Ok(());
        }
        thread::sleep(Duration::from_millis(50));
    }
    cleanup_request_files(&paths, request_id)?;
    Err(io::Error::new(
        io::ErrorKind::TimedOut,
        "computer action timed out",
    ))
}

fn publish_staged_request(paths: &ComputerPaths, request_id: &str) -> io::Result<()> {
    let staged = paths.staged_request(request_id);
    let request = paths.request(request_id);
    let response = paths.response(request_id);
    if staged.is_file() {
        if request.exists() {
            fs::remove_file(&request)?;
        }
        fs::rename(staged, request)?;
        return Ok(());
    }
    // A retried QGA guest-exec may arrive after the first dispatcher already
    // published the request or after the helper produced the response.
    if request.is_file() || response.is_file() {
        return Ok(());
    }
    Err(io::Error::new(
        io::ErrorKind::NotFound,
        "staged computer request is unavailable",
    ))
}

fn write_response(path: &Path, response: &Response) -> io::Result<()> {
    let encoded = serde_json::to_vec(response).map_err(io::Error::other)?;
    if encoded.len() <= MAX_RESPONSE_BYTES {
        return atomic_write(path, &encoded);
    }
    let bounded = failure(
        &response.request_id,
        "response exceeds the 16 MiB transport limit",
    );
    atomic_write(
        path,
        &serde_json::to_vec(&bounded).map_err(io::Error::other)?,
    )
}

fn execute_request(paths: &ComputerPaths, request_id: &str, request: Request) -> Response {
    if let Err(error) = validate_request(request_id, &request) {
        return failure(request_id, error);
    }
    if let Err(error) = validate_authority(paths, &request) {
        return failure(request_id, error);
    }
    match platform::execute(&request, || {
        validate_authority(paths, &request).map_err(str::to_owned)
    }) {
        Ok(Output::Screenshot(screenshot)) => Response {
            schema_version: SCHEMA_VERSION,
            request_id: request_id.to_owned(),
            ok: true,
            error: None,
            screenshot: Some(screenshot),
            accessibility: None,
            input: None,
        },
        Ok(Output::Accessibility(accessibility)) => Response {
            schema_version: SCHEMA_VERSION,
            request_id: request_id.to_owned(),
            ok: true,
            error: None,
            screenshot: None,
            accessibility: Some(accessibility),
            input: None,
        },
        Ok(Output::Input) => Response {
            schema_version: SCHEMA_VERSION,
            request_id: request_id.to_owned(),
            ok: true,
            error: None,
            screenshot: None,
            accessibility: None,
            input: Some(InputResult { applied: true }),
        },
        Err(error) => failure(request_id, &sanitize_error(&error)),
    }
}

fn validate_request<'a>(request_id: &str, request: &'a Request) -> Result<(), &'a str> {
    if request.schema_version != SCHEMA_VERSION
        || request.request_id != request_id
        || !request.lease_id.starts_with("lease_")
        || request.control_epoch < 1
        || request.expires_unix_ms <= unix_millis()
    {
        return Err("request identity or expiry is invalid");
    }
    let payload_count = [
        request.screenshot.is_some(),
        request.accessibility.is_some(),
        request.mouse.is_some(),
        request.key.is_some(),
        request.text.is_some(),
    ]
    .into_iter()
    .filter(|present| *present)
    .count();
    if payload_count != 1 {
        return Err("request must contain exactly one operation payload");
    }
    match request.operation.as_str() {
        "screenshot"
            if request
                .screenshot
                .as_ref()
                .is_some_and(|value| (320..=3840).contains(&value.max_width)) =>
        {
            Ok(())
        }
        "accessibility_snapshot"
            if request.accessibility.as_ref().is_some_and(|value| {
                (1..=10).contains(&value.max_depth) && (1..=500).contains(&value.max_nodes)
            }) =>
        {
            Ok(())
        }
        "mouse" if request.mouse.as_ref().is_some_and(valid_mouse) => Ok(()),
        "key" if request.key.as_ref().is_some_and(valid_key) => Ok(()),
        "type_text"
            if request.text.as_ref().is_some_and(|value| {
                !value.value.is_empty() && value.value.len() <= 4096 && !value.value.contains('\0')
            }) =>
        {
            Ok(())
        }
        _ => Err("operation payload is invalid"),
    }
}

fn valid_mouse(input: &Mouse) -> bool {
    match input.action.as_str() {
        "move" => (0..=16384).contains(&input.x) && (0..=16384).contains(&input.y),
        "click" => {
            (0..=16384).contains(&input.x)
                && (0..=16384).contains(&input.y)
                && matches!(input.button.as_str(), "left" | "right" | "middle")
        }
        "scroll" => {
            (-100..=100).contains(&input.delta_x)
                && (-100..=100).contains(&input.delta_y)
                && (input.delta_x != 0 || input.delta_y != 0)
        }
        _ => false,
    }
}

fn valid_key(input: &KeyInput) -> bool {
    let key = input.key.to_ascii_lowercase();
    let key_ok = matches!(
        key.as_str(),
        "enter"
            | "escape"
            | "tab"
            | "backspace"
            | "delete"
            | "space"
            | "up"
            | "down"
            | "left"
            | "right"
            | "home"
            | "end"
            | "page_up"
            | "page_down"
            | "f1"
            | "f2"
            | "f3"
            | "f4"
            | "f5"
            | "f6"
            | "f7"
            | "f8"
            | "f9"
            | "f10"
            | "f11"
            | "f12"
    );
    let mut modifiers = input
        .modifiers
        .iter()
        .map(|value| value.to_ascii_lowercase())
        .collect::<Vec<_>>();
    modifiers.sort();
    let unique = modifiers.windows(2).all(|pair| pair[0] != pair[1]);
    let shortcut_key = key.len() == 1
        && key.bytes().all(|byte| byte.is_ascii_alphanumeric())
        && !modifiers.is_empty();
    (key_ok || shortcut_key)
        && unique
        && modifiers
            .iter()
            .all(|value| matches!(value.as_str(), "control" | "alt" | "shift" | "meta"))
}

fn validate_authority<'a>(paths: &ComputerPaths, request: &'a Request) -> Result<(), &'a str> {
    let raw = fs::read(&paths.authority).map_err(|_| "desktop control authority is unavailable")?;
    let authority: Authority =
        serde_json::from_slice(&raw).map_err(|_| "desktop control authority is invalid")?;
    if authority.schema_version != SCHEMA_VERSION
        || authority.state != "active"
        || authority.lease_id != request.lease_id
        || authority.control_epoch != request.control_epoch
        || authority.expires_unix_ms <= unix_millis()
    {
        return Err("desktop control authority has changed");
    }
    Ok(())
}

fn wait_for_response(config: &CLIConfig) -> io::Result<()> {
    let request_id = config
        .request_id
        .as_deref()
        .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidInput, "--request-id is required"))?;
    let response = ComputerPaths::new(&config.state_dir).response(request_id);
    let deadline = Instant::now() + config.timeout;
    while Instant::now() < deadline {
        if response.is_file() {
            return Ok(());
        }
        thread::sleep(Duration::from_millis(50));
    }
    Err(io::Error::new(
        io::ErrorKind::TimedOut,
        "computer action timed out",
    ))
}

fn cleanup(config: &CLIConfig) -> io::Result<()> {
    let request_id = config
        .request_id
        .as_deref()
        .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidInput, "--request-id is required"))?;
    let paths = ComputerPaths::new(&config.state_dir);
    cleanup_request_files(&paths, request_id)
}

fn cleanup_request_files(paths: &ComputerPaths, request_id: &str) -> io::Result<()> {
    for path in [
        paths.staged_request(request_id),
        paths.request(request_id),
        paths.response(request_id),
    ] {
        match fs::remove_file(path) {
            Ok(()) => {}
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(error) => return Err(error),
        }
    }
    Ok(())
}

fn failure(request_id: &str, error: &str) -> Response {
    Response {
        schema_version: SCHEMA_VERSION,
        request_id: request_id.to_owned(),
        ok: false,
        error: Some(sanitize_error(error)),
        screenshot: None,
        accessibility: None,
        input: None,
    }
}

fn sanitize_error(error: &str) -> String {
    let normalized = error.split_whitespace().collect::<Vec<_>>().join(" ");
    normalized.chars().take(240).collect()
}

fn valid_request_id(value: &str) -> bool {
    value.len() >= 27
        && value.len() <= 87
        && value.starts_with("action_")
        && value[7..]
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || byte == b'_' || byte == b'-')
}

fn atomic_write(path: &Path, payload: &[u8]) -> io::Result<()> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)?;
    }
    let temporary = path.with_extension(format!("{}.tmp", std::process::id()));
    let mut file = fs::File::create(&temporary)?;
    file.write_all(payload)?;
    #[cfg(not(target_os = "windows"))]
    file.sync_all()?;
    #[cfg(target_os = "windows")]
    file.flush()?;
    drop(file);
    #[cfg(not(target_os = "windows"))]
    {
        fs::rename(temporary, path)
    }
    #[cfg(target_os = "windows")]
    {
        let deadline = Instant::now() + Duration::from_secs(2);
        loop {
            if path.exists() {
                match fs::remove_file(path) {
                    Ok(()) => {}
                    Err(_) if Instant::now() < deadline => {
                        thread::sleep(Duration::from_millis(20));
                        continue;
                    }
                    Err(error) => return Err(error),
                }
            }
            match fs::rename(&temporary, path) {
                Ok(()) => return Ok(()),
                Err(_) if Instant::now() < deadline => {
                    thread::sleep(Duration::from_millis(20));
                }
                Err(error) => return Err(error),
            }
        }
    }
}

fn default_state_dir() -> PathBuf {
    if cfg!(windows) {
        env::var_os("PROGRAMDATA")
            .map(PathBuf::from)
            .unwrap_or_else(|| PathBuf::from(r"C:\ProgramData"))
            .join("VC Workspace")
            .join("Agent")
    } else {
        PathBuf::from("/var/lib/vc-workspace")
    }
}

fn unix_millis() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis()
        .try_into()
        .unwrap_or(i64::MAX)
}

struct ComputerPaths {
    state_dir: PathBuf,
    authority: PathBuf,
    requests: PathBuf,
    responses: PathBuf,
    ready: PathBuf,
}

impl ComputerPaths {
    fn new(state_dir: &Path) -> Self {
        let base = state_dir.join("computer");
        Self {
            state_dir: state_dir.to_owned(),
            authority: base.join("authority.json"),
            requests: base.join("requests"),
            responses: base.join("responses"),
            ready: base.join("helper-ready"),
        }
    }

    fn request(&self, request_id: &str) -> PathBuf {
        self.requests.join(format!("{request_id}.json"))
    }

    fn staged_request(&self, request_id: &str) -> PathBuf {
        self.requests.join(format!("{request_id}.json.tmp"))
    }

    fn response(&self, request_id: &str) -> PathBuf {
        self.responses.join(format!("{request_id}.json"))
    }
}

#[cfg_attr(not(any(target_os = "linux", target_os = "windows")), allow(dead_code))]
enum Output {
    Screenshot(ScreenshotResult),
    Accessibility(AccessibilityResult),
    Input,
}

#[cfg(any(target_os = "linux", target_os = "windows"))]
mod platform {
    use super::*;
    use enigo::{Axis, Button, Coordinate, Direction, Enigo, Mouse as _, Settings};
    #[cfg(not(target_os = "windows"))]
    use enigo::{Key, Keyboard};
    use image::{codecs::jpeg::JpegEncoder, imageops::FilterType, DynamicImage};
    use xcap::{Monitor, Window};

    pub(super) fn execute(
        request: &Request,
        authority_check: impl Fn() -> Result<(), String>,
    ) -> Result<Output, String> {
        match request.operation.as_str() {
            "screenshot" => {
                authority_check()?;
                let output =
                    screenshot(request.screenshot.as_ref().expect("validated screenshot"))?;
                authority_check()?;
                Ok(output)
            }
            "accessibility_snapshot" => {
                authority_check()?;
                let output = accessibility(
                    request
                        .accessibility
                        .as_ref()
                        .expect("validated accessibility"),
                )?;
                authority_check()?;
                Ok(output)
            }
            "mouse" => {
                authority_check()?;
                mouse(request.mouse.as_ref().expect("validated mouse"))?;
                Ok(Output::Input)
            }
            "key" => {
                authority_check()?;
                key(request.key.as_ref().expect("validated key"))?;
                Ok(Output::Input)
            }
            "type_text" => {
                type_text(
                    request.text.as_ref().expect("validated text"),
                    authority_check,
                )?;
                Ok(Output::Input)
            }
            _ => Err("unsupported operation".into()),
        }
    }

    fn screenshot(input: &Screenshot) -> Result<Output, String> {
        let monitors =
            Monitor::all().map_err(|error| format!("screen capture unavailable: {error}"))?;
        let monitor = monitors
            .iter()
            .find(|monitor| monitor.is_primary().unwrap_or(false))
            .or_else(|| monitors.first())
            .ok_or_else(|| "interactive session has no display".to_owned())?;
        let desktop_bounds = DesktopBounds {
            x: monitor.x().map_err(|_| "display origin unavailable")?,
            y: monitor.y().map_err(|_| "display origin unavailable")?,
            width: monitor.width().map_err(|_| "display size unavailable")?,
            height: monitor.height().map_err(|_| "display size unavailable")?,
        };
        let image = monitor
            .capture_image()
            .map_err(|error| format!("screen capture failed: {error}"))?;
        let image = if image.width() > input.max_width {
            let height = (u64::from(image.height()) * u64::from(input.max_width)
                / u64::from(image.width())) as u32;
            image::imageops::resize(&image, input.max_width, height.max(1), FilterType::Triangle)
        } else {
            image
        };
        let mut encoded = Vec::new();
        JpegEncoder::new_with_quality(&mut encoded, 78)
            .encode_image(&DynamicImage::ImageRgba8(image.clone()))
            .map_err(|error| format!("screen encoding failed: {error}"))?;
        let digest = format!("{:x}", Sha256::digest(&encoded));
        Ok(Output::Screenshot(ScreenshotResult {
            content_type: "image/jpeg",
            data: BASE64.encode(encoded),
            width: image.width(),
            height: image.height(),
            sha256: digest,
            desktop_bounds,
        }))
    }

    fn accessibility(input: &Accessibility) -> Result<Output, String> {
        #[cfg(target_os = "linux")]
        if let Ok(snapshot) = linux_accessibility(input) {
            return Ok(Output::Accessibility(snapshot));
        }

        #[cfg(target_os = "windows")]
        if let Ok(snapshot) = windows_accessibility(input) {
            return Ok(Output::Accessibility(snapshot));
        }

        // Window enumeration remains a deliberately reduced fallback when a
        // platform accessibility provider is unavailable in the user session.
        let windows =
            Window::all().map_err(|error| format!("window snapshot unavailable: {error}"))?;
        let truncated = windows.len() > input.max_nodes;
        let nodes = windows
            .into_iter()
            .take(input.max_nodes)
            .filter_map(|window| {
                let id = window.id().ok()?;
                let width = window.width().ok()?;
                let height = window.height().ok()?;
                if width == 0 || height == 0 || window.is_minimized().unwrap_or(false) {
                    return None;
                }
                let app = window.app_name().unwrap_or_default();
                let title = window.title().unwrap_or_default();
                let name = match (app.is_empty(), title.is_empty()) {
                    (false, false) => format!("{app} — {title}"),
                    (false, true) => app,
                    (true, false) => title,
                    (true, true) => String::new(),
                };
                Some(AccessibilityNode {
                    id: format!("window-{id}"),
                    parent_id: None,
                    role: "window".into(),
                    name,
                    value: String::new(),
                    x: window.x().unwrap_or_default(),
                    y: window.y().unwrap_or_default(),
                    width,
                    height,
                    focused: window.is_focused().unwrap_or(false),
                    enabled: true,
                })
            })
            .collect();
        Ok(Output::Accessibility(AccessibilityResult {
            source: "window_enumeration",
            truncated,
            nodes,
        }))
    }

    #[cfg(target_os = "linux")]
    fn linux_accessibility(input: &Accessibility) -> Result<AccessibilityResult, String> {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(|error| format!("AT-SPI runtime unavailable: {error}"))?;
        runtime.block_on(linux_accessibility_async(input))
    }

    #[cfg(target_os = "linux")]
    async fn linux_accessibility_async(
        input: &Accessibility,
    ) -> Result<AccessibilityResult, String> {
        use atspi::proxy::{accessible::AccessibleProxy, bus::BusProxy, component::ComponentProxy};
        use atspi::{CoordType, State};
        use std::collections::{HashSet, VecDeque};
        use zbus::proxy::CacheProperties;

        let session_address = env::var("DBUS_SESSION_BUS_ADDRESS")
            .map_err(|_| "AT-SPI session bus is unavailable".to_owned())?;
        let address: zbus::address::Address = session_address
            .as_str()
            .try_into()
            .map_err(|error: zbus::Error| format!("AT-SPI session address is invalid: {error}"))?;
        let session = zbus::connection::Builder::address(address)
            .map_err(|error| format!("AT-SPI session connection failed: {error}"))?
            .build()
            .await
            .map_err(|error| format!("AT-SPI session connection failed: {error}"))?;
        let bus = BusProxy::new(&session)
            .await
            .map_err(|error| format!("AT-SPI bus unavailable: {error}"))?;
        let a11y_address = bus
            .get_address()
            .await
            .map_err(|error| format!("AT-SPI address unavailable: {error}"))?;
        let address: zbus::address::Address = a11y_address
            .as_str()
            .try_into()
            .map_err(|error: zbus::Error| format!("AT-SPI address is invalid: {error}"))?;
        let connection = zbus::connection::Builder::address(address)
            .map_err(|error| format!("AT-SPI connection failed: {error}"))?
            .method_timeout(Duration::from_secs(2))
            .build()
            .await
            .map_err(|error| format!("AT-SPI connection failed: {error}"))?;

        let mut queue = VecDeque::from([(
            "org.a11y.atspi.Registry".to_owned(),
            "/org/a11y/atspi/accessible/root".to_owned(),
            None,
            0usize,
        )]);
        let mut seen = HashSet::new();
        let mut nodes = Vec::with_capacity(input.max_nodes.min(128));
        let mut truncated = false;
        while let Some((bus_name, path, parent_id, depth)) = queue.pop_front() {
            if nodes.len() >= input.max_nodes {
                truncated = true;
                break;
            }
            if !seen.insert((bus_name.clone(), path.clone())) {
                continue;
            }
            let proxy = match AccessibleProxy::builder(&connection)
                .destination(bus_name.clone())
                .and_then(|builder| builder.path(path.clone()))
            {
                Ok(builder) => builder.cache_properties(CacheProperties::No).build().await,
                Err(error) => return Err(format!("AT-SPI element is invalid: {error}")),
            };
            let Ok(proxy) = proxy else {
                continue;
            };
            let role = proxy
                .get_role_name()
                .await
                .unwrap_or_else(|_| "unknown".into());
            let name = if role.to_ascii_lowercase().contains("password") {
                String::new()
            } else {
                proxy.name().await.unwrap_or_default()
            };
            let states = proxy.get_state().await.unwrap_or_default();
            let bounds = match ComponentProxy::builder(&connection)
                .destination(bus_name.clone())
                .and_then(|builder| builder.path(path.clone()))
            {
                Ok(builder) => match builder.cache_properties(CacheProperties::No).build().await {
                    Ok(component) => component.get_extents(CoordType::Screen).await.ok(),
                    Err(_) => None,
                },
                Err(_) => None,
            };
            let id = atspi_node_id(&bus_name, &path);
            let (x, y, width, height) = bounds
                .map(|(x, y, width, height)| (x, y, width.max(0) as u32, height.max(0) as u32))
                .unwrap_or_default();
            nodes.push(AccessibilityNode {
                id: id.clone(),
                parent_id,
                role: bounded_accessibility_text(&role),
                name: bounded_accessibility_text(&name),
                // AT-SPI Text may contain passwords or document contents.
                // The bounded semantic tree intentionally exposes labels only.
                value: String::new(),
                x,
                y,
                width,
                height,
                focused: states.contains(State::Focused),
                enabled: states.contains(State::Enabled),
            });
            let children = proxy.get_children().await.unwrap_or_default();
            if depth + 1 >= input.max_depth {
                truncated |= !children.is_empty();
                continue;
            }
            for child in children {
                let Some(child_bus) = child.name_as_str() else {
                    continue;
                };
                queue.push_back((
                    child_bus.to_owned(),
                    child.path_as_str().to_owned(),
                    Some(id.clone()),
                    depth + 1,
                ));
            }
        }
        if !queue.is_empty() {
            truncated = true;
        }
        if nodes.len() <= 1 {
            return Err("AT-SPI returned no application controls".into());
        }
        Ok(AccessibilityResult {
            source: "linux_atspi",
            truncated,
            nodes,
        })
    }

    #[cfg(target_os = "linux")]
    fn atspi_node_id(bus: &str, path: &str) -> String {
        let digest = Sha256::digest(format!("{bus}\0{path}").as_bytes());
        let suffix = digest[..12]
            .iter()
            .map(|byte| format!("{byte:02x}"))
            .collect::<String>();
        format!("atspi-{suffix}")
    }

    #[cfg(any(target_os = "linux", target_os = "windows"))]
    fn bounded_accessibility_text(value: &str) -> String {
        value
            .chars()
            .filter(|character| !character.is_control())
            .take(512)
            .collect()
    }

    #[cfg(target_os = "windows")]
    fn windows_accessibility(input: &Accessibility) -> Result<AccessibilityResult, String> {
        use std::collections::{HashSet, VecDeque};
        use uiautomation::types::{TreeScope, UIProperty};
        use uiautomation::variants::Value;
        use uiautomation::UIAutomation;

        let automation =
            UIAutomation::new().map_err(|error| format!("UIA unavailable: {error}"))?;
        let walker = automation
            .get_control_view_walker()
            .map_err(|error| format!("UIA tree unavailable: {error}"))?;
        // Fetch the required properties together, rather than making seven
        // synchronous cross-process calls per element. Cache one element only;
        // asking a provider for an unbounded subtree defeats max_nodes.
        let cache = automation
            .create_cache_request()
            .map_err(|error| format!("UIA cache unavailable: {error}"))?;
        cache
            .set_tree_scope(TreeScope::Element)
            .map_err(|e| e.to_string())?;
        for property in [
            UIProperty::RuntimeId,
            UIProperty::BoundingRectangle,
            UIProperty::ControlType,
            UIProperty::Name,
            UIProperty::IsPassword,
            UIProperty::HasKeyboardFocus,
            UIProperty::IsEnabled,
        ] {
            cache.add_property(property).map_err(|e| e.to_string())?;
        }
        let root = automation
            .get_root_element_build_cache(&cache)
            .map_err(|error| format!("UIA desktop unavailable: {error}"))?;
        let mut queue = VecDeque::from([(root, None, 0usize)]);
        let mut seen = HashSet::new();
        let mut nodes = Vec::with_capacity(input.max_nodes.min(128));
        let mut truncated = false;

        while let Some((element, parent_id, depth)) = queue.pop_front() {
            if nodes.len() >= input.max_nodes {
                truncated = true;
                break;
            }
            let runtime_id = match element
                .get_cached_property_value(UIProperty::RuntimeId)
                .and_then(|value| value.get_value())
            {
                Ok(Value::ArrayI4(values)) if values.len() <= 64 => values,
                _ => Vec::new(),
            };
            let id = if runtime_id.is_empty() {
                format!("uia-node-{}", nodes.len())
            } else {
                format!(
                    "uia-{}",
                    runtime_id
                        .iter()
                        .map(i32::to_string)
                        .collect::<Vec<_>>()
                        .join("-")
                )
            };
            if !seen.insert(id.clone()) {
                truncated = true;
                continue;
            }
            let rect = element.get_cached_bounding_rectangle().ok();
            let role = element
                .get_cached_control_type()
                .map(|value| format!("{value:?}").to_ascii_lowercase())
                .unwrap_or_else(|_| "unknown".into());
            let name = if element.is_cached_password().unwrap_or(true) {
                String::new()
            } else {
                bounded_accessibility_text(&element.get_cached_name().unwrap_or_default())
            };
            nodes.push(AccessibilityNode {
                id: id.clone(),
                parent_id,
                role,
                name,
                // Never return UIA ValuePattern text here: it may be a password
                // or another secret. Password element names are redacted too.
                value: String::new(),
                x: rect.map(|value| value.get_left()).unwrap_or_default(),
                y: rect.map(|value| value.get_top()).unwrap_or_default(),
                width: rect
                    .map(|value| value.get_width().max(0) as u32)
                    .unwrap_or_default(),
                height: rect
                    .map(|value| value.get_height().max(0) as u32)
                    .unwrap_or_default(),
                focused: element.has_cached_keyboard_focus().unwrap_or(false),
                enabled: element.is_cached_enabled().unwrap_or(false),
            });
            if depth + 1 >= input.max_depth {
                if walker.get_first_child(&element).is_ok() {
                    truncated = true;
                }
                continue;
            }
            let remaining = input.max_nodes.saturating_sub(nodes.len() + queue.len());
            truncated |= enqueue_windows_children(
                &walker,
                &cache,
                &element,
                (&id, depth + 1),
                remaining,
                &mut queue,
            );
        }
        if !queue.is_empty() {
            truncated = true;
        }
        if nodes.is_empty() {
            return Err("UIA returned an empty control tree".into());
        }
        Ok(AccessibilityResult {
            source: "windows_uia",
            truncated,
            nodes,
        })
    }

    #[cfg(target_os = "windows")]
    fn enqueue_windows_children(
        walker: &uiautomation::UITreeWalker,
        cache: &uiautomation::core::UICacheRequest,
        element: &uiautomation::UIElement,
        parent: (&str, usize),
        remaining: usize,
        queue: &mut std::collections::VecDeque<(uiautomation::UIElement, Option<String>, usize)>,
    ) -> bool {
        if remaining == 0 {
            return true;
        }
        let Ok(mut child) = walker.get_first_child_build_cache(element, cache) else {
            return false;
        };
        for _ in 0..remaining {
            queue.push_back((child.clone(), Some(parent.0.to_owned()), parent.1));
            match walker.get_next_sibling_build_cache(&child, cache) {
                Ok(sibling) => child = sibling,
                Err(_) => return false,
            }
        }
        // Includes a broken provider repeatedly returning the same sibling.
        // The queue is bounded before traversal, not only when emitting nodes.
        true
    }

    fn mouse(input: &super::Mouse) -> Result<(), String> {
        #[cfg(target_os = "windows")]
        if input.action == "move" || input.action == "click" {
            vc_workspace_windows_session::move_cursor_physical(input.x, input.y)
                .map_err(|error| format!("mouse move failed: {error}"))?;
            if input.action == "click" {
                let button = match input.button.as_str() {
                    "left" => uiautomation::inputs::MouseButton::LEFT,
                    "right" => uiautomation::inputs::MouseButton::RIGHT,
                    "middle" => uiautomation::inputs::MouseButton::MIDDLE,
                    _ => return Err("unsupported mouse button".into()),
                };
                uiautomation::inputs::Mouse::new()
                    .move_time(0)
                    .interval(5)
                    .auto_move(false)
                    .click_button(button)
                    .map_err(|error| format!("mouse click failed: {error}"))?;
            }
            return Ok(());
        }

        let mut enigo = Enigo::new(&Settings::default())
            .map_err(|error| format!("input unavailable: {error}"))?;
        match input.action.as_str() {
            "move" => enigo
                .move_mouse(input.x, input.y, Coordinate::Abs)
                .map_err(|error| format!("mouse move failed: {error}")),
            "click" => {
                enigo
                    .move_mouse(input.x, input.y, Coordinate::Abs)
                    .map_err(|error| format!("mouse move failed: {error}"))?;
                let button = match input.button.as_str() {
                    "left" => Button::Left,
                    "right" => Button::Right,
                    "middle" => Button::Middle,
                    _ => return Err("unsupported mouse button".into()),
                };
                enigo
                    .button(button, Direction::Click)
                    .map_err(|error| format!("mouse click failed: {error}"))
            }
            "scroll" => {
                if input.delta_x != 0 {
                    enigo
                        .scroll(input.delta_x, Axis::Horizontal)
                        .map_err(|error| format!("horizontal scroll failed: {error}"))?;
                }
                if input.delta_y != 0 {
                    enigo
                        .scroll(input.delta_y, Axis::Vertical)
                        .map_err(|error| format!("vertical scroll failed: {error}"))?;
                }
                Ok(())
            }
            _ => Err("unsupported mouse action".into()),
        }
    }

    fn key(input: &KeyInput) -> Result<(), String> {
        #[cfg(target_os = "windows")]
        {
            windows_key(input)
        }

        #[cfg(not(target_os = "windows"))]
        {
            let mut enigo = Enigo::new(&Settings::default())
                .map_err(|error| format!("input unavailable: {error}"))?;
            let modifiers = input
                .modifiers
                .iter()
                .map(|value| named_key(value))
                .collect::<Result<Vec<_>, _>>()?;
            for modifier in &modifiers {
                enigo
                    .key(*modifier, Direction::Press)
                    .map_err(|error| format!("modifier press failed: {error}"))?;
            }
            let result = enigo
                .key(named_key(&input.key)?, Direction::Click)
                .map_err(|error| format!("key input failed: {error}"));
            for modifier in modifiers.iter().rev() {
                let _ = enigo.key(*modifier, Direction::Release);
            }
            result
        }
    }

    fn type_text(
        input: &TextInput,
        authority_check: impl Fn() -> Result<(), String>,
    ) -> Result<(), String> {
        #[cfg(target_os = "windows")]
        {
            for chunk in text_chunks(&input.value, 32) {
                authority_check()?;
                vc_workspace_windows_session::send_unicode_text_chunk(&chunk)
                    .map_err(|error| format!("text input failed: {error}"))?;
            }
            let _ = input.sensitive;
            Ok(())
        }

        #[cfg(not(target_os = "windows"))]
        {
            let mut enigo = Enigo::new(&Settings::default())
                .map_err(|error| format!("input unavailable: {error}"))?;
            let mut chunk = String::new();
            for character in input.value.chars() {
                chunk.push(character);
                if chunk.chars().count() >= 32 {
                    authority_check()?;
                    enigo
                        .text(&chunk)
                        .map_err(|error| format!("text input failed: {error}"))?;
                    chunk.clear();
                }
            }
            if !chunk.is_empty() {
                authority_check()?;
                enigo
                    .text(&chunk)
                    .map_err(|error| format!("text input failed: {error}"))?;
            }
            let _ = input.sensitive;
            Ok(())
        }
    }

    #[cfg(target_os = "windows")]
    fn windows_key(input: &KeyInput) -> Result<(), String> {
        vc_workspace_windows_session::send_key_chord(&input.key, &input.modifiers)
            .map_err(|error| format!("key input failed: {error}"))
    }

    #[cfg(target_os = "windows")]
    fn text_chunks(value: &str, size: usize) -> Vec<String> {
        let mut chunks = Vec::new();
        let mut chunk = String::new();
        for character in value.chars() {
            chunk.push(character);
            if chunk.chars().count() >= size {
                chunks.push(std::mem::take(&mut chunk));
            }
        }
        if !chunk.is_empty() {
            chunks.push(chunk);
        }
        chunks
    }

    #[cfg(not(target_os = "windows"))]
    fn named_key(value: &str) -> Result<Key, String> {
        let normalized = value.to_ascii_lowercase();
        match normalized.as_str() {
            "enter" => Ok(Key::Return),
            "escape" => Ok(Key::Escape),
            "tab" => Ok(Key::Tab),
            "backspace" => Ok(Key::Backspace),
            "delete" => Ok(Key::Delete),
            "space" => Ok(Key::Space),
            "up" => Ok(Key::UpArrow),
            "down" => Ok(Key::DownArrow),
            "left" => Ok(Key::LeftArrow),
            "right" => Ok(Key::RightArrow),
            "home" => Ok(Key::Home),
            "end" => Ok(Key::End),
            "page_up" => Ok(Key::PageUp),
            "page_down" => Ok(Key::PageDown),
            "f1" => Ok(Key::F1),
            "f2" => Ok(Key::F2),
            "f3" => Ok(Key::F3),
            "f4" => Ok(Key::F4),
            "f5" => Ok(Key::F5),
            "f6" => Ok(Key::F6),
            "f7" => Ok(Key::F7),
            "f8" => Ok(Key::F8),
            "f9" => Ok(Key::F9),
            "f10" => Ok(Key::F10),
            "f11" => Ok(Key::F11),
            "f12" => Ok(Key::F12),
            "control" => Ok(Key::Control),
            "alt" => Ok(Key::Alt),
            "shift" => Ok(Key::Shift),
            "meta" => Ok(Key::Meta),
            _ if normalized.len() == 1 && normalized.as_bytes()[0].is_ascii_alphanumeric() => Ok(
                Key::Unicode(normalized.chars().next().expect("one character")),
            ),
            _ => Err("unsupported key".into()),
        }
    }
}

#[cfg(not(any(target_os = "linux", target_os = "windows")))]
mod platform {
    use super::*;

    pub(super) fn execute(
        _request: &Request,
        _authority_check: impl Fn() -> Result<(), String>,
    ) -> Result<Output, String> {
        Err("Computer Use helper is supported only on Linux and Windows guests".into())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn request() -> Request {
        Request {
            schema_version: SCHEMA_VERSION,
            request_id: "action_abcdefghijklmnopqrstuvwxyz".into(),
            lease_id: "lease_abcdefghijklmnopqrstuvwxyz".into(),
            control_epoch: 1,
            expires_unix_ms: unix_millis() + 5_000,
            operation: "type_text".into(),
            screenshot: None,
            accessibility: None,
            mouse: None,
            key: None,
            text: Some(TextInput {
                value: "hello".into(),
                sensitive: false,
            }),
        }
    }

    #[test]
    fn request_validation_rejects_stale_and_ambiguous_payloads() {
        let mut value = request();
        assert!(validate_request(&value.request_id, &value).is_ok());
        value.expires_unix_ms = unix_millis() - 1;
        assert!(validate_request(&value.request_id, &value).is_err());
        value.expires_unix_ms = unix_millis() + 5_000;
        value.key = Some(KeyInput {
            key: "enter".into(),
            modifiers: Vec::new(),
        });
        assert!(validate_request(&value.request_id, &value).is_err());
    }

    #[test]
    fn request_id_cannot_escape_the_spool_directory() {
        assert!(valid_request_id("action_abcdefghijklmnopqrstuvwxyz"));
        assert!(!valid_request_id("action_../../authority"));
        assert!(!valid_request_id("short"));
    }

    #[test]
    fn authority_must_match_lease_and_epoch() {
        let root = env::temp_dir().join(format!("vc-workspace-computer-test-{}", unix_millis()));
        let paths = ComputerPaths::new(&root);
        fs::create_dir_all(paths.authority.parent().unwrap()).unwrap();
        let value = request();
        let authority = serde_json::json!({
            "schema_version": 1,
            "lease_id": value.lease_id,
            "control_epoch": value.control_epoch,
            "state": "active",
            "expires_unix_ms": unix_millis() + 5_000
        });
        fs::write(&paths.authority, serde_json::to_vec(&authority).unwrap()).unwrap();
        assert!(validate_authority(&paths, &value).is_ok());
        let revoked = serde_json::json!({
            "schema_version": 1,
            "lease_id": value.lease_id,
            "control_epoch": value.control_epoch + 1,
            "state": "revoked",
            "expires_unix_ms": unix_millis()
        });
        fs::write(&paths.authority, serde_json::to_vec(&revoked).unwrap()).unwrap();
        assert!(validate_authority(&paths, &value).is_err());
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn staged_request_is_published_atomically_and_retries_are_idempotent() {
        let root = env::temp_dir().join(format!("vc-workspace-dispatch-test-{}", unix_millis()));
        let paths = ComputerPaths::new(&root);
        fs::create_dir_all(&paths.requests).unwrap();
        let request_id = "action_abcdefghijklmnopqrstuvwxyz";
        fs::write(paths.staged_request(request_id), b"request").unwrap();
        publish_staged_request(&paths, request_id).unwrap();
        assert_eq!(fs::read(paths.request(request_id)).unwrap(), b"request");
        publish_staged_request(&paths, request_id).unwrap();
        fs::remove_dir_all(root).unwrap();
    }
}
