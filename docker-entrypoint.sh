#!/bin/sh
# Turn "I mounted my WireGuard files" into a running gateway.
#
# THE POINT OF THIS SCRIPT
# Everything BondVPN needs can be derived except the tunnels themselves, and the
# tunnels are the one thing only you have. So: mount your provider's .conf files
# and start the container. No config file to write, nothing to edit.
#
# If you DO mount a config at /etc/bondvpn/config.yml it is used untouched, and
# none of the generation below happens - a hand-written config is always the
# authority.
set -eu

CFG=/etc/bondvpn/config.yml
WGDIR=${WGDIR:-/etc/wireguard}

log() { echo "bondvpn-entrypoint: $*"; }

if [ ! -f "$CFG" ]; then
    # ---- find what they mounted ------------------------------------------
    # Sorted, so wg0 is the first tunnel every time. Unsorted, the pin order
    # would change between restarts and clients would swap exits for no reason.
    tunnels=$(find "$WGDIR" -maxdepth 1 -name '*.conf' 2>/dev/null | sort || true)
    count=$(printf '%s\n' "$tunnels" | grep -c . || true)

    if [ "$count" = "0" ]; then
        cat >&2 <<EOF

No WireGuard configs found in $WGDIR.

That is the one thing this image cannot supply for you. Download the device
configs from your VPN provider - three of them, on DIFFERENT servers - and
mount the directory holding them:

  docker run --rm -it \\
    --network host --cap-add NET_ADMIN --cap-add SYS_MODULE \\
    -v /etc/wireguard:/etc/wireguard:ro \\
    ghcr.io/wonderingstars/bondvpn:latest

Everything else has a working default. See README.md for the compose file.

EOF
        exit 1
    fi

    log "found $count tunnel config(s) in $WGDIR"

    # ---- write a config around them --------------------------------------
    # CLIENTS defaults to the subnet the shipped compose file puts the download
    # containers on. It is deliberately NOT the LAN: bonding a subnet that
    # contains this host's own address policy-routes the host's own replies into
    # a tunnel and the box stops answering. bondvpn refuses that outright, but
    # the default should never get near it.
    CLIENTS=${CLIENTS:-10.99.0.0/24}
    DNS=${DNS:-10.64.0.1}
    LISTEN=${LISTEN:-127.0.0.1:8099}
    DEFAULT_MODE=${DEFAULT_MODE:-bond}

    mkdir -p /etc/bondvpn
    {
        echo "# BONDVPN-GENERATED - do not hand-edit and expect the env to win."
        echo "# Generated on first start from the files mounted in $WGDIR."
        echo "# Mount your own $CFG to take over completely, or delete this file"
        echo "# to have it regenerated from the current environment."
        echo "tunnels:"
        printf '%s\n' "$tunnels" | while read -r t; do
            [ -n "$t" ] && echo "  - $t"
        done
        echo
        echo "clients: $CLIENTS"
        echo "default: $DEFAULT_MODE"
        echo "dns: $DNS"
        echo "lan: auto"
        echo "wan_interface: auto"
        echo "listen: $LISTEN"
        echo "dashboard: ${DASHBOARD:-true}"

        # PIN is a space-separated list of client addresses that must each keep
        # ONE exit - torrent clients. Left empty by default because pinning an
        # address that is not there would be a promise about a container nobody
        # started.
        if [ -n "${PIN:-}" ]; then
            echo
            echo "routes:"
            echo "  pin:"
            for ip in $PIN; do echo "    - $ip"; done
        fi

        # The two disks anyone running this actually cares about: where
        # downloads land while they run, and where the finished media lives.
        # They are mounted at fixed paths inside the container so the panel
        # reads the real filesystem rather than the container's own root - and
        # an unreadable one is drawn as unreadable rather than as full, which
        # matters when the library is an NFS export that has gone away.
        if [ -d /staging ] || [ -d /library ]; then
            echo
            echo "disks:"
            [ -d /staging ] && echo "  - staging /staging"
            [ -d /library ] && echo "  - library /library"
        fi
    } > "$CFG"

    log "wrote $CFG"
elif head -n1 "$CFG" 2>/dev/null | grep -q "BONDVPN-GENERATED"; then
    # A config WE wrote on a previous start, persisted in the volume. Say so, and
    # say how to change it - claiming the user "mounted" it sent people hunting
    # for a bind mount that does not exist when they wanted to change PIN or DNS.
    log "reusing the generated $CFG from a previous start"
    log "  (delete it to regenerate from the current environment, or mount your own)"
else
    log "using the $CFG you mounted"
fi

# ---- the sysctl the bond needs ------------------------------------------
# Layer-4 hashing. Without it every connection to a given server lands on ONE
# tunnel and the bond does nothing - measured 400 of 400 usenet-shaped flows on
# a single tunnel at policy 0. With --network host this is the HOST's setting,
# so it needs a writable /proc/sys: that means --privileged, or setting it on
# the host once. Not fatal either way; bondvpn says so in check and in status.
#
# Tested by attempting it, not with `[ -w ]`: inside a container /proc/sys is
# mounted read-only while the file's own mode bits still say writable, so the
# test passes and the redirect then fails with a raw shell error in the logs.
# The redirect is inside a SUBSHELL whose stderr is redirected, because a failed
# `>` is reported by the shell itself rather than by the command - so
# `echo 1 > file 2>/dev/null` still prints "can't create ...: Read-only file
# system" over the friendly explanation below.
if ( echo 1 > /proc/sys/net/ipv4/fib_multipath_hash_policy ) 2>/dev/null; then
    log "layer-4 multipath hashing enabled"
else
    log "cannot set fib_multipath_hash_policy - without it every connection to a"
    log "  given server lands on ONE tunnel and the bond does nothing. Either add"
    log "  --privileged, or run this once on the host:"
    log "    sysctl -w net.ipv4.fib_multipath_hash_policy=1"
fi

# ---- say what is wrong before trying to route ---------------------------
# check changes nothing and reports everything, so a container that is about to
# fail says why in its own logs rather than restart-looping in silence.
if [ "${1:-run}" = "run" ]; then
    bondvpn check || log "check reported problems - see above"
fi

exec bondvpn "$@"
