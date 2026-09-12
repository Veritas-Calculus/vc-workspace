//! Remove X11 filesystem nodes only AFTER the fixed UID and its login writers
//! have closed. The caller holds account/birth gates and keeps login disabled.
//! Sticky root-owned directories prevent another unprivileged UID replacing an
//! owned node between inspection and unlink. Root administrators are out of scope.
use super::*;
use rustix::{
    fd::AsRawFd,
    fs::AtFlags,
    process::{pidfd_open, Pid, PidfdFlags},
};
use std::collections::BTreeSet;

fn number(name: &str, socket: bool) -> Option<u16> {
    let raw = if socket {
        name.strip_prefix('X')?
    } else {
        name.strip_prefix(".X")?.strip_suffix("-lock")?
    };
    if raw.is_empty() || raw.len() > 4 || !raw.bytes().all(|c| c.is_ascii_digit()) {
        return None;
    }
    let value: u16 = raw.parse().ok()?;
    (value.to_string() == raw).then_some(value)
}

fn directory(path: &Path) -> io::Result<fs::File> {
    let file = fs::File::from(rustix::fs::open(
        path,
        OFlags::RDONLY | OFlags::DIRECTORY | OFlags::NOFOLLOW | OFlags::CLOEXEC,
        Mode::empty(),
    )?);
    let meta = file.metadata()?;
    if meta.uid() != 0 || (meta.mode() & 0o022 != 0 && meta.mode() & 0o1000 == 0) {
        return Err(denied(
            "X11 directory must be root-owned and sticky or protected",
        ));
    }
    Ok(file)
}

fn entry(parent: &fs::File, name: &str) -> io::Result<Option<fs::File>> {
    match rustix::fs::openat(
        parent,
        name,
        OFlags::PATH | OFlags::NOFOLLOW | OFlags::CLOEXEC,
        Mode::empty(),
    ) {
        Ok(fd) => Ok(Some(fs::File::from(fd))),
        Err(rustix::io::Errno::NOENT) => Ok(None),
        Err(error) => Err(error.into()),
    }
}

fn entries(
    parent: &fs::File,
    uid: u32,
    socket: bool,
    displays: &mut BTreeSet<u16>,
) -> io::Result<()> {
    // Enumerate the pinned directory, not a subsequently resolved /tmp path.
    for (index, item) in fs::read_dir(format!("/proc/self/fd/{}", parent.as_raw_fd()))?.enumerate()
    {
        if index >= 8192 {
            return Err(denied("X11 directory scan exceeds limit"));
        }
        let item = item?;
        let name = item.file_name();
        let Some(name) = name.to_str() else {
            continue;
        };
        let Some(display) = number(name, socket) else {
            continue;
        };
        if let Some(node) = entry(parent, name)? {
            if node.metadata()?.uid() == uid {
                displays.insert(display);
            }
        }
        if displays.len() > 64 {
            return Err(denied("owned X11 node limit exceeded"));
        }
    }
    Ok(())
}

fn dead_pid(raw: &str) -> io::Result<bool> {
    let value = raw.trim();
    if value.is_empty() || !value.bytes().all(|c| c.is_ascii_digit()) || raw.len() > 32 {
        return Err(denied("invalid X11 lock PID"));
    }
    let number: i32 = value.parse().map_err(|_| denied("invalid X11 lock PID"))?;
    let pid = Pid::from_raw(number)
        .filter(|p| p.as_raw_nonzero().get() > 1)
        .ok_or_else(|| denied("invalid X11 lock PID"))?;
    match pidfd_open(pid, PidfdFlags::NONBLOCK) {
        Ok(fd) => birth::exited(&fd),
        Err(rustix::io::Errno::SRCH) => Ok(true),
        Err(error) => Err(error.into()),
    }
}

fn unchanged(parent: &fs::File, name: &str, expected: &fs::Metadata) -> io::Result<bool> {
    let Some(node) = entry(parent, name)? else {
        return Ok(false);
    };
    let meta = node.metadata()?;
    if meta.dev() != expected.dev()
        || meta.ino() != expected.ino()
        || meta.uid() != expected.uid()
        || meta.mode() != expected.mode()
        || meta.nlink() != 1
    {
        return Err(denied("X11 node changed during account closure"));
    }
    Ok(true)
}

pub(super) fn cleanup(uid: u32) -> io::Result<()> {
    if uid < 1000 {
        return Err(denied("managed UID required for X11 cleanup"));
    }
    let temporary = directory(Path::new("/tmp"))?;
    let sockets = match directory(Path::new("/tmp/.X11-unix")) {
        Ok(value) => Some(value),
        Err(error) if error.kind() == io::ErrorKind::NotFound => None,
        Err(error) => return Err(error),
    };
    let mut displays = BTreeSet::new();
    entries(&temporary, uid, false, &mut displays)?;
    if let Some(sockets) = &sockets {
        entries(sockets, uid, true, &mut displays)?;
    }
    for display in displays {
        let lock_name = format!(".X{display}-lock");
        let socket_name = format!("X{display}");
        let lock = entry(&temporary, &lock_name)?;
        let socket = match &sockets {
            Some(parent) => entry(parent, &socket_name)?,
            None => None,
        };
        // A mixed-owner pair or unexpected node is not a recoverable stale
        // display. Never follow links, rewrite content or unlink foreign data.
        let lock_meta = lock.as_ref().map(fs::File::metadata).transpose()?;
        let socket_meta = socket.as_ref().map(fs::File::metadata).transpose()?;
        if lock_meta.as_ref().is_some_and(|m| {
            m.uid() != uid
                || !m.is_file()
                || m.nlink() != 1
                || m.mode() & 0o022 != 0
                || m.len() > 32
        }) || socket_meta
            .as_ref()
            .is_some_and(|m| m.uid() != uid || !m.file_type().is_socket() || m.nlink() != 1)
        {
            return Err(denied("unowned or unsafe X11 display nodes"));
        }
        if let Some(meta) = &lock_meta {
            let file = fs::File::from(rustix::fs::openat(
                &temporary,
                lock_name.as_str(),
                OFlags::RDONLY | OFlags::NOFOLLOW | OFlags::NONBLOCK | OFlags::CLOEXEC,
                Mode::empty(),
            )?);
            let opened = file.metadata()?;
            if opened.dev() != meta.dev() || opened.ino() != meta.ino() {
                return Err(denied("X11 lock changed before read"));
            }
            let mut raw = String::new();
            file.take(33).read_to_string(&mut raw)?;
            if !dead_pid(&raw)? {
                return Err(denied("X11 lock still identifies a live process"));
            }
        }
        if let (Some(parent), Some(meta)) = (&sockets, &socket_meta) {
            if unchanged(parent, &socket_name, meta)? {
                rustix::fs::unlinkat(parent, socket_name.as_str(), AtFlags::empty())?;
            }
        }
        if let Some(meta) = &lock_meta {
            if unchanged(&temporary, &lock_name, meta)? {
                rustix::fs::unlinkat(&temporary, lock_name.as_str(), AtFlags::empty())?;
            }
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn only_canonical_x11_names_and_bounded_pids() {
        for value in ["0", "10", "9999"] {
            assert_eq!(number(&format!("X{value}"), true), value.parse().ok());
            assert_eq!(
                number(&format!(".X{value}-lock"), false),
                value.parse().ok()
            );
        }
        for value in ["", "00", "01", "-1", "10000", "../1", "2/x", "2\n"] {
            assert!(number(&format!("X{value}"), true).is_none());
            assert!(number(&format!(".X{value}-lock"), false).is_none());
        }
        for value in [
            "",
            "1",
            "0",
            "-1",
            "2/3",
            "2\n3",
            "99999999999999999999999999999999999",
        ] {
            assert!(dead_pid(value).is_err());
        }
        assert!(!dead_pid(&std::process::id().to_string()).unwrap());
    }
}
