packer {
  required_plugins {
    proxmox = {
      source  = "github.com/hashicorp/proxmox"
      version = ">= 1.2.4"
    }
  }
}

variable "pve_url" {
  type = string
}

variable "pve_insecure_tls" {
  type        = bool
  default     = false
  description = "Skip PVE TLS verification. Only for a self-signed endpoint whose CA cannot be added to the build host trust store."
}

variable "pve_username" {
  type = string
}

variable "pve_token" {
  type      = string
  sensitive = true
  default   = ""
}

variable "pve_password" {
  type      = string
  sensitive = true
  default   = ""
}

variable "pve_node" {
  type = string
}

variable "vm_id" {
  type = number
}

variable "template_name" {
  type = string
}

variable "iso_file" {
  type = string
}

variable "iso_checksum" {
  type = string
}

variable "storage_pool" {
  type = string
}

variable "bridge" {
  type    = string
  default = "vmbr0"
}

variable "cores" {
  type = number
}

variable "memory_mb" {
  type = number
}

variable "disk_size" {
  type = string
}

variable "firmware" {
  type = string
  validation {
    condition     = contains(["seabios", "ovmf"], var.firmware)
    error_message = "Firmware must be seabios or ovmf."
  }
}

variable "builder_password" {
  type      = string
  sensitive = true
}

variable "agent_binary" {
  type = string
}

variable "pam_module" {
  type = string
}

variable "native_pam_module" {
  type = string
}

variable "xrdp_package" {
  type = string
  validation {
    condition     = fileexists(var.xrdp_package)
    error_message = "The reviewed Debian xrdp package must exist."
  }
}

variable "xrdp_package_sha256" {
  type = string
  validation {
    condition     = can(regex("^[0-9a-f]{64}$", var.xrdp_package_sha256))
    error_message = "An independently supplied xrdp package SHA-256 is required."
  }
}

variable "mirror_protocol" {
  type = string
}

variable "mirror_host" {
  type = string
}

variable "mirror_directory" {
  type = string
}

variable "mirror_url" {
  type = string
}

variable "security_mirror_url" {
  type = string
}

variable "http_bind_address" {
  type    = string
  default = ""
}

variable "http_interface" {
  type    = string
  default = ""
}

variable "http_port_min" {
  type    = number
  default = 8840
}

variable "http_port_max" {
  type    = number
  default = 8847
}

source "proxmox-iso" "debian13_xfce" {
  proxmox_url              = var.pve_url
  username                 = var.pve_username
  token                    = var.pve_token
  password                 = var.pve_password
  insecure_skip_tls_verify = var.pve_insecure_tls
  node                     = var.pve_node
  vm_id                    = var.vm_id
  vm_name                  = var.template_name
  template_name            = var.template_name
  template_description     = "VC Workspace Debian 13 XFCE, xrdp, QEMU Guest Agent and VC Workspace Agent"
  tags                     = "vc-workspace;template;debian-13;rdp"
  task_timeout             = "45m"

  boot_iso {
    type         = "ide"
    iso_file     = var.iso_file
    iso_checksum = var.iso_checksum
    unmount      = true
  }

  boot_wait = "8s"
  boot_command = [
    "<esc><wait>",
    "auto url=http://{{ .HTTPIP }}:{{ .HTTPPort }}/preseed.cfg ",
    "debian-installer=en_US.UTF-8 locale=en_US.UTF-8 keyboard-configuration/xkb-keymap=us ",
    "hostname=vc-workspace-debian-13-xfce domain=local netcfg/choose_interface=auto ",
    "fb=false debconf/frontend=noninteractive initrd=/install.amd/initrd.gz --- <enter>"
  ]
  http_content = {
    "/preseed.cfg" = templatefile(abspath("${path.root}/preseed.pkrtpl.hcl"), {
      builder_password = var.builder_password
      mirror_protocol  = var.mirror_protocol
      mirror_host      = var.mirror_host
      mirror_directory = var.mirror_directory
    })
  }
  http_bind_address     = var.http_bind_address
  http_interface        = var.http_interface
  http_network_protocol = "tcp4"
  http_port_min         = var.http_port_min
  http_port_max         = var.http_port_max

  bios                    = var.firmware
  cores                   = var.cores
  memory                  = var.memory_mb
  cpu_type                = "host"
  os                      = "l26"
  qemu_agent              = true
  scsi_controller         = "virtio-scsi-single"
  cloud_init              = true
  cloud_init_storage_pool = var.storage_pool

  disks {
    type         = "scsi"
    disk_size    = var.disk_size
    storage_pool = var.storage_pool
    format       = "raw"
    io_thread    = true
    discard      = true
    ssd          = true
  }

  network_adapters {
    model         = "virtio"
    bridge        = var.bridge
    packet_queues = 4
  }

  ssh_username = "vdi-builder"
  ssh_password = var.builder_password
  ssh_timeout  = "45m"
}

build {
  sources = ["source.proxmox-iso.debian13_xfce"]

  provisioner "file" {
    source      = var.agent_binary
    destination = "/tmp/vc-workspace-guest-agent"
  }

  provisioner "file" {
    source      = var.pam_module
    destination = "/tmp/pam_vcworkspace.so"
  }

  provisioner "file" {
    source      = var.native_pam_module
    destination = "/tmp/pam_vcworkspace_native.so"
  }

  provisioner "file" {
    source      = var.xrdp_package
    destination = "/tmp/vc-workspace-xrdp.deb"
  }

  provisioner "file" {
    source      = abspath("${path.root}/xrdp/install-package.py")
    destination = "/tmp/vc-workspace-install-xrdp.py"
  }

  provisioner "file" {
    source      = abspath("${path.root}/../../../assets/brand/vc-workspace-desktop-background.png")
    destination = "/tmp/vc-workspace-desktop-background.png"
  }

  provisioner "file" {
    source      = abspath("${path.root}/../../../internal/guestdesktop/session_layout.py")
    destination = "/tmp/vc-workspace-session-layout.py"
  }

  provisioner "file" {
    source      = abspath("${path.root}/../../guest/linux/vc-workspace-agent.service")
    destination = "/tmp/vc-workspace-agent.service"
  }

  provisioner "file" {
    source      = abspath("${path.root}/../../guest/linux/vc-workspace-accounts.service")
    destination = "/tmp/vc-workspace-accounts.service"
  }

  provisioner "file" {
    source      = abspath("${path.root}/../../guest/linux/vc-workspace-accounts.timer")
    destination = "/tmp/vc-workspace-accounts.timer"
  }

  provisioner "file" {
    source      = abspath("${path.root}/../../guest/linux/install-login-fence.py")
    destination = "/tmp/vc-workspace-install-login-fence.py"
  }

  provisioner "shell" {
    script          = abspath("${path.root}/configure-desktop.sh")
    execute_command = "{{ .Vars }} sudo -E bash '{{ .Path }}'"
    environment_vars = [
      "VC_WORKSPACE_MIRROR_URL=${var.mirror_url}",
      "VC_WORKSPACE_SECURITY_MIRROR_URL=${var.security_mirror_url}",
      "VC_WORKSPACE_XRDP_PACKAGE_SHA256=${var.xrdp_package_sha256}"
    ]
  }
}
