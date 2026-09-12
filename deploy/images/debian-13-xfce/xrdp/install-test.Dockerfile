# Fresh amd64 packaging test, not a PVE/GUI acceptance substitute.
FROM debian:13-slim AS test
ARG DEBIAN_MIRROR=http://deb.debian.org/debian
ARG DEBIAN_SECURITY_MIRROR=http://deb.debian.org/debian-security
ENV DEBIAN_FRONTEND=noninteractive
RUN rm /etc/apt/sources.list.d/debian.sources && \
    printf 'deb %s trixie main\ndeb %s trixie-security main\n' \
      "$DEBIAN_MIRROR" "$DEBIAN_SECURITY_MIRROR" >/etc/apt/sources.list && \
    apt-get update && apt-get install -y --no-install-recommends python3 xrdp=0.10.1-3.1+deb13u2
# The desktop ISO does not have Docker's documentation exclusions. Restore the
# full package semantics for this installation test; keep dpkg verification strict.
RUN test -f /etc/dpkg/dpkg.cfg.d/docker && rm /etc/dpkg/dpkg.cfg.d/docker
COPY install-package.py test-install-package.py /test/
RUN python3 /test/test-install-package.py
COPY --from=xrdp_bundle /xrdp.deb /bundle/xrdp.deb
COPY --from=xrdp_bundle /xrdp-package.sha256 /bundle/xrdp-package.sha256
RUN cd /bundle && sha256sum --check --strict xrdp-package.sha256 && \
    cp xrdp.deb /tmp/vc-workspace-xrdp.deb && \
    if VC_WORKSPACE_XRDP_PACKAGE_SHA256=0000000000000000000000000000000000000000000000000000000000000000 python3 /test/install-package.py; then exit 1; fi && \
    test "$(dpkg-query -W '-f=${Version}' xrdp)" = 0.10.1-3.1+deb13u2 && \
    test ! -e /usr/share/vc-workspace/rdp-runtime.json && \
    VC_WORKSPACE_XRDP_PACKAGE_SHA256="$(cut -d' ' -f1 xrdp-package.sha256)" python3 /test/install-package.py && \
    if VC_WORKSPACE_XRDP_PACKAGE_SHA256="$(cut -d' ' -f1 xrdp-package.sha256)" python3 /test/install-package.py; then exit 1; fi && \
    test "$(dpkg-query -W '-f=${Version}' xrdp)" = 0.10.1-3.1+deb13u2+vcw1 && \
    /usr/sbin/xrdp --version
FROM scratch
COPY --from=test /usr/share/vc-workspace/rdp-runtime.json /rdp-runtime.json
