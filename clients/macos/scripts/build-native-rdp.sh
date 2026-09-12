#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <app-contents-directory>" >&2
  exit 2
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
client_dir="$(cd "${script_dir}/.." && pwd)"
contents_dir="$1"
frameworks_dir="${contents_dir}/Frameworks"
resources_dir="${contents_dir}/Resources"

freerdp_version="3.31.0"
freerdp_sha256="3c66cdd4506b86c451dd0817cb60aa8434c32f56ac1f92aa543f332b376113af"
openssl_version="3.5.8"
openssl_sha256="a8f84a39918ec6415ce765d9b429d313ba97b8143169c172e734b9514464f5b2"
runtime_root="${client_dir}/.build/native-rdp"
source_dir="${VC_WORKSPACE_FREERDP_SOURCE:-${runtime_root}/FreeRDP-${freerdp_version}}"
build_dir="${VC_WORKSPACE_FREERDP_BUILD_DIR:-${runtime_root}/build}"
archive="${runtime_root}/FreeRDP-${freerdp_version}.tar.gz"
openssl_source_dir="${runtime_root}/openssl-${openssl_version}"
openssl_build_dir="${runtime_root}/openssl-build"
openssl_install_dir="${runtime_root}/openssl-install"
openssl_archive="${runtime_root}/openssl-${openssl_version}.tar.gz"

for command_name in cmake ninja clang make patch shasum install_name_tool otool rg; do
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "native RDP build requires ${command_name}" >&2
    exit 1
  fi
done

mkdir -p "${runtime_root}"
if [[ ! -f "${source_dir}/CMakeLists.txt" ]]; then
  if [[ ! -f "${archive}" ]]; then
    curl --fail --location --retry 3 \
      "https://github.com/FreeRDP/FreeRDP/archive/refs/tags/${freerdp_version}.tar.gz" \
      --output "${archive}"
  fi
  actual_sha256="$(shasum -a 256 "${archive}" | awk '{ print $1 }')"
  if [[ "${actual_sha256}" != "${freerdp_sha256}" ]]; then
    echo "FreeRDP archive checksum mismatch" >&2
    exit 1
  fi
  mkdir -p "${source_dir}"
  tar -xzf "${archive}" --strip-components=1 -C "${source_dir}"
fi

for freerdp_patch in "${client_dir}/patches/freerdp-mac-clipboard.patch" "${client_dir}/patches/freerdp-mac-embedded-resize.patch" "${client_dir}/patches/freerdp-mac-embedded-lifecycle.patch" "${client_dir}/patches/freerdp-pointer-cache-diagnostic.patch" "${client_dir}/patches/freerdp-event-failure-status.patch" "${client_dir}/patches/freerdp-pointer-cache-trace.patch" "${client_dir}/patches/freerdp-mac-clipboard-read-policy.patch" "${client_dir}/patches/freerdp-mac-edit-actions.patch" "${client_dir}/patches/freerdp-tcp-diagnostic.patch"; do
  # BSD patch may automatically flip a reverse probe back to forward. Force
  # the requested direction, otherwise an unapplied patch looks installed.
  if patch --dry-run --silent --batch --force --reverse --fuzz=0 -p1 -d "${source_dir}" < "${freerdp_patch}" >/dev/null 2>&1; then
    : # already patched
  elif patch --dry-run --silent --batch --force --forward --fuzz=0 -p1 -d "${source_dir}" < "${freerdp_patch}" >/dev/null 2>&1; then
    patch --silent --batch --force --forward --fuzz=0 -p1 -d "${source_dir}" < "${freerdp_patch}"
  else
    echo "FreeRDP patch does not apply cleanly: ${freerdp_patch}" >&2
    exit 1
  fi
done

cp "${client_dir}/NativeRDP/VCWClipboardDelivery.h" "${source_dir}/client/Mac/VCWClipboardDelivery.h"

if [[ ! -f "${openssl_source_dir}/Configure" ]]; then
  if [[ ! -f "${openssl_archive}" ]]; then
    curl --fail --location --retry 3 \
      "https://github.com/openssl/openssl/releases/download/openssl-${openssl_version}/openssl-${openssl_version}.tar.gz" \
      --output "${openssl_archive}"
  fi
  actual_sha256="$(shasum -a 256 "${openssl_archive}" | awk '{ print $1 }')"
  if [[ "${actual_sha256}" != "${openssl_sha256}" ]]; then
    echo "OpenSSL archive checksum mismatch" >&2
    exit 1
  fi
  mkdir -p "${openssl_source_dir}"
  tar -xzf "${openssl_archive}" --strip-components=1 -C "${openssl_source_dir}"
fi

if [[ ! -f "${openssl_install_dir}/lib/libssl.3.dylib" ]]; then
  mkdir -p "${openssl_build_dir}" "${openssl_install_dir}"
  cpu_count="$(sysctl -n hw.ncpu 2>/dev/null || echo 4)"
  (
    cd "${openssl_build_dir}"
    CFLAGS="-mmacosx-version-min=14.0" LDFLAGS="-mmacosx-version-min=14.0" \
      "${openssl_source_dir}/config" \
      --prefix="${openssl_install_dir}" \
      --libdir=lib \
      no-asm no-tests no-docs no-apps
    make -j "${cpu_count}" build_sw
    make install_sw
  )
fi

if [[ -f "${build_dir}/CMakeCache.txt" ]] &&
   ! rg -Fq "CMAKE_HOME_DIRECTORY:INTERNAL=${source_dir}" "${build_dir}/CMakeCache.txt"; then
  echo "FreeRDP source root changed; replacing generated CMake build directory"
  cmake -E remove_directory "${build_dir}"
fi

cmake -S "${source_dir}" -B "${build_dir}" -GNinja \
  -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_C_STANDARD=23 \
  -DCMAKE_OSX_DEPLOYMENT_TARGET=14.0 \
  -DCMAKE_PREFIX_PATH="${openssl_install_dir}" \
  -DCMAKE_IGNORE_PREFIX_PATH="/opt/homebrew;/usr/local;/opt/local" \
  -DOPENSSL_ROOT_DIR="${openssl_install_dir}" \
  -DOPENSSL_USE_STATIC_LIBS=FALSE \
  -DUSE_VERSION_FROM_GIT_TAG=OFF \
  -DUSE_GIT_FOR_REVISION=OFF \
  -DBUILD_SHARED_LIBS=ON \
  -DWITH_CLIENT=ON \
  -DWITH_CLIENT_COMMON=ON \
  -DWITH_CLIENT_MAC=ON \
  -DWITH_CLIENT_SDL=OFF \
  -DWITH_SERVER=OFF \
  -DWITH_PROXY=OFF \
  -DWITH_SHADOW=OFF \
  -DWITH_X11=OFF \
  -DWITH_WAYLAND=OFF \
  -DWITH_FFMPEG=OFF \
  -DWITH_DSP_FFMPEG=OFF \
  -DWITH_SWSCALE=OFF \
  -DWITH_CAIRO=OFF \
  -DWITH_CUPS=OFF \
  -DWITH_PCSC=OFF \
  -DWITH_PKCS11=OFF \
  -DWITH_OPENH264=OFF \
  -DWITH_OPUS=OFF \
  -DWITH_PULSE=OFF \
  -DWITH_ALSA=OFF \
  -DWITH_OSS=OFF \
  -DWITH_AAD=OFF \
  -DWITH_JSON_DISABLED=ON \
  -DWITH_URIPARSER=OFF \
  -DWITH_MANPAGES=OFF \
  -DWITH_SAMPLE=OFF \
  -DWITH_TESTS=OFF \
  -DWITH_VERBOSE_WINPR_ASSERT=OFF \
  -DWITH_WINPR_TOOLS=OFF \
  -DCHANNEL_VIDEO=OFF \
  -DCHANNEL_URBDRC=OFF \
  -DCHANNEL_SMARTCARD=OFF \
  -DCHANNEL_PRINTER=OFF \
  -DCHANNEL_SERIAL=OFF \
  -DCHANNEL_PARALLEL=OFF \
  -DCHANNEL_DRIVE=OFF \
  -DCHANNEL_LOCATION=OFF \
  -DCHANNEL_GEOMETRY=OFF \
  -DCHANNEL_ENCOMSP=OFF \
  -DCHANNEL_REMDESK=OFF \
  -DCHANNEL_RAIL=OFF \
  -DCHANNEL_AUDIN=OFF \
  -DCHANNEL_AINPUT=OFF

cmake --build "${build_dir}" --target MacFreeRDP-library --parallel
bash "${script_dir}/check-clipboard-read-policy.sh"

bridge_dir="${build_dir}/vc-workspace"
bridge_path="${bridge_dir}/libVCWorkspaceRDP.dylib"
mkdir -p "${bridge_dir}"
clang -std=c11 -Wall -Wextra -Werror "${client_dir}/NativeRDP/tests/display-presentation.c" -o "${bridge_dir}/display-presentation-test"
"${bridge_dir}/display-presentation-test"
clang -std=c11 -Wall -Wextra -Werror "${client_dir}/NativeRDP/tests/runtime-diagnostic.c" -o "${bridge_dir}/runtime-diagnostic-test"
"${bridge_dir}/runtime-diagnostic-test"
clang -std=c11 -Wall -Wextra -Werror -DVCW_TEST_WINPR_FORMAT "${client_dir}/NativeRDP/tests/runtime-diagnostic.c" -o "${bridge_dir}/runtime-diagnostic-winpr-test"
"${bridge_dir}/runtime-diagnostic-winpr-test"
clang -dynamiclib -std=gnu2x -mmacosx-version-min=14.0 \
  -fvisibility=hidden \
  -I"${source_dir}/client/Mac" \
  -I"${source_dir}/include" \
  -I"${source_dir}/winpr/include" \
  -I"${build_dir}/include" \
  -I"${build_dir}/winpr/include" \
  -I"${openssl_install_dir}/include" \
  "${client_dir}/NativeRDP/VCWorkspaceRDPBridge.m" \
  "${build_dir}/client/Mac/libMacFreeRDP-library.dylib" \
  "${build_dir}/client/common/libfreerdp-client3.3.dylib" \
  "${build_dir}/libfreerdp/libfreerdp3.3.dylib" \
  "${build_dir}/winpr/libwinpr/libwinpr3.3.dylib" \
  "${openssl_install_dir}/lib/libcrypto.3.dylib" \
  -framework AppKit \
  -Wl,-rpath,@loader_path \
  -Wl,-install_name,@rpath/libVCWorkspaceRDP.dylib \
  -o "${bridge_path}"

mkdir -p "${frameworks_dir}" "${resources_dir}/Licenses"
cp "${bridge_path}" "${frameworks_dir}/libVCWorkspaceRDP.dylib"
cp "${build_dir}/client/Mac/libMacFreeRDP-library.dylib" "${frameworks_dir}/libMacFreeRDP-library.dylib"
cp "${build_dir}/client/common/libfreerdp-client3.3.dylib" "${frameworks_dir}/libfreerdp-client3.3.dylib"
cp "${build_dir}/libfreerdp/libfreerdp3.3.dylib" "${frameworks_dir}/libfreerdp3.3.dylib"
cp "${build_dir}/winpr/libwinpr/libwinpr3.3.dylib" "${frameworks_dir}/libwinpr3.3.dylib"
cp -R "${build_dir}/client/Mac/CertificateDialog.nib" "${resources_dir}/CertificateDialog.nib"
cp -R "${build_dir}/client/Mac/PasswordDialog.nib" "${resources_dir}/PasswordDialog.nib"
cp "${source_dir}/LICENSE" "${resources_dir}/Licenses/FreeRDP.txt"
cp -L "${openssl_install_dir}/lib/libssl.3.dylib" "${frameworks_dir}/libssl.3.dylib"
cp -L "${openssl_install_dir}/lib/libcrypto.3.dylib" "${frameworks_dir}/libcrypto.3.dylib"
cp "${openssl_source_dir}/LICENSE.txt" "${resources_dir}/Licenses/OpenSSL.txt"
if [[ -d "${openssl_install_dir}/lib/ossl-modules" ]]; then
  mkdir -p "${frameworks_dir}/ossl-modules"
  cp -R "${openssl_install_dir}/lib/ossl-modules/." "${frameworks_dir}/ossl-modules/"
fi

while IFS= read -r -d '' library; do
  library_name="$(basename "${library}")"
  install_name_tool -id "@rpath/${library_name}" "${library}"
  while IFS= read -r dependency; do
    [[ -z "${dependency}" ]] && continue
    dependency_name="$(basename "${dependency}")"
    install_name_tool -change "${dependency}" "@rpath/${dependency_name}" "${library}"
  done < <(otool -L "${library}" | awk 'NR > 1 && $1 ~ /^\// && $1 !~ /^\/System\// && $1 !~ /^\/usr\/lib\// { print $1 }')

  if [[ "$(dirname "${library}")" == "${frameworks_dir}" ]]; then
    desired_rpath="@loader_path"
  else
    desired_rpath="@loader_path/.."
  fi
  if ! otool -l "${library}" | rg -q "path ${desired_rpath} "; then
    install_name_tool -add_rpath "${desired_rpath}" "${library}"
  fi

  while IFS= read -r runtime_path; do
    case "${runtime_path}" in
      /opt/homebrew/*|/usr/local/*|/opt/local/*|/Users/*|/private/*)
        install_name_tool -delete_rpath "${runtime_path}" "${library}"
        ;;
    esac
  done < <(otool -l "${library}" | awk '/cmd LC_RPATH/ { getline; getline; print $2 }')
done < <(find "${frameworks_dir}" -type f -name '*.dylib' -print0)

bundle_dependency_error=0
while IFS= read -r -d '' library; do
  while IFS= read -r dependency; do
    case "${dependency}" in
      /opt/homebrew/*|/usr/local/*|/opt/local/*|/Users/*|/private/*)
        echo "build-host dependency: ${library}: ${dependency}" >&2
        bundle_dependency_error=1
        ;;
    esac
  done < <(otool -L "${library}" | awk 'NR > 1 { print $1 }')
  while IFS= read -r runtime_path; do
    case "${runtime_path}" in
      /opt/homebrew/*|/usr/local/*|/opt/local/*|/Users/*|/private/*)
        echo "build-host rpath: ${library}: ${runtime_path}" >&2
        bundle_dependency_error=1
        ;;
    esac
  done < <(otool -l "${library}" | awk '/cmd LC_RPATH/ { getline; getline; print $2 }')
done < <(find "${frameworks_dir}" -type f -name '*.dylib' -print0)

if [[ "${bundle_dependency_error}" -ne 0 ]]; then
  echo "native RDP bundle still contains a build-host dependency" >&2
  exit 1
fi

echo "Bundled native FreeRDP ${freerdp_version} with OpenSSL ${openssl_version} LTS"
