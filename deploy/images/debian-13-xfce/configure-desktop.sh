#!/usr/bin/env bash
set -euo pipefail

if [ "${VC_VDI_MIRROR_URL:-}" = "" ] || [ "${VC_VDI_SECURITY_MIRROR_URL:-}" = "" ]; then
  echo "VC Workspace mirror URLs are required" >&2
  exit 1
fi

. /etc/os-release
if [ "${VERSION_CODENAME:-}" != "trixie" ]; then
  echo "expected Debian 13 trixie, found ${PRETTY_NAME:-unknown}" >&2
  exit 1
fi

install -m 0755 /tmp/vc-vdi-guest-agent /usr/local/sbin/vc-vdi-guest-agent
install -d -m 0755 /var/lib/vc-vdi

cat >/etc/apt/sources.list <<SOURCES
deb ${VC_VDI_MIRROR_URL} trixie main contrib non-free-firmware
deb ${VC_VDI_MIRROR_URL} trixie-updates main contrib non-free-firmware
deb ${VC_VDI_SECURITY_MIRROR_URL} trixie-security main contrib non-free-firmware
SOURCES

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends \
  cloud-init \
  dbus-x11 \
  qemu-guest-agent \
  spice-vdagent \
  sudo \
  xfce4 \
  xfce4-goodies \
  xorgxrdp \
  xrdp

if ! id vdi >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash vdi
fi
passwd --lock vdi
printf 'startxfce4\n' >/home/vdi/.xsession
chown vdi:vdi /home/vdi/.xsession
usermod -aG ssl-cert xrdp

cat >/etc/systemd/system/vc-vdi-agent.service <<'UNIT'
[Unit]
Description=VC Workspace Guest Agent
After=network-online.target xrdp.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/sbin/vc-vdi-guest-agent --state-dir /var/lib/vc-vdi
Restart=always
RestartSec=3
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/vc-vdi

[Install]
WantedBy=multi-user.target
UNIT

systemctl enable qemu-guest-agent xrdp vc-vdi-agent

rm -f /etc/ssh/ssh_host_*
truncate -s 0 /etc/machine-id
rm -f /var/lib/dbus/machine-id
cloud-init clean --logs --seed
rm -f /etc/sudoers.d/vdi-builder /tmp/vc-vdi-guest-agent
passwd --lock vdi-builder
