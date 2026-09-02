CREATE TABLE IF NOT EXISTS managed_desktops (
  vmid bigint PRIMARY KEY CHECK (vmid > 0),
  display_name text NOT NULL,
  node text NOT NULL DEFAULT '',
  present boolean NOT NULL DEFAULT true,
  enabled boolean NOT NULL DEFAULT true,
  first_seen_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS agent_principals (
  id text PRIMARY KEY,
  display_name text NOT NULL,
  token_digest bytea UNIQUE,
  enabled boolean NOT NULL DEFAULT true,
  created_by text REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz
);

-- Preserve old lease history without silently granting it access. An administrator
-- must explicitly enable the imported principal and assign a desktop before reuse.
INSERT INTO agent_principals(id, display_name, enabled)
SELECT DISTINCT agent_id, agent_id, false
FROM desktop_leases
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS user_desktop_assignments (
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  desktop_vmid bigint NOT NULL REFERENCES managed_desktops(vmid) ON DELETE CASCADE,
  created_by text REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, desktop_vmid)
);

CREATE TABLE IF NOT EXISTS agent_desktop_assignments (
  agent_id text NOT NULL REFERENCES agent_principals(id) ON DELETE CASCADE,
  desktop_vmid bigint NOT NULL REFERENCES managed_desktops(vmid) ON DELETE CASCADE,
  created_by text REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (agent_id, desktop_vmid)
);

CREATE INDEX IF NOT EXISTS managed_desktops_present_idx
  ON managed_desktops(present, enabled, vmid);
CREATE INDEX IF NOT EXISTS user_desktop_assignments_desktop_idx
  ON user_desktop_assignments(desktop_vmid);
CREATE INDEX IF NOT EXISTS agent_desktop_assignments_desktop_idx
  ON agent_desktop_assignments(desktop_vmid);
