#!/usr/bin/env bash
set -euo pipefail

device_id="${1:-0000:00:02.0}"
sysfs_root="${VC_VDI_SYSFS_ROOT:-/sys}"
proc_cmdline_file="${VC_VDI_PROC_CMDLINE_FILE:-/proc/cmdline}"
etc_root="${VC_VDI_ETC_ROOT:-/etc}"
qm_bin="${VC_VDI_QM_BIN:-qm}"
pct_bin="${VC_VDI_PCT_BIN:-pct}"
oob_confirmed="${VC_VDI_OUT_OF_BAND_CONSOLE_CONFIRMED:-false}"
device_path="${sysfs_root}/bus/pci/devices/${device_id}"
reasons=()

append_reason() {
  reasons+=("$1")
}

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
driver="unbound"
if [[ -L "$device_path/driver" ]]; then
  driver="$(basename "$(readlink "$device_path/driver")")"
fi

cmdline="$(<"$proc_cmdline_file")"
iommu_mode="implicit_or_disabled"
if [[ "$cmdline" == *"intel_iommu=on"* || "$cmdline" == *"amd_iommu=on"* ]]; then
  iommu_mode="explicit"
else
  append_reason kernel_iommu_not_explicit
fi

bootloader="unknown"
if [[ -s "$etc_root/kernel/proxmox-boot-uuids" ]]; then
  bootloader="proxmox-boot-tool"
elif [[ -f "$etc_root/default/grub" ]]; then
  bootloader="grub"
else
  append_reason bootloader_not_detected
fi

iommu_group="missing"
iommu_group_devices=""
if [[ -L "$device_path/iommu_group" ]]; then
  group_path="$(readlink -f "$device_path/iommu_group")"
  iommu_group="$(basename "$group_path")"
  group_devices=()
  while IFS= read -r group_device; do
    group_devices+=("$group_device")
  done < <(find "$group_path/devices" -mindepth 1 -maxdepth 1 -exec basename {} \; | sort)
  iommu_group_devices="$(IFS=,; echo "${group_devices[*]}")"
  if (( ${#group_devices[@]} != 1 )); then
    append_reason iommu_group_requires_manual_review
  fi
else
  append_reason iommu_group_missing
fi

running_vms="unknown"
if command -v "$qm_bin" >/dev/null 2>&1; then
  running_vms="$($qm_bin list | awk 'NR > 1 && $3 == "running" { ids = ids (ids ? "," : "") $1 } END { print ids }')"
  running_vms="${running_vms:-none}"
  if [[ "$running_vms" != "none" ]]; then
    append_reason running_guests_present
  fi
else
  append_reason qm_not_available
fi
running_containers="unknown"
if command -v "$pct_bin" >/dev/null 2>&1; then
  running_containers="$($pct_bin list | awk 'NR > 1 && $2 == "running" { ids = ids (ids ? "," : "") $1 } END { print ids }')"
  running_containers="${running_containers:-none}"
  if [[ "$running_containers" != "none" ]]; then
    append_reason running_guests_present
  fi
else
  append_reason pct_not_available
fi

case "$class" in
  0x03*|0X03*) ;;
  *) append_reason not_a_display_controller ;;
esac
if [[ "$driver" != "vfio-pci" && "$driver" != "unbound" ]]; then
  append_reason device_in_use_by_host
fi
if [[ "$boot_vga" == "1" && "$oob_confirmed" != "true" ]]; then
  append_reason primary_display_requires_out_of_band_console
fi

printf 'device=%s\nvendor=%s\ndevice_id=%s\nclass=%s\nboot_vga=%s\ndriver=%s\n' \
  "$device_id" "$vendor" "$device" "$class" "$boot_vga" "$driver"
printf 'bootloader=%s\niommu_mode=%s\niommu_group=%s\niommu_group_devices=%s\nrunning_vms=%s\nrunning_containers=%s\n' \
  "$bootloader" "$iommu_mode" "$iommu_group" "$iommu_group_devices" "$running_vms" "$running_containers"

if (( ${#reasons[@]} > 0 )); then
  printf 'status=blocked\nreasons=%s\n' "$(IFS=,; echo "${reasons[*]}")"
  exit 1
fi

printf 'status=ready\nreason=host_ready_for_exclusive_passthrough\n'
