# BondVPN as a container: drop in your WireGuard files, start it, done.
#
# The image carries the released binary rather than building it - BondVPN is
# closed source, and this repository holds no code. The version is passed in so
# the tag and the binary inside it cannot drift apart.
#
# WHY ALPINE AND NOT scratch
# BondVPN shells out to wg-quick, ip and iptables: they are the kernel's own
# interface for what it does, and reimplementing netlink and netfilter inside a
# static binary would be a far bigger surface than a 15 MB base image.
FROM alpine:3.20

ARG VERSION=1.5.1
ARG TARGETARCH=amd64

RUN apk add --no-cache \
        wireguard-tools \
        iproute2 \
        iptables \
        ip6tables \
        curl \
        ca-certificates

# Fetched from the release, then checked against the published SHA256SUMS. An
# image that silently packaged a truncated download would fail at run time on
# somebody else's gateway, which is the worst place to find out.
RUN set -eu; \
    cd /tmp; \
    curl -fsSLO "https://github.com/wonderingStars/bondvpn/releases/download/v${VERSION}/bondvpn-linux-${TARGETARCH}"; \
    curl -fsSLO "https://github.com/wonderingStars/bondvpn/releases/download/v${VERSION}/SHA256SUMS"; \
    grep " \*\?bondvpn-linux-${TARGETARCH}\$" SHA256SUMS | sed "s/ \*/ /" > want.txt; \
    sha256sum -c want.txt; \
    install -m 0755 "bondvpn-linux-${TARGETARCH}" /usr/local/bin/bondvpn; \
    rm -rf /tmp/*

COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
COPY docker-healthcheck.sh /usr/local/bin/docker-healthcheck.sh
COPY config.example.yml /usr/share/bondvpn/config.example.yml
RUN chmod +x /usr/local/bin/docker-entrypoint.sh /usr/local/bin/docker-healthcheck.sh

# The interface and the status API. Published here for documentation; with
# --network host (which this needs) the port is the host's own.
EXPOSE 8099

# A gateway that cannot route is not healthy, whatever the process is doing.
# The probe reads the port from the config rather than hardcoding 8099, because
# `listen` is configurable - a hardcoded probe reports a working gateway as
# unhealthy the moment someone changes it. /healthz 503s on real problems only;
# warnings (a missing keepalive, an unset host sysctl) keep it green.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s \
    CMD /usr/local/bin/docker-healthcheck.sh

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["run"]
