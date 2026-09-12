#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

if ! command -v kubectl >/dev/null 2>&1; then
  echo "kubectl is required to verify Kubernetes manifests" >&2
  exit 1
fi

# kubectl create --dry-run=client still performs API discovery, including for
# cert-manager CRDs. Do not contact the user's current (possibly unrelated)
# cluster in an offline check. Structural checks run on rendered YAML instead.
kubectl kustomize "${repo_dir}/deploy/kubernetes/base" >/dev/null
kubectl kustomize "${repo_dir}/deploy/kubernetes/overlays/infra" >/dev/null
cd "${repo_dir}"
node --test deploy/kubernetes/infra-bootstrap.test.mjs deploy/kubernetes/infra-certificates.test.mjs
echo "Verified Kubernetes rendering and structural/security boundaries offline; server schema/rollout checks remain separate"
