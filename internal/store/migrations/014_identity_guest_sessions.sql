CREATE TABLE IF NOT EXISTS identity_profiles (
  id text PRIMARY KEY,
  display_name text NOT NULL,
  platform text NOT NULL CHECK (platform IN ('linux', 'windows')),
  mode text NOT NULL CHECK (mode IN (
    'managed_local',
    'linux_sssd_ad',
    'linux_sssd_freeipa',
    'linux_sssd_ldap',
    'linux_sssd_oidc',
    'windows_ad',
    'windows_entra'
  )),
  enabled boolean NOT NULL DEFAULT true,
  experimental boolean NOT NULL DEFAULT false,
  config jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_by text REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (
    (platform = 'linux' AND mode LIKE 'linux_%') OR
    (platform = 'windows' AND mode LIKE 'windows_%') OR
    mode = 'managed_local'
  )
);

INSERT INTO identity_profiles(id, display_name, platform, mode, enabled, experimental, config)
VALUES
  ('managed-local-linux', 'Managed local accounts · Linux', 'linux', 'managed_local', true, false, '{}'::jsonb),
  ('managed-local-windows', 'Managed local accounts · Windows', 'windows', 'managed_local', true, false, '{}'::jsonb)
ON CONFLICT (id) DO NOTHING;

ALTER TABLE managed_desktops
  ADD COLUMN IF NOT EXISTS access_mode text NOT NULL DEFAULT 'personal'
    CHECK (access_mode IN ('personal', 'shared')),
  ADD COLUMN IF NOT EXISTS owner_user_id text REFERENCES users(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS identity_profile_id text REFERENCES identity_profiles(id) ON DELETE SET NULL;

-- Preserve existing grants without silently revoking access. One-user desktops
-- become personal; desktops with more than one user assignment become shared.
WITH assignment_counts AS (
  SELECT desktop_vmid, count(*) AS user_count
  FROM user_desktop_assignments
  GROUP BY desktop_vmid
)
UPDATE managed_desktops desktop
SET access_mode = 'shared'
FROM assignment_counts
WHERE desktop.vmid = assignment_counts.desktop_vmid
  AND assignment_counts.user_count > 1;

WITH ranked AS (
  SELECT user_id, desktop_vmid,
         row_number() OVER (PARTITION BY desktop_vmid ORDER BY created_at, user_id) AS position
  FROM user_desktop_assignments
), owners AS (
  UPDATE managed_desktops d
  SET owner_user_id = ranked.user_id
  FROM ranked
  WHERE d.vmid = ranked.desktop_vmid
    AND d.access_mode = 'personal'
    AND ranked.position = 1
    AND d.owner_user_id IS NULL
  RETURNING d.vmid, d.owner_user_id
)
DELETE FROM user_desktop_assignments assignment
USING managed_desktops desktop
WHERE assignment.desktop_vmid = desktop.vmid
  AND desktop.access_mode = 'personal'
  AND assignment.user_id = desktop.owner_user_id;

CREATE INDEX IF NOT EXISTS managed_desktops_owner_idx
  ON managed_desktops(owner_user_id) WHERE owner_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS managed_desktops_identity_profile_idx
  ON managed_desktops(identity_profile_id) WHERE identity_profile_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS identity_groups (
  id text PRIMARY KEY,
  provider_id text REFERENCES oidc_providers(id) ON DELETE CASCADE,
  external_id text,
  display_name text NOT NULL,
  source text NOT NULL CHECK (source IN ('local', 'oidc', 'scim')),
  enabled boolean NOT NULL DEFAULT true,
  created_by text REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider_id, external_id)
);

CREATE TABLE IF NOT EXISTS identity_group_memberships (
  group_id text NOT NULL REFERENCES identity_groups(id) ON DELETE CASCADE,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  source text NOT NULL CHECK (source IN ('local', 'oidc', 'scim')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, user_id)
);

CREATE TABLE IF NOT EXISTS group_desktop_assignments (
  group_id text NOT NULL REFERENCES identity_groups(id) ON DELETE CASCADE,
  desktop_vmid bigint NOT NULL REFERENCES managed_desktops(vmid) ON DELETE CASCADE,
  created_by text REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, desktop_vmid)
);

CREATE INDEX IF NOT EXISTS identity_group_memberships_user_idx
  ON identity_group_memberships(user_id, group_id);
CREATE INDEX IF NOT EXISTS group_desktop_assignments_desktop_idx
  ON group_desktop_assignments(desktop_vmid, group_id);

CREATE TABLE IF NOT EXISTS guest_identity_bindings (
  desktop_vmid bigint NOT NULL REFERENCES managed_desktops(vmid) ON DELETE CASCADE,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  profile_id text REFERENCES identity_profiles(id) ON DELETE SET NULL,
  guest_username text NOT NULL,
  state text NOT NULL DEFAULT 'provisioning'
    CHECK (state IN ('provisioning', 'ready', 'failed', 'disabled')),
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (desktop_vmid, user_id),
  UNIQUE (desktop_vmid, guest_username)
);

CREATE INDEX IF NOT EXISTS guest_identity_bindings_user_idx
  ON guest_identity_bindings(user_id, desktop_vmid);

CREATE TABLE IF NOT EXISTS desktop_connection_sessions (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  desktop_vmid bigint NOT NULL REFERENCES managed_desktops(vmid) ON DELETE CASCADE,
  guest_username text NOT NULL,
  state text NOT NULL DEFAULT 'active'
    CHECK (state IN ('active', 'revoking', 'revoked', 'expired')),
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  closed_at timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS desktop_connection_sessions_active_desktop_idx
  ON desktop_connection_sessions(desktop_vmid)
  WHERE state IN ('active', 'revoking');
CREATE INDEX IF NOT EXISTS desktop_connection_sessions_user_idx
  ON desktop_connection_sessions(user_id, state, expires_at);
CREATE INDEX IF NOT EXISTS desktop_connection_sessions_expiry_idx
  ON desktop_connection_sessions(state, expires_at);
