#!/usr/bin/env bash
set -euo pipefail

# Run against an already-built, local image. No external service/OS account,
# privileged container, host network, Docker socket mount or published port.
worker_image="${1:?usage: test-container.sh local-image}"
worker_arch="$(docker image inspect "$worker_image" --format '{{.Architecture}}')"
case "$worker_arch" in amd64|arm64) ;; *) echo 'unsupported test image architecture' >&2; exit 2;; esac
fixture_dir="$(mktemp -d "${TMPDIR:-/tmp}/vcw-rdp-guards.XXXXXXXX")"
fixture_id="$(basename "$fixture_dir")"
network_id=""
container_name="${fixture_id}-test"

cleanup() {
  local result=$?
  local owner
  trap - EXIT
  if owner="$(docker inspect "$container_name" --format '{{index .Config.Labels "vc-workspace.test-instance"}}' 2>/dev/null)"; then
    if [[ "$owner" == "$fixture_id" ]]; then
      docker stop --time 3 "$container_name" >/dev/null || result=1
    else
      echo 'refusing to stop an unrelated container' >&2; result=1
    fi
  fi
  if [[ -n "$network_id" ]]; then
    owner="$(docker network inspect "$network_id" --format '{{index .Labels "vc-workspace.test-instance"}}')" || result=1
    if [[ "$owner" == "$fixture_id" ]]; then
      docker network rm "$network_id" >/dev/null || result=1
    else
      echo 'refusing to remove an unrelated network' >&2; result=1
    fi
  fi
  rm -f "$fixture_dir/rdpsession.test"
  rmdir "$fixture_dir" || result=1
  exit "$result"
}
trap cleanup EXIT
GOOS=linux GOARCH="$worker_arch" CGO_ENABLED=0 go test -c -o "$fixture_dir/rdpsession.test" ./internal/rdpsession
network_id="$(docker network create --internal --label "vc-workspace.test-instance=$fixture_id" "$fixture_id")"
docker run --rm --name "$container_name" --label "vc-workspace.test-instance=$fixture_id" \
  --platform "linux/$worker_arch" --network "$network_id" --read-only \
  --cap-drop ALL --security-opt no-new-privileges --pids-limit 128 --memory 512m \
  --tmpfs /tmp:rw,nosuid,nodev,mode=1777 \
  --mount "type=bind,source=$fixture_dir/rdpsession.test,target=/rdpsession.test,readonly" \
  --env VC_WORKSPACE_TEST_SESSION_WORKER=/opt/vcw/libexec/vc-workspace-session-worker \
  --env VC_WORKSPACE_TEST_SESSION_WORKER_BIND=auto \
  --entrypoint /rdpsession.test "$worker_image" -test.v -test.count=1 -test.timeout=2m
