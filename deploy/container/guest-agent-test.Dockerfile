# Linux-only dependencies and real Unix UID/Xorg integration tests. Build with
# --target artifact to export the release binary without the test environment.
ARG RUST_IMAGE=rust:1.88-bookworm
FROM ${RUST_IMAGE} AS dependencies
ARG DEBIAN_MIRROR=http://deb.debian.org/debian
RUN sed -i "s|http://deb.debian.org/debian|${DEBIAN_MIRROR}|g" /etc/apt/sources.list.d/debian.sources \
    && apt-get update && apt-get install -y --no-install-recommends \
    libclang-dev libdbus-1-dev libdrm-dev libegl1-mesa-dev libgbm-dev \
    libpipewire-0.3-dev libwayland-dev libx11-dev libxcb1-dev libxkbcommon-dev \
    libxrandr-dev libxtst-dev pkg-config xvfb xauth xterm dbus-x11 python3 \
    python3-gi gir1.2-gtk-3.0 at-spi2-core openbox libpam0g-dev libsystemd-dev \
    && apt-get clean
WORKDIR /workspace
RUN rustup component add clippy

FROM dependencies AS build
COPY Cargo.toml Cargo.lock rust-toolchain.toml ./
COPY crates ./crates
RUN install -d /out && cc -O2 -Wall -Wextra -Werror -fPIC -fvisibility=hidden -shared \
    -Wl,-z,relro,-z,now,-z,noexecstack,-z,defs -o /out/pam_vcworkspace.so \
    crates/guest-agent/native/linux/pam_vcworkspace.c -lpam -lsystemd
RUN cc -O2 -Wall -Wextra -Werror -fPIC -fvisibility=hidden -shared -DVC_WORKSPACE_NATIVE_PAM \
    -Wl,-z,relro,-z,now,-z,noexecstack,-z,defs -o /out/pam_vcworkspace_native.so \
    crates/guest-agent/native/linux/pam_vcworkspace.c -lpam -lsystemd
RUN --mount=type=cache,target=/usr/local/cargo/registry,id=vcw-agent-registry \
    --mount=type=cache,target=/workspace/target,id=vcw-agent-native-target \
    cargo test --locked --workspace \
    && cargo test --locked -p vc-workspace-guest-agent --no-run --message-format=json \
       | python3 -c 'import sys,json; [print(x["executable"]) for l in sys.stdin if (x:=json.loads(l)).get("executable") and x.get("profile",{}).get("test")]' \
       | xargs -I{} install -D '{}' /out/vc-workspace-guest-agent-tests \
    && cargo clippy --locked --workspace --all-targets -- -D warnings \
    && cargo build --locked --release -p vc-workspace-guest-agent \
    && install -D target/release/vc-workspace-guest-agent /out/vc-workspace-guest-agent

FROM scratch AS artifact
COPY --from=build /out/vc-workspace-guest-agent /vc-workspace-guest-agent
COPY --from=build /out/pam_vcworkspace.so /pam_vcworkspace.so
COPY --from=build /out/pam_vcworkspace_native.so /pam_vcworkspace_native.so

# Cross-link a Debian-compatible amd64 artifact on an arm64 developer machine;
# unlike emulator-based builds this does not require changing host binfmt state.
FROM dependencies AS cross-amd64
RUN dpkg --add-architecture amd64 && apt-get update && apt-get install -y --no-install-recommends \
    gcc-x86-64-linux-gnu g++-x86-64-linux-gnu \
    libdbus-1-dev:amd64 libdrm-dev:amd64 libegl1-mesa-dev:amd64 libgbm-dev:amd64 \
    libpipewire-0.3-dev:amd64 libwayland-dev:amd64 libx11-dev:amd64 libxcb1-dev:amd64 \
    libxkbcommon-dev:amd64 libxrandr-dev:amd64 libxtst-dev:amd64 libpam0g-dev:amd64 libsystemd-dev:amd64 && apt-get clean \
    && rustup target add x86_64-unknown-linux-gnu
ENV CARGO_TARGET_X86_64_UNKNOWN_LINUX_GNU_LINKER=x86_64-linux-gnu-gcc \
    CC_x86_64_unknown_linux_gnu=x86_64-linux-gnu-gcc \
    CXX_x86_64_unknown_linux_gnu=x86_64-linux-gnu-g++ \
    PKG_CONFIG_ALLOW_CROSS=1 \
    PKG_CONFIG_LIBDIR=/usr/lib/x86_64-linux-gnu/pkgconfig:/usr/share/pkgconfig \
    BINDGEN_EXTRA_CLANG_ARGS="--target=x86_64-linux-gnu"
COPY Cargo.toml Cargo.lock rust-toolchain.toml ./
COPY crates ./crates
RUN install -d /out && x86_64-linux-gnu-gcc -O2 -Wall -Wextra -Werror -fPIC -fvisibility=hidden -shared \
    -Wl,-z,relro,-z,now,-z,noexecstack,-z,defs -o /out/pam_vcworkspace.so \
    crates/guest-agent/native/linux/pam_vcworkspace.c -lpam -lsystemd
RUN x86_64-linux-gnu-gcc -O2 -Wall -Wextra -Werror -fPIC -fvisibility=hidden -shared -DVC_WORKSPACE_NATIVE_PAM \
    -Wl,-z,relro,-z,now,-z,noexecstack,-z,defs -o /out/pam_vcworkspace_native.so \
    crates/guest-agent/native/linux/pam_vcworkspace.c -lpam -lsystemd
RUN --mount=type=cache,target=/usr/local/cargo/registry,id=vcw-agent-registry \
    --mount=type=cache,target=/workspace/target,id=vcw-agent-amd64-target \
    cargo build --locked --release --target x86_64-unknown-linux-gnu -p vc-workspace-guest-agent \
    && install -D target/x86_64-unknown-linux-gnu/release/vc-workspace-guest-agent /out/vc-workspace-guest-agent

FROM scratch AS amd64-artifact
COPY --from=cross-amd64 /out/vc-workspace-guest-agent /vc-workspace-guest-agent
COPY --from=cross-amd64 /out/pam_vcworkspace.so /pam_vcworkspace.so
COPY --from=cross-amd64 /out/pam_vcworkspace_native.so /pam_vcworkspace_native.so

FROM build AS test
COPY tools/test-computer-session.py /workspace/tools/test-computer-session.py
COPY tools/test-linux-account-lease.py /workspace/tools/test-linux-account-lease.py
COPY tools/test-linux-native-account.py /workspace/tools/test-linux-native-account.py
COPY tools/test-linux-native-pam.py /workspace/tools/test-linux-native-pam.py
COPY tools/test-linux-display-cleanup.py /workspace/tools/test-linux-display-cleanup.py
COPY deploy/guest/linux/install-login-fence.py /workspace/deploy/guest/linux/install-login-fence.py
COPY internal/computer/revoke_legacy_linux.py /workspace/tools/revoke-legacy-linux.py
# Make runs the destructive UID-reuse account suite in its own disposable
# container; its preserved identity tombstones must not contaminate GUI users.
CMD ["python3", "/workspace/tools/test-computer-session.py"]
