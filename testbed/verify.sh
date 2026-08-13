#!/bin/bash
# End-to-end verification of the BondVPN daemon against the test bed.
set -u

FAILS=0
pass() { echo "  PASS  $1"; }
fail() {
	echo "  FAIL  $1"
	FAILS=$((FAILS + 1))
}
check() { # check <description> <expected> <actual>
	if [ "$2" = "$3" ]; then pass "$1 ($3)"; else fail "$1 (expected $2, got $3)"; fi
}

reach() { # reach <client-netns> -> "yes"/"no"
	if ip netns exec "$1" curl -s -o /dev/null --max-time 8 http://1.1.1.1; then
		echo yes
	else
		echo no
	fi
}
identity() { # identity <client-netns> -> the tunnel that carried the request
	ip netns exec "$1" curl -s --max-time 8 http://203.0.113.1/ 2>/dev/null | tr -d '\n'
}
armed() {
	if iptables -C DOCKER-USER -s 10.199.0.0/24 -j REJECT --reject-with icmp-port-unreachable 2>/dev/null; then
		echo yes
	else
		echo no
	fi
}
natted() {
	if iptables -t nat -C POSTROUTING -s 10.199.0.0/24 -o 'wg+' -j MASQUERADE 2>/dev/null; then
		echo yes
	else
		echo no
	fi
}
dnatted() { # both halves - checking only UDP would miss the TCP fallback
	if iptables -t nat -C PREROUTING -s 10.199.0.0/24 -p udp --dport 53 -j DNAT --to-destination 1.1.1.1:53 2>/dev/null &&
		iptables -t nat -C PREROUTING -s 10.199.0.0/24 -p tcp --dport 53 -j DNAT --to-destination 1.1.1.1:53 2>/dev/null; then
		echo yes
	else
		echo no
	fi
}
resolves() { # the client's own resolver does not exist, so this can only work
	if ip netns exec "$1" getent hosts example.com >/dev/null 2>&1; then
		echo yes
	else
		echo no
	fi
}

# The identity responders take several seconds to bind in their namespaces.
# Probing before they are listening reads as "the tunnel does not work", which
# cost one wrong diagnosis already.
echo "== 0. Wait for the test bed's identity responders =="
for i in 0 1 2; do
	for _ in $(seq 1 40); do
		ip netns exec "srv$i" curl -s --max-time 2 http://203.0.113.1/ >/dev/null 2>&1 && break
		sleep 1
	done
done
check "responders are answering" "tunnel wg0" "$(ip netns exec srv0 curl -s --max-time 3 http://203.0.113.1/ | tr -d '\n')"

echo
echo "== 1. Control: the leak path is open before BondVPN starts =="
# If this fails, every later "blocked" result would be meaningless.
check "client0 reaches the internet" yes "$(reach client0)"
check "client1 reaches the internet" yes "$(reach client1)"
check "kill switch not yet armed" no "$(armed)"

echo
echo "== 2. Start the daemon =="
nohup bondvpn run >/var/log/bondvpn.log 2>&1 &
DAEMON=$!
for _ in $(seq 1 30); do
	sleep 2
	if bondvpn status >/tmp/status.json 2>/dev/null; then break; fi
done
if kill -0 "$DAEMON" 2>/dev/null; then
	pass "daemon is running (pid $DAEMON)"
else
	fail "daemon died"
fi
head -6 /var/log/bondvpn.log

echo
echo "== 3. Status =="
bondvpn status >/tmp/status.json
STATUS_EXIT=$?
check "status exits 0 (no problems)" 0 "$STATUS_EXIT"
python3 - <<'PY'
import json
s = json.load(open('/tmp/status.json'))
print(f"  bonded={s['bonded']} hash_policy={s['hash_policy']} "
      f"killswitch={s['killswitch']} problems={s['problems']}")
for t in s['tunnels']:
    print(f"  {t['iface']} up={t['up']} in_bond={t['in_bond']} "
          f"handshake_age={t['handshake_age']}s pinned={t['pinned_clients']}")
PY
BONDED=$(python3 -c "import json;print(json.load(open('/tmp/status.json'))['bonded'])")
PROBLEMS=$(python3 -c "import json;print(len(json.load(open('/tmp/status.json'))['problems']))")
PINS=$(python3 -c "import json;s=json.load(open('/tmp/status.json'));print(len({c['tunnel'] for c in s['clients'] if c['mode']=='pin'}))")
check "three tunnels bonded" 3 "$BONDED"
check "no problems reported" 0 "$PROBLEMS"
check "pinned clients on distinct tunnels" 2 "$PINS"
check "kill switch armed" yes "$(armed)"
check "client traffic translated onto the tunnels" yes "$(natted)"
check "client DNS redirected onto the tunnels" yes "$(dnatted)"
check "status reports the running version" "$(bondvpn version | awk '{print $2}')" \
	"$(python3 -c "import json;print(json.load(open('/tmp/status.json'))['version'])")"

echo
echo "== 4. Traffic actually takes the tunnel it was pinned to =="
check "client0 (pinned) exits via wg0" "tunnel wg0" "$(identity client0)"
check "client1 (pinned) exits via wg1" "tunnel wg1" "$(identity client1)"
echo "  client2 (bonded) exited via: $(identity client2)"

echo
echo "== 4b. DNS is forced onto the tunnels =="
# Every client is configured with a resolver that does not exist, so anything
# resolving at all means the query was rewritten onto a tunnel.
check "client0 resolves despite a dead resolver" yes "$(resolves client0)"
check "client2 resolves despite a dead resolver" yes "$(resolves client2)"

echo
echo "== 4c. The firewall repairs itself =="
# A firewalld or ufw reload, an iptables-restore, or another tool's cleanup
# takes these rules out from under us. Reporting that is not enough: the kill
# switch failing open is exactly the case where nobody is reading status.
iptables -F DOCKER-USER
iptables -t nat -D POSTROUTING -s 10.199.0.0/24 -o 'wg+' -j MASQUERADE 2>/dev/null
iptables -t nat -D PREROUTING -s 10.199.0.0/24 -p udp --dport 53 -j DNAT --to-destination 1.1.1.1:53 2>/dev/null
check "kill switch really was removed" no "$(armed)"
echo "  waiting for the daemon to notice..."
for _ in $(seq 1 12); do
	sleep 3
	[ "$(armed)" = yes ] && [ "$(natted)" = yes ] && [ "$(dnatted)" = yes ] && break
done
check "kill switch re-armed by itself" yes "$(armed)"
check "client NAT restored by itself" yes "$(natted)"
check "DNS redirect restored by itself" yes "$(dnatted)"
check "traffic still takes its pinned tunnel" "tunnel wg0" "$(identity client0)"

echo
echo "== 4e. A tunnel that dies is brought back =="
# Dropping a dead tunnel from the bond is only half the job. Without recovery
# the gateway degrades permanently on the first blip and reports itself healthy
# about the tunnels that remain.
wg-quick down /etc/wireguard/wg1.conf >/dev/null 2>&1
check "wg1 really is gone" no "$(
	if ip link show wg1 >/dev/null 2>&1; then echo yes; else echo no; fi
)"
echo "  waiting for the daemon to bring it back..."
for _ in $(seq 1 20); do
	sleep 3
	ip link show wg1 >/dev/null 2>&1 && break
done
check "wg1 is back" yes "$(
	if ip link show wg1 >/dev/null 2>&1; then echo yes; else echo no; fi
)"
for _ in $(seq 1 15); do
	sleep 2
	bondvpn status >/tmp/status3.json 2>/dev/null
	[ "$(python3 -c "import json;print(json.load(open('/tmp/status3.json'))['bonded'])")" = 3 ] && break
done
check "back in the bond" 3 "$(python3 -c "import json;print(json.load(open('/tmp/status3.json'))['bonded'])")"
# `status` from the command line reports the plan it would build, which can run
# ahead of what the daemon has installed by up to one pass. Wait for the
# kernel, not for the report.
for _ in $(seq 1 15); do
	[ "$(identity client1)" = "tunnel wg1" ] && break
	sleep 2
done
check "client1 exits via wg1 again" "tunnel wg1" "$(identity client1)"

echo
echo "== 4f. Flushed routing rules are reinstalled =="
# A flush does not change the plan, so a daemon watching only its own plan
# would never notice. Every client would quietly fall back to the main table
# and leave through the ISP connection.
ip rule del priority 95 2>/dev/null
ip rule del priority 95 2>/dev/null
ip rule del priority 100 2>/dev/null
check "rules really were flushed" 2 "$(ip rule show | grep -cE '^(90|91|95|100):')"
echo "  waiting for the daemon to reinstall them..."
for _ in $(seq 1 12); do
	sleep 3
	[ "$(ip rule show | grep -cE '^(90|91|95|100):')" = 5 ] && break
done
check "all five rules are back" 5 "$(ip rule show | grep -cE '^(90|91|95|100):')"
check "client0 exits via wg0 again" "tunnel wg0" "$(identity client0)"

echo
echo "== 4d. A provider config as downloaded is refused =="
BAD=$(bondvpn run -config /run/bondtest/bad/config.yml 2>&1)
BAD_EXIT=$?
check "refused" 1 "$BAD_EXIT"
case "$BAD" in
*"Table = off"*) pass "explains the missing Table = off" ;;
*) fail "does not explain Table = off: $BAD" ;;
esac
case "$BAD" in
*resolvconf*) pass "explains the DNS line" ;;
*) fail "does not explain the DNS line: $BAD" ;;
esac
check "the running daemon was left alone" yes "$(armed)"

echo
echo "== 5. Leak test =="
bondvpn leak-test
LEAK_EXIT=$?
check "leak-test exits 0 (nothing escaped)" 0 "$LEAK_EXIT"

echo
echo "== 6. The restore put the pins back =="
sleep 5
bondvpn status >/tmp/status2.json
PINS2=$(python3 -c "import json;s=json.load(open('/tmp/status2.json'));print(len({c['tunnel'] for c in s['clients'] if c['mode']=='pin'}))")
BONDED2=$(python3 -c "import json;print(json.load(open('/tmp/status2.json'))['bonded'])")
check "three tunnels bonded again" 3 "$BONDED2"
check "pins spread again" 2 "$PINS2"
check "client0 still exits via wg0" "tunnel wg0" "$(identity client0)"

echo
echo "== 7. Killing the daemon leaves the kill switch armed =="
kill -TERM "$DAEMON" 2>/dev/null
sleep 3
check "daemon has exited" "" "$(ps -p "$DAEMON" -o pid= 2>/dev/null | tr -d ' ')"
check "kill switch still armed" yes "$(armed)"

echo
echo "== 8. Tunnels down with the kill switch armed: nothing escapes =="
for i in 0 1 2; do wg-quick down "/etc/wireguard/wg$i.conf" >/dev/null 2>&1; done
check "client0 is blocked" no "$(reach client0)"
check "client2 is blocked" no "$(reach client2)"

echo
echo "== 9. stop disarms it =="
bondvpn stop
check "kill switch removed" no "$(armed)"
check "client NAT removed" no "$(natted)"
check "DNS redirect removed" no "$(dnatted)"
check "client0 reaches the internet again" yes "$(reach client0)"

echo
if [ "$FAILS" -eq 0 ]; then
	echo "ALL CHECKS PASSED"
else
	echo "$FAILS CHECK(S) FAILED"
fi
exit "$FAILS"
