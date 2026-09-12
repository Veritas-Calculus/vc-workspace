#!/bin/sh
set -eu

attempt=0
while [ ! -s /shared/ca.crt ]; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 60 ]; then
    echo "LDAP lab CA was not created" >&2
    exit 1
  fi
  sleep 1
done

install -m 0644 /shared/ca.crt /usr/local/share/ca-certificates/vc-workspace-identity-lab.crt
update-ca-certificates >/dev/null
install -d -m 0755 /usr/share/vc-workspace
printf '%s\n' '{"schema_version":1,"identity_modes":["managed_local","linux_sssd_ldap"]}' \
  >/usr/share/vc-workspace/identity-capabilities.json

exec "$@"
