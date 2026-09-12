#!/usr/bin/env bash
set -euo pipefail

mirror_url="${VC_WORKSPACE_MIRROR_URL:-${VC_VDI_MIRROR_URL:-}}"
security_mirror_url="${VC_WORKSPACE_SECURITY_MIRROR_URL:-${VC_VDI_SECURITY_MIRROR_URL:-}}"
if [ "${mirror_url}" = "" ] || [ "${security_mirror_url}" = "" ]; then
  echo "VC Workspace mirror URLs are required" >&2
  exit 1
fi

. /etc/os-release
if [ "${VERSION_CODENAME:-}" != "trixie" ]; then
  echo "expected Debian 13 trixie, found ${PRETTY_NAME:-unknown}" >&2
  exit 1
fi

install -m 0755 /tmp/vc-workspace-guest-agent /usr/local/sbin/vc-workspace-guest-agent
install -d -m 0755 /usr/local/lib/security
install -m 0644 /tmp/pam_vcworkspace.so /usr/local/lib/security/pam_vcworkspace.so
install -m 0644 /tmp/pam_vcworkspace_native.so /usr/local/lib/security/pam_vcworkspace_native.so
install -d -m 0755 /var/lib/vc-workspace
install -d -m 0755 /etc/vc-workspace
install -d -m 0755 /usr/share/backgrounds/vc-workspace
install -m 0644 /tmp/vc-workspace-desktop-background.png /usr/share/backgrounds/vc-workspace/desktop-background.png
printf '%s\n' managed >/etc/vc-workspace/background-policy
chmod 0644 /etc/vc-workspace/background-policy

cat >/usr/local/bin/vc-workspace-apply-background <<'BACKGROUND'
#!/usr/bin/env bash
set -u
wallpaper=/usr/share/backgrounds/vc-workspace/desktop-background.png
[ "$(cat /etc/vc-workspace/background-policy 2>/dev/null || true)" = managed ] || exit 0
command -v xfconf-query >/dev/null 2>&1 || exit 0
sleep 1
mapfile -t image_properties < <(xfconf-query -c xfce4-desktop -l 2>/dev/null | grep -E '/(last-image|image-path)$' || true)
if [ "${#image_properties[@]}" -eq 0 ]; then
  image_properties=(/backdrop/screen0/monitorrdp0/workspace0/last-image)
fi
for property in "${image_properties[@]}"; do
  xfconf-query -c xfce4-desktop -p "$property" --create -t string -s "$wallpaper" >/dev/null 2>&1 || true
  style_property="${property%/*}/image-style"
  xfconf-query -c xfce4-desktop -p "$style_property" --create -t int -s 5 >/dev/null 2>&1 || true
done
BACKGROUND
chmod 0755 /usr/local/bin/vc-workspace-apply-background
install -d -m 0755 /etc/xdg/autostart
cat >/etc/xdg/autostart/vc-workspace-background.desktop <<'BACKGROUND_DESKTOP'
[Desktop Entry]
Type=Application
Name=VC Workspace Background
Exec=/usr/local/bin/vc-workspace-apply-background
OnlyShowIn=XFCE;
NoDisplay=true
X-GNOME-Autostart-enabled=true
BACKGROUND_DESKTOP

cat >/etc/apt/sources.list <<SOURCES
deb ${mirror_url} trixie main contrib non-free-firmware
deb ${mirror_url} trixie-updates main contrib non-free-firmware
deb ${security_mirror_url} trixie-security main contrib non-free-firmware
SOURCES

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends \
  adcli \
  at-spi2-core \
  cloud-init \
  dbus-x11 \
  freeipa-client \
  gir1.2-gtk-3.0 \
  krb5-user \
  libegl1 \
  libgbm1 \
  libpipewire-0.3-0 \
  libwayland-client0 \
  libwayland-server0 \
  libnss-sss \
  libpam-sss \
  libxcb1 \
  oddjob-mkhomedir \
  qemu-guest-agent \
  python3-gi \
  realmd \
  spice-vdagent \
  sssd-ad \
  sssd-idp \
  sssd-ipa \
  sssd-ldap \
  sssd-tools \
  sudo \
  wmctrl \
  x11-utils \
  xfce4 \
  xfce4-goodies \
  xorgxrdp \
  xrdp

# Only a fresh image is in scope. The installer rejects unknown/newer Debian
# versions instead of silently downgrading a security update.
python3 /tmp/vc-workspace-install-xrdp.py
rm -f /tmp/vc-workspace-install-xrdp.py /tmp/vc-workspace-xrdp.deb

install -d -m 0755 /usr/local/lib/vc-workspace /var/lib/vc-workspace/display-scale
install -m 0644 /tmp/vc-workspace-session-layout.py /usr/local/lib/vc-workspace/session_layout.py
cat >/etc/xdg/autostart/vc-workspace-display.desktop <<'DISPLAY_DESKTOP'
[Desktop Entry]
Type=Application
Name=VC Workspace Display
Exec=/usr/bin/python3 /usr/local/lib/vc-workspace/session_layout.py
OnlyShowIn=XFCE;
NoDisplay=true
DISPLAY_DESKTOP

install -d -m 0755 /usr/share/vc-workspace
cat >/usr/share/vc-workspace/identity-capabilities.json <<'CAPABILITIES'
{
  "schema_version": 1,
  "platform": "linux",
  "modes": ["managed_local", "linux_sssd_ad", "linux_sssd_freeipa", "linux_sssd_ldap", "linux_sssd_oidc"],
  "sssd_oidc": "experimental"
}
CAPABILITIES

if ! id vdi >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash vdi
fi
passwd --lock vdi
printf 'startxfce4\n' >/home/vdi/.xsession
chown vdi:vdi /home/vdi/.xsession
install -d -o vdi -g vdi -m 0700 \
  /home/vdi/.config/xfce4/xfconf/xfce-perchannel-xml
cat >/home/vdi/.config/xfce4/xfconf/xfce-perchannel-xml/xsettings.xml <<'XSETTINGS'
<?xml version="1.0" encoding="UTF-8"?>
<channel name="xsettings" version="1.0">
  <property name="Xft" type="empty">
    <property name="DPI" type="int" value="96"/>
  </property>
</channel>
XSETTINGS
chown -R vdi:vdi /home/vdi/.config
# Agent identities and their Helper autostart are created on demand by the
# root dispatcher. Fresh templates must not install a shared vdi Helper.
/usr/local/sbin/vc-workspace-guest-agent computer-v2-control-init
# This is a fresh image, not an upgrade of an occupied desktop. Keep both
# login creators stopped while explicitly installing the two fixed policies.
systemctl stop xrdp.service xrdp-sesman.service
python3 /tmp/vc-workspace-install-login-fence.py
python3 /tmp/vc-workspace-install-login-fence.py --native
/usr/local/sbin/vc-workspace-guest-agent computer-v2-capabilities | python3 -c 'import json,sys; v=json.load(sys.stdin); assert v["native_account_fence"]=="credential_revision_v1" and v["native_login_birth_fence"]=="pam_logind_native_v1"'
rm -f /tmp/vc-workspace-install-login-fence.py
usermod -aG ssl-cert xrdp

for unit in vc-workspace-agent.service vc-workspace-accounts.service vc-workspace-accounts.timer; do
  install -m 0644 "/tmp/${unit}" "/etc/systemd/system/${unit}"
  rm -f "/tmp/${unit}"
done
systemctl daemon-reload
systemd-analyze verify /etc/systemd/system/vc-workspace-{agent.service,accounts.service,accounts.timer}
systemctl enable qemu-guest-agent xrdp vc-workspace-agent vc-workspace-accounts.timer

rm -f /etc/ssh/ssh_host_*
truncate -s 0 /etc/machine-id
rm -f /var/lib/dbus/machine-id
cloud-init clean --logs --seed
rm -f /etc/sudoers.d/vdi-builder /tmp/vc-workspace-guest-agent /tmp/pam_vcworkspace.so /tmp/pam_vcworkspace_native.so /tmp/vc-workspace-desktop-background.png
passwd --lock vdi-builder
