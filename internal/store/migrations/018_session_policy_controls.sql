ALTER TABLE desktop_access_policies
  ADD COLUMN IF NOT EXISTS clipboard_redirection boolean NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS drive_redirection boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS managed_background boolean NOT NULL DEFAULT true;
