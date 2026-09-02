ALTER TABLE image_profiles
  ADD COLUMN IF NOT EXISTS source_iso_checksum text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS driver_iso_checksum text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS security_mirror_url text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS storage_pool text NOT NULL DEFAULT 'ceph-pve',
  ADD COLUMN IF NOT EXISTS bridge text NOT NULL DEFAULT 'vmbr0',
  ADD COLUMN IF NOT EXISTS windows_image_name text NOT NULL DEFAULT '';

UPDATE image_profiles
SET source_iso_checksum='sha256:65273beed27b2df543b68b65630ba525cfbad8df2b12035732b2dff87d6664e7',
    security_mirror_url='http://10.31.0.2/debian-security',
    storage_pool='ceph-pve', bridge='vmbr0'
WHERE id='debian-13-xfce' AND source_iso_checksum='';

UPDATE image_profiles
SET source_iso_checksum='sha256:d485d370406cbcb68959718817bd12ed87c537a14c885f84962e07136fc4a049',
    driver_iso_checksum='sha256:0040e268e1095b080abfec74214d094bc7fe565568533505b99d70622061c187',
    storage_pool='ceph-pve', bridge='vmbr0', windows_image_name='Windows 10 Pro'
WHERE id='windows-10-22h2' AND source_iso_checksum='';

UPDATE image_profiles
SET source_iso_checksum='sha256:7b4ac87391b659f7724229682b642256289a1c00504056249f0f12029157d3d2',
    driver_iso_checksum='sha256:0040e268e1095b080abfec74214d094bc7fe565568533505b99d70622061c187',
    storage_pool='ceph-pve', bridge='vmbr0', windows_image_name='Windows 11 Enterprise Evaluation'
WHERE id='windows-11' AND source_iso_checksum='';

ALTER TABLE pve_jobs
  ADD COLUMN IF NOT EXISTS progress integer NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
  ADD COLUMN IF NOT EXISTS detail text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS pve_jobs_operation_created_at_idx
  ON pve_jobs(operation, created_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS pve_jobs_one_active_image_build_per_profile_idx
  ON pve_jobs ((request->>'image_profile_id'))
  WHERE operation='image.build' AND state IN ('accepted','running');
