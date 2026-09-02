ALTER TABLE gpu_profiles
  ADD COLUMN IF NOT EXISTS resource_mapping text NOT NULL DEFAULT '';

