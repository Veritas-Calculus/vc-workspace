-- Earlier Agent sessions were Linux-only. Keep every existing UID and session;
-- unknown/deleted legacy identities are not inferred from an account name.
ALTER TABLE agent_guest_sessions
  ADD COLUMN os_family text NOT NULL DEFAULT 'linux' CHECK (os_family IN ('linux','windows')),
  ADD COLUMN guest_sid text NOT NULL DEFAULT '';
ALTER TABLE agent_guest_sessions ALTER COLUMN os_family DROP DEFAULT;

CREATE FUNCTION vcw_valid_windows_account_sid(value text) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE STRICT AS $$
DECLARE
  parts text[];
  part text;
BEGIN
  IF value !~ '^S-1-5-21-(0|[1-9][0-9]{0,9})-(0|[1-9][0-9]{0,9})-(0|[1-9][0-9]{0,9})-([1-9][0-9]{0,9})$' THEN
    RETURN false;
  END IF;
  parts := string_to_array(value,'-');
  FOREACH part IN ARRAY parts[5:8] LOOP
    IF part::bigint > 4294967295 THEN RETURN false; END IF;
  END LOOP;
  RETURN parts[8]::bigint >= 1000;
END;
$$;

ALTER TABLE agent_guest_sessions DROP CONSTRAINT agent_guest_sessions_check;
ALTER TABLE agent_guest_sessions ADD CONSTRAINT agent_guest_account_identity CHECK (
  (os_family='linux' AND guest_sid='') OR
  (os_family='windows' AND guest_uid=0 AND (guest_sid='' OR vcw_valid_windows_account_sid(guest_sid)))
);
ALTER TABLE agent_guest_sessions ADD CONSTRAINT agent_guest_ready_identity CHECK (
  state<>'ready' OR (session_id<>'' AND instance_id ~ '^[0-9a-f]{64}$' AND
    ((os_family='linux' AND guest_uid>=1000 AND session_id LIKE 'linux:' || guest_uid::text || ':%') OR
     (os_family='windows' AND vcw_valid_windows_account_sid(guest_sid) AND session_id ~ '^windows:[1-9][0-9]*:[0-9a-f]{16}$')))
);
CREATE UNIQUE INDEX agent_guest_linux_account_unique ON agent_guest_sessions(desktop_vmid,guest_uid) WHERE guest_uid>=1000;
CREATE UNIQUE INDEX agent_guest_windows_account_unique ON agent_guest_sessions(desktop_vmid,guest_sid) WHERE guest_sid<>'';

CREATE FUNCTION protect_agent_guest_account_binding() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF ROW(NEW.desktop_vmid,NEW.agent_id,NEW.guest_username,NEW.os_family)
     IS DISTINCT FROM ROW(OLD.desktop_vmid,OLD.agent_id,OLD.guest_username,OLD.os_family) THEN
    RAISE EXCEPTION 'Agent account subject and platform are immutable' USING ERRCODE='23514';
  END IF;
  IF (OLD.guest_uid<>0 OR OLD.guest_sid<>'') AND
     ROW(NEW.guest_uid,NEW.guest_sid) IS DISTINCT FROM ROW(OLD.guest_uid,OLD.guest_sid) THEN
    RAISE EXCEPTION 'Agent account identity cannot be replaced or cleared' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER agent_guest_account_binding_guard BEFORE UPDATE ON agent_guest_sessions
FOR EACH ROW EXECUTE FUNCTION protect_agent_guest_account_binding();
