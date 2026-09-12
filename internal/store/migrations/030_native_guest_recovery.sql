-- Recovery never modifies a Guest fence. The caller supplies a freshly verified,
-- closed, immutable Guest identity while holding the distributed desktop lock.
-- The append-only receipt and state advance must commit in the same transaction.
CREATE TABLE native_guest_recoveries (
  id text PRIMARY KEY,
  desktop_vmid bigint NOT NULL,
  user_id text NOT NULL,
  actor_user_id text NOT NULL REFERENCES users(id),
  reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 500),
  previous_revision bigint NOT NULL CHECK (previous_revision>0),
  previous_connection_id text NOT NULL,
  recovered_revision bigint NOT NULL CHECK (recovered_revision>previous_revision),
  recovered_connection_id text NOT NULL CHECK (recovered_connection_id ~ '^conn_[A-Za-z0-9_-]{8,128}$'),
  transaction_id bigint NOT NULL DEFAULT txid_current(),
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (desktop_vmid,user_id) REFERENCES native_guest_accounts(desktop_vmid,user_id),
  UNIQUE (desktop_vmid,user_id,recovered_revision)
);
CREATE FUNCTION protect_native_guest_recovery_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'Native recovery receipts are append-only' USING ERRCODE='23514';
END;
$$;
CREATE TRIGGER native_guest_recovery_receipt_guard BEFORE UPDATE OR DELETE ON native_guest_recoveries
FOR EACH ROW EXECUTE FUNCTION protect_native_guest_recovery_receipt();

CREATE OR REPLACE FUNCTION protect_native_guest_account_version() RETURNS trigger LANGUAGE plpgsql AS $$
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
  IF NEW.revision>OLD.revision AND OLD.operation='revoke' AND OLD.state='applied'
     AND NEW.operation='revoke' AND NEW.state='applied' AND NEW.expires_at IS NULL AND NEW.native_session_digest IS NULL
     AND EXISTS(SELECT 1 FROM native_guest_recoveries r WHERE
       r.desktop_vmid=OLD.desktop_vmid AND r.user_id=OLD.user_id
       AND r.previous_revision=OLD.revision AND r.previous_connection_id=OLD.connection_id
       AND r.recovered_revision=NEW.revision AND r.recovered_connection_id=NEW.connection_id
       AND r.transaction_id=txid_current()) THEN
    INSERT INTO native_guest_credential_ids(connection_id,desktop_vmid,user_id,revision)
      VALUES(NEW.connection_id,NEW.desktop_vmid,NEW.user_id,NEW.revision);
    RETURN NEW;
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
