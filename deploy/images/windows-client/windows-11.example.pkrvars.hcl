pve_url                = "https://pve.example.com:8006/api2/json"
pve_username           = "vc-vdi-builder@pve!packer"
pve_token              = "replace-with-token-secret"
pve_password           = ""
pve_node               = "infra-node4"
vm_id                  = 9111
template_name          = "vc-vdi-windows-11"
windows_image_name     = "Windows 11 Enterprise Evaluation"
pve_os_type            = "win11"
windows_iso_file       = "local:iso/Win11_25H2_Enterprise_Eval_zh-cn_x64.iso"
windows_iso_checksum   = "sha256:7b4ac87391b659f7724229682b642256289a1c00504056249f0f12029157d3d2"
virtio_iso_file        = "local:iso/virtio-win-0.1.271.iso"
virtio_iso_checksum    = "sha256:0040e268e1095b080abfec74214d094bc7fe565568533505b99d70622061c187"
storage_pool           = "ceph-pve"
bridge                 = "vmbr0"
cores                  = 4
memory_mb              = 8192
disk_size              = "64G"
administrator_password = "replace-with-one-time-build-password"
# Microsoft evaluation media does not require a product key. Production
# deployments must replace the evaluation ISO with correctly licensed media.
product_key        = ""
agent_binary       = "../../../dist/vc-vdi-guest-agent-windows-amd64.exe"
cloudbase_init_msi = "../../../dist/CloudbaseInitSetup.msi"
