ALTER TABLE image_profiles
  ADD COLUMN IF NOT EXISTS default_cores integer NOT NULL DEFAULT 4 CHECK (default_cores BETWEEN 1 AND 256),
  ADD COLUMN IF NOT EXISTS default_memory_mb integer NOT NULL DEFAULT 4096 CHECK (default_memory_mb BETWEEN 512 AND 1048576),
  ADD COLUMN IF NOT EXISTS default_disk_gb integer NOT NULL DEFAULT 32 CHECK (default_disk_gb BETWEEN 8 AND 16384),
  ADD COLUMN IF NOT EXISTS firmware text NOT NULL DEFAULT 'seabios' CHECK (firmware IN ('seabios', 'uefi')),
  ADD COLUMN IF NOT EXISTS tpm_version text NOT NULL DEFAULT 'none' CHECK (tpm_version IN ('none', '2.0'));

UPDATE image_profiles
SET default_cores=4, default_memory_mb=4096, default_disk_gb=32, firmware='seabios', tpm_version='none'
WHERE id='debian-13-xfce';

UPDATE image_profiles
SET default_cores=4, default_memory_mb=8192, default_disk_gb=64, firmware='uefi', tpm_version='2.0'
WHERE id IN ('windows-10-22h2','windows-11');
