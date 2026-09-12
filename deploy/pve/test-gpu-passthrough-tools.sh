#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
fixture_root="$(mktemp -d)"
trap 'rm -rf -- "$fixture_root"' EXIT

sysfs_root="$fixture_root/sys"
etc_root="$fixture_root/etc"
bin_root="$fixture_root/bin"
backup_root="$fixture_root/backups"
proc_cmdline="$fixture_root/proc-cmdline"
device_id="0000:00:02.0"
device_path="$sysfs_root/bus/pci/devices/$device_id"
group_path="$sysfs_root/kernel/iommu_groups/7"

mkdir -p "$device_path" "$group_path/devices" "$sysfs_root/bus/pci/drivers/vfio-pci" \
  "$etc_root/default/grub.d" "$etc_root/modprobe.d" "$etc_root/modules-load.d" \
  "$etc_root/kernel" "$bin_root" "$backup_root"
printf '0x8086\n' >"$device_path/vendor"
printf '0x1912\n' >"$device_path/device"
printf '0x030000\n' >"$device_path/class"
printf '1\n' >"$device_path/boot_vga"
printf 'BOOT_IMAGE=/boot/vmlinuz-test quiet intel_iommu=on iommu=pt\n' >"$proc_cmdline"
printf 'GRUB_CMDLINE_LINUX_DEFAULT="quiet"\n' >"$etc_root/default/grub"
ln -s "$sysfs_root/bus/pci/drivers/vfio-pci" "$device_path/driver"
ln -s "$group_path" "$device_path/iommu_group"
ln -s "$device_path" "$group_path/devices/$device_id"

cat >"$bin_root/qm" <<'EOF'
#!/usr/bin/env bash
printf ' VMID NAME                 STATUS     MEM(MB)    BOOTDISK(GB) PID\n'
EOF
chmod 0755 "$bin_root/qm"
cat >"$bin_root/pct" <<'EOF'
#!/usr/bin/env bash
printf 'VMID       Status     Lock         Name\n'
EOF
chmod 0755 "$bin_root/pct"

common_env=(
  "VC_WORKSPACE_SYSFS_ROOT=$sysfs_root"
  "VC_WORKSPACE_PROC_CMDLINE_FILE=$proc_cmdline"
  "VC_WORKSPACE_ETC_ROOT=$etc_root"
  "VC_WORKSPACE_QM_BIN=$bin_root/qm"
  "VC_WORKSPACE_PCT_BIN=$bin_root/pct"
  "VC_WORKSPACE_BACKUP_ROOT=$backup_root"
  "VC_WORKSPACE_OUT_OF_BAND_CONSOLE_CONFIRMED=true"
)

preflight_output="$(env "${common_env[@]}" "$repo_root/deploy/pve/gpu-passthrough-preflight.sh" "$device_id")"
grep -q '^status=ready$' <<<"$preflight_output"

audit_output="$(env "${common_env[@]}" "$repo_root/deploy/pve/gpu-passthrough-host-audit.sh" "$device_id")"
grep -q '^bootloader=grub$' <<<"$audit_output"
grep -q '^running_vms=none$' <<<"$audit_output"
grep -q '^running_containers=none$' <<<"$audit_output"
grep -q '^status=ready$' <<<"$audit_output"

prepare_output="$(env "${common_env[@]}" "$repo_root/deploy/pve/gpu-passthrough-host-prepare.sh" --device "$device_id")"
grep -q '^mode=plan$' <<<"$prepare_output"
grep -q '^status=plan$' <<<"$prepare_output"
[[ ! -e "$etc_root/modprobe.d/vc-workspace-vfio.conf" ]]
[[ ! -e "$etc_root/default/grub.d/99-vc-workspace-iommu.cfg" ]]

printf 'BOOT_IMAGE=/boot/vmlinuz-test quiet\n' >"$proc_cmdline"
if env "${common_env[@]}" "$repo_root/deploy/pve/gpu-passthrough-preflight.sh" "$device_id" >"$fixture_root/blocked-output" 2>&1; then
  printf 'expected preflight to fail without explicit IOMMU\n' >&2
  exit 1
fi
grep -q '^reason=kernel_iommu_not_explicit$' "$fixture_root/blocked-output"
printf 'BOOT_IMAGE=/boot/vmlinuz-test quiet intel_iommu=on iommu=pt\n' >"$proc_cmdline"

cat >"$bin_root/qm" <<'EOF'
#!/usr/bin/env bash
printf ' VMID NAME                 STATUS     MEM(MB)    BOOTDISK(GB) PID\n'
printf '  149 fixture-vm           running    1024              8.00 42\n'
EOF
chmod 0755 "$bin_root/qm"
if env "${common_env[@]}" "$repo_root/deploy/pve/gpu-passthrough-host-audit.sh" "$device_id" >"$fixture_root/running-output" 2>&1; then
  printf 'expected audit to block while guests are running\n' >&2
  exit 1
fi
grep -q '^running_vms=149$' "$fixture_root/running-output"
grep -q 'running_guests_present' "$fixture_root/running-output"

rollback_fixture="$backup_root/vc-workspace-gpu-backup-20260902T000000Z"
mkdir -p "$rollback_fixture"
cat >"$rollback_fixture/manifest" <<EOF
device=$device_id
vendor=0x8086
device_id=0x1912
bootloader=grub
grub_fragment=$etc_root/default/grub.d/99-vc-workspace-iommu.cfg
kernel_cmdline=$etc_root/kernel/cmdline
vfio_config=$etc_root/modprobe.d/vc-workspace-vfio.conf
modules_config=$etc_root/modules-load.d/vc-workspace-vfio.conf
EOF
: >"$rollback_fixture/created-files"
rollback_output="$(env "${common_env[@]}" "$repo_root/deploy/pve/gpu-passthrough-host-rollback.sh" "$rollback_fixture")"
grep -q '^mode=plan$' <<<"$rollback_output"
grep -q '^status=plan$' <<<"$rollback_output"

sed 's#vc-workspace-vfio.conf#unexpected.conf#' "$rollback_fixture/manifest" >"$rollback_fixture/manifest.invalid"
mv "$rollback_fixture/manifest.invalid" "$rollback_fixture/manifest"
if env "${common_env[@]}" "$repo_root/deploy/pve/gpu-passthrough-host-rollback.sh" "$rollback_fixture" >"$fixture_root/rollback-invalid-output" 2>&1; then
  printf 'expected rollback to reject a manifest target mismatch\n' >&2
  exit 1
fi
grep -q '^reason=manifest_target_mismatch$' "$fixture_root/rollback-invalid-output"

printf 'gpu passthrough tool tests passed\n'
