//! Register sesexec before PAM verifies credentials. UID process absence alone
//! cannot prove that an authenticated root session creator has stopped.
use super::*;
use rustix::{
    event::{poll, PollFd, PollFlags, Timespec},
    fd::OwnedFd,
    process::{getppid, pidfd_open, pidfd_send_signal, Pid, PidfdFlags, Signal},
};

mod domain;
pub(crate) const PAM_PREFIX: &str = "# VC Workspace login birth fence v2\nauth requisite /usr/local/lib/security/pam_vcworkspace.so\nsession requisite /usr/local/lib/security/pam_vcworkspace.so\n";
const PAM_MODULE: &str = "/usr/local/lib/security/pam_vcworkspace.so";
const PAM_PATH: &str = "/etc/pam.d/xrdp-sesman";
const JOURNAL: &str = "login-writers.json";
const MAX_WRITERS: usize = 4;
const EXECUTABLES: &[&str] = &[
    "/usr/lib/xrdp/xrdp-sesexec",
    "/usr/libexec/xrdp/xrdp-sesexec",
    "/usr/lib/x86_64-linux-gnu/xrdp/xrdp-sesexec",
    "/usr/lib/aarch64-linux-gnu/xrdp/xrdp-sesexec",
];

fn arm_parent_death(parent: i32) -> io::Result<()> {
    rustix::process::set_parent_process_death_signal(Some(Signal::KILL))?;
    if parent <= 1 || getppid().map(|pid| pid.as_raw_nonzero().get()) != Some(parent) {
        return Err(denied("login dispatcher exited before client startup"));
    }
    Ok(())
}

// An exec trampoline avoids unsafe pre_exec callbacks. A killed dispatcher
// cannot leave sesrun holding the account gate indefinitely. The post-prctl
// parent check also covers death before this trampoline started.
pub(crate) fn login_client(arguments: &[String]) -> io::Result<()> {
    use std::os::unix::process::CommandExt;
    require_root()?;
    if arguments.len() != 4
        || arguments[0] != "--guest-user"
        || arguments[2] != "--parent-pid"
        || !managed_name(&arguments[1])
        || !arguments[1].starts_with("vca")
    {
        return Err(denied("invalid bounded login client options"));
    }
    let parent = arguments[3]
        .parse()
        .map_err(|_| denied("invalid login parent"))?;
    arm_parent_death(parent)?;
    let path = Path::new("/usr/bin/xrdp-sesrun");
    trusted_root_path(path.parent().ok_or_else(|| denied("invalid client path"))?)?;
    let meta = fs::symlink_metadata(path)?;
    if !meta.is_file() || meta.uid() != 0 || meta.mode() & 0o6022 != 0 {
        return Err(denied("unsafe login client executable"));
    }
    // Executing a file with credentials/capabilities can clear PDEATHSIG.
    let mut capabilities = [0u8; 256];
    if rustix::fs::getxattr(path, "security.capability", &mut capabilities)
        != Err(rustix::io::Errno::NODATA)
    {
        return Err(denied("login client file capabilities are unsupported"));
    }
    Err(Command::new(path)
        .args(["-t", "Xorg", "-g", "1600x900", "-F", "0", &arguments[1]])
        .exec())
}

pub(crate) fn configured() -> bool {
    root_file(Path::new(PAM_PATH), 65536).is_ok_and(|raw| raw.starts_with(PAM_PREFIX.as_bytes()))
        && trusted_root_path(Path::new("/usr/local/lib/security")).is_ok()
        && fs::symlink_metadata(PAM_MODULE)
            .is_ok_and(|m| m.is_file() && m.uid() == 0 && m.mode() & 0o022 == 0 && m.nlink() == 1)
}

pub(in crate::computer::session::account) fn lock(directory: &Path) -> io::Result<fs::File> {
    trusted_root_path(directory)?;
    let file = fs::File::from(rustix::fs::open(
        directory.join("login-writers.lock"),
        OFlags::RDWR | OFlags::CREATE | OFlags::NOFOLLOW | OFlags::NONBLOCK | OFlags::CLOEXEC,
        Mode::RUSR | Mode::WUSR,
    )?);
    let meta = file.metadata()?;
    if !meta.is_file() || meta.uid() != 0 || meta.nlink() != 1 || meta.mode() & 0o777 != 0o600 {
        return Err(denied("unsafe login writer gate"));
    }
    flock(&file, FlockOperation::NonBlockingLockExclusive)?;
    let mut removed = false;
    for (index, entry) in fs::read_dir(directory)?.enumerate() {
        if index >= 1024 {
            return Err(denied("login writer directory limit exceeded"));
        }
        let entry = entry?;
        let name = entry.file_name();
        let Some(label) = name
            .to_str()
            .and_then(|n| n.strip_prefix(".login-writers-"))
            .and_then(|n| n.strip_suffix(".tmp"))
        else {
            continue;
        };
        if label.len() != 32
            || !label
                .bytes()
                .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
        {
            continue;
        }
        private_record(&entry.path())?;
        fs::remove_file(entry.path())?;
        removed = true;
    }
    if removed {
        fs::File::open(directory)?.sync_all()?;
    }
    Ok(file)
}

fn proc_text(path: impl AsRef<Path>, maximum: usize) -> io::Result<String> {
    let mut bytes = Vec::new();
    fs::File::open(path)?
        .take(maximum as u64 + 1)
        .read_to_end(&mut bytes)?;
    if bytes.len() > maximum {
        return Err(denied("process metadata exceeds limit"));
    }
    String::from_utf8(bytes).map_err(|_| denied("invalid process metadata"))
}

fn start_ticks(stat: &str, pid: i32) -> io::Result<u64> {
    let (head, fields) = stat
        .rsplit_once(") ")
        .ok_or_else(|| denied("invalid process stat"))?;
    if !head.starts_with(&format!("{pid} (")) {
        return Err(denied("process stat identity mismatch"));
    }
    fields
        .split_whitespace()
        .nth(19)
        .and_then(|s| s.parse().ok())
        .filter(|t| *t > 0)
        .ok_or_else(|| denied("invalid process start time"))
}

fn valid_boot_id(value: &str) -> bool {
    value.len() == 36
        && value.bytes().enumerate().all(|(i, b)| {
            if [8, 13, 18, 23].contains(&i) {
                b == b'-'
            } else {
                b.is_ascii_digit() || (b'a'..=b'f').contains(&b)
            }
        })
}

fn boot_id() -> io::Result<String> {
    let value = proc_text("/proc/sys/kernel/random/boot_id", 64)?
        .trim()
        .to_owned();
    if !valid_boot_id(&value) {
        return Err(denied("invalid boot identity"));
    }
    Ok(value)
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq)]
#[serde(deny_unknown_fields)]
struct ProcessIdentity {
    pid: i32,
    start_ticks: u64,
    boot_id: String,
}

pub(super) fn exited(fd: &OwnedFd) -> io::Result<bool> {
    let mut fds = [PollFd::new(fd, PollFlags::IN)];
    poll(
        &mut fds,
        Some(&Timespec {
            tv_sec: 0,
            tv_nsec: 0,
        }),
    )?;
    let flags = fds[0].revents();
    if flags.intersects(PollFlags::ERR | PollFlags::NVAL) {
        return Err(denied("invalid process handle"));
    }
    Ok(flags.intersects(PollFlags::IN | PollFlags::HUP))
}

fn capture(pid: i32, executables: &[&str]) -> io::Result<(ProcessIdentity, OwnedFd)> {
    let pid = Pid::from_raw(pid)
        .filter(|p| p.as_raw_nonzero().get() > 1)
        .ok_or_else(|| denied("invalid login writer PID"))?;
    let fd = pidfd_open(pid, PidfdFlags::NONBLOCK)?;
    let number = pid.as_raw_nonzero().get();
    let path = PathBuf::from(format!("/proc/{number}"));
    let ticks = start_ticks(&proc_text(path.join("stat"), 8192)?, number)?;
    let status = proc_text(path.join("status"), 65536)?;
    if !status
        .lines()
        .any(|line| line.split_whitespace().collect::<Vec<_>>() == ["Uid:", "0", "0", "0", "0"])
    {
        return Err(denied("login writer is not a root process"));
    }
    let executable = fs::read_link(path.join("exe"))?;
    if !executables.iter().any(|p| executable == Path::new(p)) {
        return Err(denied("unexpected login writer executable"));
    }
    trusted_root_path(
        executable
            .parent()
            .ok_or_else(|| denied("invalid login executable"))?,
    )?;
    let trusted = fs::symlink_metadata(&executable)?;
    let actual = fs::metadata(path.join("exe"))?;
    if !trusted.is_file()
        || trusted.uid() != 0
        || trusted.mode() & 0o022 != 0
        || trusted.dev() != actual.dev()
        || trusted.ino() != actual.ino()
        || exited(&fd)?
    {
        return Err(denied("login writer executable changed"));
    }
    Ok((
        ProcessIdentity {
            pid: number,
            start_ticks: ticks,
            boot_id: boot_id()?,
        },
        fd,
    ))
}

impl ProcessIdentity {
    fn validate(&self) -> io::Result<()> {
        if self.pid <= 1 || self.start_ticks == 0 || !valid_boot_id(&self.boot_id) {
            return Err(denied("invalid login writer identity"));
        }
        Ok(())
    }

    fn open(&self) -> io::Result<Option<OwnedFd>> {
        self.validate()?;
        if self.boot_id != boot_id()? {
            return Ok(None);
        }
        let fd = match pidfd_open(
            Pid::from_raw(self.pid).ok_or_else(|| denied("invalid PID"))?,
            PidfdFlags::NONBLOCK,
        ) {
            Ok(fd) => fd,
            Err(rustix::io::Errno::SRCH) => return Ok(None),
            Err(error) => return Err(error.into()),
        };
        if exited(&fd)? {
            return Ok(None);
        }
        let observed = match proc_text(format!("/proc/{}/stat", self.pid), 8192) {
            Ok(raw) => start_ticks(&raw, self.pid)?,
            Err(error) if error.kind() == io::ErrorKind::NotFound && exited(&fd)? => {
                return Ok(None)
            }
            Err(error) => return Err(error),
        };
        if observed != self.start_ticks {
            return Ok(None);
        }
        // Registration authenticated the root executable BEFORE PAM proceeded.
        // This private record owns that process incarnation, including any later
        // exec in it. Do not require ptracing a full-capability root process from
        // the capability-bounded reaper or grant the reaper CAP_SYS_PTRACE.
        // Boot/start-time checks and this still-live pidfd prevent signalling a
        // replacement PID; no signal is sent via the numeric journal value.
        if exited(&fd)? {
            return Ok(None);
        }
        Ok(Some(fd))
    }
}

#[derive(Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct Writer {
    process: ProcessIdentity,
    scope: Option<domain::Scope>,
    lease_id: String,
    control_epoch: u64,
    login_generation: u64,
    expires_unix_seconds: u64,
}

struct LoginVersion {
    lease_id: String,
    control_epoch: u64,
    login_generation: u64,
    expires_unix_seconds: u64,
}

impl From<&Fence> for LoginVersion {
    fn from(value: &Fence) -> Self {
        Self {
            lease_id: value.lease_id.clone(),
            control_epoch: value.control_epoch,
            login_generation: value.login_generation,
            expires_unix_seconds: value.expires_unix_seconds,
        }
    }
}

impl Writer {
    fn live(&self, uid: u32) -> io::Result<bool> {
        Ok(self.process.open()?.is_some()
            || match &self.scope {
                Some(scope) => !scope.absent(uid, &self.process.boot_id)?,
                None => false,
            })
    }
    fn matches(&self, fence: &LoginVersion) -> bool {
        self.lease_id == fence.lease_id
            && self.control_epoch == fence.control_epoch
            && self.login_generation == fence.login_generation
            && self.expires_unix_seconds == fence.expires_unix_seconds
    }
}

#[derive(Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct Journal {
    schema_version: u8,
    identity: AccountIdentity,
    writers: Vec<Writer>,
}

fn read(directory: &Path, account: &Account) -> io::Result<Journal> {
    let value: Journal = match private_record(&directory.join(JOURNAL)) {
        Ok(raw) => {
            serde_json::from_slice(&raw).map_err(|_| denied("invalid login writer journal"))?
        }
        Err(error) if error.kind() == io::ErrorKind::NotFound => Journal {
            schema_version: 2,
            identity: identity(account),
            writers: Vec::new(),
        },
        Err(error) => return Err(error),
    };
    if value.schema_version != 2
        || value.identity != identity(account)
        || value.writers.len() > MAX_WRITERS
    {
        return Err(denied("login writer journal identity changed"));
    }
    let mut seen = std::collections::BTreeSet::new();
    for writer in &value.writers {
        writer.process.validate()?;
        if let Some(scope) = &writer.scope {
            scope.validate()?;
        }
        if !seen.insert(writer.process.pid) {
            return Err(denied("duplicate login writer"));
        }
        if account.username.starts_with("vcw") {
            if writer.login_generation != 1 {
                return Err(denied("invalid Native login writer version"));
            }
            vc_workspace_guest_lifecycle::native::Fence {
                schema_version: 1,
                identity: value.identity.clone(),
                connection_id: writer.lease_id.clone(),
                revision: writer.control_epoch,
                expires_unix_seconds: writer.expires_unix_seconds,
                phase: vc_workspace_guest_lifecycle::native::Phase::Issued,
            }
            .validate()?;
        } else {
            Fence {
                schema_version: 1,
                identity: value.identity.clone(),
                lease_id: writer.lease_id.clone(),
                control_epoch: writer.control_epoch,
                login_generation: writer.login_generation,
                expires_unix_seconds: writer.expires_unix_seconds,
                phase: Phase::Opening,
            }
            .validate()?;
        }
    }
    Ok(value)
}

fn persist_journal(directory: &Path, value: &Journal) -> io::Result<()> {
    // PAM cannot take the account gate: its caller is waiting on authentication
    // while holding that gate. Use a distinct temporary namespace, so account
    // gate recovery cannot unlink an in-progress PAM journal publication.
    let raw = serde_json::to_vec(value).map_err(io::Error::other)?;
    if raw.len() > 4096 {
        return Err(denied("login journal exceeds limit"));
    }
    let mut nonce = [0u8; 16];
    getrandom::fill(&mut nonce).map_err(io::Error::other)?;
    let label: String = nonce.iter().map(|b| format!("{b:02x}")).collect();
    let temporary = directory.join(format!(".login-writers-{label}.tmp"));
    let mut file = fs::OpenOptions::new()
        .create_new(true)
        .write(true)
        .mode(0o600)
        .open(&temporary)?;
    let result = (|| {
        file.write_all(&raw)?;
        file.sync_all()?;
        fs::rename(&temporary, directory.join(JOURNAL))?;
        fs::File::open(directory)?.sync_all()
    })();
    let _ = fs::remove_file(temporary);
    result
}

pub(in crate::computer::session::account) fn absent(
    directory: &Path,
    account: &Account,
) -> io::Result<bool> {
    let mut current_boot = false;
    let boot = boot_id()?;
    for writer in read(directory, account)?.writers {
        if writer.live(account.uid)? {
            return Ok(false);
        }
        current_boot |= writer.process.boot_id == boot;
    }
    // No old process or manager request survives a kernel boot. Avoid requiring
    // logind on the separate account-only/container path with no PAM journal.
    if current_boot {
        domain::user_idle(account.uid)
    } else {
        Ok(true)
    }
}

// Caller holds account gate then birth gate. No new authentication can register
// while root creators are being drained, and no new version may be published.
pub(super) fn drain(directory: &Path, account: &Account) -> io::Result<()> {
    let writers = read(directory, account)?.writers;
    let mut handles = Vec::new();
    for writer in &writers {
        // Reject a replaced cgroup before signalling anything. The pinned PID
        // is still the authenticated creator, not a subsequently resolved unit.
        if let Some(scope) = &writer.scope {
            scope.absent(account.uid, &writer.process.boot_id)?;
        }
        if let Some(fd) = writer.process.open()? {
            handles.push(fd);
        }
    }
    for fd in &handles {
        match pidfd_send_signal(fd, Signal::TERM) {
            Ok(()) | Err(rustix::io::Errno::SRCH) => (),
            Err(error) => return Err(error.into()),
        }
    }
    // xrdp's session executive can now close Xorg/chansrv and their sockets.
    // This shared grace is bounded even with several or unresponsive creators.
    // Account/birth gates stay held; no new login or version may be published.
    let graceful_deadline = Instant::now() + Duration::from_secs(2);
    while !handles.is_empty() && Instant::now() < graceful_deadline {
        let mut live = false;
        for fd in &handles {
            live |= !exited(fd)?;
        }
        if !live {
            break;
        }
        thread::sleep(Duration::from_millis(20));
    }
    // ALWAYS close the pinned domain afterwards, including root orphans after
    // their original creator has exited. TERM is never a closure receipt.
    for writer in writers {
        if let Some(scope) = &writer.scope {
            scope.drain(account.uid, &writer.process.boot_id)?;
        }
        if let Some(fd) = writer.process.open()? {
            match pidfd_send_signal(&fd, Signal::KILL) {
                Ok(()) | Err(rustix::io::Errno::SRCH) => (),
                Err(error) => return Err(error.into()),
            }
            let deadline = Instant::now() + Duration::from_secs(2);
            while !exited(&fd)? {
                if Instant::now() >= deadline {
                    return Err(denied("login writer remains alive"));
                }
                thread::sleep(Duration::from_millis(20));
            }
        }
    }
    Ok(())
}

pub(super) fn one_live(directory: &Path, account: &Account, fence: &Fence) -> io::Result<()> {
    let mut live = 0;
    for writer in read(directory, account)?.writers {
        if writer.live(account.uid)? {
            if !writer.matches(&LoginVersion::from(fence)) {
                return Err(denied("another login version remains alive"));
            }
            if writer.process.open()?.is_none() || writer.scope.is_none() {
                return Err(denied("login creator has no committed live process domain"));
            }
            live += 1;
        }
    }
    if live != 1 {
        return Err(denied("unique registered session creator required"));
    }
    Ok(())
}

pub(crate) fn register() -> io::Result<()> {
    register_inner(false)
}

pub(in crate::computer::session::account) fn register_native() -> io::Result<()> {
    register_inner(true)
}

fn register_inner(native: bool) -> io::Result<()> {
    require_root()?;
    if env::var("PAM_SERVICE").ok().as_deref() != Some("xrdp-sesman") {
        return Err(denied("xrdp PAM caller required"));
    }
    let kind = env::var("PAM_TYPE").map_err(|_| denied("missing PAM operation"))?;
    let username = env::var("PAM_USER").map_err(|_| denied("missing PAM identity"))?;
    // The system's normal local/directory authentication stack is unchanged.
    if !username.starts_with(if native { "vcw" } else { "vca" }) || kind == "close_session" {
        return Ok(());
    }
    if !managed_name(&username)
        || !(if native {
            super::super::native::configured()
        } else {
            configured()
        })
        || !matches!(kind.as_str(), "auth" | "open_session")
    {
        return Err(denied("unsupported managed PAM login"));
    }
    let parent = getppid()
        .ok_or_else(|| denied("missing PAM parent"))?
        .as_raw_nonzero()
        .get();
    let (process, handle) = capture(parent, EXECUTABLES)?;
    arm_parent_death(parent)?;
    let exchange = domain::Exchange::open(parent)?;
    let directory = Path::new(BASE).join("users").join(&username);
    let _birth = lock(&directory)?;
    let account =
        read_account(&username, &directory)?.ok_or_else(|| denied("unbound PAM account"))?;
    if !verify_identity(&account)? {
        return Err(denied("PAM account was removed"));
    }
    let fence = if native {
        let value = super::super::native::login_fence(&directory, &account)?;
        LoginVersion {
            lease_id: value.connection_id,
            control_epoch: value.revision,
            login_generation: 1,
            expires_unix_seconds: value.expires_unix_seconds,
        }
    } else {
        let value =
            read_fence(&directory, &account)?.ok_or_else(|| denied("missing login intent"))?;
        if value.phase != Phase::Opening {
            return Err(denied("Agent login intent is no longer opening"));
        }
        LoginVersion::from(&value)
    };
    if fence.expires_unix_seconds <= now() || exited(&handle)? {
        return Err(denied("PAM login intent is no longer open"));
    }
    let mut journal = read(&directory, &account)?;
    let mut live = Vec::new();
    for writer in journal.writers {
        if writer.live(account.uid)? {
            live.push(writer);
        }
    }
    journal.writers = live;
    if let Some(writer) = journal.writers.iter().find(|w| w.process == process) {
        if !writer.matches(&fence) || kind != "open_session" || writer.scope.is_none() {
            return Err(denied("PAM login version changed"));
        }
    } else {
        // UDS authentication bypasses pam_authenticate. It must not create an
        // Agent session via open_session without a prior versioned sys login.
        if kind != "auth" || journal.writers.len() >= MAX_WRITERS {
            return Err(denied("unregistered PAM session"));
        }
        journal.writers.push(Writer {
            process: process.clone(),
            scope: None,
            lease_id: fence.lease_id,
            control_epoch: fence.control_epoch,
            login_generation: fence.login_generation,
            expires_unix_seconds: fence.expires_unix_seconds,
        });
        persist_journal(&directory, &journal)?;
    }
    // Before the handshake, a durable writer with no scope means PAM has not
    // yet been acknowledged and must not continue to password/session modules.
    // Schema 1 journals are rejected: their no-scope writers could already fork.
    let scope = exchange.bind(&account, &process, &handle)?;
    let writer = journal
        .writers
        .iter_mut()
        .find(|w| w.process == process)
        .ok_or_else(|| denied("PAM writer disappeared"))?;
    if let Some(existing) = &writer.scope {
        if existing != &scope {
            return Err(denied("PAM domain changed between login stages"));
        }
    }
    writer.scope = Some(scope);
    if writer.expires_unix_seconds <= now() || exited(&handle)? {
        return Err(denied("PAM login expired during domain creation"));
    }
    persist_journal(&directory, &journal)?;
    exchange.acknowledge()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::BufRead;
    const CHILD: &str = "computer::session::account::lease::birth::tests::death_signal_child";

    struct Child(std::process::Child);
    impl Drop for Child {
        fn drop(&mut self) {
            let _ = self.0.kill();
            let _ = self.0.wait();
        }
    }

    #[test]
    fn process_start_parser_handles_parentheses_and_rejects_missing_identity() {
        let mut fields = vec!["0"; 20];
        fields[0] = "S";
        fields[19] = "43210";
        let value = format!("123 (a tricky ) name) {}", fields.join(" "));
        assert_eq!(start_ticks(&value, 123).unwrap(), 43210);
        assert!(start_ticks(&value, 124).is_err());
        assert!(start_ticks("123 (name) S", 123).is_err());
        fields[19] = "0";
        assert!(start_ticks(&format!("123 (name) {}", fields.join(" ")), 123).is_err());
    }

    #[test]
    #[ignore = "private subprocess for the disposable kernel test"]
    fn death_signal_child() {
        assert_eq!(env::var("VC_WORKSPACE_DISPOSABLE_TEST").as_deref(), Ok("1"));
        require_root().unwrap();
        let mode = env::var("VC_WORKSPACE_DEATH_TEST_MODE").unwrap();
        if mode == "child" {
            let parent = env::var("VC_WORKSPACE_DEATH_TEST_PARENT")
                .unwrap()
                .parse()
                .unwrap();
            arm_parent_death(parent).unwrap();
            println!("armed");
            io::stdout().flush().unwrap();
        } else {
            assert_eq!(mode, "parent");
            let mut child = Child(
                Command::new(env::current_exe().unwrap())
                    .args(["--ignored", "--exact", CHILD, "--nocapture"])
                    .env("VC_WORKSPACE_DEATH_TEST_MODE", "child")
                    .env(
                        "VC_WORKSPACE_DEATH_TEST_PARENT",
                        std::process::id().to_string(),
                    )
                    .stdout(Stdio::piped())
                    .spawn()
                    .unwrap(),
            );
            let mut stdout = io::BufReader::new(child.0.stdout.take().unwrap());
            let mut line = String::new();
            loop {
                line.clear();
                assert!(stdout.read_line(&mut line).unwrap() > 0);
                if line.trim() == "armed" {
                    break;
                }
            }
            println!("owned-child:{}", child.0.id());
            io::stdout().flush().unwrap();
            loop {
                thread::sleep(Duration::from_secs(1));
            }
        }
        loop {
            thread::sleep(Duration::from_secs(1));
        }
    }

    #[test]
    #[ignore = "actual pidfd/death-signal test; disposable Linux container required"]
    fn kernel_handles_and_parent_death_prevent_stale_process_signals() {
        use std::os::unix::process::CommandExt;
        assert_eq!(env::var("VC_WORKSPACE_DISPOSABLE_TEST").as_deref(), Ok("1"));
        require_root().unwrap();
        let mut child = Child(Command::new("/usr/bin/sleep").arg("60").spawn().unwrap());
        let pid = child.0.id() as i32;
        let (identity, fd) = capture(pid, &["/usr/bin/sleep"]).unwrap();
        assert!(identity.open().unwrap().is_some());
        let mut stale = identity.clone();
        stale.start_ticks += 1;
        assert!(stale.open().unwrap().is_none());
        stale = identity.clone();
        stale.boot_id = "00000000-0000-0000-0000-000000000000".into();
        assert!(stale.open().unwrap().is_none());
        stale = identity.clone();
        stale.boot_id = "a".repeat(36);
        assert!(stale.open().is_err());
        assert!(capture(pid, EXECUTABLES).is_err());
        assert!(!exited(&fd).unwrap());
        child.0.kill().unwrap();
        child.0.wait().unwrap();
        assert!(exited(&fd).unwrap());
        assert!(identity.open().unwrap().is_none());
        let unprivileged = Child(
            Command::new("/usr/bin/sleep")
                .arg("60")
                .uid(65534)
                .gid(65534)
                .spawn()
                .unwrap(),
        );
        assert!(capture(unprivileged.0.id() as i32, &["/usr/bin/sleep"]).is_err());

        let mut parent = Child(
            Command::new(env::current_exe().unwrap())
                .args(["--ignored", "--exact", CHILD, "--nocapture"])
                .env("VC_WORKSPACE_DEATH_TEST_MODE", "parent")
                .stdout(Stdio::piped())
                .spawn()
                .unwrap(),
        );
        let mut stdout = io::BufReader::new(parent.0.stdout.take().unwrap());
        let mut line = String::new();
        let grandchild = loop {
            line.clear();
            assert!(stdout.read_line(&mut line).unwrap() > 0);
            if let Some(number) = line.trim().strip_prefix("owned-child:") {
                break number.parse::<i32>().unwrap();
            }
        };
        let fd = pidfd_open(Pid::from_raw(grandchild).unwrap(), PidfdFlags::NONBLOCK).unwrap();
        assert!(!exited(&fd).unwrap());
        parent.0.kill().unwrap();
        parent.0.wait().unwrap();
        let deadline = Instant::now() + Duration::from_secs(2);
        while !exited(&fd).unwrap() {
            assert!(Instant::now() < deadline);
            thread::sleep(Duration::from_millis(20));
        }
        // No process is signalled using a numeric PID loaded from the journal.
    }
}
