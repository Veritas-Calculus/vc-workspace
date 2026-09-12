-- Native credential retirement preserves a user's desktop. Account revocation
-- terminates it. Neither operation may be addressed by username alone.
-- No legacy UID/SID or credential version is inferred by this migration.
CREATE TABLE native_guest_accounts (
  desktop_vmid bigint NOT NULL,
  user_id text NOT NULL,
  guest_username text NOT NULL CHECK (guest_username ~ '^vcw[0-9a-f]{12}$'),
  os_family text NOT NULL CHECK (os_family IN ('linux','windows')),
  guest_uid bigint NOT NULL DEFAULT 0 CHECK (guest_uid BETWEEN 0 AND 4294967295),
  guest_sid text NOT NULL DEFAULT '',
  revision bigint NOT NULL DEFAULT 0 CHECK (revision >= 0),
  operation text NOT NULL DEFAULT 'none' CHECK (operation IN ('none','issue','retire','revoke')),
  state text NOT NULL DEFAULT 'idle' CHECK (state IN ('idle','pending','applied')),
  connection_id text NOT NULL DEFAULT '',
  expires_at timestamptz,
  native_session_digest bytea,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (desktop_vmid,user_id),
  FOREIGN KEY (desktop_vmid,user_id) REFERENCES guest_identity_bindings(desktop_vmid,user_id),
  UNIQUE (desktop_vmid,guest_username),
  CHECK ((os_family='linux' AND guest_uid>=1000 AND guest_sid='') OR
         (os_family='windows' AND guest_uid=0 AND vcw_valid_windows_account_sid(guest_sid))),
  CHECK ((revision=0 AND operation='none' AND state='idle' AND connection_id='' AND expires_at IS NULL) OR
         (revision>0 AND operation<>'none' AND state<>'idle' AND connection_id ~ '^conn_[A-Za-z0-9_-]{8,128}$' AND
          ((operation IN ('issue','retire') AND expires_at IS NOT NULL) OR (operation='revoke' AND expires_at IS NULL)))),
  CHECK ((operation='issue' AND state='pending' AND native_session_digest IS NOT NULL AND octet_length(native_session_digest)>0) OR
         ((operation<>'issue' OR state<>'pending') AND native_session_digest IS NULL))
);
CREATE UNIQUE INDEX native_guest_linux_account_unique ON native_guest_accounts(desktop_vmid,guest_uid) WHERE guest_uid>=1000;
CREATE UNIQUE INDEX native_guest_windows_account_unique ON native_guest_accounts(desktop_vmid,guest_sid) WHERE guest_sid<>'';
CREATE UNIQUE INDEX native_guest_connection_unique ON native_guest_accounts(connection_id) WHERE connection_id<>'';
CREATE INDEX native_guest_account_pending ON native_guest_accounts(updated_at) WHERE state='pending';

-- Retain used connection IDs when the latest account intent advances. A lost
-- issue receipt must not allow that old ID to become a different login later.
CREATE TABLE native_guest_credential_ids (
  connection_id text PRIMARY KEY,
  desktop_vmid bigint NOT NULL,
  user_id text NOT NULL,
  revision bigint NOT NULL CHECK (revision>0),
  FOREIGN KEY (desktop_vmid,user_id) REFERENCES native_guest_accounts(desktop_vmid,user_id),
  UNIQUE (desktop_vmid,user_id,revision)
);

CREATE FUNCTION protect_native_guest_account_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(72914405);
  IF TG_OP='INSERT' THEN
    IF NEW.revision<>0 OR NEW.state<>'idle' OR NEW.operation<>'none' OR NOT EXISTS(
      SELECT 1 FROM guest_identity_bindings b WHERE b.desktop_vmid=NEW.desktop_vmid AND b.user_id=NEW.user_id AND b.guest_username=NEW.guest_username
    ) THEN
      RAISE EXCEPTION 'Native accounts must first pin an existing subject without credentials' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;
  IF ROW(NEW.desktop_vmid,NEW.user_id,NEW.guest_username,NEW.os_family,NEW.guest_uid,NEW.guest_sid)
     IS DISTINCT FROM ROW(OLD.desktop_vmid,OLD.user_id,OLD.guest_username,OLD.os_family,OLD.guest_uid,OLD.guest_sid) THEN
    RAISE EXCEPTION 'Native Guest account identity is immutable' USING ERRCODE='23514';
  END IF;
  IF NEW.revision=OLD.revision THEN
    IF ROW(NEW.operation,NEW.connection_id,NEW.expires_at) IS DISTINCT FROM ROW(OLD.operation,OLD.connection_id,OLD.expires_at) OR
       (NEW.state<>OLD.state AND NOT (OLD.state='pending' AND NEW.state='applied')) OR
       (NEW.native_session_digest IS DISTINCT FROM OLD.native_session_digest AND NOT (OLD.state='pending' AND NEW.state='applied' AND NEW.native_session_digest IS NULL)) THEN
      RAISE EXCEPTION 'Native operation completion cannot change its intent' USING ERRCODE='23514';
    END IF;
  ELSIF NEW.revision>OLD.revision AND NEW.revision-OLD.revision=1 AND NEW.state='pending' THEN
    IF NEW.operation='issue' THEN
      IF NOT (OLD.state='idle' OR (OLD.state='applied' AND OLD.operation IN ('retire','revoke'))) OR NEW.connection_id=OLD.connection_id OR
         (OLD.operation='retire' AND OLD.expires_at<=now()) THEN
        RAISE EXCEPTION 'New Native credentials require settled prior work' USING ERRCODE='23514';
      END IF;
      IF EXISTS(SELECT 1 FROM desktop_leases WHERE desktop_id=NEW.desktop_vmid::text AND state='active' AND expires_at>now()) OR
         EXISTS(SELECT 1 FROM native_guest_accounts a WHERE a.desktop_vmid=NEW.desktop_vmid AND a.user_id<>NEW.user_id AND
           (a.state='pending' OR (a.operation='issue' AND a.state='applied'))) THEN
        RAISE EXCEPTION 'Desktop already has a control reservation' USING ERRCODE='23505';
      END IF;
      INSERT INTO native_guest_credential_ids(connection_id,desktop_vmid,user_id,revision)
      VALUES(NEW.connection_id,NEW.desktop_vmid,NEW.user_id,NEW.revision);
    ELSIF NEW.operation='retire' THEN
      IF OLD.operation<>'issue' OR OLD.state<>'applied' OR OLD.expires_at<=now() OR
         NEW.connection_id<>OLD.connection_id OR NEW.expires_at IS DISTINCT FROM OLD.expires_at THEN
        RAISE EXCEPTION 'Credential retirement must preserve the current OS session deadline' USING ERRCODE='23514';
      END IF;
    ELSIF NEW.operation='revoke' THEN
      IF OLD.state='idle' OR NEW.connection_id<>OLD.connection_id OR OLD.operation='revoke' THEN
        RAISE EXCEPTION 'Account revocation must target the current intent once' USING ERRCODE='23514';
      END IF;
    ELSE
      RAISE EXCEPTION 'Invalid Native operation' USING ERRCODE='23514';
    END IF;
  ELSE
    RAISE EXCEPTION 'Native account version cannot regress or skip' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER native_guest_account_version_guard BEFORE INSERT OR UPDATE ON native_guest_accounts
FOR EACH ROW EXECUTE FUNCTION protect_native_guest_account_version();

-- Both account families share the Guest OS identity namespace. Serialize
-- cross-table discovery as well as protecting each table's own unique index.
CREATE FUNCTION protect_cross_subject_guest_account() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.guest_uid=0 AND NEW.guest_sid='' THEN RETURN NEW; END IF;
  PERFORM pg_advisory_xact_lock(72915506,hashtext(NEW.desktop_vmid::text));
  IF TG_TABLE_NAME='native_guest_accounts' THEN
    IF EXISTS(SELECT 1 FROM agent_guest_sessions a WHERE a.desktop_vmid=NEW.desktop_vmid AND
      ((NEW.guest_uid>=1000 AND a.guest_uid=NEW.guest_uid) OR (NEW.guest_sid<>'' AND a.guest_sid=NEW.guest_sid))) THEN
      RAISE EXCEPTION 'OS identity belongs to an Agent' USING ERRCODE='23505';
    END IF;
  ELSE
    IF EXISTS(SELECT 1 FROM native_guest_accounts a WHERE a.desktop_vmid=NEW.desktop_vmid AND
      ((NEW.guest_uid>=1000 AND a.guest_uid=NEW.guest_uid) OR (NEW.guest_sid<>'' AND a.guest_sid=NEW.guest_sid))) THEN
      RAISE EXCEPTION 'OS identity belongs to a Native user' USING ERRCODE='23505';
    END IF;
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER native_guest_cross_subject_guard BEFORE INSERT OR UPDATE ON native_guest_accounts
FOR EACH ROW EXECUTE FUNCTION protect_cross_subject_guest_account();
CREATE TRIGGER agent_guest_cross_subject_guard BEFORE INSERT OR UPDATE ON agent_guest_sessions
FOR EACH ROW EXECUTE FUNCTION protect_cross_subject_guest_account();

-- Once pinned, changing the presentation binding cannot redirect later work.
CREATE FUNCTION protect_native_guest_binding_subject() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF ROW(NEW.desktop_vmid,NEW.user_id,NEW.guest_username,NEW.profile_id)
     IS DISTINCT FROM ROW(OLD.desktop_vmid,OLD.user_id,OLD.guest_username,OLD.profile_id) AND
     EXISTS(SELECT 1 FROM native_guest_accounts a WHERE a.desktop_vmid=OLD.desktop_vmid AND a.user_id=OLD.user_id) THEN
    RAISE EXCEPTION 'Pinned Native Guest binding cannot be reassigned' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER native_guest_binding_subject_guard BEFORE UPDATE ON guest_identity_bindings
FOR EACH ROW EXECUTE FUNCTION protect_native_guest_binding_subject();

CREATE FUNCTION protect_agent_lease_from_native_reservation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(72914405);
  IF NEW.state='active' AND EXISTS(SELECT 1 FROM native_guest_accounts a WHERE a.desktop_vmid::text=NEW.desktop_id AND
    (a.state='pending' OR (a.operation='issue' AND a.state='applied'))) THEN
    RAISE EXCEPTION 'Desktop has a Native credential reservation' USING ERRCODE='23505';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER agent_lease_native_reservation_guard BEFORE INSERT OR UPDATE ON desktop_leases
FOR EACH ROW EXECUTE FUNCTION protect_agent_lease_from_native_reservation();
