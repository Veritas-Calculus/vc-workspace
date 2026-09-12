# syntax=docker/dockerfile:1.7
# Headless library worker. No X11/Wayland client, clipboard, device channels,
# shell entry point or public listener. Embed /opt/vcw in the broker image once
# its Windows lifecycle is enabled; this image alone is not an MCP service.
ARG DEBIAN_IMAGE=debian:trixie-slim
FROM ${DEBIAN_IMAGE} AS build
ARG DEBIAN_MIRROR=http://deb.debian.org/debian
ARG DEBIAN_SECURITY_MIRROR=http://deb.debian.org/debian-security
RUN sed -i "s|http://deb.debian.org/debian$|${DEBIAN_MIRROR}|g; s|http://deb.debian.org/debian-security$|${DEBIAN_SECURITY_MIRROR}|g" /etc/apt/sources.list.d/debian.sources \
    && apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates cmake ninja-build gcc g++ pkg-config libssl-dev zlib1g-dev libicu-dev \
    && apt-get clean
# Same reviewed, immutable upstream archive as the native macOS build. Do not
# use a distribution's older FreeRDP without reviewing its security fixes.
ADD --checksum=sha256:3c66cdd4506b86c451dd0817cb60aa8434c32f56ac1f92aa543f332b376113af https://github.com/FreeRDP/FreeRDP/archive/refs/tags/3.31.0.tar.gz /tmp/freerdp.tar.gz
RUN mkdir /src && tar -xzf /tmp/freerdp.tar.gz -C /src --strip-components=1 \
    && cmake -S /src -B /build -GNinja \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/opt/vcw -DCMAKE_INSTALL_LIBDIR=lib \
    -DCMAKE_INSTALL_RPATH=/opt/vcw/lib -DCMAKE_C_STANDARD=23 \
    -DUSE_VERSION_FROM_GIT_TAG=OFF -DUSE_GIT_FOR_REVISION=OFF -DBUILD_SHARED_LIBS=ON \
    -DWITH_CLIENT=OFF -DWITH_CLIENT_COMMON=OFF -DWITH_CLIENT_SDL=OFF \
    -DWITH_SERVER=OFF -DWITH_PROXY=OFF -DWITH_SHADOW=OFF -DWITH_SAMPLE=OFF \
    -DWITH_X11=OFF -DWITH_WAYLAND=OFF -DWITH_CHANNELS=OFF \
    -DWITH_FFMPEG=OFF -DWITH_DSP_FFMPEG=OFF -DWITH_SWSCALE=OFF \
    -DWITH_CAIRO=OFF -DWITH_CUPS=OFF -DWITH_PCSC=OFF -DWITH_PKCS11=OFF \
    -DWITH_OPENH264=OFF -DWITH_OPUS=OFF -DWITH_PULSE=OFF -DWITH_ALSA=OFF -DWITH_OSS=OFF \
    -DWITH_AAD=OFF -DWITH_JSON_DISABLED=ON -DWITH_URIPARSER=OFF \
    -DWITH_KRB5=OFF -DWITH_GSSAPI=OFF -DWITH_MANPAGES=OFF -DWITH_TESTS=OFF \
    -DWITH_VERBOSE_WINPR_ASSERT=OFF -DWITH_WINPR_TOOLS=OFF \
    && cmake --build /build -j 4 && cmake --install /build
COPY apps/session-worker /worker
RUN PKG_CONFIG_PATH=/opt/vcw/lib/pkgconfig cmake -S /worker -B /worker-build -GNinja \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/opt/vcw -DCMAKE_INSTALL_RPATH=/opt/vcw/lib \
    && cmake --build /worker-build -j 4 \
    && ctest --test-dir /worker-build --output-on-failure \
    && cmake --install /worker-build

FROM ${DEBIAN_IMAGE} AS runtime
ARG DEBIAN_MIRROR=http://deb.debian.org/debian
ARG DEBIAN_SECURITY_MIRROR=http://deb.debian.org/debian-security
RUN sed -i "s|http://deb.debian.org/debian$|${DEBIAN_MIRROR}|g; s|http://deb.debian.org/debian-security$|${DEBIAN_SECURITY_MIRROR}|g" /etc/apt/sources.list.d/debian.sources \
    && apt-get update && apt-get install -y --no-install-recommends libssl3t64 zlib1g libicu76 ca-certificates \
    && apt-get clean \
    && useradd --uid 65532 --create-home --shell /usr/sbin/nologin vcw-worker
COPY --from=build /opt/vcw/lib /opt/vcw/lib
COPY --from=build /opt/vcw/libexec/vc-workspace-session-worker /opt/vcw/libexec/vc-workspace-session-worker
COPY --from=build /src/LICENSE /usr/share/doc/vcw-session-worker/FreeRDP-LICENSE
USER 65532:65532
ENTRYPOINT ["/opt/vcw/libexec/vc-workspace-session-worker"]
