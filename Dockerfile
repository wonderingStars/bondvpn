# BondVPN as a container: drop in your WireGuard files, start it, done.
#
# Built from the source in this repository. It used to download a released
# binary and verify it against the published SHA256SUMS, which made sense while
# this was closed source and the repository held no code. It is AGPL now, the
# code is right here, and building what you can read is the better default.
#
# WHY ALPINE AND NOT scratch
# BondVPN shells out to wg-quick, ip and iptables: they are the kernel's own
# interface for what it does, and reimplementing netlink and netfilter inside a
# static binary would be a far bigger surface than a 15 MB base image.
#
# WHY wireguard-go IS IN HERE
# wireguard-tools alone gives you `wg` and `wg-quick`, both of which need a
# kernel that HAS WireGuard. A Synology NAS runs kernel 4.4, which predates it
# by a decade: without the userspace implementation the container starts,
# looks healthy, and cannot create a single tunnel. Measured on a DS1019+.

FROM golang:1.24-alpine AS build
WORKDIR /src
# No dependencies at all - go.mod has no require block - so there is nothing to
# fetch and no module cache step worth having.
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/bondvpn .

# wireguard-go is not packaged by Alpine at any version - checked against the
# 3.20 repositories and edge/community, which carry wireguard-tools and nothing
# else - so it is built here from upstream, pinned to a tag.
#
# Cloned rather than `go install`ed: the Go module proxy only knows this module
# up to v0.0.20201121, five years stale, because upstream's newer tags are
# unprefixed (0.0.20250522, no "v") and the proxy does not treat those as module
# versions. Building from the tag is the only way to get a current one.
#
# It is built in a separate directory, so BondVPN's own go.mod is untouched and
# this project still has no dependencies of its own.
ARG WIREGUARD_GO_VERSION=0.0.20250522
RUN apk add --no-cache git \
    && git clone -q --depth 1 --branch "${WIREGUARD_GO_VERSION}" \
         https://git.zx2c4.com/wireguard-go /tmp/wireguard-go \
    && cd /tmp/wireguard-go \
    && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/wireguard-go . \
    && rm -rf /tmp/wireguard-go

FROM alpine:3.20

# iptables-legacy as well as the default nft build. Alpine's `iptables` is the
# nf_tables variant, and a kernel without nf_tables answers every call with
# "Could not fetch rule set generation id: Invalid argument" - so on a Synology
# NAS (kernel 4.4) the container starts, passes its checks, and then cannot arm
# the kill switch. The entrypoint picks whichever one this kernel actually
# supports; both are installed because the image does not know where it will run.
RUN apk add --no-cache \
        wireguard-tools \
        iproute2 \
        iptables \
        ip6tables \
        iptables-legacy \
        curl \
        ca-certificates

COPY --from=build /out/bondvpn /usr/local/bin/bondvpn
COPY --from=build /out/wireguard-go /usr/local/bin/wireguard-go
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
