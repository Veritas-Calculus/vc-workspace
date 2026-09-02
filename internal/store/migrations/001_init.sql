CREATE TABLE IF NOT EXISTS users (
  id text PRIMARY KEY,
  username text NOT NULL,
  username_normalized text NOT NULL UNIQUE,
  display_name text NOT NULL,
  password_hash text,
  role text NOT NULL CHECK (role IN ('platform_admin', 'user')),
  disabled boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS web_sessions (
  token_digest bytea PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf_token text NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS web_sessions_user_id_idx ON web_sessions(user_id);
CREATE INDEX IF NOT EXISTS web_sessions_expires_at_idx ON web_sessions(expires_at);

CREATE TABLE IF NOT EXISTS oidc_providers (
  id text PRIMARY KEY,
  name text NOT NULL,
  issuer text NOT NULL UNIQUE,
  client_id text NOT NULL,
  client_secret_ciphertext bytea,
  enabled boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS external_identities (
  provider_id text NOT NULL REFERENCES oidc_providers(id) ON DELETE CASCADE,
  subject text NOT NULL,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (provider_id, subject)
);

CREATE TABLE IF NOT EXISTS pve_jobs (
  id text PRIMARY KEY,
  idempotency_key text NOT NULL UNIQUE,
  operation text NOT NULL,
  state text NOT NULL,
  source_vmid bigint,
  target_vmid bigint,
  target_node text,
  upid text,
  request jsonb NOT NULL DEFAULT '{}'::jsonb,
  error text,
  created_by text REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS desktop_leases (
  id text PRIMARY KEY,
  agent_id text NOT NULL,
  desktop_id text NOT NULL,
  state text NOT NULL,
  control_epoch bigint NOT NULL DEFAULT 1,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS audit_events (
  id bigserial PRIMARY KEY,
  actor_id text,
  event_type text NOT NULL,
  target_type text,
  target_id text,
  detail jsonb NOT NULL DEFAULT '{}'::jsonb,
  occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS audit_events_occurred_at_idx ON audit_events(occurred_at DESC);
