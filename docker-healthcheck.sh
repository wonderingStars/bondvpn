#!/bin/sh
# Probe /healthz at whatever address `listen` actually is.
#
# WHY THIS IS NOT A ONE-LINE curl IN THE DOCKERFILE
# `listen` is configurable (LISTEN env, or a mounted config, or an edit to the
# persisted generated config). A hardcoded http://127.0.0.1:8099 probe reports a
# perfectly healthy gateway as unhealthy the moment anyone changes it - and the
# quickstart's own config volume comment invites editing. So read the port back
# from the config the daemon is actually using.
set -eu

CFG=${CFG:-/etc/bondvpn/config.yml}

# Default matches the binary's own default (config.go). Overridden by the
# listen: line if the config exists yet - on the very first health tick it may
# not, in which case the default is correct because the entrypoint has not been
# given a different LISTEN.
listen=127.0.0.1:8099
if [ -f "$CFG" ]; then
    l=$(sed -n 's/^[[:space:]]*listen:[[:space:]]*//p' "$CFG" | sed 's/[[:space:]]*#.*$//' | head -n1)
    [ -n "$l" ] && listen=$l
fi

# A bind on 0.0.0.0 or :: is reachable on loopback; probe there rather than
# trying to curl a wildcard address.
host=${listen%:*}
port=${listen##*:}
case "$host" in
    0.0.0.0 | "" | "::" | "[::]") host=127.0.0.1 ;;
esac

exec curl -fsS "http://${host}:${port}/healthz"
