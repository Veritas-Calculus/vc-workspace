#!/usr/bin/env bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "Run this script as root." >&2
  exit 1
fi

server_ip=${VC_WORKSPACE_AD_LAB_SERVER_IP:?set VC_WORKSPACE_AD_LAB_SERVER_IP}
domain=${VC_WORKSPACE_AD_LAB_DOMAIN:-ad.vcw.test}
realm=${VC_WORKSPACE_AD_LAB_REALM:-AD.VCW.TEST}
server_hostname=${VC_WORKSPACE_AD_LAB_SERVER_HOSTNAME:-dc1.ad.vcw.test}
client_hostname=${VC_WORKSPACE_AD_LAB_CLIENT_HOSTNAME:-client1.ad.vcw.test}

hostnamectl set-hostname "$client_hostname"
sed -i "/[[:space:]]${server_hostname//./\\.}\([[:space:]]\|$\)/d" /etc/hosts
sed -i "/[[:space:]]${client_hostname//./\\.}\([[:space:]]\|$\)/d" /etc/hosts
printf '%s %s %s\n' "$server_ip" "$server_hostname" "${server_hostname%%.*}" >>/etc/hosts

if [ ! -e /root/resolv.conf.pre-vcw-lab ]; then
  cp -a /etc/resolv.conf /root/resolv.conf.pre-vcw-lab
fi
systemctl disable --now systemd-resolved >/dev/null 2>&1 || true
if [ -L /etc/resolv.conf ]; then
  unlink /etc/resolv.conf
fi
printf 'search %s\nnameserver %s\n' "$domain" "$server_ip" >/etc/resolv.conf

apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y \
  realmd adcli sssd sssd-ad sssd-tools libnss-sss libpam-sss \
  krb5-user oddjob oddjob-mkhomedir packagekit samba-common-bin \
  bind9-dnsutils pamtester

install -d -o root -g root -m 0755 /usr/share/vc-workspace
printf '{"schema":1,"platform":"linux","identity_modes":["managed_local","linux_sssd_ad"]}\n' \
  >/usr/share/vc-workspace/identity-capabilities.json
chmod 0644 /usr/share/vc-workspace/identity-capabilities.json

host -t SRV "_ldap._tcp.$domain" "$server_ip" >/dev/null
host -t SRV "_kerberos._udp.$domain" "$server_ip" >/dev/null
realm discover "$domain" >/dev/null

echo "Samba AD lab client prerequisites are ready."
printf 'hostname=%s domain=%s realm=%s dns=%s\n' "$client_hostname" "$domain" "$realm" "$server_ip"
