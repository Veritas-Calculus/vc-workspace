//! Atomically publish the private VM ordering fence and every user's view.
//! Epoch/lease semantics belong to the protocol caller, not this byte store.
use super::*;

pub struct AuthorityUpdate {
    transaction: OwnedHandle,
    root: Key,
    users: Vec<(String, String, Key)>,
    touched: bool,
    staged: bool,
    committed: bool,
}

impl Drop for AuthorityUpdate {
    fn drop(&mut self) {
        if !self.committed {
            // SAFETY: this transaction is private and non-inheritable; rollback
            // also handles a dropped/failed preparation. It may already have
            // been aborted by KTM's timeout or conflict detection.
            unsafe { RollbackTransaction(self.transaction.as_raw_handle()) };
        }
    }
}

// Reopen an already verified key object, not its path. RegOpenKeyTransacted has
// no OPEN_LINK option; looking up the original string again would lose the
// non-following open's identity. The empty subkey returns a new owned handle.
fn attach(key: &Key, transaction: &OwnedHandle) -> io::Result<Key> {
    let mut raw = null_mut();
    // SAFETY: borrowed validated key/transaction handles and a writable output;
    // empty subkey has no namespace components or registry link to traverse.
    status(unsafe {
        RegOpenKeyTransactedW(
            key.0,
            wide("").as_ptr(),
            0,
            KEY_ALL_ACCESS | KEY_WOW64_64KEY,
            &mut raw,
            transaction.as_raw_handle(),
            null_mut(),
        )
    })?;
    let key = Key(raw);
    no_link(&key)?;
    Ok(key)
}

fn optional_binary(key: &Key, name: &str) -> io::Result<Option<Vec<u8>>> {
    match read_value(key, name) {
        Err(error) if error.raw_os_error() == Some(ERROR_FILE_NOT_FOUND as i32) => Ok(None),
        Ok((REG_BINARY, value)) if !value.is_empty() => Ok(Some(value)),
        Ok(_) => Err(denied("invalid authority snapshot type")),
        Err(error) => Err(error),
    }
}

impl AuthorityUpdate {
    pub(super) fn begin(store: &AuthorityRegistry) -> io::Result<Self> {
        require_system()?;
        let root = store.root(true)?;
        // SAFETY: unnamed, non-inheritable transaction with a 5-second KTM
        // deadline. No filesystem or third-party resource manager is enlisted.
        let transaction =
            own(unsafe { CreateTransaction(null_mut(), null_mut(), 0, 0, 0, 5000, null()) })?;
        let root = attach(&root, &transaction)?;
        verify_security(&root, None)?;
        let mut update = Self {
            transaction,
            root,
            users: Vec::new(),
            touched: false,
            staged: false,
            committed: false,
        };
        // Acquire write intent BEFORE reading the previous fence. A read then
        // write without this reservation could rely on an isolation level
        // weaker than compare-and-swap. Conflicting publishers fail; callers
        // must start a new transaction and revalidate rather than reuse a read.
        write_value(&update.root, "UpdateLock", b"1")?;
        for (username, sid) in store.users()? {
            let key = open(update.root.0, &username, KEY_ALL_ACCESS)?;
            verify_security(&key, Some(&sid))?;
            verify_sid(&key, &sid)?;
            let key = attach(&key, &update.transaction)?;
            verify_security(&key, Some(&sid))?;
            verify_sid(&key, &sid)?;
            update.users.push((username, sid, key));
        }
        Ok(update)
    }

    pub fn previous(&self) -> io::Result<Option<Vec<u8>>> {
        optional_binary(&self.root, "Fence")
    }

    /// Only used to validate migration from an unfenced experimental registry.
    /// Missing per-user authority still means denied, not a default lease.
    pub fn existing_authorities(&self) -> io::Result<Vec<Vec<u8>>> {
        self.users
            .iter()
            .filter_map(|(_, _, key)| optional_binary(key, "Authority").transpose())
            .collect()
    }

    pub fn stage(
        &mut self,
        target: Option<(&str, &str)>,
        desired: &[u8],
        revoked: &[u8],
    ) -> io::Result<()> {
        let repeated = self.touched;
        self.touched = true;
        self.staged = false;
        if repeated
            || desired.is_empty()
            || revoked.is_empty()
            || desired.len() > MAX_VALUE
            || revoked.len() > MAX_VALUE
        {
            return Err(denied("invalid or repeated authority transaction stage"));
        }
        if let Some((username, sid)) = target {
            identity(username, sid)?;
            if !self
                .users
                .iter()
                .any(|(name, registered, _)| name == username && registered == sid)
            {
                return Err(denied("authority target is not registered"));
            }
        }
        write_value(&self.root, "Fence", desired)?;
        for (username, sid, key) in &self.users {
            let value = if target == Some((username.as_str(), sid.as_str())) {
                desired
            } else {
                revoked
            };
            write_value(key, "Authority", value)?;
        }
        self.staged = true;
        Ok(())
    }

    pub fn commit(mut self) -> io::Result<()> {
        if !self.staged {
            return Err(denied(
                "authority transaction has no complete staged update",
            ));
        }
        require_system()?;
        // SAFETY: only this process owns the non-inherited transaction handle.
        // No successful result is returned for conflict, timeout or rollback.
        check(unsafe { CommitTransaction(self.transaction.as_raw_handle()) })?;
        self.committed = true;
        Ok(())
    }
}
