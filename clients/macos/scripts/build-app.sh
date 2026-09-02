#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
client_dir="$(cd "${script_dir}/.." && pwd)"
configuration="${1:-debug}"
module_cache="${SWIFTPM_MODULECACHE_OVERRIDE:-${client_dir}/.build/module-cache}"

case "${configuration}" in
  debug|release) ;;
  *) echo "usage: $0 [debug|release]" >&2; exit 2 ;;
esac

mkdir -p "${module_cache}"
export SWIFTPM_MODULECACHE_OVERRIDE="${module_cache}"
export CLANG_MODULE_CACHE_PATH="${CLANG_MODULE_CACHE_PATH:-${module_cache}}"

cd "${client_dir}"
swift build --configuration "${configuration}" --disable-sandbox
binary_dir="$(swift build --configuration "${configuration}" --show-bin-path --disable-sandbox)"
app_dir="${client_dir}/.build/app/VC Workspace.app"
legacy_app_dir="${client_dir}/.build/app/VC VDI.app"
contents_dir="${app_dir}/Contents"

rm -rf "${app_dir}" "${legacy_app_dir}"
mkdir -p "${contents_dir}/MacOS" "${contents_dir}/Resources" "${contents_dir}/Frameworks"
cp "${binary_dir}/VCVDI" "${contents_dir}/MacOS/VCVDI"
cp "${client_dir}/App/Info.plist" "${contents_dir}/Info.plist"
cp "${client_dir}/App/VCWorkspace.icns" "${contents_dir}/Resources/VCWorkspace.icns"
chmod 0755 "${contents_dir}/MacOS/VCVDI"
"${script_dir}/build-native-rdp.sh" "${contents_dir}"

signing_identity="${VC_VDI_CODESIGN_IDENTITY:-}"
if [[ -z "${signing_identity}" ]] && command -v security >/dev/null 2>&1; then
  signing_identity="$(security find-identity -p codesigning -v 2>/dev/null | awk '/Apple Development/ { print $2; exit }')"
fi
if [[ -n "${signing_identity}" ]]; then
  while IFS= read -r -d '' library; do
    codesign --force --sign "${signing_identity}" --timestamp=none "${library}"
  done < <(find "${contents_dir}/Frameworks" -type f -name '*.dylib' -print0)
  codesign --force --sign "${signing_identity}" --timestamp=none "${app_dir}"
else
  while IFS= read -r -d '' library; do
    codesign --force --sign - --timestamp=none "${library}"
  done < <(find "${contents_dir}/Frameworks" -type f -name '*.dylib' -print0)
  codesign --force --sign - --timestamp=none "${app_dir}"
fi

echo "${app_dir}"
