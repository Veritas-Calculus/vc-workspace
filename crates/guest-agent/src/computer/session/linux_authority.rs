//! Guest-side publication fence. Separate from the retired shared temporary
//! file so a still-running old executable cannot overwrite the new authority.
use super::linux::{
    discover_target, initialize_control, require_root, root_file, trusted_root_path, BASE,
};
use super::*;
use rustix::fs::{flock, FlockOperation, Mode, OFlags};
use std::os::unix::fs::{MetadataExt, OpenOptionsExt, PermissionsExt};

pub(super) const TRANSPORT: &str = "stdin_epoch_account_v2";
pub(super) const SNAPSHOT: &str = "authority-account-fenced.json";

fn record(raw: &[u8]) -> io::Result<(BoundAuthority, Authority)> {
    if raw.is_empty() || raw.len() > MAX_REQUEST {
        return Err(denied("authority exceeds limit"));
    }
    let bound: BoundAuthority = serde_json::from_slice(raw).map_err(io::Error::other)?;
    let authority: Authority =
        serde_json::from_value(bound.authority.clone()).map_err(io::Error::other)?;
    if bound.schema_version != VERSION
        || authority.schema_version != SCHEMA_VERSION
        || authority.control_epoch < 1
        || authority.lease_id.len() > 128
        || !authority.lease_id.starts_with("lease_")
        || !authority
            .lease_id
            .bytes()
            .all(|c| c.is_ascii_alphanumeric() || matches!(c, b'_' | b'-'))
        || !matches!(authority.state.as_str(), "active" | "revoked")
        || (authority.state == "active"
            && (authority.expires_unix_ms <= 0
                || !bound
                    .target
                    .as_ref()
                    .is_some_and(|key| valid_key(key) && key.sid.is_empty())))
        || (authority.state == "revoked" && bound.target.is_some())
    {
        return Err(denied("invalid Linux bound authority"));
    }
    Ok((bound, authority))
}

fn fresh(authority: &Authority) -> io::Result<()> {
    let now = unix_millis();
    if (authority.state == "active" && authority.expires_unix_ms <= now)
        || (authority.state == "revoked" && authority.expires_unix_ms > now)
    {
        return Err(denied("invalid Linux authority deadline"));
    }
    Ok(())
}

fn transition(raw: &[u8], next: &BoundAuthority, authority: &Authority) -> io::Result<()> {
    // Expiry never erases a previously committed ordering fence.
    let (old_bound, old) = record(raw)?;
    if authority.control_epoch < old.control_epoch {
        return Err(denied("authority epoch cannot move backwards"));
    }
    if authority.control_epoch == old.control_epoch && authority.state == "active" {
        let same_user = old_bound
            .target
            .as_ref()
            .zip(next.target.as_ref())
            .is_some_and(|(old, next)| {
                old.username == next.username && old.uid == next.uid && old.sid == next.sid
            });
        if old.state == "revoked"
            || old.lease_id != authority.lease_id
            || !same_user
            || authority.expires_unix_ms < old.expires_unix_ms
        {
            return Err(denied(
                "revoked epoch, changed lease/owner or regressed expiry",
            ));
        }
    }
    Ok(())
}

struct Staged(PathBuf);
impl Drop for Staged {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.0);
    }
}

pub(super) fn publish() -> io::Result<()> {
    require_root()?;
    let mut raw = Vec::new();
    io::stdin()
        .take(MAX_REQUEST as u64 + 1)
        .read_to_end(&mut raw)?;
    let (mut bound, authority) = record(&raw)?;
    if bound.login_generation.is_some() {
        return Err(denied("account login version is derived by the Guest"));
    }
    fresh(&authority)?;
    initialize_control()?;
    let base = Path::new(BASE);
    trusted_root_path(base)?;
    // This inode is never unlinked or replaced. Conflicting publishers fail
    // before reading the old snapshot; no stale read can later be committed.
    let lock = fs::File::from(rustix::fs::open(
        base.join("authority.lock"),
        OFlags::RDWR | OFlags::CREATE | OFlags::NOFOLLOW | OFlags::NONBLOCK | OFlags::CLOEXEC,
        Mode::RUSR | Mode::WUSR,
    )?);
    let metadata = lock.metadata()?;
    if !metadata.is_file()
        || metadata.uid() != 0
        || metadata.mode() & 0o777 != 0o600
        || metadata.nlink() != 1
    {
        return Err(denied("unsafe authority lock"));
    }
    flock(&lock, FlockOperation::NonBlockingLockExclusive)?;
    // The exclusive lock proves no new-protocol publisher still owns these
    // private staging files. Reclaim only our exact namespace after a crash.
    for entry in fs::read_dir(base.join("inbox"))?.take(1024) {
        let entry = entry?;
        let name = entry.file_name();
        let Some(nonce) = name
            .to_str()
            .and_then(|name| name.strip_prefix(".authority-"))
            .and_then(|name| name.strip_suffix(".tmp"))
        else {
            continue;
        };
        if nonce.len() != 32
            || !nonce
                .bytes()
                .all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c))
        {
            continue;
        }
        let meta = fs::symlink_metadata(entry.path())?;
        if !meta.is_file()
            || meta.uid() != 0
            || meta.nlink() != 1
            || meta.mode() & 0o022 != 0
            || meta.len() > MAX_REQUEST as u64
        {
            return Err(denied("unsafe abandoned authority staging"));
        }
        fs::remove_file(entry.path())?;
    }
    let path = base.join(SNAPSHOT);
    match root_file(&path, MAX_REQUEST) {
        Ok(previous) => transition(&previous, &bound, &authority)?,
        Err(error) if error.kind() == io::ErrorKind::NotFound => {
            for legacy in ["authority-fenced.json", "authority.json"] {
                match root_file(&base.join(legacy), MAX_REQUEST) {
                    Ok(previous) => {
                        if authority.state != "revoked" {
                            return Err(denied(
                                "unfenced authority must be revoked before activation",
                            ));
                        }
                        transition(&previous, &bound, &authority)?;
                    }
                    Err(error) if error.kind() == io::ErrorKind::NotFound => (),
                    Err(error) => return Err(error),
                }
            }
        }
        Err(error) => return Err(error),
    }
    if let Some(target) = &bound.target {
        if discover_target(&target.username)? != *target {
            return Err(denied("Helper changed before authorization"));
        }
    }
    let account_guard = match &bound.target {
        Some(target) => super::account::bind_authority(target, &authority)?,
        None => None,
    };
    bound.login_generation = account_guard.as_ref().map(|(_, generation)| *generation);
    fresh(&authority)?;
    let mut nonce = [0u8; 16];
    getrandom::fill(&mut nonce).map_err(io::Error::other)?;
    let name: String = nonce.iter().map(|b| format!("{b:02x}")).collect();
    let temporary = base.join("inbox").join(format!(".authority-{name}.tmp"));
    let mut file = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(&temporary)?;
    let staging = Staged(temporary);
    file.write_all(&serde_json::to_vec(&bound).map_err(io::Error::other)?)?;
    file.set_permissions(fs::Permissions::from_mode(0o644))?;
    file.sync_all()?;
    fresh(&authority)?;
    fs::rename(&staging.0, &path)?;
    fs::File::open(base)?.sync_all()?;
    fs::File::open(base.join("inbox"))?.sync_all()?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn grant() -> serde_json::Value {
        serde_json::json!({"schema_version":2,"target":{
            "username":"vca0123456789ab","uid":1001,"session_id":"linux:1001:123::10","instance_id":"a".repeat(64)
        },"authority":{"schema_version":1,"lease_id":"lease_a-Z_012","control_epoch":10,"state":"active","expires_unix_ms":unix_millis()+30_000}})
    }

    #[test]
    fn accepts_urlsafe_lease_ids_but_not_paths_or_whitespace() {
        let mut value = grant();
        assert!(record(&serde_json::to_vec(&value).unwrap()).is_ok());
        for bad in [
            "lease_a/b",
            "lease_a b",
            "lease_a\n",
            "lease_é",
            "other_token",
        ] {
            value["authority"]["lease_id"] = bad.into();
            assert!(record(&serde_json::to_vec(&value).unwrap()).is_err());
        }
    }

    #[test]
    fn expired_or_corrupt_history_never_resets_the_fence() {
        let next = grant();
        let (bound, authority) = record(&serde_json::to_vec(&next).unwrap()).unwrap();
        let mut previous = next;
        previous["authority"]["expires_unix_ms"] = 1.into();
        previous["authority"]["control_epoch"] = 11.into();
        assert!(transition(&serde_json::to_vec(&previous).unwrap(), &bound, &authority).is_err());
        for corrupt in [b"".as_slice(), b"{}", b"{", b"null"] {
            assert!(transition(corrupt, &bound, &authority).is_err());
        }
    }
}
