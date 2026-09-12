ALTER TABLE managed_desktops
  ADD COLUMN IF NOT EXISTS os_family text NOT NULL DEFAULT 'unknown'
    CHECK (os_family IN ('linux', 'windows', 'unknown'));

CREATE INDEX IF NOT EXISTS managed_desktops_os_family_idx
  ON managed_desktops(os_family, present, enabled);
