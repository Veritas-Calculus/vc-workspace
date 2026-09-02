CREATE TABLE IF NOT EXISTS desktop_access_policies (
    vmid bigint PRIMARY KEY CHECK (vmid > 0),
    privilege_mode text NOT NULL DEFAULT 'standard' CHECK (privilege_mode IN ('standard', 'local_admin')),
    desired_revision bigint NOT NULL DEFAULT 1 CHECK (desired_revision > 0),
    applied_revision bigint NOT NULL DEFAULT 0 CHECK (applied_revision >= 0),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'applied', 'failed')),
    os_family text NOT NULL DEFAULT 'unknown' CHECK (os_family IN ('linux', 'windows', 'unknown')),
    last_error text NOT NULL DEFAULT '',
    updated_by text REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS desktop_access_policies_state_idx
    ON desktop_access_policies(state, updated_at DESC);
