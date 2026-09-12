//! Session-bound Computer Use. The root dispatcher and unprivileged helper
//! mutually authenticate Unix peers; root never reads user-controlled files.
#![cfg_attr(not(target_os = "linux"), allow(dead_code))]
use super::*;
use std::collections::BTreeMap;
use std::io::Read;
#[cfg(target_os = "linux")]
mod account;
#[cfg(target_os = "linux")]
pub(crate) use account::reconcile_expired as reconcile_accounts;
#[cfg(target_os = "linux")]
mod linux_authority;
#[cfg(any(target_os = "windows", test))]
mod windows;

const VERSION: u8 = 2;
const MAX_REQUEST: usize = 64 * 1024;

// PVE decodes QGA stdout from base64 into a byte scalar before JSON encoding.
// Literal UTF-8 can consequently arrive as Latin-1-shaped code points. Emit
// ASCII-only JSON on this boundary; JSON escapes preserve all Unicode exactly
// without guessing whether a caller intentionally used mojibake-like text.
fn qga_json(value: &impl Serialize) -> io::Result<String> {
    use std::fmt::Write as _;
    let raw = serde_json::to_string(value).map_err(io::Error::other)?;
    if raw.len() > MAX_RESPONSE_BYTES {
        return Err(denied("QGA response exceeds limit"));
    }
    let mut ascii = String::with_capacity(raw.len());
    for character in raw.chars() {
        if character.is_ascii() {
            ascii.push(character);
        } else {
            let mut units = [0u16; 2];
            for unit in character.encode_utf16(&mut units) {
                write!(ascii, "\\u{unit:04x}").expect("writing to a string cannot fail");
            }
        }
        if ascii.len() > MAX_RESPONSE_BYTES {
            return Err(denied("escaped QGA response exceeds limit"));
        }
    }
    Ok(ascii)
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq)]
#[serde(deny_unknown_fields)]
struct SessionKey {
    username: String,
    uid: u32,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    sid: String,
    session_id: String,
    instance_id: String,
}

#[derive(Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct BoundAction {
    schema_version: u8,
    target: SessionKey,
    timeout_ms: u64,
    request: serde_json::Value,
}

#[derive(Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct BoundAuthority {
    schema_version: u8,
    target: Option<SessionKey>,
    authority: serde_json::Value,
    #[cfg(target_os = "linux")]
    #[serde(default, skip_serializing_if = "Option::is_none")]
    login_generation: Option<u64>,
}

fn managed_name(value: &str) -> bool {
    value.len() == 15
        && (value.starts_with("vca") || value.starts_with("vcw"))
        && value[3..]
            .bytes()
            .all(|b| b.is_ascii_hexdigit() && !b.is_ascii_uppercase())
}

fn valid_key(key: &SessionKey) -> bool {
    managed_name(&key.username)
        && valid_os_identity(key)
        && !key.session_id.is_empty()
        && key.session_id.len() <= 128
        && key
            .session_id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"_:.-".contains(&b))
        && key.instance_id.len() == 64
        && key
            .instance_id
            .bytes()
            .all(|b| b.is_ascii_hexdigit() && !b.is_ascii_uppercase())
}

fn valid_os_identity(key: &SessionKey) -> bool {
    if let Some(rest) = key.session_id.strip_prefix("windows:") {
        let parts: Vec<_> = rest.split(':').collect();
        return key.uid == 0
            && key.sid != "S-1-5-18"
            && vc_workspace_windows_session::valid_account_sid(&key.sid)
            && parts.len() == 2
            && parts[0]
                .parse::<u32>()
                .is_ok_and(|id| id > 0 && id.to_string() == parts[0])
            && parts[1].len() == 16
            && parts[1]
                .bytes()
                .all(|b| b.is_ascii_hexdigit() && !b.is_ascii_uppercase())
            && parts[1] != "0000000000000000";
    }
    let prefix = format!("linux:{}:", key.uid);
    if let Some(rest) = key.session_id.strip_prefix(&prefix) {
        if let Some((pid, display)) = rest.split_once(':') {
            return key.sid.is_empty()
                && key.uid >= 1000
                && pid.parse::<u32>().is_ok_and(|pid| pid > 0)
                && local_display(display);
        }
    }
    false
}

fn local_display(value: &str) -> bool {
    let Some(number) = value.strip_prefix(':') else {
        return false;
    };
    let parts: Vec<_> = number.split('.').collect();
    value.len() <= 16
        && (1..=2).contains(&parts.len())
        && parts
            .iter()
            .all(|part| !part.is_empty() && part.bytes().all(|b| b.is_ascii_digit()))
}

fn denied(message: &str) -> io::Error {
    io::Error::new(io::ErrorKind::PermissionDenied, message)
}

fn encode_frame(stream: &mut impl Write, value: &impl Serialize) -> io::Result<()> {
    let data = serde_json::to_vec(value).map_err(io::Error::other)?;
    if data.len() > MAX_RESPONSE_BYTES {
        return Err(io::Error::other("response exceeds limit"));
    }
    stream.write_all(&(data.len() as u32).to_be_bytes())?;
    stream.write_all(&data)
}

fn decode_frame(stream: &mut impl Read, maximum: usize) -> io::Result<serde_json::Value> {
    let mut size = [0u8; 4];
    stream.read_exact(&mut size)?;
    let size = u32::from_be_bytes(size) as usize;
    if size == 0 || size > maximum {
        return Err(io::Error::other("IPC frame exceeds limit"));
    }
    let mut raw = vec![0; size];
    stream.read_exact(&mut raw)?;
    serde_json::from_slice(&raw).map_err(io::Error::other)
}

struct CachedReply {
    fingerprint: Vec<u8>,
    expires: i64,
    result: Option<serde_json::Value>,
    bytes: usize,
}

#[derive(Default)]
struct ReplayCache(BTreeMap<String, CachedReply>);

impl ReplayCache {
    // A retry can never be valid past its original deadline. Pruning only
    // expired identifiers preserves at-most-once input without an ever-growing
    // cache or forcing users to restart their desktop after 128 actions.
    fn begin(
        &mut self,
        id: &str,
        fingerprint: Vec<u8>,
        expires: i64,
        now: i64,
    ) -> Result<Option<serde_json::Value>, &'static str> {
        self.0.retain(|_, entry| entry.expires > now);
        if expires <= now {
            return Err("request has expired");
        }
        if let Some(entry) = self.0.get(id) {
            if entry.fingerprint != fingerprint {
                return Err("request identifier was reused");
            }
            return entry
                .result
                .clone()
                .map(Some)
                .ok_or("action already attempted; result unavailable");
        }
        if self.0.len() >= 128 {
            return Err("too many unexpired actions; retry later");
        }
        self.0.insert(
            id.into(),
            CachedReply {
                fingerprint,
                expires,
                result: None,
                bytes: 0,
            },
        );
        Ok(None)
    }

    fn complete(&mut self, id: &str, result: &serde_json::Value) {
        const MAX_CACHED_BYTES: usize = 32 * 1024 * 1024;
        let bytes = serde_json::to_vec(result)
            .map(|raw| raw.len())
            .unwrap_or(usize::MAX);
        let used: usize = self.0.values().map(|entry| entry.bytes).sum();
        if bytes <= MAX_CACHED_BYTES.saturating_sub(used) {
            if let Some(entry) = self.0.get_mut(id) {
                entry.result = Some(result.clone());
                entry.bytes = bytes;
            }
        }
        // If an observation is too large to cache, retain the reservation.
        // Retrying must never re-execute an input whose result was lost.
    }
}

pub(super) fn run_cli(arguments: &[String]) -> Option<ExitCode> {
    let command = arguments.first()?;
    if !command.starts_with("computer-v2-") {
        return None;
    }
    #[cfg(target_os = "linux")]
    let result = linux::run(command, &arguments[1..]);
    #[cfg(target_os = "windows")]
    let result = windows::run(command, &arguments[1..]);
    #[cfg(not(any(target_os = "linux", target_os = "windows")))]
    let result: io::Result<()> = Err(io::Error::new(
        io::ErrorKind::Unsupported,
        "session-bound transport is not implemented on this platform",
    ));
    Some(match result {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => {
            eprintln!(
                "vc-workspace-guest-agent: {}",
                sanitize_error(&error.to_string())
            );
            ExitCode::FAILURE
        }
    })
}

#[cfg(target_os = "linux")]
mod linux {
    use super::*;
    use rustix::{
        fs::{flock, FlockOperation, Mode, OFlags},
        net::sockopt::socket_peercred,
        process::{geteuid, getsid, kill_process_group, Pid, Signal},
    };
    use std::{
        os::unix::{
            fs::{MetadataExt, OpenOptionsExt, PermissionsExt},
            net::{UnixListener, UnixStream},
            process::CommandExt,
        },
        sync::mpsc,
    };

    pub(super) const BASE: &str = "/var/lib/vc-workspace/computer-v2";

    pub(super) fn initialize(username: &str) -> io::Result<()> {
        require_root()?;
        let uid = user_uid(username)?;
        initialize_control()?;
        root_directory(&format!("{BASE}/users/{username}"), 0o755)?;
        let runtime = Path::new(BASE).join("users").join(username).join("runtime");
        match fs::create_dir(&runtime) {
            Ok(()) => {
                fs::set_permissions(&runtime, fs::Permissions::from_mode(0o700))?;
                rustix::fs::chown(&runtime, Some(rustix::process::Uid::from_raw(uid)), None)?;
            }
            Err(error) if error.kind() == io::ErrorKind::AlreadyExists => (),
            Err(error) => return Err(error),
        }
        let meta = fs::symlink_metadata(&runtime)?;
        if !meta.is_dir() || meta.uid() != uid || meta.mode() & 0o077 != 0 {
            return Err(denied("runtime must be private to the target UID"));
        }
        Ok(())
    }

    pub(super) fn initialize_control() -> io::Result<()> {
        require_root()?;
        for directory in [
            "/var/lib/vc-workspace".to_owned(),
            BASE.to_owned(),
            format!("{BASE}/inbox"),
            format!("{BASE}/users"),
        ] {
            root_directory(
                &directory,
                if directory.ends_with("/inbox") {
                    0o700
                } else {
                    0o755
                },
            )?;
        }
        prune_inbox()
    }

    fn prune_inbox() -> io::Result<()> {
        // Crash leftovers contain input text. Only this root-owned private
        // inbox is scanned; user directories and response paths are never read.
        let inbox = Path::new(BASE).join("inbox");
        trusted_root_path(&inbox)?;
        for item in fs::read_dir(&inbox)?.take(1024) {
            let item = item?;
            let name = item.file_name();
            let Some(name) = name.to_str() else { continue };
            let Some(id) = name
                .strip_suffix(".json.tmp")
                .or_else(|| name.strip_suffix(".json"))
            else {
                continue;
            };
            if !valid_request_id(id) {
                continue;
            }
            let meta = fs::symlink_metadata(item.path())?;
            if !meta.is_file() || meta.uid() != 0 {
                continue;
            }
            let raw = root_file(&item.path(), MAX_REQUEST)?;
            let expired = serde_json::from_slice::<BoundAction>(&raw).is_ok_and(|action| {
                action.request["expires_unix_ms"]
                    .as_i64()
                    .is_some_and(|deadline| deadline <= unix_millis())
            });
            let abandoned =
                meta.modified()?.elapsed().unwrap_or_default() > Duration::from_secs(60);
            if expired || abandoned {
                fs::remove_file(item.path())?;
            }
        }
        Ok(())
    }

    pub(super) fn root_directory(directory: &str, mode: u32) -> io::Result<()> {
        trusted_root_path(
            Path::new(directory)
                .parent()
                .ok_or_else(|| denied("invalid directory"))?,
        )?;
        match fs::create_dir(directory) {
            Ok(()) => fs::set_permissions(directory, fs::Permissions::from_mode(mode))?,
            Err(error) if error.kind() == io::ErrorKind::AlreadyExists => (),
            Err(error) => return Err(error),
        }
        trusted_root_path(Path::new(directory))?;
        if fs::metadata(directory)?.mode() & 0o777 != mode {
            return Err(denied("unexpected control directory permissions"));
        }
        Ok(())
    }

    pub(super) fn user_uid(username: &str) -> io::Result<u32> {
        if !managed_name(username) {
            return Err(denied("managed username required"));
        }
        let output = Command::new("/usr/bin/getent")
            .args(["-s", "files", "passwd", username])
            .output()?;
        let text = String::from_utf8(output.stdout).map_err(io::Error::other)?;
        let parts: Vec<_> = text.trim().split(':').collect();
        if !output.status.success() || parts.len() != 7 || parts[0] != username {
            return Err(denied("local managed account required"));
        }
        let uid: u32 = parts[2].parse().map_err(io::Error::other)?;
        if uid < 1000 {
            return Err(denied("system account cannot host Computer Use"));
        }
        Ok(uid)
    }

    pub(super) fn require_root() -> io::Result<()> {
        if !geteuid().is_root() {
            return Err(denied("system dispatcher requires root"));
        }
        Ok(())
    }

    pub(super) fn trusted_root_path(path: &Path) -> io::Result<()> {
        for component in path.ancestors() {
            let meta = fs::symlink_metadata(component)?;
            if !meta.is_dir() || meta.uid() != 0 || meta.mode() & 0o022 != 0 {
                return Err(denied(
                    "control directory must be root-owned and not writable by users",
                ));
            }
        }
        Ok(())
    }

    pub(super) fn root_file(path: &Path, maximum: usize) -> io::Result<Vec<u8>> {
        trusted_root_path(path.parent().ok_or_else(|| denied("invalid root file"))?)?;
        let fd = rustix::fs::open(
            path,
            OFlags::RDONLY | OFlags::NOFOLLOW | OFlags::NONBLOCK | OFlags::CLOEXEC,
            Mode::empty(),
        )?;
        let file = fs::File::from(fd);
        let meta = file.metadata()?;
        if !meta.is_file()
            || meta.uid() != 0
            || meta.mode() & 0o022 != 0
            || meta.len() > maximum as u64
        {
            return Err(denied("invalid root-owned input file"));
        }
        let mut raw = Vec::new();
        file.take(maximum as u64 + 1).read_to_end(&mut raw)?;
        if raw.len() > maximum {
            return Err(denied("input exceeds limit"));
        }
        Ok(raw)
    }

    fn stage_file(path: &Path) -> io::Result<()> {
        require_root()?;
        trusted_root_path(
            path.parent()
                .ok_or_else(|| denied("invalid staging path"))?,
        )?;
        match fs::symlink_metadata(path) {
            Ok(meta) => {
                if !meta.is_file() || meta.uid() != 0 {
                    return Err(denied("unsafe staging file"));
                }
                // Replace the inode rather than chmod an old QGA-created
                // writable file that another UID might still have open.
                fs::remove_file(path)?;
            }
            Err(error) if error.kind() == io::ErrorKind::NotFound => (),
            Err(error) => return Err(error),
        }
        fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(path)?
            .sync_all()
    }

    fn socket_path(username: &str) -> PathBuf {
        Path::new(BASE)
            .join("users")
            .join(username)
            .join("runtime/helper.sock")
    }

    fn connect(username: &str) -> io::Result<UnixStream> {
        require_root()?;
        let expected = user_uid(username)?;
        trusted_root_path(&Path::new(BASE).join("users").join(username))?;
        let stream = UnixStream::connect(socket_path(username))?;
        if socket_peercred(&stream)?.uid.as_raw() != expected {
            return Err(denied("Helper UID does not match target account"));
        }
        stream.set_read_timeout(Some(Duration::from_secs(18)))?;
        stream.set_write_timeout(Some(Duration::from_secs(2)))?;
        Ok(stream)
    }

    pub(super) fn discover_target(username: &str) -> io::Result<SessionKey> {
        let mut stream = connect(username)?;
        stream.set_read_timeout(Some(Duration::from_secs(3)))?;
        encode_frame(&mut stream, &serde_json::json!({"kind":"session"}))?;
        let response = decode_frame(&mut stream, MAX_REQUEST)?;
        let key: SessionKey =
            serde_json::from_value(response["target"].clone()).map_err(io::Error::other)?;
        if response["schema_version"] != VERSION
            || response["authority_transport"] != linux_authority::TRANSPORT
            || !valid_key(&key)
            || key.username != username
            || key.uid != user_uid(username)?
            || !key.sid.is_empty()
        {
            return Err(denied("Helper identity or fencing protocol mismatch"));
        }
        Ok(key)
    }

    fn session_id() -> io::Result<String> {
        let display = env::var("DISPLAY").map_err(|_| denied("an Xorg display is required"))?;
        if !local_display(&display) {
            return Err(denied("local Xorg display required"));
        }
        let sid = getsid(None)?;
        Ok(format!(
            "linux:{}:{}:{}",
            geteuid().as_raw(),
            sid.as_raw_nonzero(),
            display
        ))
    }

    fn current_key(username: &str, instance: String) -> io::Result<SessionKey> {
        let uid = user_uid(username)?;
        if geteuid().as_raw() != uid {
            return Err(denied("Helper must run as its target OS account"));
        }
        let key = SessionKey {
            username: username.into(),
            uid,
            sid: String::new(),
            session_id: session_id()?,
            instance_id: instance,
        };
        if !valid_key(&key) {
            return Err(denied("invalid interactive session identity"));
        }
        Ok(key)
    }

    fn validate_binding(key: &SessionKey, action: &BoundAction) -> Result<Request, String> {
        if action.schema_version != VERSION
            || &action.target != key
            || !valid_key(key)
            || !(250..=15_000).contains(&action.timeout_ms)
        {
            return Err("interactive session binding has changed".into());
        }
        let request: Request =
            serde_json::from_value(action.request.clone()).map_err(|_| "invalid action request")?;
        validate_request(&request.request_id, &request).map_err(str::to_owned)?;
        if request.expires_unix_ms > unix_millis() + 20_000 {
            return Err("action deadline exceeds limit".into());
        }
        Ok(request)
    }

    fn validate_authority(key: &SessionKey, request: &Request) -> Result<(), String> {
        let raw = root_file(
            &Path::new(BASE).join(linux_authority::SNAPSHOT),
            MAX_REQUEST,
        )
        .map_err(|_| "root-owned authority unavailable")?;
        let bound: BoundAuthority =
            serde_json::from_slice(&raw).map_err(|_| "invalid authority")?;
        let authority: Authority =
            serde_json::from_value(bound.authority).map_err(|_| "invalid authority")?;
        if bound.schema_version != VERSION
            || bound.target.as_ref() != Some(key)
            || authority.schema_version != SCHEMA_VERSION
            || authority.state != "active"
            || authority.lease_id != request.lease_id
            || authority.control_epoch != request.control_epoch
            || authority.expires_unix_ms <= unix_millis()
            || request.expires_unix_ms <= unix_millis()
        {
            return Err("desktop control authority has changed".into());
        }
        super::account::validate_action_account(key, &authority, bound.login_generation)
            .map_err(|_| "Agent account login authority has changed".to_string())?;
        Ok(())
    }

    fn worker(username: &str, instance: &str) -> io::Result<()> {
        let key = current_key(username, instance.into())?;
        let mut raw = Vec::new();
        io::stdin()
            .take(MAX_REQUEST as u64 + 1)
            .read_to_end(&mut raw)?;
        if raw.len() > MAX_REQUEST {
            return Err(denied("request exceeds limit"));
        }
        let action: BoundAction = serde_json::from_slice(&raw).map_err(io::Error::other)?;
        let request = validate_binding(&key, &action).map_err(io::Error::other)?;
        let result = validate_authority(&key, &request)
            .and_then(|()| platform::execute(&request, || validate_authority(&key, &request)));
        let response = match result {
            Ok(output) => {
                validate_authority(&key, &request).map_err(io::Error::other)?;
                let mut response = failure(&request.request_id, "");
                response.ok = true;
                response.error = None;
                match output {
                    Output::Screenshot(value) => response.screenshot = Some(value),
                    Output::Accessibility(value) => response.accessibility = Some(value),
                    Output::Input => response.input = Some(InputResult { applied: true }),
                }
                response
            }
            Err(error) => failure(&request.request_id, &error),
        };
        encode_frame(&mut io::stdout(), &response)
    }

    fn isolated_action(key: &SessionKey, action: &BoundAction) -> io::Result<serde_json::Value> {
        let request = validate_binding(key, action).map_err(io::Error::other)?;
        validate_authority(key, &request).map_err(io::Error::other)?;
        let payload = serde_json::to_vec(action).map_err(io::Error::other)?;
        let mut child = Worker(
            Command::new(env::current_exe()?)
                .args([
                    "computer-v2-worker",
                    "--guest-user",
                    &key.username,
                    "--instance-id",
                    &key.instance_id,
                ])
                .stdin(Stdio::piped())
                .stdout(Stdio::piped())
                .stderr(Stdio::null())
                .process_group(0)
                .spawn()?,
        );
        let stdout = child
            .0
            .stdout
            .take()
            .ok_or_else(|| io::Error::other("worker stdout missing"))?;
        let (sender, receiver) = mpsc::sync_channel(1);
        thread::spawn(move || {
            let _ = sender.send(decode_frame(
                &mut io::BufReader::new(stdout),
                MAX_RESPONSE_BYTES,
            ));
        });
        if let Some(mut input) = child.0.stdin.take() {
            input.write_all(&payload)?;
        }
        let remaining = request
            .expires_unix_ms
            .saturating_sub(unix_millis())
            .clamp(1, action.timeout_ms as i64) as u64;
        let deadline = Instant::now() + Duration::from_millis(remaining);
        let success = loop {
            if let Some(status) = child.0.try_wait()? {
                break status.success();
            }
            if Instant::now() >= deadline {
                break false;
            }
            thread::sleep(Duration::from_millis(10));
        };
        // Also stop descendants after successful exit: a background child
        // retaining stdout cannot pin the Helper's only dispatch loop.
        drop(child);
        if !success {
            return Err(io::Error::other("session action failed or timed out"));
        }
        let result = receiver
            .recv_timeout(Duration::from_millis(250))
            .map_err(|_| io::Error::other("worker output reader failed"))?;
        validate_authority(key, &request).map_err(io::Error::other)?;
        result
    }

    struct Worker(std::process::Child);
    impl Drop for Worker {
        fn drop(&mut self) {
            if let Some(pid) = Pid::from_raw(self.0.id() as i32) {
                let _ = kill_process_group(pid, Signal::KILL);
            }
            let _ = self.0.kill();
            let _ = self.0.wait();
        }
    }

    fn helper(username: &str) -> io::Result<()> {
        trusted_root_path(&Path::new(BASE).join("users").join(username))?;
        let mut nonce = [0u8; 32];
        getrandom::fill(&mut nonce).map_err(io::Error::other)?;
        let instance = nonce.iter().map(|b| format!("{b:02x}")).collect();
        let key = current_key(username, instance)?;
        let socket = socket_path(username);
        let runtime = socket.parent().ok_or_else(|| denied("invalid runtime"))?;
        let meta = fs::symlink_metadata(runtime)?;
        if !meta.is_dir() || meta.uid() != key.uid || meta.mode() & 0o077 != 0 {
            return Err(denied("runtime must be private to the target UID"));
        }
        let lock = fs::OpenOptions::new()
            .read(true)
            .write(true)
            .create(true)
            .truncate(false)
            .open(runtime.join("helper.lock"))?;
        flock(&lock, FlockOperation::NonBlockingLockExclusive)?;
        if socket.exists() {
            fs::remove_file(&socket)?;
        }
        let listener = UnixListener::bind(&socket)?;
        fs::set_permissions(&socket, fs::Permissions::from_mode(0o600))?;
        // Results are scoped to this Helper incarnation. Bounded in-memory
        // replay prevents a retried QGA dispatch from repeating an input.
        let mut replies = ReplayCache::default();
        for incoming in listener.incoming() {
            let mut stream = match incoming {
                Ok(value) => value,
                Err(_) => continue,
            };
            if !socket_peercred(&stream)
                .map(|value| value.uid.is_root())
                .unwrap_or(false)
            {
                continue;
            }
            let _ = stream.set_read_timeout(Some(Duration::from_secs(2)));
            let _ = stream.set_write_timeout(Some(Duration::from_secs(2)));
            let message = match decode_frame(&mut stream, MAX_REQUEST) {
                Ok(value) => value,
                Err(_) => continue,
            };
            let response = if message["kind"] == "session" {
                serde_json::json!({"schema_version":VERSION,"target":key,"authority_transport":linux_authority::TRANSPORT})
            } else if message["kind"] == "action" {
                let action: BoundAction = match serde_json::from_value(message["action"].clone()) {
                    Ok(value) => value,
                    Err(_) => continue,
                };
                let request = match validate_binding(&key, &action) {
                    Ok(value) => value,
                    Err(_) => continue,
                };
                // Always authorize before replaying even a previously valid observation.
                if validate_authority(&key, &request).is_err() {
                    let _ = encode_frame(
                        &mut stream,
                        &failure(&request.request_id, "desktop control authority has changed"),
                    );
                    continue;
                }
                let fingerprint =
                    Sha256::digest(serde_json::to_vec(&action).map_err(io::Error::other)?).to_vec();
                match replies.begin(
                    &request.request_id,
                    fingerprint,
                    request.expires_unix_ms,
                    unix_millis(),
                ) {
                    Ok(Some(result)) => result,
                    Ok(None) => {
                        let result = isolated_action(&key, &action).unwrap_or_else(|_| {
                            serde_json::to_value(failure(
                                &request.request_id,
                                "session action failed or timed out",
                            ))
                            .unwrap()
                        });
                        replies.complete(&request.request_id, &result);
                        result
                    }
                    Err(error) => serde_json::to_value(failure(&request.request_id, error))
                        .map_err(io::Error::other)?,
                }
            } else {
                continue;
            };
            let _ = encode_frame(&mut stream, &response);
        }
        Ok(())
    }

    pub(super) fn run(command: &str, arguments: &[String]) -> io::Result<()> {
        if command == "computer-v2-login-client" {
            return super::account::login_client(arguments);
        }
        let mut username = "";
        let mut request_id = "";
        let mut instance = "";
        if arguments.len() % 2 != 0 {
            return Err(denied("command options require values"));
        }
        for pair in arguments.chunks(2) {
            match pair[0].as_str() {
                "--guest-user" if username.is_empty() => username = &pair[1],
                "--request-id" if request_id.is_empty() => request_id = &pair[1],
                "--instance-id" if instance.is_empty() => instance = &pair[1],
                _ => return Err(denied("unknown command option")),
            }
        }
        if command == "computer-v2-capabilities" {
            if !arguments.is_empty() {
                return Err(denied("capabilities takes no options"));
            }
            let birth = if super::account::login_birth_configured() {
                "pam_logind_jobs_v3"
            } else {
                "unavailable"
            };
            let native_birth = if super::account::native_login_configured() {
                "pam_logind_native_v1"
            } else {
                "unavailable"
            };
            println!("{{\"schema_version\":2,\"session_transport\":\"unix_peercred\",\"authority_transport\":\"stdin_epoch_account_v2\",\"experimental_account_fence\":\"lease_epoch_login_generation_v1\",\"login_birth_fence\":\"{birth}\",\"native_account_fence\":\"credential_revision_v1\",\"native_login_birth_fence\":\"{native_birth}\"}}");
            return Ok(());
        }
        if command == "computer-v2-pam-policy" && arguments.is_empty() {
            print!("{}", super::account::PAM_PREFIX);
            return Ok(());
        }
        if command == "computer-v2-pam-domain" && arguments.is_empty() {
            return super::account::register_login();
        }
        if command == "computer-v2-native-pam-domain" && arguments.is_empty() {
            return super::account::native_pam_register();
        }
        if command == "computer-v2-native-pam-policy" && arguments.is_empty() {
            print!("{}", super::account::native_pam_policy());
            return Ok(());
        }
        if command == "computer-v2-pam-register" {
            return Err(denied(
                "legacy PAM registration retired; offline Guest upgrade required",
            ));
        }
        if command == "computer-v2-control-init" && arguments.is_empty() {
            return initialize_control();
        }
        if command == "computer-v2-accounts-reconcile" && arguments.is_empty() {
            return super::account::reconcile_expired();
        }
        if command == "computer-v2-authority-stage" && arguments.is_empty() {
            return Err(denied(
                "shared authority staging is retired; upgrade the control plane",
            ));
        }
        if command == "computer-v2-authority" && arguments.is_empty() {
            return linux_authority::publish();
        }
        if !managed_name(username) {
            return Err(denied("managed guest account required"));
        }
        let expected_options = match command {
            "computer-v2-init"
            | "computer-v2-helper"
            | "computer-v2-session"
            | "computer-v2-account-provision"
            | "computer-v2-account-lease"
            | "computer-v2-account-login"
            | "computer-v2-account-inspect"
            | "computer-v2-account-enable"
            | "computer-v2-account-disable" => 2,
            "computer-v2-native-account-provision"
            | "computer-v2-native-account-inspect"
            | "computer-v2-native-account-credential" => 2,
            "computer-v2-worker" if request_id.is_empty() && !instance.is_empty() => 4,
            "computer-v2-stage"
            | "computer-v2-publish"
            | "computer-v2-dispatch"
            | "computer-v2-cleanup"
                if instance.is_empty() && !request_id.is_empty() =>
            {
                4
            }
            _ => return Err(denied("invalid session command options")),
        };
        if arguments.len() != expected_options {
            return Err(denied("invalid session command options"));
        }
        match command {
            "computer-v2-native-account-provision"
            | "computer-v2-native-account-inspect"
            | "computer-v2-native-account-credential" => {
                super::account::native_run(command, username)
            }
            "computer-v2-account-provision"
            | "computer-v2-account-lease"
            | "computer-v2-account-login"
            | "computer-v2-account-inspect"
            | "computer-v2-account-enable"
            | "computer-v2-account-disable" => super::account::run(command, username),
            "computer-v2-init" => initialize(username),
            "computer-v2-helper" => helper(username),
            "computer-v2-worker" => worker(username, instance),
            "computer-v2-stage" => {
                if !valid_request_id(request_id) {
                    return Err(denied("invalid request identifier"));
                }
                user_uid(username)?;
                stage_file(
                    &Path::new(BASE)
                        .join("inbox")
                        .join(format!("{request_id}.json.tmp")),
                )
            }
            "computer-v2-session" => {
                let key = discover_target(username)?;
                println!(
                    "{}",
                    qga_json(
                        &serde_json::json!({"schema_version":VERSION,"target":key,"authority_transport":linux_authority::TRANSPORT})
                    )?
                );
                Ok(())
            }
            "computer-v2-publish" => {
                require_root()?;
                if !valid_request_id(request_id) {
                    return Err(denied("invalid request ID"));
                }
                let path = Path::new(BASE)
                    .join("inbox")
                    .join(format!("{request_id}.json"));
                let temporary = path.with_extension("json.tmp");
                let raw = root_file(&temporary, MAX_REQUEST)?;
                let action: BoundAction = serde_json::from_slice(&raw).map_err(io::Error::other)?;
                if action.target.username != username || action.request["request_id"] != request_id
                {
                    return Err(denied("publish target mismatch"));
                }
                validate_binding(&action.target, &action).map_err(io::Error::other)?;
                if path.exists() {
                    if root_file(&path, MAX_REQUEST)? != raw {
                        return Err(denied("request identifier was reused"));
                    }
                    return fs::remove_file(temporary);
                }
                fs::set_permissions(&temporary, fs::Permissions::from_mode(0o600))?;
                fs::rename(temporary, path)
            }
            "computer-v2-dispatch" => {
                require_root()?;
                if !valid_request_id(request_id) {
                    return Err(denied("invalid request ID"));
                }
                let path = Path::new(BASE)
                    .join("inbox")
                    .join(format!("{request_id}.json"));
                let raw = root_file(&path, MAX_REQUEST)?;
                let action: BoundAction = serde_json::from_slice(&raw).map_err(io::Error::other)?;
                if action.target.username != username || action.request["request_id"] != request_id
                {
                    return Err(denied("dispatch target mismatch"));
                }
                let mut stream = connect(username)?;
                encode_frame(
                    &mut stream,
                    &serde_json::json!({"kind":"action","action":action}),
                )?;
                let response = decode_frame(&mut stream, MAX_RESPONSE_BYTES)?;
                println!("{}", qga_json(&response)?);
                // Keep the immutable request until explicit cleanup. A lost
                // QGA result can retry the same dispatch without repeating input.
                Ok(())
            }
            "computer-v2-cleanup" => {
                require_root()?;
                if !valid_request_id(request_id) {
                    return Err(denied("invalid request ID"));
                }
                let inbox = Path::new(BASE).join("inbox");
                trusted_root_path(&inbox)?;
                for suffix in [".json", ".json.tmp"] {
                    match fs::remove_file(inbox.join(format!("{request_id}{suffix}"))) {
                        Ok(()) => (),
                        Err(error) if error.kind() == io::ErrorKind::NotFound => (),
                        Err(error) => return Err(error),
                    }
                }
                Ok(())
            }
            _ => Err(denied("unknown session command")),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn qga_json_preserves_unicode_without_transport_encoding_guesses() {
        let value = serde_json::json!({"name":"中文🙂 café Ã©", "literal":"\\u4f60\n\"quoted\"", "ok":true});
        let encoded = qga_json(&value).unwrap();
        assert!(encoded.is_ascii());
        assert_eq!(
            serde_json::from_str::<serde_json::Value>(&encoded).unwrap(),
            value
        );
        let ascii = serde_json::json!({"data":"/9j/AAABase64==", "ok":true});
        assert_eq!(
            qga_json(&ascii).unwrap(),
            serde_json::to_string(&ascii).unwrap()
        );
        assert!(qga_json(&"中".repeat(MAX_RESPONSE_BYTES / 4)).is_err());
    }
    #[test]
    fn identities_and_frames_are_bounded() {
        let mut key = SessionKey {
            username: "vca0123456789ab".into(),
            uid: 1001,
            sid: String::new(),
            session_id: "linux:1001:123::10.0".into(),
            instance_id: "a".repeat(64),
        };
        assert!(valid_key(&key));
        key.username = "vdi".into();
        assert!(!valid_key(&key));
        key.username = "vca../../passwd".into();
        assert!(!valid_key(&key));
        let mut frame = Vec::new();
        encode_frame(&mut frame, &serde_json::json!({"kind":"session"})).unwrap();
        assert_eq!(
            decode_frame(&mut frame.as_slice(), 1024).unwrap()["kind"],
            "session"
        );
        assert!(decode_frame(&mut u32::MAX.to_be_bytes().as_slice(), 1024).is_err());
        for value in [":0", ":10.0", ":999.2"] {
            assert!(local_display(value));
        }
        for value in [":", ":.0", ":1.", ":1..2", "localhost:10", "other:0"] {
            assert!(!local_display(value));
        }
    }

    #[test]
    fn windows_keys_bind_sid_session_and_logon_not_a_unix_uid() {
        let mut key = SessionKey {
            username: "vca0123456789ab".into(),
            uid: 0,
            sid: "S-1-5-21-1-2-3-1001".into(),
            session_id: "windows:2:000000000001a2b3".into(),
            instance_id: "a".repeat(64),
        };
        assert!(valid_key(&key));
        key.uid = 1001;
        assert!(!valid_key(&key));
        key.uid = 0;
        for sid in [
            "S-1-5-18",
            "S-1-5-32-544",
            "S-1-5-21-1-2-3-500",
            "S-1-5-21-01-2-3-1001",
        ] {
            key.sid = sid.into();
            assert!(!valid_key(&key));
        }
        key.sid = "S-1-5-21-1-2-3-1001".into();
        for session in [
            "windows:0:000000000001a2b3",
            "windows:02:000000000001a2b3",
            "windows:2",
            "windows:2:0000000000000000",
            "linux:1001:123::10",
        ] {
            key.session_id = session.into();
            assert!(!valid_key(&key));
        }
    }

    #[test]
    fn replay_never_reexecutes_pending_input_and_recovers_capacity_after_expiry() {
        let mut cache = ReplayCache::default();
        assert_eq!(cache.begin("one", vec![1], 100, 1).unwrap(), None);
        assert!(cache.begin("one", vec![1], 100, 2).is_err());
        let value = serde_json::json!({"input":{"applied":true}});
        cache.complete("one", &value);
        assert_eq!(cache.begin("one", vec![1], 100, 2).unwrap(), Some(value));
        assert!(cache.begin("one", vec![2], 100, 2).is_err());
        for i in 0..127 {
            assert!(cache.begin(&format!("next{i}"), vec![1], 100, 2).is_ok());
        }
        assert!(cache.begin("full", vec![1], 100, 2).is_err());
        assert!(cache.begin("expired", vec![1], 100, 100).is_err());
        assert!(cache.begin("fresh", vec![1], 200, 100).is_ok());
        assert_eq!(cache.0.len(), 1);
    }
}
