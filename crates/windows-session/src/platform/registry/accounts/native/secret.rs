//! SYSTEM-user DPAPI, not machine-wide DPAPI. Protected account keys enforce
//! access separately; entropy binds each ciphertext to its immutable owner.
use super::*;
use vc_workspace_guest_lifecycle::Zeroizing;
use windows_sys::Win32::Security::Cryptography::*;

#[derive(Clone, PartialEq, Eq, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub(super) struct Encrypted {
    schema_version: u8,
    revision: u64,
    bytes: Vec<u8>,
}

fn valid_secret(secret: &str) -> bool {
    (24..=256).contains(&secret.len()) && secret.bytes().all(|b| (33..=126).contains(&b))
}

fn entropy(owner: &Record, revision: u64) -> io::Result<Vec<u8>> {
    Namespace::Native.validate(&owner.username)?;
    let sid = owner
        .sid
        .as_deref()
        .ok_or_else(|| denied("secret owner is not SID bound"))?;
    if !valid_account_sid(sid)
        || sid == SYSTEM
        || owner.nonce.len() != 32
        || !owner
            .nonce
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
        || owner.schema_version != 1
        || revision == 0
        || revision > i64::MAX as u64
    {
        return Err(denied("invalid secret owner"));
    }
    serde_json::to_vec(
        &serde_json::json!({"purpose":"VCWorkspace.NativeCredential.V1",
        "username":owner.username,"sid":sid,"nonce":owner.nonce,"revision":revision}),
    )
    .map_err(|_| denied("invalid secret context"))
}

fn transform(input: &[u8], context: &[u8], protect: bool) -> io::Result<Zeroizing<Vec<u8>>> {
    require_system()?;
    if input.is_empty() || input.len() > 4096 || context.is_empty() || context.len() > 4096 {
        return Err(denied("invalid bounded protected secret"));
    }
    let mut input = Zeroizing::new(input.to_vec());
    let mut context = context.to_vec();
    let input = CRYPT_INTEGER_BLOB {
        cbData: input.len() as u32,
        pbData: input.as_mut_ptr(),
    };
    let context = CRYPT_INTEGER_BLOB {
        cbData: context.len() as u32,
        pbData: context.as_mut_ptr(),
    };
    let mut output = CRYPT_INTEGER_BLOB::default();
    let ok = if protect {
        // Omitting CRYPTPROTECT_LOCAL_MACHINE is intentional: other OS users
        // must not decrypt even if they somehow obtain the registry ciphertext.
        unsafe {
            CryptProtectData(
                &input,
                null(),
                &context,
                null(),
                null(),
                CRYPTPROTECT_UI_FORBIDDEN,
                &mut output,
            )
        }
    } else {
        unsafe {
            CryptUnprotectData(
                &input,
                null_mut(),
                &context,
                null(),
                null(),
                CRYPTPROTECT_UI_FORBIDDEN,
                &mut output,
            )
        }
    };
    check(ok)?;
    let _allocation = LocalAllocation(output.pbData.cast());
    if output.pbData.is_null() || output.cbData == 0 || output.cbData > 4096 {
        return Err(denied("invalid protected secret output"));
    }
    let bytes = unsafe { std::slice::from_raw_parts_mut(output.pbData, output.cbData as usize) };
    let result = Zeroizing::new(bytes.to_vec());
    for byte in bytes {
        unsafe { std::ptr::write_volatile(byte, 0) };
    }
    Ok(result)
}

#[cfg_attr(not(test), allow(dead_code))] // Lifecycle activation awaits Profile acceptance.
impl Encrypted {
    pub(super) fn protect(owner: &Record, revision: u64, value: &str) -> io::Result<Self> {
        if !valid_secret(value) {
            return Err(denied("invalid credential secret"));
        }
        let context = entropy(owner, revision)?;
        let ciphertext = transform(value.as_bytes(), &context, true)?;
        Ok(Self {
            schema_version: 1,
            revision,
            bytes: ciphertext.to_vec(),
        })
    }
    pub(super) fn validate(&self, maximum_revision: u64) -> io::Result<()> {
        if self.schema_version != 1
            || self.revision == 0
            || self.revision > maximum_revision
            || self.bytes.is_empty()
            || self.bytes.len() > 4096
        {
            return Err(denied("invalid credential ciphertext"));
        }
        Ok(())
    }
    pub(super) fn unprotect(
        &self,
        owner: &Record,
        maximum_revision: u64,
    ) -> io::Result<Zeroizing<String>> {
        self.validate(maximum_revision)?;
        let context = entropy(owner, self.revision)?;
        let plaintext = transform(&self.bytes, &context, false)?;
        let secret =
            std::str::from_utf8(&plaintext).map_err(|_| denied("invalid recovered credential"))?;
        if !valid_secret(secret) {
            return Err(denied("invalid recovered credential"));
        }
        Ok(Zeroizing::new(secret.to_owned()))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn system_secret_is_owner_bound_and_never_serializes_plaintext() {
        require_system().unwrap();
        let owner = Record {
            schema_version: 1,
            username: "vcw0123456789ab".into(),
            nonce: "a".repeat(32),
            sid: Some("S-1-5-21-1-2-3-1001".into()),
        };
        let value = "Vcw1!ephemeral-native-secret-for-test";
        let sealed = Encrypted::protect(&owner, 7, value).unwrap();
        assert_eq!(&*sealed.unprotect(&owner, 7).unwrap(), value);
        let bytes = serde_json::to_vec(&sealed).unwrap();
        assert!(!bytes.windows(value.len()).any(|w| w == value.as_bytes()));
        assert!(sealed.unprotect(&owner, 6).is_err());
        for change in 0..3 {
            let mut wrong = Record {
                schema_version: 1,
                username: owner.username.clone(),
                nonce: owner.nonce.clone(),
                sid: owner.sid.clone(),
            };
            match change {
                0 => wrong.username = "vcw0123456789ac".into(),
                1 => wrong.nonce = "b".repeat(32),
                _ => wrong.sid = Some("S-1-5-21-1-2-3-1002".into()),
            }
            assert!(sealed.unprotect(&wrong, 7).is_err());
        }
        let mut corrupt = sealed.clone();
        let last = corrupt.bytes.len() - 1;
        corrupt.bytes[last] ^= 1;
        assert!(corrupt.unprotect(&owner, 7).is_err());
        check(unsafe { ImpersonateSelf(SecurityImpersonation) }).unwrap();
        let rejected = sealed.unprotect(&owner, 7).is_err();
        check(unsafe { RevertToSelf() }).unwrap();
        assert!(rejected);
    }
}
