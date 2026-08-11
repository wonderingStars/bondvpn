#!/bin/sh
# BondVPN installer. Run as root from the directory holding the downloaded
# binary, config.example.yml and bondvpn.service.
set -eu

BIN_DIR=/usr/local/bin
CFG_DIR=/etc/bondvpn
UNIT=/etc/systemd/system/bondvpn.service

if [ "$(id -u)" != "0" ]; then
	echo "run this as root" >&2
	exit 1
fi

case "$(uname -m)" in
	x86_64)          ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	*)               echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

SRC="bondvpn-linux-$ARCH"
[ -f "$SRC" ] || SRC=bondvpn
if [ ! -f "$SRC" ]; then
	echo "no binary found here - download bondvpn-linux-$ARCH from the releases page" >&2
	exit 1
fi

missing=
for tool in wg wg-quick ip iptables curl; do
	command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
done
if [ -n "$missing" ]; then
	echo "missing required tools:$missing" >&2
	exit 1
fi

install -m 0755 "$SRC" "$BIN_DIR/bondvpn"
install -d -m 0755 "$CFG_DIR"

# Never overwrite a working configuration. An installer that clobbers the file
# describing which client uses which tunnel can hand every torrent client the
# same exit on an upgrade, and nothing about that looks broken.
if [ -f "$CFG_DIR/config.yml" ]; then
	echo "keeping your existing $CFG_DIR/config.yml"
	[ -f config.example.yml ] && install -m 0600 config.example.yml "$CFG_DIR/config.example.yml"
else
	install -m 0600 config.example.yml "$CFG_DIR/config.yml"
	echo "wrote a starting configuration to $CFG_DIR/config.yml - EDIT IT before starting"
fi

if [ -f bondvpn.service ]; then
	install -m 0644 bondvpn.service "$UNIT"
	command -v systemctl >/dev/null 2>&1 && systemctl daemon-reload
fi

echo
"$BIN_DIR/bondvpn" version
cat <<'EOF'

Installed. Nothing is running yet - deliberately, because starting before the
configuration is right arms a kill switch around the wrong subnet.

  1. edit /etc/bondvpn/config.yml
  2. systemctl enable --now bondvpn
  3. bondvpn status
  4. bondvpn leak-test     (disruptive: ~30s with no tunnels)
EOF
