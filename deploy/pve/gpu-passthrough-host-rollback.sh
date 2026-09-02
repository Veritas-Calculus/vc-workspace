#!/usr/bin/env bash
set -euo pipefail

backup_dir="${1:-}"
apply="${2:-}"
etc_root="${VC_VDI_ETC_ROOT:-/etc}"
backup_root="${VC_VDI_BACKUP_ROOT:-/root}"
if [[ -z "$backup_dir" || ( "$apply" != "" && "$apply" != "--apply" ) ]]; then
  printf 'usage: %s /root/vc-vdi-gpu-backup-TIMESTAMP [--apply]\n' "$0" >&2
  exit 2
fi
if [[ ! -f "$backup_dir/manifest" || ! -f "$backup_dir/created-files" ]]; then
  printf 'status=blocked\nreason=invalid_backup_directory\n'
  exit 1
fi
if [[ "$backup_dir" != "$backup_root"/vc-vdi-gpu-backup-* ]]; then
  printf 'status=blocked\nreason=backup_directory_outside_allowed_root\n'
  exit 1
fi

manifest_value() {
  local key="$1"
  awk -F= -v key="$key" '$1 == key { sub(/^[^=]*=/, ""); print; found=1; exit } END { if (!found) exit 1 }' "$backup_dir/manifest"
}

bootloader="$(manifest_value bootloader)"
grub_fragment="$(manifest_value grub_fragment)"
kernel_cmdline="$(manifest_value kernel_cmdline)"
vfio_config="$(manifest_value vfio_config)"
modules_config="$(manifest_value modules_config)"
expected_grub_fragment="$etc_root/default/grub.d/99-vc-vdi-iommu.cfg"
expected_kernel_cmdline="$etc_root/kernel/cmdline"
expected_vfio_config="$etc_root/modprobe.d/vc-vdi-vfio.conf"
expected_modules_config="$etc_root/modules-load.d/vc-vdi-vfio.conf"
if [[ "$grub_fragment" != "$expected_grub_fragment" || \
      "$kernel_cmdline" != "$expected_kernel_cmdline" || \
      "$vfio_config" != "$expected_vfio_config" || \
      "$modules_config" != "$expected_modules_config" ]]; then
  printf 'status=blocked\nreason=manifest_target_mismatch\n'
  exit 1
fi
if [[ "$bootloader" != "grub" && "$bootloader" != "proxmox-boot-tool" ]]; then
  printf 'status=blocked\nreason=manifest_bootloader_invalid\n'
  exit 1
fi
allowed_targets=("$grub_fragment" "$kernel_cmdline" "$vfio_config" "$modules_config")
printf 'mode=%s\nbackup_dir=%s\nbootloader=%s\n' "$([[ "$apply" == "--apply" ]] && echo apply || echo plan)" "$backup_dir" "$bootloader"
printf 'target=%s\n' "${allowed_targets[@]}"

if [[ "$apply" != "--apply" ]]; then
  printf 'status=plan\nreason=no_changes_without_apply\n'
  exit 0
fi
if [[ $EUID -ne 0 ]]; then
  printf 'status=blocked\nreason=root_required\n'
  exit 1
fi

is_allowed_target() {
  local candidate="$1"
  local target
  for target in "${allowed_targets[@]}"; do
    if [[ "$candidate" == "$target" ]]; then
      return 0
    fi
  done
  return 1
}

for target in "${allowed_targets[@]}"; do
  if [[ -e "$backup_dir$target" ]]; then
    install -d -m 0755 "$(dirname "$target")"
    cp -a -- "$backup_dir$target" "$target"
  fi
done
while IFS= read -r target; do
  [[ -n "$target" ]] || continue
  if ! is_allowed_target "$target"; then
    printf 'status=blocked\nreason=backup_contains_unexpected_target\ntarget=%s\n' "$target"
    exit 1
  fi
  rm -f -- "$target"
done <"$backup_dir/created-files"

if [[ "$bootloader" == "grub" ]]; then
  update-grub
else
  proxmox-boot-tool refresh
fi
update-initramfs -u -k all
printf 'status=rolled_back\nreboot_required=true\nreboot_performed=false\n'
