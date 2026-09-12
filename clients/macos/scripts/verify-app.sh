#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
client_dir="$(cd "${script_dir}/.." && pwd)"
app_dir="${1:-${client_dir}/.build/app/VC Workspace.app}"
contents_dir="${app_dir}/Contents"

if [[ ! -d "${app_dir}" ]]; then
  echo "VC Workspace.app not found: ${app_dir}" >&2
  exit 1
fi

for command_name in codesign file find otool plutil vtool; do
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "app verification requires ${command_name}" >&2
    exit 1
  fi
done

icon_name="$(plutil -extract CFBundleIconFile raw "${contents_dir}/Info.plist")"
icon_path="${contents_dir}/Resources/${icon_name%.icns}.icns"
if [[ ! -f "${icon_path}" ]] || ! grep -Fq 'Mac OS X icon' <<<"$(file "${icon_path}")"; then
  echo "missing or invalid application icon: ${icon_path}" >&2
  exit 1
fi

codesign --verify --deep --strict --verbose=2 "${app_dir}"

verification_failed=0
while IFS= read -r -d '' binary; do
  if ! grep -Fq 'Mach-O' <<<"$(file "${binary}")"; then
    continue
  fi

  while IFS= read -r dependency; do
    case "${dependency}" in
      /opt/homebrew/*|/usr/local/*|/opt/local/*|/Users/*|/private/*)
        echo "build-host dependency: ${binary}: ${dependency}" >&2
        verification_failed=1
        ;;
    esac
  done < <(otool -L "${binary}" | awk 'NR > 1 { print $1 }')

  while IFS= read -r runtime_path; do
    case "${runtime_path}" in
      /opt/homebrew/*|/usr/local/*|/opt/local/*|/Users/*|/private/*)
        echo "build-host rpath: ${binary}: ${runtime_path}" >&2
        verification_failed=1
        ;;
    esac
  done < <(otool -l "${binary}" | awk '/cmd LC_RPATH/ { getline; getline; print $2 }')

  if ! grep -Eq '^[[:space:]]+minos 14\.0$' <<<"$(vtool -show-build "${binary}")"; then
    echo "unexpected deployment target: ${binary}" >&2
    verification_failed=1
  fi
done < <(find "${contents_dir}/MacOS" "${contents_dir}/Frameworks" -type f -print0)

if [[ "${verification_failed}" -ne 0 ]]; then
  exit 1
fi

echo "Verified self-contained VC Workspace.app for macOS 14"
