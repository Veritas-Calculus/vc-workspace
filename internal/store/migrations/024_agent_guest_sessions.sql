CREATE TABLE agent_guest_sessions (
  desktop_vmid bigint NOT NULL REFERENCES managed_desktops(vmid),
  agent_id text NOT NULL REFERENCES agent_principals(id),
  guest_username text NOT NULL CHECK (guest_username ~ '^vca[0-9a-f]{12}$'),
  lease_id text NOT NULL REFERENCES desktop_leases(id),
  control_epoch bigint NOT NULL CHECK (control_epoch > 0),
  generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
  state text NOT NULL DEFAULT 'provisioning' CHECK (state IN ('provisioning','ready','revoking','disabled')),
  guest_uid bigint NOT NULL DEFAULT 0 CHECK (guest_uid=0 OR guest_uid BETWEEN 1000 AND 4294967295),
  session_id text NOT NULL DEFAULT '',
  instance_id text NOT NULL DEFAULT '',
  expires_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(desktop_vmid,agent_id),
  UNIQUE(desktop_vmid,guest_username),
  CHECK (state<>'ready' OR (guest_uid>=1000 AND session_id<>'' AND instance_id ~ '^[0-9a-f]{64}$'))
);
CREATE INDEX agent_guest_sessions_pending_idx ON agent_guest_sessions(updated_at) WHERE state='revoking';

-- An in-progress bootstrap is also revoked: registration precedes Guest work.
-- This trigger shares the lease transaction with the existing fencing queue.
CREATE FUNCTION revoke_agent_guest_session() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.state='active' AND (NEW.state<>'active' OR NEW.control_epoch<>OLD.control_epoch) THEN
    UPDATE agent_guest_sessions SET state='revoking',updated_at=now()
    WHERE lease_id=OLD.id AND state IN ('provisioning','ready');
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER desktop_lease_agent_guest_revocation
AFTER UPDATE OF state,control_epoch ON desktop_leases
FOR EACH ROW EXECUTE FUNCTION revoke_agent_guest_session();
