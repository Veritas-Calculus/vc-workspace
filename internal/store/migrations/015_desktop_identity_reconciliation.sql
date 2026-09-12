ALTER TABLE managed_desktops
  ADD COLUMN IF NOT EXISTS identity_state text NOT NULL DEFAULT 'pending'
    CHECK (identity_state IN ('pending', 'applied', 'restart_required', 'failed')),
  ADD COLUMN IF NOT EXISTS applied_identity_profile_id text REFERENCES identity_profiles(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS identity_last_error text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS identity_updated_at timestamptz NOT NULL DEFAULT now();

CREATE INDEX IF NOT EXISTS managed_desktops_identity_state_idx
  ON managed_desktops(identity_state, identity_updated_at DESC);
