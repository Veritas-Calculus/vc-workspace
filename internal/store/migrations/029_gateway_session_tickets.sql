-- Do not infer a connection's initiating device/session from its user. Legacy
-- connections remain NULL and cannot receive a Gateway ticket.
ALTER TABLE desktop_connection_sessions ADD COLUMN native_session_digest bytea
  CHECK (native_session_digest IS NULL OR octet_length(native_session_digest) BETWEEN 1 AND 128);

CREATE FUNCTION protect_connection_native_session() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.native_session_digest IS DISTINCT FROM OLD.native_session_digest THEN
    RAISE EXCEPTION 'Connection initiating session is immutable' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER connection_native_session_guard BEFORE UPDATE ON desktop_connection_sessions
FOR EACH ROW EXECUTE FUNCTION protect_connection_native_session();

-- One transport ticket per logical connection. Reconnect must create a fresh
-- authorized connection; a spent/failed ticket is never made reusable.
CREATE TABLE gateway_session_tickets (
  token_digest bytea PRIMARY KEY CHECK (octet_length(token_digest)=32),
  connection_id text NOT NULL UNIQUE REFERENCES desktop_connection_sessions(id) ON DELETE CASCADE,
  gateway_id text NOT NULL CHECK (gateway_id ~ '^gw_[a-z0-9_-]{8,64}$'),
  target_host inet NOT NULL CHECK (family(target_host)=4 AND masklen(target_host)=32 AND
    (target_host <<= inet '10.0.0.0/8' OR target_host <<= inet '172.16.0.0/12' OR target_host <<= inet '192.168.0.0/16')),
  target_port integer NOT NULL CHECK (target_port=3389),
  certificate_sha256 bytea NOT NULL CHECK (octet_length(certificate_sha256)=32),
  policy_revision bigint NOT NULL CHECK (policy_revision>0),
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  session_deadline timestamptz NOT NULL,
  tunnel_id text UNIQUE CHECK (tunnel_id ~ '^tun_[A-Za-z0-9_-]{24,128}$'),
  consumed_at timestamptz,
  lease_until timestamptz,
  closed_at timestamptz,
  CHECK (expires_at>created_at AND expires_at<=created_at+interval '30 seconds' AND expires_at<=session_deadline),
  CHECK ((tunnel_id IS NULL AND consumed_at IS NULL AND lease_until IS NULL AND closed_at IS NULL) OR
    (tunnel_id IS NOT NULL AND consumed_at IS NOT NULL AND lease_until IS NOT NULL AND
      consumed_at>=created_at AND lease_until>=consumed_at AND lease_until<=session_deadline AND
      (closed_at IS NULL OR closed_at>=consumed_at)))
);
CREATE INDEX gateway_ticket_expiry ON gateway_session_tickets(expires_at);

CREATE FUNCTION protect_gateway_ticket() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF ROW(NEW.token_digest,NEW.connection_id,NEW.gateway_id,NEW.target_host,NEW.target_port,
      NEW.certificate_sha256,NEW.policy_revision,NEW.created_at,NEW.expires_at,NEW.session_deadline)
    IS DISTINCT FROM ROW(OLD.token_digest,OLD.connection_id,OLD.gateway_id,OLD.target_host,OLD.target_port,
      OLD.certificate_sha256,OLD.policy_revision,OLD.created_at,OLD.expires_at,OLD.session_deadline) THEN
    RAISE EXCEPTION 'Gateway ticket authority is immutable' USING ERRCODE='23514';
  END IF;
  IF OLD.consumed_at IS NOT NULL AND
    ROW(NEW.tunnel_id,NEW.consumed_at) IS DISTINCT FROM ROW(OLD.tunnel_id,OLD.consumed_at) THEN
    RAISE EXCEPTION 'Gateway tickets cannot be consumed again' USING ERRCODE='23514';
  END IF;
  IF OLD.closed_at IS NOT NULL AND
    ROW(NEW.closed_at,NEW.lease_until) IS DISTINCT FROM ROW(OLD.closed_at,OLD.lease_until) THEN
    RAISE EXCEPTION 'Closed Gateway tunnels cannot be renewed' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER gateway_ticket_guard BEFORE UPDATE ON gateway_session_tickets
FOR EACH ROW EXECUTE FUNCTION protect_gateway_ticket();
