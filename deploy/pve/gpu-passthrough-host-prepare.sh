#!/usr/bin/env bash
set -euo pipefail

device_id="0000:00:02.0"
apply=false
ack_headless=false
sysfs_root="${VC_VDI_SYSFS_ROOT:-/sys}"
etc_root="${VC_VDI_ETC_ROOT:-/etc}"
qm_bin="${VC_VDI_QM_BIN:-qm}"
pct_bin="${VC_VDI_PCT_BIN:-pct}"
backup_root="${VC_VDI_BACKUP_ROOT:-/root}"

usage() {
  printf 'usage: %s [--device PCI_ID] [--apply --ack-headless]\n' "$0"
}

while (( $# > 0 )); do
  case "$1" in
    --device)
      [[ $# -ge 2 ]] || { usage >&2; exit 2; }
      device_id="$2"
      shift 2
      ;;
    --apply)
      apply=true
      shift
      ;;
    --ack-headless)
      ack_headless=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
done

device_path="${sysfs_root}/bus/pci/devices/${device_id}"
if [[ ! -d "$device_path" ]]; then
  printf 'status=blocked\nreason=device_not_found\ndevice=%s\n' "$device_id"
  exit 1
fi

vendor="$(<"$device_path/vendor")"
device="$(<"$device_path/device")"
class="$(<"$device_path/class")"
boot_vga="unknown"
if [[ -f "$device_path/boot_vga" ]]; then
  boot_vga="$(<"$device_path/boot_vga")"
fi
case "$class" in
  0x03*|0X03*) ;;
  *)
    printf 'status=blocked\nreason=not_a_display_controller\nclass=%s\n' "$class"
    exit 1
    ;;
esac

bootloader="unknown"
if [[ -s "$etc_root/kernel/proxmox-boot-uuids" ]]; then
  bootloader="proxmox-boot-tool"
elif [[ -f "$etc_root/default/grub" ]]; then
  bootloader="grub"
else
  printf 'status=blocked\nreason=bootloader_not_detected\n'
  exit 1
fi

running_vms="unknown"
if command -v "$qm_bin" >/dev/null 2>&1; then
  running_vms="$($qm_bin list | awk 'NR > 1 && $3 == "running" { ids = ids (ids ? "," : "") $1 } END { print ids }')"
  running_vms="${running_vms:-none}"
fi
running_containers="unknown"
if command -v "$pct_bin" >/dev/null 2>&1; then
  running_containers="$($pct_bin list | awk 'NR > 1 && $2 == "running" { ids = ids (ids ? "," : "") $1 } END { print ids }')"
  running_containers="${running_containers:-none}"
fi

vendor_id="${vendor#0x}"
device_value="${device#0x}"
kernel_args='intel_iommu=on iommu=pt initcall_blacklist=sysfb_init'
grub_fragment="$etc_root/default/grub.d/99-vc-vdi-iommu.cfg"
kernel_cmdline="$etc_root/kernel/cmdline"
vfio_config="$etc_root/modprobe.d/vc-vdi-vfio.conf"
modules_config="$etc_root/modules-load.d/vc-vdi-vfio.conf"

printf 'mode=%s\ndevice=%s\nvendor=%s\ndevice_id=%s\nclass=%s\nboot_vga=%s\nbootloader=%s\nrunning_vms=%s\nrunning_containers=%s\n' \
  "$([[ "$apply" == true ]] && echo apply || echo plan)" "$device_id" "$vendor" "$device" "$class" "$boot_vga" "$bootloader" "$running_vms" "$running_containers"
printf 'kernel_args=%s\nvfio_ids=%s:%s\n' "$kernel_args" "$vendor_id" "$device_value"
printf 'planned_file=%s\nplanned_file=%s\n' "$vfio_config" "$modules_config"
if [[ "$bootloader" == "grub" ]]; then
  printf 'planned_file=%s\nplanned_refresh=update-grub\n' "$grub_fragment"
else
  printf 'planned_file=%s\nplanned_refresh=proxmox-boot-tool refresh\n' "$kernel_cmdline"
fi
printf 'planned_refresh=update-initramfs -u -k all\nplanned_reboot=manual\n'

if [[ "$apply" != true ]]; then
  printf 'status=plan\nreason=no_changes_without_apply\n'
  exit 0
fi

if [[ $EUID -ne 0 ]]; then
  printf 'status=blocked\nreason=root_required\n'
  exit 1
fi
if [[ "$running_vms" == "unknown" || "$running_containers" == "unknown" ]]; then
  printf 'status=blocked\nreason=guest_inventory_not_available\n'
  exit 1
fi
if [[ "$running_vms" != "none" || "$running_containers" != "none" ]]; then
  printf 'status=blocked\nreason=running_guests_present\nrunning_vms=%s\nrunning_containers=%s\n' "$running_vms" "$running_containers"
  exit 1
fi
if [[ "$boot_vga" == "1" && "$ack_headless" != true ]]; then
  printf 'status=blocked\nreason=primary_display_requires_ack_headless\n'
  exit 1
fi

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup_dir="${backup_root}/vc-vdi-gpu-backup-${timestamp}"
install -d -m 0700 "$backup_dir"
created_files="$backup_dir/created-files"
: >"$created_files"

backup_target() {
  local target="$1"
  if [[ -e "$target" ]]; then
    install -d -m 0700 "$backup_dir$(dirname "$target")"
    cp -a -- "$target" "$backup_dir$target"
  else
    printf '%s\n' "$target" >>"$created_files"
  fi
}

write_config() {
  local target="$1"
  local content="$2"
  local temp_file
  install -d -m 0755 "$(dirname "$target")"
  temp_file="$(mktemp "$(dirname "$target")/.vc-vdi.XXXXXX")"
  printf '%s\n' "$content" >"$temp_file"
  chmod 0644 "$temp_file"
  mv -f -- "$temp_file" "$target"
}

backup_target "$vfio_config"
backup_target "$modules_config"
write_config "$vfio_config" "options vfio-pci ids=${vendor_id}:${device_value} disable_vga=1
softdep i915 pre: vfio-pci
blacklist i915"
write_config "$modules_config" "vfio
vfio_iommu_type1
vfio_pci"

if [[ "$bootloader" == "grub" ]]; then
  backup_target "$grub_fragment"
  # Preserve the variable reference for update-grub, which sources this file.
  # shellcheck disable=SC2016
  write_config "$grub_fragment" 'GRUB_CMDLINE_LINUX_DEFAULT="${GRUB_CMDLINE_LINUX_DEFAULT} intel_iommu=on iommu=pt initcall_blacklist=sysfb_init"'
  update-grub
else
  backup_target "$kernel_cmdline"
  current_cmdline="$(<"$kernel_cmdline")"
  for argument in intel_iommu=on iommu=pt initcall_blacklist=sysfb_init; do
    if [[ " $current_cmdline " != *" $argument "* ]]; then
      current_cmdline+=" $argument"
    fi
  done
  write_config "$kernel_cmdline" "$current_cmdline"
  proxmox-boot-tool refresh
fi

update-initramfs -u -k all
cat >"$backup_dir/manifest" <<EOF
device=${device_id}
vendor=${vendor}
device_id=${device}
bootloader=${bootloader}
grub_fragment=${grub_fragment}
kernel_cmdline=${kernel_cmdline}
vfio_config=${vfio_config}
modules_config=${modules_config}
EOF
chmod 0600 "$backup_dir/manifest" "$created_files"

printf 'status=prepared\nbackup_dir=%s\nreboot_required=true\nreboot_performed=false\n' "$backup_dir"
