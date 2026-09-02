ALTER TABLE gpu_profiles
  ADD COLUMN IF NOT EXISTS mdev_type text NOT NULL DEFAULT '';

ALTER TABLE gpu_profiles
  DROP CONSTRAINT IF EXISTS gpu_profiles_mdev_type_check;

ALTER TABLE gpu_profiles
  ADD CONSTRAINT gpu_profiles_mdev_type_check CHECK (
    (mode = 'mdev' AND mdev_type <> '') OR
    (mode <> 'mdev' AND mdev_type = '')
  );

INSERT INTO gpu_profiles(
  id,display_name,mode,vendor_id,device_class,mdev_type,enabled,exclusive,allow_live_migration
)
VALUES
  ('intel-gvtg-v5-4','Intel GVT-g · 1920×1200','mdev','0x8086','0x030000','i915-GVTg_V5_4',false,false,false),
  ('intel-gvtg-v5-8','Intel GVT-g · 1024×768','mdev','0x8086','0x030000','i915-GVTg_V5_8',false,false,false)
ON CONFLICT(id) DO NOTHING;
