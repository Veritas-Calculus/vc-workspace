FROM debian:13-slim

ARG DEBIAN_MIRROR=http://deb.debian.org/debian
ARG DEBIAN_SECURITY_MIRROR=http://deb.debian.org/debian-security

RUN sed -i \
        -e "s#http://deb.debian.org/debian-security#${DEBIAN_SECURITY_MIRROR}#g" \
        -e "s#http://deb.debian.org/debian#${DEBIAN_MIRROR}#g" \
        /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        ca-certificates \
        dbus \
        libnss-sss \
        libpam-sss \
        oddjob \
        oddjob-mkhomedir \
        pamtester \
        procps \
        sssd \
        sssd-ldap \
        sssd-tools \
        systemd \
        systemd-sysv \
    && rm -rf /var/lib/apt/lists/* \
    && systemctl mask systemd-remount-fs.service

COPY entrypoint.sh /usr/local/bin/vc-workspace-lab-sssd-client

STOPSIGNAL SIGRTMIN+3
ENTRYPOINT ["/usr/local/bin/vc-workspace-lab-sssd-client"]
CMD ["/sbin/init"]
