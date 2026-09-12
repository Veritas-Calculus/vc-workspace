#!/usr/bin/env bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "Run this script as root." >&2
  exit 1
fi

credentials_file=${VC_WORKSPACE_AD_LAB_CREDENTIALS_FILE:-/root/vc-workspace-ad-lab.env}
server_ip=${VC_WORKSPACE_AD_LAB_SERVER_IP:?set VC_WORKSPACE_AD_LAB_SERVER_IP}
dns_forwarder=${VC_WORKSPACE_AD_LAB_DNS_FORWARDER:-10.31.0.252}
domain=${VC_WORKSPACE_AD_LAB_DOMAIN:-ad.vcw.test}
realm=${VC_WORKSPACE_AD_LAB_REALM:-AD.VCW.TEST}
netbios_domain=${VC_WORKSPACE_AD_LAB_NETBIOS_DOMAIN:-VCWLAB}
hostname=${VC_WORKSPACE_AD_LAB_HOSTNAME:-dc1.ad.vcw.test}
allowed_group=${VC_WORKSPACE_AD_LAB_ALLOWED_GROUP:-workspace-users}

if [ ! -r "$credentials_file" ]; then
  echo "AD lab credentials file is not readable." >&2
  exit 1
fi
# shellcheck disable=SC1090
source "$credentials_file"
: "${VCW_AD_ADMIN_PASSWORD:?missing from credentials file}"
: "${VCW_AD_ALICE_PASSWORD:?missing from credentials file}"
: "${VCW_AD_BOB_PASSWORD:?missing from credentials file}"

hostnamectl set-hostname "$hostname"
sed -i "/[[:space:]]${hostname//./\\.}\([[:space:]]\|$\)/d" /etc/hosts
printf '%s %s %s\n' "$server_ip" "$hostname" "${hostname%%.*}" >>/etc/hosts

apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y \
  samba samba-ad-dc winbind krb5-user bind9-dnsutils ldb-tools smbclient

systemctl disable --now smbd nmbd winbind >/dev/null 2>&1 || true
systemctl stop samba-ad-dc >/dev/null 2>&1 || true

if [ ! -f /var/lib/samba/private/sam.ldb ]; then
  if [ -e /etc/samba/smb.conf ]; then
    install -o root -g root -m 0600 /etc/samba/smb.conf /root/smb.conf.pre-vcw-lab
    mv /etc/samba/smb.conf /etc/samba/smb.conf.pre-vcw-lab
  fi
  samba-tool domain provision \
    --use-rfc2307 \
    --realm="$realm" \
    --domain="$netbios_domain" \
    --server-role=dc \
    --dns-backend=SAMBA_INTERNAL \
    --option="dns forwarder = $dns_forwarder" \
    --adminpass="$VCW_AD_ADMIN_PASSWORD"
fi

install -o root -g root -m 0644 /var/lib/samba/private/krb5.conf /etc/krb5.conf
if ! grep -Eq '^[[:space:]]*interfaces[[:space:]]*=' /etc/samba/smb.conf; then
  sed -i "/^\[global\]/a\\\tinterfaces = lo eth0\n\tbind interfaces only = yes" /etc/samba/smb.conf
fi
if [ ! -e /root/resolv.conf.pre-vcw-lab ]; then
  cp -a /etc/resolv.conf /root/resolv.conf.pre-vcw-lab
fi
systemctl disable --now systemd-resolved >/dev/null 2>&1 || true
if [ -L /etc/resolv.conf ]; then
  unlink /etc/resolv.conf
fi
printf 'search %s\nnameserver 127.0.0.1\n' "$domain" >/etc/resolv.conf

systemctl unmask samba-ad-dc >/dev/null
systemctl enable samba-ad-dc >/dev/null
systemctl restart samba-ad-dc
for _ in $(seq 1 60); do
  if samba-tool domain info 127.0.0.1 >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
samba-tool domain info 127.0.0.1 >/dev/null

if ! samba-tool group show "$allowed_group" >/dev/null 2>&1; then
  samba-tool group add "$allowed_group" >/dev/null
fi
if ! samba-tool user show alice >/dev/null 2>&1; then
  samba-tool user create alice "$VCW_AD_ALICE_PASSWORD" >/dev/null
fi
if ! samba-tool user show bob >/dev/null 2>&1; then
  samba-tool user create bob "$VCW_AD_BOB_PASSWORD" >/dev/null
fi
samba-tool user setexpiry alice --noexpiry >/dev/null
samba-tool user setexpiry bob --noexpiry >/dev/null
if ! samba-tool group listmembers "$allowed_group" | grep -Fxiq alice; then
  samba-tool group addmembers "$allowed_group" alice >/dev/null
fi

printf '%s\n' "$VCW_AD_ADMIN_PASSWORD" | kinit "administrator@$realm"
klist -s
short_hostname=${hostname%%.*}
mapfile -t registered_addresses < <(
  samba-tool dns query "$hostname" "$domain" "$short_hostname" A --use-kerberos=required |
    sed -n 's/.*A: \([0-9.]*\).*/\1/p'
)
for registered_address in "${registered_addresses[@]}"; do
  if [ "$registered_address" != "$server_ip" ]; then
    samba-tool dns delete "$hostname" "$domain" "$short_hostname" A "$registered_address" --use-kerberos=required >/dev/null
  fi
done
if ! samba-tool dns query "$hostname" "$domain" "$short_hostname" A --use-kerberos=required | grep -Fq "A: $server_ip"; then
  samba-tool dns add "$hostname" "$domain" "$short_hostname" A "$server_ip" --use-kerberos=required >/dev/null
fi
kdestroy

echo "Samba AD lab server is ready."
printf 'hostname=%s domain=%s realm=%s allowed_group=%s\n' "$hostname" "$domain" "$realm" "$allowed_group"
