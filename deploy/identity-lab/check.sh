#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)
compose_file="$script_dir/compose.yaml"
project_name=vc-workspace-identity-lab
lab_env=$(mktemp "${TMPDIR:-/tmp}/vc-workspace-identity-lab.XXXXXX")
chmod 0600 "$lab_env"

if docker compose version >/dev/null 2>&1; then
  compose=(docker compose)
elif command -v docker-compose >/dev/null 2>&1; then
  compose=(docker-compose)
else
  echo "Identity lab requires Docker Compose v2 or docker-compose v1." >&2
  exit 1
fi

compose_command() {
  "${compose[@]}" --project-name "$project_name" --env-file "$lab_env" -f "$compose_file" "$@"
}

cleanup() {
  compose_command down --volumes --remove-orphans >/dev/null 2>&1 || true
  rm -f "$lab_env"
}
diagnose() {
  echo "Identity lab failed; container status and recent logs follow:" >&2
  compose_command ps --all >&2 || compose_command ps -a >&2 || true
  compose_command logs --no-color --tail 80 postgres keycloak ldap sssd-client >&2 || true
}
trap diagnose ERR
trap cleanup EXIT INT TERM

random_secret() {
  openssl rand -hex 24
}

postgres_password=$(random_secret)
oidc_admin_password=$(random_secret)
oidc_client_secret=$(random_secret)
oidc_user_password=$(random_secret)
ldap_admin_password=$(random_secret)
ldap_alice_password=$(random_secret)
ldap_bob_password=$(random_secret)

{
  printf 'VC_WORKSPACE_LAB_POSTGRES_PASSWORD=%s\n' "$postgres_password"
  printf 'VC_WORKSPACE_LAB_OIDC_ADMIN_PASSWORD=%s\n' "$oidc_admin_password"
  printf 'VC_WORKSPACE_LAB_OIDC_CLIENT_SECRET=%s\n' "$oidc_client_secret"
  printf 'VC_WORKSPACE_LAB_OIDC_USER_PASSWORD=%s\n' "$oidc_user_password"
  printf 'VC_WORKSPACE_LAB_LDAP_ADMIN_PASSWORD=%s\n' "$ldap_admin_password"
  printf 'VC_WORKSPACE_LAB_LDAP_ALICE_PASSWORD=%s\n' "$ldap_alice_password"
  printf 'VC_WORKSPACE_LAB_LDAP_BOB_PASSWORD=%s\n' "$ldap_bob_password"
} >"$lab_env"

for port in 18080 18081 55436; do
  if (exec 3<>"/dev/tcp/127.0.0.1/$port") >/dev/null 2>&1; then
    echo "Identity lab requires free local TCP port $port" >&2
    false
  fi
done

echo "Starting disposable Keycloak, OpenLDAP, PostgreSQL, and Debian 13 SSSD lab..."
if [ "${VC_WORKSPACE_IDENTITY_LAB_SKIP_BUILD:-false}" = true ]; then
  compose_command up --detach --no-build
else
  compose_command up --detach --build
fi

discovery_url=http://127.0.0.1:18080/realms/vc-workspace-lab/.well-known/openid-configuration
for _ in $(seq 1 90); do
  if curl --fail --silent --show-error "$discovery_url" >/dev/null 2>&1; then
    break
  fi
  sleep 2
done
curl --fail --silent --show-error "$discovery_url" >/dev/null

sssd_container=$(compose_command ps --quiet sssd-client)
if [ -z "$sssd_container" ]; then
  echo "SSSD client container did not start" >&2
  false
fi
for _ in $(seq 1 45); do
  system_state=$(docker exec "$sssd_container" systemctl is-system-running 2>/dev/null || true)
  if [ "$system_state" = running ] || [ "$system_state" = degraded ]; then
    break
  fi
  sleep 1
done
if [ "${system_state:-}" != running ] && [ "${system_state:-}" != degraded ]; then
  echo "SSSD client systemd did not become ready (state: ${system_state:-unknown})" >&2
  false
fi

echo "Running VC Workspace against the real OIDC authorization flow and SSSD reconciliation plan..."
(
  cd "$repo_root"
  if [ -n "${VC_WORKSPACE_IDENTITY_LAB_TEST_BINARY:-}" ]; then
    test_command=(
      "$VC_WORKSPACE_IDENTITY_LAB_TEST_BINARY"
      -test.run 'TestLive(OIDC|LDAP)Identity'
      -test.v
      -test.count=1
      -test.timeout=5m
    )
  else
    test_command=(
      go test ./internal/httpapi
      -run 'TestLive(OIDC|LDAP)Identity'
      -v
      -count=1
      -timeout 5m
    )
  fi
  VC_WORKSPACE_LIVE_OIDC_IDENTITY_AUDIT=true \
  VC_WORKSPACE_LIVE_OIDC_ISSUER=http://127.0.0.1:18080/realms/vc-workspace-lab \
  VC_WORKSPACE_LIVE_OIDC_CLIENT_ID=vc-workspace-lab \
  VC_WORKSPACE_LIVE_OIDC_CLIENT_SECRET="$oidc_client_secret" \
  VC_WORKSPACE_LIVE_OIDC_USERNAME=alice \
  VC_WORKSPACE_LIVE_OIDC_PASSWORD="$oidc_user_password" \
  VC_WORKSPACE_LIVE_OIDC_ADMIN_USERNAME=vc-workspace-lab-admin \
  VC_WORKSPACE_LIVE_OIDC_ADMIN_PASSWORD="$oidc_admin_password" \
  VC_WORKSPACE_LIVE_DATABASE_URL="postgres://vc_workspace:${postgres_password}@127.0.0.1:55436/vc_workspace_identity_lab?sslmode=disable" \
  VC_WORKSPACE_LIVE_LDAP_IDENTITY_AUDIT=true \
  VC_WORKSPACE_LIVE_LDAP_CLIENT_CONTAINER="$sssd_container" \
  VC_WORKSPACE_LIVE_LDAP_ALICE_PASSWORD="$ldap_alice_password" \
  VC_WORKSPACE_LIVE_LDAP_BOB_PASSWORD="$ldap_bob_password" \
  "${test_command[@]}"
)

echo "Identity lab passed; disposable containers and volumes will now be removed."
