#!/bin/bash
# BondVPN test bed.
#
# Builds a complete miniature of the real deployment inside network namespaces:
# three WireGuard tunnels with REAL handshakes (each peering with a fake "VPN
# server" in its own namespace), a client bridge with three stand-in containers,
# and - deliberately - a working NAT path straight out to the internet.
#
# That last part is the point. If the client cannot reach the internet even
# without BondVPN running, then a passing leak test proves nothing. The leak
# path is the control.
set -eu

BR=br-bondtest
CLIENTS=10.199.0.0/24
KEYS=/run/bondtest
UPLINK=eth0

teardown() {
	for i in 0 1 2; do
		ip netns del "srv$i" 2>/dev/null || true
		ip netns del "client$i" 2>/dev/null || true
		ip link del "wg$i" 2>/dev/null || true
		ip link del "veth$i" 2>/dev/null || true
		ip link del "ctl$i" 2>/dev/null || true
	done
	ip netns del bondvpn-probe 2>/dev/null || true
	ip link del bvprobe 2>/dev/null || true
	ip link del "$BR" 2>/dev/null || true
	iptables -t nat -F POSTROUTING 2>/dev/null || true
	iptables -t nat -F PREROUTING 2>/dev/null || true
	iptables -F DOCKER-USER 2>/dev/null || true
	iptables -D FORWARD -j DOCKER-USER 2>/dev/null || true
	iptables -X DOCKER-USER 2>/dev/null || true
	ip6tables -F DOCKER-USER 2>/dev/null || true
	ip6tables -D FORWARD -j DOCKER-USER 2>/dev/null || true
	ip6tables -X DOCKER-USER 2>/dev/null || true
	ip rule del priority 90 2>/dev/null || true
	ip rule del priority 91 2>/dev/null || true
	while ip rule del priority 95 2>/dev/null; do :; done
	ip rule del priority 100 2>/dev/null || true
	pkill -f "http.server 80" 2>/dev/null || true
	rm -rf "$KEYS" /etc/netns/client0 /etc/netns/client1 /etc/netns/client2
}

teardown
if [ "${1:-}" = "down" ]; then
	echo "test bed torn down"
	exit 0
fi

mkdir -p "$KEYS" /etc/wireguard /etc/bondvpn
chmod 700 "$KEYS"
sysctl -qw net.ipv4.ip_forward=1

ip link add "$BR" type bridge
ip addr add 10.199.0.1/24 dev "$BR"
ip link set "$BR" up

# The leak path: clients NAT straight out of the uplink. BondVPN has to be what
# closes this.
iptables -t nat -A POSTROUTING -s "$CLIENTS" -o "$UPLINK" -j MASQUERADE

for i in 0 1 2; do
	wg genkey >"$KEYS/c$i"
	wg pubkey <"$KEYS/c$i" >"$KEYS/c$i.pub"
	wg genkey >"$KEYS/s$i"
	wg pubkey <"$KEYS/s$i" >"$KEYS/s$i.pub"
	chmod 600 "$KEYS/c$i" "$KEYS/s$i"
done

for i in 0 1 2; do
	# The far end of tunnel i: its own namespace, reachable over a veth.
	ip netns add "srv$i"
	ip link add "veth$i" type veth peer name "vs$i"
	ip link set "vs$i" netns "srv$i"
	ip addr add "10.20$i.0.1/24" dev "veth$i"
	ip link set "veth$i" up
	iptables -t nat -A POSTROUTING -s "10.20$i.0.0/24" -o "$UPLINK" -j MASQUERADE

	ip netns exec "srv$i" ip link set lo up
	ip netns exec "srv$i" ip addr add "10.20$i.0.2/24" dev "vs$i"
	ip netns exec "srv$i" ip link set "vs$i" up
	ip netns exec "srv$i" ip route add default via "10.20$i.0.1"
	ip netns exec "srv$i" sysctl -qw net.ipv4.ip_forward=1

	ip netns exec "srv$i" ip link add wgs type wireguard
	ip netns exec "srv$i" wg set wgs listen-port 51820 private-key "$KEYS/s$i" \
		peer "$(cat "$KEYS/c$i.pub")" allowed-ips "10.10.$i.1/32"
	ip netns exec "srv$i" ip addr add "10.10.$i.2/24" dev wgs
	ip netns exec "srv$i" ip link set wgs up
	ip netns exec "srv$i" iptables -t nat -A POSTROUTING -s "10.10.$i.0/24" -o "vs$i" -j MASQUERADE

	# Each server answers on the same address with a DIFFERENT body, so a client
	# request proves which tunnel actually carried it. Counting bytes on an
	# interface would only show that something moved.
	mkdir -p "$KEYS/www$i"
	echo "tunnel wg$i" >"$KEYS/www$i/index.html"
	ip netns exec "srv$i" ip addr add 203.0.113.1/32 dev lo
	ip netns exec "srv$i" sh -c \
		"cd $KEYS/www$i && nohup python3 -m http.server 80 --bind 203.0.113.1 >/dev/null 2>&1 &"

	# BondVPN brings these up itself with wg-quick, so this is the real path.
	cat >"/etc/wireguard/wg$i.conf" <<EOF
[Interface]
PrivateKey = $(cat "$KEYS/c$i")
Address = 10.10.$i.1/24
Table = off

[Peer]
PublicKey = $(cat "$KEYS/s$i.pub")
Endpoint = 10.20$i.0.2:51820
AllowedIPs = 0.0.0.0/0
PersistentKeepalive = 5
EOF
	chmod 600 "/etc/wireguard/wg$i.conf"

	# A stand-in container on the client bridge.
	ip netns add "client$i"
	ip link add "ctl$i" type veth peer name "cn$i"
	ip link set "ctl$i" master "$BR"
	ip link set "ctl$i" up
	ip link set "cn$i" netns "client$i"
	ip netns exec "client$i" ip link set lo up
	ip netns exec "client$i" ip addr add "10.199.0.1$i/24" dev "cn$i"
	ip netns exec "client$i" ip link set "cn$i" up
	ip netns exec "client$i" ip route add default via 10.199.0.1

	# Point every client at a resolver that does not exist. If name resolution
	# still works, it can only be because BondVPN rewrote the query onto the
	# tunnel - which is the claim being tested. A client pointed at a working
	# resolver would resolve either way and prove nothing.
	mkdir -p "/etc/netns/client$i"
	echo "nameserver 192.0.2.53" >"/etc/netns/client$i/resolv.conf"
done

cat >/etc/bondvpn/config.yml <<'EOF'
tunnels:
  - /etc/wireguard/wg0.conf
  - /etc/wireguard/wg1.conf
  - /etc/wireguard/wg2.conf

clients: 10.199.0.0/24
default: bond

routes:
  pin:
    - 10.199.0.10
    - 10.199.0.11
  bond:
    - 10.199.0.12

# Reachable through the fake servers, which NAT to the real internet - so a
# client resolving anything proves the query took a tunnel.
dns: 1.1.1.1

lan: auto
wan_interface: auto
stale_handshake: 180
listen: 127.0.0.1:8099
EOF

# A provider's file as downloaded, kept for the check that BondVPN refuses it.
mkdir -p "$KEYS/bad"
cat >"$KEYS/bad/wgbad.conf" <<'EOF'
[Interface]
PrivateKey = aGVsbG8gdGhlcmU=
Address = 10.66.85.202/32
DNS = 10.64.0.1

[Peer]
PublicKey = c29tZSBwdWJsaWMga2V5
AllowedIPs = 0.0.0.0/0
Endpoint = 185.213.154.67:51820
EOF
cat >"$KEYS/bad/config.yml" <<EOF
tunnels:
  - $KEYS/bad/wgbad.conf
clients: 10.199.0.0/24
dns: 1.1.1.1
EOF

echo "test bed up: 3 tunnels, 3 clients on $CLIENTS, leak path OPEN"
