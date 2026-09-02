ALTER TABLE oidc_auth_requests
  ADD COLUMN IF NOT EXISTS client_kind text NOT NULL DEFAULT 'web';

CREATE TABLE IF NOT EXISTS native_auth_codes (
  code_digest bytea PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS native_sessions (
  token_digest bytea PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS native_auth_codes_expires_at_idx ON native_auth_codes(expires_at);
CREATE INDEX IF NOT EXISTS native_sessions_user_id_idx ON native_sessions(user_id);
CREATE INDEX IF NOT EXISTS native_sessions_expires_at_idx ON native_sessions(expires_at);
