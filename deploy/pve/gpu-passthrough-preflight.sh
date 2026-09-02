#!/usr/bin/env bash
set -euo pipefail

device_id="${1:-0000:00:02.0}"
sysfs_root="${VC_VDI_SYSFS_ROOT:-/sys}"
proc_cmdline_file="${VC_VDI_PROC_CMDLINE_FILE:-/proc/cmdline}"
device_path="${sysfs_root}/bus/pci/devices/${device_id}"

if [[ ! -d "$device_path" ]]; then
  printf 'status=blocked\nreason=device_not_found\ndevice=%s\n' "$device_id"
  exit 1
fi

vendor="$(<"$device_path/vendor")"
device="$(<"$device_path/device")"
class="$(<"$device_path/class")"
driver="unbound"
if [[ -L "$device_path/driver" ]]; then
  driver="$(basename "$(readlink "$device_path/driver")")"
fi

cmdline="$(<"$proc_cmdline_file")"
iommu_mode="implicit_or_disabled"
if [[ "$cmdline" == *"intel_iommu=on"* || "$cmdline" == *"amd_iommu=on"* ]]; then
  iommu_mode="explicit"
fi

printf 'device=%s\nvendor=%s\ndevice_id=%s\nclass=%s\ndriver=%s\niommu_mode=%s\n' \
  "$device_id" "$vendor" "$device" "$class" "$driver" "$iommu_mode"

case "$class" in
  0x03*|0X03*) ;;
  *)
    printf 'status=blocked\nreason=not_a_display_controller\n'
    exit 1
    ;;
esac

if [[ "$iommu_mode" != "explicit" ]]; then
  printf 'status=blocked\nreason=kernel_iommu_not_explicit\n'
  exit 1
fi

if [[ ! -L "$device_path/iommu_group" ]]; then
  printf 'status=blocked\nreason=iommu_group_missing\n'
  exit 1
fi

group_path="$(readlink -f "$device_path/iommu_group")"
group_id="$(basename "$group_path")"
group_devices=()
while IFS= read -r group_device; do
  group_devices+=("$group_device")
done < <(find "$group_path/devices" -mindepth 1 -maxdepth 1 -exec basename {} \; | sort)
printf 'iommu_group=%s\niommu_group_devices=%s\n' "$group_id" "$(IFS=,; echo "${group_devices[*]}")"

if (( ${#group_devices[@]} != 1 )); then
  printf 'status=blocked\nreason=iommu_group_requires_manual_review\n'
  exit 1
fi

if [[ "$driver" != "vfio-pci" && "$driver" != "unbound" ]]; then
  printf 'status=blocked\nreason=device_in_use_by_host\n'
  exit 1
fi

printf 'status=ready\nreason=exclusive_passthrough_preflight_passed\n'
