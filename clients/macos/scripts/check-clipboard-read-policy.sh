#!/usr/bin/env bash
set -euo pipefail
client_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_dir="${VC_WORKSPACE_FREERDP_SOURCE:-${client_dir}/.build/native-rdp/FreeRDP-3.31.0}"
build_dir="${VC_WORKSPACE_FREERDP_BUILD_DIR:-${client_dir}/.build/native-rdp/build}"
test_dir="${build_dir}/vc-workspace"
mkdir -p "${test_dir}"
clang -std=c11 -Wall -Wextra -Werror "${client_dir}/NativeRDP/tests/text-input.c" -o "${test_dir}/text-input-test"
"${test_dir}/text-input-test"
clang -Wall -Wextra -Werror -std=gnu2x -mmacosx-version-min=14.0 \
  "${client_dir}/NativeRDP/tests/marked-text.m" -framework Foundation -o "${test_dir}/marked-text-test"
"${test_dir}/marked-text-test"
clang -Wall -Wextra -Werror -std=gnu2x -mmacosx-version-min=14.0 \
  "${client_dir}/NativeRDP/tests/text-input-client.m" "${client_dir}/NativeRDP/VCWTextInputClient.m" \
  -framework AppKit -o "${test_dir}/text-input-client-test"
"${test_dir}/text-input-client-test"
clang -Wall -Wextra -Werror -std=gnu2x -mmacosx-version-min=14.0 \
  "${client_dir}/NativeRDP/tests/text-input-host.m" "${client_dir}/NativeRDP/VCWTextInputHost.m" \
  "${client_dir}/NativeRDP/VCWTextInputClient.m" -framework AppKit -o "${test_dir}/text-input-host-test"
"${test_dir}/text-input-host-test"
clang -Wall -Wextra -Werror -std=gnu2x -mmacosx-version-min=14.0 \
  "${client_dir}/NativeRDP/tests/clipboard-delivery.m" -framework Foundation \
  -o "${test_dir}/clipboard-delivery-test"
"${test_dir}/clipboard-delivery-test"
clang -Wall -Wextra -Werror -std=gnu2x -mmacosx-version-min=14.0 \
  -isystem "${source_dir}/client/Mac" -isystem "${source_dir}/include" \
  -isystem "${source_dir}/winpr/include" -isystem "${build_dir}/include" \
  -isystem "${build_dir}/winpr/include" \
  "${client_dir}/NativeRDP/tests/clipboard-read-policy.m" \
  "${build_dir}/client/Mac/libMacFreeRDP-library.dylib" \
  "${build_dir}/libfreerdp/libfreerdp3.3.dylib" \
  "${build_dir}/winpr/libwinpr/libwinpr3.3.dylib" \
  -Wl,-rpath,"${build_dir}/client/Mac" \
  -Wl,-rpath,"${build_dir}/client/common" \
  -Wl,-rpath,"${build_dir}/libfreerdp" \
  -Wl,-rpath,"${build_dir}/winpr/libwinpr" \
  -framework AppKit -o "${test_dir}/clipboard-read-policy-test"
"${test_dir}/clipboard-read-policy-test" "${build_dir}/client/Mac/libMacFreeRDP-library.dylib"
clang -Wall -Wextra -Werror -Wno-unused-parameter -std=gnu2x -mmacosx-version-min=14.0 \
  -DMRDPView=VCWFixtureRDPView \
  -isystem "${source_dir}/client/Mac" -isystem "${source_dir}/include" \
  -isystem "${source_dir}/winpr/include" -isystem "${build_dir}/include" \
  -isystem "${build_dir}/winpr/include" \
  "${client_dir}/NativeRDP/tests/clipboard-callback.m" \
  "${source_dir}/client/Mac/Clipboard.m" \
  "${build_dir}/libfreerdp/libfreerdp3.3.dylib" \
  "${build_dir}/winpr/libwinpr/libwinpr3.3.dylib" \
  -Wl,-rpath,"${build_dir}/libfreerdp" -Wl,-rpath,"${build_dir}/winpr/libwinpr" \
  -framework AppKit -o "${test_dir}/clipboard-callback-test"
"${test_dir}/clipboard-callback-test"
openssl_include="$(sed -n 's/^OPENSSL_INCLUDE_DIR:PATH=//p' "${build_dir}/CMakeCache.txt")"
test -f "${openssl_include}/openssl/bio.h"
clang -Wall -Wextra -Werror -std=gnu2x -mmacosx-version-min=14.0 \
  -isystem "${source_dir}/client/Mac" -isystem "${source_dir}/include" \
  -isystem "${source_dir}/winpr/include" -isystem "${build_dir}/include" \
  -isystem "${build_dir}/winpr/include" -isystem "${source_dir}/libfreerdp/core" -isystem "${openssl_include}" \
  "${client_dir}/NativeRDP/tests/edit-actions.m" \
  "${client_dir}/NativeRDP/tests/edit-input-context.c" \
  "${build_dir}/client/Mac/libMacFreeRDP-library.dylib" \
  "${build_dir}/libfreerdp/libfreerdp3.3.dylib" \
  "${build_dir}/winpr/libwinpr/libwinpr3.3.dylib" \
  -Wl,-rpath,"${build_dir}/client/Mac" -Wl,-rpath,"${build_dir}/client/common" \
  -Wl,-rpath,"${build_dir}/libfreerdp" -Wl,-rpath,"${build_dir}/winpr/libwinpr" \
  -framework AppKit -o "${test_dir}/edit-actions-test"
"${test_dir}/edit-actions-test" "${build_dir}/client/Mac/libMacFreeRDP-library.dylib"
