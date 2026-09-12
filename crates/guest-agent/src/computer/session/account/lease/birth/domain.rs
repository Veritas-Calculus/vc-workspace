//! The PAM caller owns logind's session reference. Rust independently verifies
//! its pidfd membership before acknowledging authentication. Revocation writes
//! through a pinned cgroup directory, never an asynchronously resolved unit name.
use super::*;
use rustix::{
    fd::AsFd,
    net::{recv, send, sockopt, RecvFlags, SendFlags, SocketType},
};
use zbus::{
    blocking::{connection::Builder, Connection, Proxy},
    zvariant::{Fd, OwnedObjectPath},
};

const CGROUP_ROOT: &str = "/sys/fs/cgroup";
const CGROUP2_MAGIC: i64 = 0x6367_7270;

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq)]
#[serde(deny_unknown_fields)]
pub(super) struct Scope {
    session_id: String,
    invocation_id: Vec<u8>,
    device: u64,
    inode: u64,
}

fn valid_session_id(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 64
        && !matches!(value, "auto" | "self")
        && value
            .bytes()
            .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit())
}

fn relative_group(uid: u32, id: &str) -> String {
    format!("/user.slice/user-{uid}.slice/session-{id}.scope")
}

fn open_directory(uid: u32, id: &str) -> io::Result<fs::File> {
    if uid < 1000 || !valid_session_id(id) {
        return Err(denied("invalid login process domain"));
    }
    // Walk each component with NOFOLLOW, retaining the final directory handle.
    // No untrusted path from the journal or D-Bus is ever passed to open().
    let mut directory = fs::File::from(rustix::fs::open(
        CGROUP_ROOT,
        OFlags::RDONLY | OFlags::DIRECTORY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
        Mode::empty(),
    )?);
    if rustix::fs::fstatfs(&directory)?.f_type as i64 != CGROUP2_MAGIC {
        return Err(denied("unified cgroup filesystem required"));
    }
    for component in [
        "user.slice".into(),
        format!("user-{uid}.slice"),
        format!("session-{id}.scope"),
    ] {
        directory = fs::File::from(rustix::fs::openat(
            &directory,
            component,
            OFlags::RDONLY | OFlags::DIRECTORY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
            Mode::empty(),
        )?);
        let meta = directory.metadata()?;
        if meta.uid() != 0
            || meta.mode() & 0o022 != 0
            || rustix::fs::fstatfs(&directory)?.f_type as i64 != CGROUP2_MAGIC
        {
            return Err(denied("unsafe login cgroup directory"));
        }
    }
    Ok(directory)
}

fn read_at(directory: &fs::File, name: &str) -> io::Result<String> {
    let file = fs::File::from(rustix::fs::openat(
        directory,
        name,
        OFlags::RDONLY | OFlags::NOFOLLOW | OFlags::NONBLOCK | OFlags::CLOEXEC,
        Mode::empty(),
    )?);
    let mut raw = String::new();
    file.take(1025).read_to_string(&mut raw)?;
    if raw.len() > 1024 {
        return Err(denied("oversized cgroup metadata"));
    }
    Ok(raw)
}

fn populated_text(raw: &str) -> io::Result<bool> {
    let values: Vec<_> = raw
        .lines()
        .filter_map(|line| line.strip_prefix("populated "))
        .collect();
    match values.as_slice() {
        ["0"] => Ok(false),
        ["1"] => Ok(true),
        _ => Err(denied("invalid cgroup population state")),
    }
}

fn populated(directory: &fs::File) -> io::Result<bool> {
    match read_at(directory, "cgroup.events") {
        Ok(raw) => populated_text(&raw),
        // cgroup removal is possible only after it and its descendants emptied.
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(false),
        Err(error) => Err(error),
    }
}

impl Scope {
    pub(super) fn validate(&self) -> io::Result<()> {
        if !valid_session_id(&self.session_id)
            || self.invocation_id.len() != 16
            || self.invocation_id.iter().all(|b| *b == 0)
            || self.inode == 0
        {
            return Err(denied("invalid login scope identity"));
        }
        Ok(())
    }

    fn open(&self, uid: u32, boot: &str) -> io::Result<Option<fs::File>> {
        self.validate()?;
        if boot != boot_id()? {
            return Ok(None);
        }
        let directory = match open_directory(uid, &self.session_id) {
            Ok(value) => value,
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(None),
            Err(error) => return Err(error),
        };
        let meta = directory.metadata()?;
        // A reused name is not proof of absence, and never permission to kill.
        if meta.dev() != self.device || meta.ino() != self.inode {
            return Err(denied("login cgroup incarnation changed"));
        }
        Ok(Some(directory))
    }

    pub(super) fn absent(&self, uid: u32, boot: &str) -> io::Result<bool> {
        match self.open(uid, boot)? {
            Some(directory) => Ok(!populated(&directory)?),
            None => Ok(true),
        }
    }

    pub(super) fn drain(&self, uid: u32, boot: &str) -> io::Result<()> {
        let Some(directory) = self.open(uid, boot)? else {
            return Ok(());
        };
        if !populated(&directory)? {
            return Ok(());
        }
        if read_at(&directory, "cgroup.type")?.trim() != "domain" {
            return Err(denied(
                "login domain does not support atomic tree termination",
            ));
        }
        let mut kill = match rustix::fs::openat(
            &directory,
            "cgroup.kill",
            OFlags::WRONLY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
            Mode::empty(),
        ) {
            Ok(fd) => fs::File::from(fd),
            Err(rustix::io::Errno::NOENT) if !populated(&directory)? => return Ok(()),
            Err(error) => return Err(error.into()),
        };
        // The kernel kills the entire domain, including concurrent forks. This
        // FD continues to refer to this incarnation even if the name is reused.
        kill.write_all(b"1")?;
        let deadline = Instant::now() + Duration::from_secs(2);
        while populated(&directory)? {
            if Instant::now() >= deadline {
                return Err(denied("login domain remains populated"));
            }
            thread::sleep(Duration::from_millis(20));
        }
        Ok(())
    }
}

fn bus_proxy<'a>(
    bus: &'a Connection,
    destination: &'a str,
    path: &'a str,
    interface: &'a str,
) -> io::Result<Proxy<'a>> {
    Proxy::new(bus, destination, path, interface).map_err(io::Error::other)
}

fn method_error(error: &zbus::Error, name: &str) -> bool {
    matches!(error, zbus::Error::MethodError(actual, _, _) if actual.as_str() == name)
}

// Killing a PAM leader does not cancel logind's asynchronous user@/runtime-dir
// jobs. Even a no-scope journal can have caused those jobs before losing its
// CreateSession reply. Do not admit a new version until logind has retired the
// user AND systemd has no remaining job/active unit for that immutable UID.
// All operations here are read-only; no named stop/kill can arrive late.
pub(super) fn user_idle(uid: u32) -> io::Result<bool> {
    if uid < 1000 {
        return Err(denied("invalid login domain UID"));
    }
    let bus = Builder::system()
        .map_err(io::Error::other)?
        .method_timeout(Duration::from_secs(1))
        .build()
        .map_err(io::Error::other)?;
    let login = bus_proxy(
        &bus,
        "org.freedesktop.login1",
        "/org/freedesktop/login1",
        "org.freedesktop.login1.Manager",
    )?;
    let no_user = || -> io::Result<bool> {
        match login.call::<_, _, OwnedObjectPath>("GetUser", &(uid,)) {
            Ok(_) => Ok(false),
            Err(error) if method_error(&error, "org.freedesktop.login1.NoSuchUser") => Ok(true),
            Err(error) => Err(io::Error::other(error)),
        }
    };
    if !no_user()? {
        return Ok(false);
    }
    let manager = bus_proxy(
        &bus,
        "org.freedesktop.systemd1",
        "/org/freedesktop/systemd1",
        "org.freedesktop.systemd1.Manager",
    )?;
    for name in [
        format!("user-{uid}.slice"),
        format!("user@{uid}.service"),
        format!("user-runtime-dir@{uid}.service"),
    ] {
        let path: OwnedObjectPath = match manager.call("GetUnit", &(name,)) {
            Ok(path) => path,
            Err(error) if method_error(&error, "org.freedesktop.systemd1.NoSuchUnit") => continue,
            Err(error) => return Err(io::Error::other(error)),
        };
        let unit = bus_proxy(
            &bus,
            "org.freedesktop.systemd1",
            path.as_str(),
            "org.freedesktop.systemd1.Unit",
        )?;
        let state: String = unit.get_property("ActiveState").map_err(io::Error::other)?;
        let (job, _): (u32, OwnedObjectPath) =
            unit.get_property("Job").map_err(io::Error::other)?;
        if job != 0 || !matches!(state.as_str(), "inactive" | "failed") {
            return Ok(false);
        }
    }
    no_user()
}

fn observe(uid: u32, id: &str, parent: &ProcessIdentity, handle: &OwnedFd) -> io::Result<Scope> {
    if !valid_session_id(id) || exited(handle)? {
        return Err(denied("invalid PAM domain receipt"));
    }
    let bus = Builder::system()
        .map_err(io::Error::other)?
        .method_timeout(Duration::from_secs(2))
        .build()
        .map_err(io::Error::other)?;
    let manager = bus_proxy(
        &bus,
        "org.freedesktop.systemd1",
        "/org/freedesktop/systemd1",
        "org.freedesktop.systemd1.Manager",
    )?;
    let (path, name, invocation_id): (OwnedObjectPath, String, Vec<u8>) = manager
        .call("GetUnitByPIDFD", &(Fd::from(handle.as_fd()),))
        .map_err(io::Error::other)?;
    if name != format!("session-{id}.scope") {
        return Err(denied("PAM parent is outside its login scope"));
    }
    let unit = bus_proxy(
        &bus,
        "org.freedesktop.systemd1",
        path.as_str(),
        "org.freedesktop.systemd1.Unit",
    )?;
    let current: Vec<u8> = unit
        .get_property("InvocationID")
        .map_err(io::Error::other)?;
    let scope = bus_proxy(
        &bus,
        "org.freedesktop.systemd1",
        path.as_str(),
        "org.freedesktop.systemd1.Scope",
    )?;
    let group: String = scope
        .get_property("ControlGroup")
        .map_err(io::Error::other)?;
    if group != relative_group(uid, id) || current != invocation_id {
        return Err(denied("login scope ownership changed"));
    }
    let login = bus_proxy(
        &bus,
        "org.freedesktop.login1",
        "/org/freedesktop/login1",
        "org.freedesktop.login1.Manager",
    )?;
    let session_path: OwnedObjectPath =
        login.call("GetSession", &(id,)).map_err(io::Error::other)?;
    let session = bus_proxy(
        &bus,
        "org.freedesktop.login1",
        session_path.as_str(),
        "org.freedesktop.login1.Session",
    )?;
    let (owner, _): (u32, OwnedObjectPath) =
        session.get_property("User").map_err(io::Error::other)?;
    let leader: u32 = session.get_property("Leader").map_err(io::Error::other)?;
    let service: String = session.get_property("Service").map_err(io::Error::other)?;
    let session_scope: String = session.get_property("Scope").map_err(io::Error::other)?;
    if owner != uid
        || leader != parent.pid as u32
        || service != "xrdp-sesman"
        || session_scope != name
    {
        return Err(denied("logind session is not owned by this PAM login"));
    }
    let directory = open_directory(uid, id)?;
    let meta = directory.metadata()?;
    let membership = proc_text(format!("/proc/{}/cgroup", parent.pid), 8192)?;
    if membership.trim() != format!("0::{group}") || !populated(&directory)? || exited(handle)? {
        return Err(denied("kernel login domain did not converge"));
    }
    let value = Scope {
        session_id: id.into(),
        invocation_id,
        device: meta.dev(),
        inode: meta.ino(),
    };
    value.validate()?;
    Ok(value)
}

pub(super) struct Exchange(OwnedFd);

impl Exchange {
    pub(super) fn open(parent: i32) -> io::Result<Self> {
        let fd = rustix::io::dup(io::stdin())?;
        let peer = sockopt::socket_peercred(&fd)?;
        if sockopt::socket_type(&fd)? != SocketType::SEQPACKET
            || !peer.uid.is_root()
            || peer.pid.as_raw_nonzero().get() != parent
        {
            return Err(denied("root PAM socket peer required"));
        }
        Ok(Self(fd))
    }

    fn send(&self, bytes: &[u8]) -> io::Result<()> {
        if send(&self.0, bytes, SendFlags::NOSIGNAL | SendFlags::DONTWAIT)? != bytes.len() {
            return Err(denied("incomplete PAM response"));
        }
        Ok(())
    }

    pub(super) fn bind(
        &self,
        account: &Account,
        parent: &ProcessIdentity,
        handle: &OwnedFd,
    ) -> io::Result<Scope> {
        let mut ready = Vec::from(b"VCWPAM2\0".as_slice());
        ready.extend_from_slice(&account.uid.to_be_bytes());
        self.send(&ready)?;
        let mut fds = [PollFd::new(&self.0, PollFlags::IN)];
        poll(
            &mut fds,
            Some(&Timespec {
                tv_sec: 10,
                tv_nsec: 0,
            }),
        )?;
        if !fds[0].revents().contains(PollFlags::IN) {
            return Err(denied("PAM domain receipt timed out"));
        }
        let mut raw = [0u8; 64];
        let (_, length) = recv(
            &self.0,
            &mut raw[..],
            RecvFlags::TRUNC | RecvFlags::DONTWAIT,
        )?;
        let id = raw
            .get(..length)
            .and_then(|raw| std::str::from_utf8(raw).ok())
            .filter(|id| valid_session_id(id))
            .ok_or_else(|| denied("invalid PAM domain packet"))?;
        observe(account.uid, id, parent, handle)
    }

    pub(super) fn acknowledge(&self) -> io::Result<()> {
        self.send(b"VCWACK2\0")
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn domain_names_and_kernel_population_are_strict() {
        for id in ["c1", "12", "a1b2"] {
            assert!(valid_session_id(id));
        }
        for id in [
            "", "self", "auto", "../c1", "c1.scope", "C1", "c1\n", "c1/child",
        ] {
            assert!(!valid_session_id(id));
        }
        assert!(!valid_session_id(&"a".repeat(65)));
        assert!(!populated_text("populated 0\nfrozen 0\n").unwrap());
        assert!(populated_text("populated 1\nfrozen 0\n").unwrap());
        for value in [
            "",
            "populated 2",
            "populated 0\npopulated 1",
            "populated 01",
        ] {
            assert!(populated_text(value).is_err());
        }
        let mut scope = Scope {
            session_id: "c1".into(),
            invocation_id: vec![1; 16],
            device: 1,
            inode: 2,
        };
        assert!(scope.validate().is_ok());
        scope.invocation_id.fill(0);
        assert!(scope.validate().is_err());
    }
}
