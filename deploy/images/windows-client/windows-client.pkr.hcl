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

variable "windows_image_name" {
  type = string
}

variable "pve_os_type" {
  type = string
  validation {
    condition     = contains(["win10", "win11"], var.pve_os_type)
    error_message = "PVE OS type must be win10 or win11."
  }
}

variable "windows_iso_file" {
  type = string
}

variable "windows_iso_checksum" {
  type = string
}

variable "virtio_iso_file" {
  type = string
}

variable "virtio_iso_checksum" {
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

variable "administrator_password" {
  type      = string
  sensitive = true
}

variable "product_key" {
  type      = string
  sensitive = true
}

variable "agent_binary" {
  type = string
}

variable "cloudbase_init_msi" {
  type = string
}

source "proxmox-iso" "windows_client" {
  proxmox_url              = var.pve_url
  username                 = var.pve_username
  token                    = var.pve_token
  password                 = var.pve_password
  insecure_skip_tls_verify = var.pve_insecure_tls
  node                     = var.pve_node
  vm_id                    = var.vm_id
  vm_name                  = var.template_name
  template_name            = var.template_name
  template_description     = "VC Workspace Windows client, RDP, VirtIO, QEMU Guest Agent, Cloudbase-Init and VC Workspace Agent"
  tags                     = "vc-vdi;template;windows;rdp"
  task_timeout             = "90m"

  boot_iso {
    type         = "ide"
    iso_file     = var.windows_iso_file
    iso_checksum = var.windows_iso_checksum
    unmount      = true
  }

  additional_iso_files {
    type         = "ide"
    iso_file     = var.virtio_iso_file
    iso_checksum = var.virtio_iso_checksum
    unmount      = true
  }

  additional_iso_files {
    type             = "ide"
    iso_storage_pool = "local"
    unmount          = true
    cd_content = {
      "Autounattend.xml" = templatefile(abspath("${path.root}/Autounattend.pkrtpl.xml"), {
        windows_image_name     = var.windows_image_name
        administrator_password = var.administrator_password
        product_key            = var.product_key
      })
    }
    # Keep all build payloads on the temporary answer ISO. WinRM is reliable
    # for running commands here, but uploading even small binaries through it
    # can be needlessly slow on a freshly installed Windows guest.
    cd_files = [
      abspath("${path.root}/Enable-WinRM.ps1"),
      abspath("${path.root}/configure-template.ps1"),
      var.agent_binary,
      var.cloudbase_init_msi,
    ]
    cd_label = "VCWORKSPACE"
  }

  boot_wait = "2s"
  boot_command = [
    "<spacebar><wait1s><spacebar><wait1s><spacebar><wait1s><spacebar><wait1s>",
    "<spacebar><wait1s><spacebar><wait1s><spacebar><wait1s><spacebar><wait1s>"
  ]

  bios       = "ovmf"
  machine    = "q35"
  os         = var.pve_os_type
  cores      = var.cores
  memory     = var.memory_mb
  cpu_type   = "host"
  qemu_agent = true

  cloud_init                          = true
  cloud_init_storage_pool             = var.storage_pool
  cloud_init_disk_type                = "ide"
  cloud_init_disable_upgrade_packages = true

  efi_config {
    efi_storage_pool  = var.storage_pool
    efi_format        = "raw"
    efi_type          = "4m"
    pre_enrolled_keys = true
  }

  tpm_config {
    tpm_storage_pool = var.storage_pool
    tpm_version      = "v2.0"
  }

  disks {
    type         = "sata"
    disk_size    = var.disk_size
    storage_pool = var.storage_pool
    format       = "raw"
    discard      = true
    ssd          = true
  }

  network_adapters {
    model  = "e1000"
    bridge = var.bridge
  }

  communicator   = "winrm"
  winrm_username = "Administrator"
  winrm_password = var.administrator_password
  winrm_timeout  = "90m"
}

build {
  sources = ["source.proxmox-iso.windows_client"]

  provisioner "powershell" {
    inline = [
      "$payloadRoot = Get-CimInstance Win32_LogicalDisk -Filter 'DriveType = 5' | Where-Object { Test-Path (Join-Path $_.DeviceID 'configure-template.ps1') } | Select-Object -First 1",
      "if (-not $payloadRoot) { throw 'VC Workspace build payload CD-ROM was not found' }",
      "& (Join-Path $payloadRoot.DeviceID 'configure-template.ps1')",
    ]
  }

  provisioner "powershell" {
    # The Proxmox builder owns the final API shutdown/template conversion.
    # /quit lets Sysprep finish generalization without tearing down the WinRM
    # command that Packer is still waiting on.
    inline = [
      "$unattendSource = 'C:\\Program Files\\Cloudbase Solutions\\Cloudbase-Init\\conf\\Unattend.xml'",
      "if (-not (Test-Path $unattendSource)) { throw 'Cloudbase-Init Unattend.xml was not found' }",
      "$unattend = 'C:\\Windows\\Temp\\cloudbase-unattend.xml'",
      "Copy-Item -LiteralPath $unattendSource -Destination $unattend -Force",
      "$sysprep = Start-Process -FilePath 'C:\\Windows\\System32\\Sysprep\\Sysprep.exe' -ArgumentList ('/oobe /generalize /quit /mode:vm /unattend:' + $unattend) -Wait -PassThru",
      "if ($sysprep.ExitCode -ne 0) { throw \"Sysprep failed with exit code $($sysprep.ExitCode)\" }",
    ]
    skip_clean  = true
    pause_after = "5s"
  }
}
