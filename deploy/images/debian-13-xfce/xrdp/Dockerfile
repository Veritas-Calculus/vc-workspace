# Debian's security-patched package is the base; never replace it with the
# unpatched upstream release. This context contains no credentials or VM data.
FROM debian:13-slim AS build
ARG DEBIAN_MIRROR=http://deb.debian.org/debian
ARG DEBIAN_SECURITY_MIRROR=http://deb.debian.org/debian-security
ENV DEBIAN_FRONTEND=noninteractive
RUN rm /etc/apt/sources.list.d/debian.sources && \
    printf 'deb %s trixie main\ndeb-src %s trixie main\ndeb %s trixie-security main\ndeb-src %s trixie-security main\n' \
      "$DEBIAN_MIRROR" "$DEBIAN_MIRROR" "$DEBIAN_SECURITY_MIRROR" "$DEBIAN_SECURITY_MIRROR" >/etc/apt/sources.list && \
    apt-get update && apt-get install -y --no-install-recommends build-essential devscripts equivs ca-certificates quilt && \
    apt-get build-dep -y xrdp=0.10.1-3.1+deb13u2
WORKDIR /src
RUN apt-get source xrdp=0.10.1-3.1+deb13u2 && \
    printf '%s  %s\n' \
      a2535f4420080630e20f0639c30c244170003ab998cc82d7913c2be856622f83 xrdp_0.10.1.orig.tar.gz \
      00d1c87fc76920120e3888d2264c94504a787b9fc65327a30343c79ec2b5ec25 xrdp_0.10.1-3.1+deb13u2.debian.tar.xz | sha256sum --check --strict
WORKDIR /src/xrdp-0.10.1
COPY restore-pointer-after-resize.patch /tmp/restore-pointer-after-resize.patch
RUN patch --batch --fuzz=0 -p1 < /tmp/restore-pointer-after-resize.patch && \
    DEBEMAIL=build@vc-workspace.invalid DEBFULLNAME='VC Workspace build' \
      dch --local +vcw --distribution trixie 'Resend the current cursor image after display reactivation.' && \
    dpkg-buildpackage --no-sign --build=binary --jobs=4 && \
    make check && \
    mkdir /out && cp /src/xrdp_*_amd64.deb /src/xrdp_*_amd64.buildinfo /out/ && \
    dpkg-deb --extract /src/xrdp_*_amd64.deb /out/root && \
    sha256sum /out/root/usr/sbin/xrdp > /out/xrdp.sha256
RUN cp /src/xrdp_*_amd64.deb /out/xrdp.deb && \
    cd /out && sha256sum xrdp.deb > xrdp-package.sha256
FROM scratch AS artifacts
COPY --from=build /out/ /
