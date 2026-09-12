//! Local SAM lifecycle backed by a separate SYSTEM-only ownership journal.
//! Never infer ownership from the account name or the ComputerV2 registry.
use super::*;
use serde::{Deserialize, Serialize};
use std::time::{SystemTime, UNIX_EPOCH};
use windows_sys::Win32::NetworkManagement::NetManagement::*;

const ACCOUNT_ROOT: &str = "VCWorkspace.AgentAccountsV2";
const MARKER_PREFIX: &str = "VCWorkspace-Agent-";
mod lease;
pub(crate) mod native;

#[derive(Clone, Copy, PartialEq, Eq)]
enum Namespace {
    Agent,
    Native,
}

impl Namespace {
    fn validate(self, name: &str) -> io::Result<()> {
        pipe_name(name, 0)?;
        let prefix = match self {
            Self::Agent => "vca",
            Self::Native => "vcw",
        };
        if !name.starts_with(prefix) {
            return Err(denied("account namespace mismatch"));
        }
        Ok(())
    }

    fn marker(self) -> &'static str {
        match self {
            Self::Agent => MARKER_PREFIX,
            Self::Native => "VCWorkspace-Native-",
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AgentAccount {
    pub username: String,
    pub sid: String,
    pub disabled: bool,
}

/// Read-only, journal-bound SAM/WTS observation. A missing SAM account retains
/// its immutable SID; it is not permission to adopt a namesake or erase a logon.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AgentAccountObservation {
    pub username: String,
    pub sid: String,
    pub disabled: Option<bool>,
    pub expires_unix_seconds: Option<u32>,
    pub sessions: Vec<Identity>,
    pub lifecycle: Option<vc_workspace_guest_lifecycle::Fence>,
}

#[derive(Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct Record {
    schema_version: u8,
    username: String,
    nonce: String,
    // None is an interrupted creation, not permission to adopt an existing user.
    sid: Option<String>,
}

fn agent_name(name: &str) -> io::Result<()> {
    Namespace::Agent.validate(name)
}

fn parse_record(bytes: &[u8], name: &str) -> io::Result<Record> {
    if bytes.len() > 4096 {
        return Err(denied("account journal exceeds limit"));
    }
    let record: Record = serde_json::from_slice(bytes).map_err(io::Error::other)?;
    if record.schema_version != 1
        || record.username != name
        || record.nonce.len() != 32
        || !record
            .nonce
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
        || record
            .sid
            .as_ref()
            .is_some_and(|sid| !valid_account_sid(sid) || sid == SYSTEM)
    {
        return Err(denied("invalid account journal identity"));
    }
    Ok(record)
}

fn write_record(key: &Key, record: &Record) -> io::Result<()> {
    write_value(
        key,
        "Account",
        &serde_json::to_vec(record).map_err(io::Error::other)?,
    )?;
    // Persist the creation marker BEFORE NetUserAdd. Registry/SAM are not a
    // distributed transaction; a retry proves the same marker before binding.
    status(unsafe { RegFlushKey(key.0) })
}

// The first named-pipe instance is an exclusive, crash-released kernel gate.
// It carries no protocol/data. A squatter causes denial, never lock bypass.
fn account_gate(name: &str) -> io::Result<OwnedHandle> {
    require_system()?;
    pipe_name(name, 0)?;
    let mut server = null_mut();
    status(unsafe { NetServerGetInfo(null(), 101, &mut server) })?;
    let _server = NetBuffer(server);
    if server.is_null()
        || unsafe { (*server.cast::<SERVER_INFO_101>()).sv101_type }
            & (SV_TYPE_DOMAIN_CTRL | SV_TYPE_DOMAIN_BAKCTRL)
            != 0
    {
        return Err(denied(
            "local SAM lifecycle cannot run on a domain controller",
        ));
    }
    let descriptor = descriptor(SYSTEM, SYSTEM)?;
    let attributes = SECURITY_ATTRIBUTES {
        nLength: size_of::<SECURITY_ATTRIBUTES>() as u32,
        lpSecurityDescriptor: descriptor.0,
        bInheritHandle: 0,
    };
    own(unsafe {
        CreateNamedPipeW(
            wide(&format!(r"\\.\pipe\VCWorkspace.AccountV2.{name}")).as_ptr(),
            PIPE_ACCESS_DUPLEX | FILE_FLAG_FIRST_PIPE_INSTANCE,
            PIPE_TYPE_BYTE | PIPE_REJECT_REMOTE_CLIENTS,
            1,
            0,
            0,
            0,
            &attributes,
        )
    })
}

struct NetBuffer(*mut u8);
impl Drop for NetBuffer {
    fn drop(&mut self) {
        unsafe {
            NetApiBufferFree(self.0.cast());
        }
    }
}

struct LocalAccount {
    account: AgentAccount,
    comment: String,
    flags: u32,
    expires_unix_seconds: u32,
}

fn login_window_open(expiry: u32, now: u64) -> bool {
    expiry != u32::MAX && u64::from(expiry) > now // TIMEQ_FOREVER is not bounded.
}

fn unix_seconds() -> io::Result<u64> {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|value| value.as_secs())
        .map_err(io::Error::other)
}

// Read only pointers documented as NUL-terminated within a live NetAPI buffer.
unsafe fn net_string(pointer: *const u16, max: usize) -> io::Result<String> {
    if pointer.is_null() {
        return Ok(String::new());
    }
    for size in 0..=max {
        if unsafe { *pointer.add(size) } == 0 {
            return String::from_utf16(unsafe { std::slice::from_raw_parts(pointer, size) })
                .map_err(io::Error::other);
        }
    }
    Err(denied("local account string exceeds limit"))
}

fn local(name: &str) -> io::Result<Option<LocalAccount>> {
    pipe_name(name, 0)?;
    let mut raw = null_mut();
    let code = unsafe { NetUserGetInfo(null(), wide(name).as_ptr(), 4, &mut raw) };
    if code == NERR_UserNotFound {
        return Ok(None);
    }
    status(code)?;
    let _allocation = NetBuffer(raw);
    if raw.is_null() {
        return Err(denied("missing local account information"));
    }
    // One SAM snapshot supplies the SID, flags and exact expiry. The returned
    // password pointer is documented as NULL; never read or log that field.
    let info = unsafe { &*raw.cast::<USER_INFO_4>() };
    let username = unsafe { net_string(info.usri4_name, 20) }?;
    let comment = unsafe { net_string(info.usri4_comment, 256) }?;
    let sid = sid_string(info.usri4_user_sid)?;
    if username != name
        || !valid_account_sid(&sid)
        || sid == SYSTEM
        || info.usri4_flags & UF_NORMAL_ACCOUNT == 0
    {
        return Err(denied("managed local SAM user required"));
    }
    Ok(Some(LocalAccount {
        account: AgentAccount {
            username,
            sid,
            disabled: info.usri4_flags & UF_ACCOUNTDISABLE != 0,
        },
        comment,
        flags: info.usri4_flags,
        expires_unix_seconds: info.usri4_acct_expires,
    }))
}

fn create_local(name: &str, marker: &str) -> io::Result<()> {
    let mut nonce = [0u8; 32];
    getrandom::fill(&mut nonce).map_err(io::Error::other)?;
    let mut password: Vec<u16> = "Vcw1!".encode_utf16().collect();
    for byte in nonce {
        for value in [byte >> 4, byte & 15] {
            password.push(b"0123456789abcdef"[value as usize] as u16);
        }
    }
    password.push(0);
    for value in &mut nonce {
        unsafe { std::ptr::write_volatile(value, 0) };
    }
    let mut username = wide(name);
    let mut comment = wide(marker);
    let info = USER_INFO_1 {
        usri1_name: username.as_mut_ptr(),
        usri1_password: password.as_mut_ptr(),
        usri1_comment: comment.as_mut_ptr(),
        usri1_priv: USER_PRIV_USER,
        usri1_flags: UF_NORMAL_ACCOUNT | UF_SCRIPT | UF_ACCOUNTDISABLE,
        ..Default::default()
    };
    let code = unsafe { NetUserAdd(null(), 1, (&info as *const USER_INFO_1).cast(), null_mut()) };
    // Best-effort wipe of our mutable buffer; no password is persisted/returned.
    for value in &mut password {
        unsafe { std::ptr::write_volatile(value, 0) };
    }
    status(code)
}

fn add_group(account_sid: &str, group_sid: &str) -> io::Result<()> {
    let mut group = null_mut();
    check(unsafe { ConvertStringSidToSidW(wide(group_sid).as_ptr(), &mut group) })?;
    let _group = LocalAllocation(group);
    let mut name = [0u16; 257];
    let mut domain = [0u16; 257];
    let (mut name_len, mut domain_len, mut kind) = (257, 257, 0);
    check(unsafe {
        LookupAccountSidW(
            null(),
            group,
            name.as_mut_ptr(),
            &mut name_len,
            domain.as_mut_ptr(),
            &mut domain_len,
            &mut kind,
        )
    })?;
    if kind != SidTypeAlias {
        return Err(denied("built-in local group required"));
    }
    let mut sid = null_mut();
    check(unsafe { ConvertStringSidToSidW(wide(account_sid).as_ptr(), &mut sid) })?;
    let _sid = LocalAllocation(sid);
    let member = LOCALGROUP_MEMBERS_INFO_0 { lgrmi0_sid: sid };
    let code = unsafe {
        NetLocalGroupAddMembers(
            null(),
            name.as_ptr(),
            0,
            (&member as *const LOCALGROUP_MEMBERS_INFO_0).cast(),
            1,
        )
    };
    if code == ERROR_MEMBER_IN_ALIAS {
        return Ok(());
    }
    status(code)
}

/// Persistent account owner, separate from temporary authority/WTS identity.
/// No arbitrary path, account import, deletion or credentials in this API.
pub struct AgentAccounts {
    root: String,
    namespace: Namespace,
}
impl Default for AgentAccounts {
    fn default() -> Self {
        Self {
            root: ACCOUNT_ROOT.into(),
            namespace: Namespace::Agent,
        }
    }
}

/// Shared by the SYSTEM service loop and explicit reconciliation command.
/// Evaluate both namespaces before propagating either error; a busy/corrupt
/// Agent record must not suppress expired Native account cleanup, or vice versa.
pub fn reconcile_accounts() -> io::Result<usize> {
    let agents = AgentAccounts::default().reconcile_expired();
    let native = native::NativeAccounts::default().reconcile_expired();
    Ok(agents? + native?)
}

impl AgentAccounts {
    fn key(&self, name: &str, create_missing: bool) -> io::Result<Key> {
        require_system()?;
        self.namespace.validate(name)?;
        let store = AuthorityRegistry {
            root: self.root.clone(),
        };
        let root = store.root(create_missing)?;
        let key = if create_missing {
            create(root.0, name, None)?
        } else {
            open(root.0, name, KEY_ALL_ACCESS)?
        };
        verify_security(&key, None)?;
        Ok(key)
    }

    fn record(&self, key: &Key, name: &str) -> io::Result<Option<Record>> {
        match read_value(key, "Account") {
            Err(error) if error.raw_os_error() == Some(ERROR_FILE_NOT_FOUND as i32) => Ok(None),
            Ok((REG_BINARY, bytes)) => parse_record(&bytes, name).map(Some),
            Ok(_) => Err(denied("invalid account journal type")),
            Err(error) => Err(error),
        }
    }

    fn bound(&self, key: &Key, name: &str) -> io::Result<LocalAccount> {
        let record = self
            .record(key, name)?
            .ok_or_else(|| denied("account is not platform owned"))?;
        let actual = local(name)?.ok_or_else(|| denied("bound account was deleted"))?;
        if record.sid.as_ref() != Some(&actual.account.sid) {
            return Err(denied("managed account SID changed or is not yet bound"));
        }
        Ok(actual)
    }

    pub fn verify(&self, name: &str) -> io::Result<AgentAccount> {
        let _gate = account_gate(name)?;
        self.bound(&self.key(name, false)?, name).map(|a| a.account)
    }

    pub fn observe(&self, name: &str) -> io::Result<AgentAccountObservation> {
        agent_name(name)?;
        let _gate = account_gate(name)?;
        let key = self.key(name, false)?;
        let sid = self
            .record(&key, name)?
            .and_then(|record| record.sid)
            .ok_or_else(|| denied("account observation requires a bound journal SID"))?;
        let before = local(name)?;
        let lifecycle = lease::read_fence(&key, name, &sid)?;
        if before
            .as_ref()
            .is_some_and(|actual| actual.account.sid != sid)
        {
            return Err(denied("observed SAM identity differs from owned SID"));
        }
        let sessions = sessions_for_sid(&sid)?;
        let after = local(name)?;
        // The gate serializes platform writes. Refuse an inconsistent snapshot
        // if an external administrator changes SAM while WTS is enumerated.
        if account_snapshot(&before) != account_snapshot(&after)
            || lease::read_fence(&key, name, &sid)? != lifecycle
            || self
                .record(&key, name)?
                .and_then(|record| record.sid)
                .as_ref()
                != Some(&sid)
        {
            return Err(denied("account changed during SAM/WTS observation"));
        }
        Ok(AgentAccountObservation {
            username: name.into(),
            sid,
            disabled: after.as_ref().map(|actual| actual.account.disabled),
            expires_unix_seconds: after.as_ref().map(|actual| actual.expires_unix_seconds),
            sessions,
            lifecycle,
        })
    }

    // Keep the lifecycle gate through Helper startup/handshake. Disable cannot
    // acknowledge between checking this account and launching its replacement.
    pub(crate) fn lock_enabled(&self, name: &str) -> io::Result<(AgentAccount, OwnedHandle)> {
        agent_name(name)?;
        let gate = account_gate(name)?;
        let key = self.key(name, false)?;
        let actual = self.bound(&key, name)?;
        let now = unix_seconds()?;
        if actual.account.disabled
            || !login_window_open(actual.expires_unix_seconds, now)
            || lease::read_fence(&key, name, &actual.account.sid)?.is_some_and(|fence| {
                !fence.login_live(now)
                    || fence.expires_unix_seconds != u64::from(actual.expires_unix_seconds)
            })
        {
            return Err(denied(
                "disabled or expired Agent account cannot start a Helper",
            ));
        }
        Ok((actual.account, gate))
    }

    pub fn provision(&self, name: &str) -> io::Result<AgentAccount> {
        let _gate = account_gate(name)?;
        let key = self.key(name, true)?;
        let mut actual = local(name)?;
        let mut record = match self.record(&key, name)? {
            Some(record) => record,
            None => {
                if actual.is_some() {
                    return Err(denied("existing account is not platform owned"));
                }
                let mut nonce = [0; 16];
                getrandom::fill(&mut nonce).map_err(io::Error::other)?;
                let record = Record {
                    schema_version: 1,
                    username: name.into(),
                    nonce: nonce.iter().map(|b| format!("{b:02x}")).collect(),
                    sid: None,
                };
                write_record(&key, &record)?;
                record
            }
        };
        if record.sid.is_none() {
            let marker = format!("{}{}", self.namespace.marker(), record.nonce);
            if actual.is_none() {
                create_local(name, &marker)?;
                actual = local(name)?;
            }
            let actual = actual.ok_or_else(|| denied("account creation not visible"))?;
            if actual.comment != marker || !actual.account.disabled {
                return Err(denied("interrupted account creation ownership mismatch"));
            }
            record.sid = Some(actual.account.sid);
            write_record(&key, &record)?;
        }
        let actual = self.bound(&key, name)?;
        // Group SIDs avoid localized names; no Administrators membership here.
        add_group(&actual.account.sid, "S-1-5-32-545")?;
        add_group(&actual.account.sid, "S-1-5-32-555")?;
        Ok(actual.account)
    }

    /// Called only after the control plane has rotated an owned password.
    /// Account expiry bounds future login, not existing desktop process lifetime.
    pub fn enable(&self, name: &str, expires_unix_seconds: u32) -> io::Result<AgentAccount> {
        agent_name(name)?;
        let _gate = account_gate(name)?;
        let key = self.key(name, false)?;
        lease::reject_legacy(&key)?;
        self.enable_locked(&key, name, expires_unix_seconds)
    }

    fn enable_locked(
        &self,
        key: &Key,
        name: &str,
        expires_unix_seconds: u32,
    ) -> io::Result<AgentAccount> {
        let actual = self.bound(key, name)?;
        let now = unix_seconds()?;
        if u64::from(expires_unix_seconds) <= now
            || u64::from(expires_unix_seconds) > now + 28_800
            || actual.flags & (UF_PASSWD_NOTREQD | UF_PASSWORD_EXPIRED) != 0
        {
            return Err(denied(
                "bounded login expiry and non-expired password required",
            ));
        }
        let expiry = USER_INFO_1017 {
            usri1017_acct_expires: expires_unix_seconds,
        };
        status(unsafe {
            NetUserSetInfo(
                null(),
                wide(name).as_ptr(),
                1017,
                (&expiry as *const USER_INFO_1017).cast(),
                null_mut(),
            )
        })?;
        self.bound(key, name)?;
        let flags = USER_INFO_1008 {
            usri1008_flags: actual.flags & !UF_ACCOUNTDISABLE,
        };
        status(unsafe {
            NetUserSetInfo(
                null(),
                wide(name).as_ptr(),
                1008,
                (&flags as *const USER_INFO_1008).cast(),
                null_mut(),
            )
        })?;
        self.bound(key, name).map(|a| a.account)
    }

    pub fn disable(&self, name: &str) -> io::Result<Option<AgentAccount>> {
        agent_name(name)?;
        let _gate = account_gate(name)?;
        match self.key(name, false) {
            Ok(key) => lease::reject_legacy(&key)?,
            Err(error) if error.raw_os_error() == Some(ERROR_FILE_NOT_FOUND as i32) => (),
            Err(error) => return Err(error),
        }
        self.disable_locked(name)
    }

    /// Local expiry enforcement, independent of control-plane reachability.
    /// Enumerate only the protected ownership journal, not matching SAM names.
    /// A failed record does not prevent other proven accounts from converging.
    /// No account deletion, enablement, password rotation or new login occurs.
    pub fn reconcile_expired(&self) -> io::Result<usize> {
        self.reconcile_records(|name| self.expire(name))
    }

    fn reconcile_records(&self, expire: impl Fn(&str) -> io::Result<bool>) -> io::Result<usize> {
        require_system()?;
        let root = match (AuthorityRegistry {
            root: self.root.clone(),
        })
        .root(false)
        {
            Err(error) if error.raw_os_error() == Some(ERROR_FILE_NOT_FOUND as i32) => {
                return Ok(0)
            }
            result => result?,
        };
        let mut names = Vec::new();
        for index in 0..=1024 {
            let mut name = [0u16; 256];
            let mut length = name.len() as u32;
            // Read-only bounded enumeration; every candidate is independently
            // authenticated under its lifecycle gate before any SAM/WTS write.
            let code = unsafe {
                RegEnumKeyExW(
                    root.0,
                    index,
                    name.as_mut_ptr(),
                    &mut length,
                    null(),
                    null_mut(),
                    null_mut(),
                    null_mut(),
                )
            };
            if code == ERROR_NO_MORE_ITEMS {
                break;
            }
            status(code)?;
            if index == 1024 || length as usize >= name.len() {
                return Err(denied("too many or invalid account records"));
            }
            names.push(String::from_utf16(&name[..length as usize]).map_err(io::Error::other)?);
        }
        let mut changed = 0;
        let mut first_error = None;
        for name in names {
            match expire(&name) {
                Ok(true) => changed += 1,
                Ok(false) => {}
                Err(error) => {
                    first_error.get_or_insert(error);
                }
            }
        }
        first_error.map_or(Ok(changed), Err)
    }

    fn expire(&self, name: &str) -> io::Result<bool> {
        agent_name(name)?;
        let _gate = account_gate(name)?;
        let key = self.key(name, false)?;
        let record = self
            .record(&key, name)?
            .ok_or_else(|| denied("missing account journal"))?;
        let actual = local(name)?;
        if actual
            .as_ref()
            .is_some_and(|actual| record.sid.as_ref() != Some(&actual.account.sid))
        {
            // An invalid lifecycle value never permits touching an unbound or
            // reused SID, even when its username matches our private record.
            return Err(denied("expiry account SID is not bound"));
        }
        let lifecycle = match record.sid.as_deref() {
            Some(sid) => match lease::read_fence(&key, name, sid) {
                Ok(fence) => fence,
                Err(error) => {
                    // Malformed history cannot keep an owned SAM/WTS logon
                    // alive forever. Close the exact identity, but preserve
                    // the corrupt journal and return failure for diagnosis.
                    self.disable_locked(name)?;
                    return Err(error);
                }
            },
            None => None,
        };
        let now = unix_seconds()?;
        if let Some(actual) = actual {
            if !actual.account.disabled
                && login_window_open(actual.expires_unix_seconds, now)
                && lifecycle.as_ref().is_none_or(|fence| {
                    fence.login_live(now)
                        && fence.expires_unix_seconds == u64::from(actual.expires_unix_seconds)
                })
            {
                return Ok(false);
            }
        }
        // Includes disabled accounts with a still-live WTS logon, and a deleted
        // SAM identity whose immutable old SID still owns a retained session.
        if let Some(fence) = &lifecycle {
            lease::revoke_record(&key, fence)?;
        }
        self.disable_locked(name)?;
        Ok(true)
    }

    fn disable_locked(&self, name: &str) -> io::Result<Option<AgentAccount>> {
        let key = match self.key(name, false) {
            Err(error) if error.raw_os_error() == Some(ERROR_FILE_NOT_FOUND as i32) => {
                // Bootstrap can fail before creating either the journal or the
                // SAM account. Nothing to disable is a converged cleanup, but an
                // existing unowned namesake is never touched.
                return if local(name)?.is_none() {
                    Ok(None)
                } else {
                    Err(denied("existing account is not platform owned"))
                };
            }
            result => result?,
        };
        let mut record = self.record(&key, name)?;
        let actual = local(name)?;
        if actual.is_none() {
            // A deleted SAM user can still have a retained WTS logon. Revoke
            // only the journal's immutable SID; never erase/rebind its record.
            if let Some(sid) = record.and_then(|record| record.sid) {
                logoff_sid(&sid)?;
            }
            return Ok(None);
        }
        let pending = record
            .as_mut()
            .ok_or_else(|| denied("existing account is not platform owned"))?;
        if pending.sid.is_none() {
            let actual = actual.ok_or_else(|| denied("account disappeared"))?;
            if !actual.account.disabled
                || actual.comment != format!("{}{}", self.namespace.marker(), pending.nonce)
            {
                return Err(denied("interrupted account creation ownership mismatch"));
            }
            pending.sid = Some(actual.account.sid);
            write_record(&key, pending)?;
        }
        let disabled = self.disable_login_locked(&key, name)?;
        logoff_sid(&disabled.sid)?;
        Ok(Some(disabled))
    }

    fn disable_login_locked(&self, key: &Key, name: &str) -> io::Result<AgentAccount> {
        let actual = self.bound(key, name)?;
        let flags = USER_INFO_1008 {
            usri1008_flags: actual.flags | UF_ACCOUNTDISABLE,
        };
        status(unsafe {
            NetUserSetInfo(
                null(),
                wide(name).as_ptr(),
                1008,
                (&flags as *const USER_INFO_1008).cast(),
                null_mut(),
            )
        })?;
        let disabled = self.bound(key, name)?;
        if !disabled.account.disabled {
            return Err(denied("account disable did not converge"));
        }
        Ok(disabled.account)
    }
}

fn session_may_own_token(id: u32, state: WTS_CONNECTSTATE_CLASS) -> bool {
    // Only a listener is documented to have no logged-on user. Transitional
    // (reset/connecting/etc.) and shadow states are not proof of absence:
    // inspect their actual token and bind SID/WTS/LUID before any action.
    id != 0 && state != WTSListen
}

fn account_snapshot(account: &Option<LocalAccount>) -> Option<(&AgentAccount, u32, u32)> {
    account
        .as_ref()
        .map(|actual| (&actual.account, actual.expires_unix_seconds, actual.flags))
}

fn sessions_for_sid(sid: &str) -> io::Result<Vec<Identity>> {
    let mut sessions = null_mut();
    let mut count = 0;
    check(unsafe { WTSEnumerateSessionsW(null_mut(), 0, 1, &mut sessions, &mut count) })?;
    let _allocation = WtsAllocation(sessions);
    if count > 1024 || (count > 0 && sessions.is_null()) {
        return Err(denied("invalid WTS list"));
    }
    let mut result = Vec::new();
    for index in 0..count as usize {
        let session = unsafe { &*sessions.add(index) };
        if !session_may_own_token(session.SessionId, session.State) {
            continue;
        }
        let mut token = null_mut();
        if unsafe { WTSQueryUserToken(session.SessionId, &mut token) } == 0 {
            let error = io::Error::last_os_error();
            if logon_disappeared(&error) {
                continue;
            }
            return Err(error);
        }
        let token = own(token)?;
        let identity = token_identity(token.as_raw_handle())?;
        if identity.sid == sid && identity.session_id == session.SessionId {
            result.push(identity);
        }
    }
    Ok(result)
}

fn logoff_sid(sid: &str) -> io::Result<()> {
    // WTSLogoffSession(FALSE) only acknowledges dispatch. Shell teardown can
    // take longer than a desktop action; do not repeatedly send logoff to the
    // same incarnation while observing it. Keep the account gate throughout.
    let deadline = Instant::now() + Duration::from_secs(30);
    let mut requested = Vec::new();
    loop {
        let sessions = sessions_for_sid(sid)?;
        if sessions.is_empty() {
            return Ok(());
        }
        if Instant::now() >= deadline {
            return Err(io::Error::new(
                io::ErrorKind::TimedOut,
                "owned WTS logoff has not converged",
            ));
        }
        dispatch_logoffs_once(&sessions, &mut requested, dispatch_logoff)?;
        thread::sleep(Duration::from_millis(100));
    }
}

fn logon_disappeared(error: &io::Error) -> bool {
    [
        Some(ERROR_NO_TOKEN as i32),
        Some(ERROR_CTX_WINSTATION_NOT_FOUND as i32),
    ]
    .contains(&error.raw_os_error())
}

fn dispatch_logoff(identity: &Identity) -> io::Result<bool> {
    let mut token = null_mut();
    if unsafe { WTSQueryUserToken(identity.session_id, &mut token) } == 0 {
        let error = io::Error::last_os_error();
        return if logon_disappeared(&error) {
            Ok(false)
        } else {
            Err(error)
        };
    }
    let token = own(token)?;
    // Recheck the kernel logon identity immediately before dispatch. Never
    // trust a recycled WTS number or interpret access denied as session absence.
    if token_identity(token.as_raw_handle())? != *identity {
        return Ok(false);
    }
    if unsafe { WTSLogoffSession(null_mut(), identity.session_id, 0) } == 0 {
        let error = io::Error::last_os_error();
        if logon_disappeared(&error) {
            return Ok(false);
        }
        return Err(error);
    }
    Ok(true)
}

fn dispatch_logoffs_once(
    sessions: &[Identity],
    requested: &mut Vec<Identity>,
    mut dispatch: impl FnMut(&Identity) -> io::Result<bool>,
) -> io::Result<()> {
    for identity in sessions {
        if requested.contains(identity) {
            continue;
        }
        if requested.len() >= 1024 {
            return Err(denied("owned logoff incarnation limit exceeded"));
        }
        if dispatch(identity)? {
            requested.push(identity.clone());
        }
    }
    Ok(())
}

#[cfg(test)]
pub(crate) mod tests;
