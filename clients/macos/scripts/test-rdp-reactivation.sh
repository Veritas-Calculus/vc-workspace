#!/usr/bin/env bash
set -euo pipefail
client_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
runtime_root="${client_dir}/.build/native-rdp"
source_dir="${runtime_root}/FreeRDP-3.31.0"
build_dir="${runtime_root}/build"
frameworks_dir="${client_dir}/.build/app/VC Workspace.app/Contents/Frameworks"
fixture="${build_dir}/vc-workspace/rdp-reactivation-test"
test -f "${frameworks_dir}/libfreerdp3.3.dylib" || { echo 'Build the current app first with make macos-app'; exit 1; }
clang -std=gnu2x -Wall -Wextra -Werror -Wno-deprecated-declarations \
  -I"${source_dir}/include" -I"${source_dir}/winpr/include" \
  -I"${build_dir}/include" -I"${build_dir}/winpr/include" \
  -I"${runtime_root}/openssl-install/include" \
  "${client_dir}/NativeRDP/tests/rdp-reactivation.c" \
  "${frameworks_dir}/libfreerdp3.3.dylib" "${frameworks_dir}/libfreerdp-client3.3.dylib" \
  "${frameworks_dir}/libwinpr3.3.dylib" "${frameworks_dir}/libcrypto.3.dylib" \
  -Wl,-rpath,"${frameworks_dir}" -o "${fixture}"
OPENSSL_MODULES="${frameworks_dir}/ossl-modules" "${fixture}" normal
OPENSSL_MODULES="${frameworks_dir}/ossl-modules" "${fixture}" missing-pointer
OPENSSL_MODULES="${frameworks_dir}/ossl-modules" "${fixture}" existing-error
OPENSSL_MODULES="${frameworks_dir}/ossl-modules" "${fixture}" cancelled
