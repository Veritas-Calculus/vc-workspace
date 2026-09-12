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
        ldap-utils \
        openssl \
        slapd \
    && rm -rf /var/lib/apt/lists/*

COPY entrypoint.sh /usr/local/bin/vc-workspace-lab-openldap

EXPOSE 389
ENTRYPOINT ["/usr/local/bin/vc-workspace-lab-openldap"]
