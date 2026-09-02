#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
rendered="$(mktemp "${TMPDIR:-/tmp}/vc-workspace-kubernetes.XXXXXX.yaml")"
trap 'rm -f "${rendered}"' EXIT

if ! command -v kubectl >/dev/null 2>&1; then
  echo "kubectl is required to verify Kubernetes manifests" >&2
  exit 1
fi

kubectl kustomize "${repo_dir}/deploy/kubernetes/base" >"${rendered}"
kubectl create --dry-run=client --validate=false -f "${rendered}" >/dev/null
kubectl create --dry-run=client --validate=false -f "${repo_dir}/deploy/kubernetes/secret.example.yaml" >/dev/null
echo "Verified Kubernetes base and secret template"
