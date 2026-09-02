#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
client_dir="$(cd "${script_dir}/.." && pwd)"
repo_dir="$(cd "${client_dir}/../.." && pwd)"
source_png="${repo_dir}/assets/brand/vc-workspace-app-icon-1024.png"
output_path="${client_dir}/App/VCWorkspace.icns"
iconset_dir="$(mktemp -d "${TMPDIR:-/tmp}/vc-workspace-icon.XXXXXX")"
trap 'rm -rf "${iconset_dir}"' EXIT

for specification in \
  "16:icon_16x16.png" "32:icon_16x16@2x.png" \
  "32:icon_32x32.png" "64:icon_32x32@2x.png" \
  "128:icon_128x128.png" "256:icon_128x128@2x.png" \
  "256:icon_256x256.png" "512:icon_256x256@2x.png" \
  "512:icon_512x512.png" "1024:icon_512x512@2x.png"; do
  size="${specification%%:*}"
  filename="${specification#*:}"
  sips -z "${size}" "${size}" "${source_png}" --out "${iconset_dir}/${filename}" >/dev/null
done

go run "${script_dir}/iconpack.go" "${output_path}" \
  icp4 "${iconset_dir}/icon_16x16.png" \
  icp5 "${iconset_dir}/icon_32x32.png" \
  icp6 "${iconset_dir}/icon_32x32@2x.png" \
  ic07 "${iconset_dir}/icon_128x128.png" \
  ic08 "${iconset_dir}/icon_256x256.png" \
  ic09 "${iconset_dir}/icon_512x512.png" \
  ic10 "${iconset_dir}/icon_512x512@2x.png"

echo "${output_path}"
