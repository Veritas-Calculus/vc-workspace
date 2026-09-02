CREATE TABLE IF NOT EXISTS gpu_profiles (
  id text PRIMARY KEY,
  display_name text NOT NULL,
  mode text NOT NULL CHECK (mode IN ('none', 'pci_passthrough', 'sriov', 'mdev')),
  vendor_id text NOT NULL DEFAULT '',
  device_class text NOT NULL DEFAULT '',
  enabled boolean NOT NULL DEFAULT true,
  exclusive boolean NOT NULL DEFAULT false,
  allow_live_migration boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO gpu_profiles(id,display_name,mode,vendor_id,device_class,enabled,exclusive,allow_live_migration)
VALUES
  ('none','CPU only','none','','',true,false,true),
  ('intel-igpu-passthrough','Intel iGPU passthrough','pci_passthrough','0x8086','0x030000',false,true,false)
ON CONFLICT(id) DO NOTHING;

CREATE TABLE IF NOT EXISTS image_profiles (
  id text PRIMARY KEY,
  display_name text NOT NULL,
  os_family text NOT NULL CHECK (os_family IN ('linux', 'windows')),
  os_version text NOT NULL,
  architecture text NOT NULL DEFAULT 'amd64',
  lifecycle text NOT NULL CHECK (lifecycle IN ('supported', 'legacy')),
  enabled boolean NOT NULL DEFAULT false,
  source_node text NOT NULL DEFAULT '',
  source_iso text NOT NULL DEFAULT '',
  driver_iso text NOT NULL DEFAULT '',
  template_vmid bigint,
  mirror_url text NOT NULL DEFAULT '',
  agent_kind text NOT NULL CHECK (agent_kind IN ('linux', 'windows')),
  desktop_protocol text NOT NULL DEFAULT 'rdp' CHECK (desktop_protocol IN ('rdp')),
  default_gpu_profile_id text NOT NULL REFERENCES gpu_profiles(id),
  build_status text NOT NULL CHECK (build_status IN ('draft', 'blocked', 'building', 'testing', 'ready', 'failed')),
  status_detail text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO image_profiles(
  id,display_name,os_family,os_version,lifecycle,enabled,mirror_url,agent_kind,
  default_gpu_profile_id,build_status,status_detail
)
VALUES
  ('debian-13-xfce','Debian 13 · XFCE','linux','13','supported',true,'','linux','none','draft','Configure a PVE template VMID after the image passes RDP validation.'),
  ('windows-11','Windows 11','windows','11','supported',false,'','windows','none','blocked','Import licensed Windows 11 and VirtIO installation media before building.'),
  ('windows-10-22h2','Windows 10 22H2','windows','10 22H2','legacy',false,'','windows','none','blocked','Windows 10 requires an eligible LTSC or ESU lifecycle and licensed installation media.')
ON CONFLICT(id) DO NOTHING;
