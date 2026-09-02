#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <output.msi>" >&2
  exit 2
fi

version=1.1.8
expected_sha256=0e7fa42e0cbc0ce7657f85730b0c6cc7afc4087a3639df0ff51a721a0be19bd5
url="https://github.com/cloudbase/cloudbase-init/releases/download/${version}/CloudbaseInitSetup_1_1_8_x64.msi"
output=$1
temporary=$(mktemp)
trap 'rm -f "$temporary"' EXIT

curl --fail --location --show-error --output "$temporary" "$url"
actual_sha256=$(shasum -a 256 "$temporary" | awk '{print $1}')
if [ "$actual_sha256" != "$expected_sha256" ]; then
  echo "Cloudbase-Init checksum mismatch: expected ${expected_sha256}, got ${actual_sha256}" >&2
  exit 1
fi

install -m 0644 "$temporary" "$output"
echo "Cloudbase-Init ${version} x64: ${actual_sha256}"
