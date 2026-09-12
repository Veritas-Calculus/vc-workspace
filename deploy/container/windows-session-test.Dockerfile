# Disposable RDP client for the Windows Guest Helper acceptance suite. This is
# not the production Windows unattended session component or a human client.
ARG DEBIAN_IMAGE=debian:trixie-slim
FROM ${DEBIAN_IMAGE}
ARG DEBIAN_MIRROR=http://deb.debian.org/debian
ARG DEBIAN_SECURITY_MIRROR=http://deb.debian.org/debian-security
RUN sed -i "s|http://deb.debian.org/debian$|${DEBIAN_MIRROR}|g; s|http://deb.debian.org/debian-security$|${DEBIAN_SECURITY_MIRROR}|g" /etc/apt/sources.list.d/debian.sources \
    && apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates freerdp3-x11 xvfb xauth tini \
    && apt-get clean
WORKDIR /tmp
# Supply all acceptance arguments via /args-from:stdin, including an explicit
# certificate fingerprint verified through QGA. No password in argv or env.
# xvfb-run waits for a readiness signal from Xvfb. It must not be PID 1; tini
# also forwards termination and reaps any remaining X11/client processes.
ENTRYPOINT ["/usr/bin/tini", "-g", "--", "xvfb-run", "-a", "-s", "-screen 0 1280x720x24 -nolisten tcp", "xfreerdp3"]
