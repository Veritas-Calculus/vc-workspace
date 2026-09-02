CREATE TABLE IF NOT EXISTS oidc_auth_requests (
  state_digest bytea PRIMARY KEY,
  nonce text NOT NULL,
  pkce_verifier text NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS oidc_auth_requests_expires_at_idx ON oidc_auth_requests(expires_at);
