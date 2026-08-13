#!/bin/bash
# Verify the shipped unit file under a real systemd, not just by reading it.
#
# The interesting property is what happens on RESTART. There is deliberately no
# ExecStop, because systemd runs ExecStop on every restart too - wiring
# `bondvpn stop` there would disarm the kill switch on each one, opening a
# window where clients route straight out of the ISP connection. This checks
# that the window does not exist.
#
# Expects the test bed to be up.
set -u

FAILS=0
pass() { echo "  PASS  $1"; }
fail() {
	echo "  FAIL  $1"
	FAILS=$((FAILS + 1))
}
check() {
	if [ "$2" = "$3" ]; then pass "$1 ($3)"; else fail "$1 (expected $2, got $3)"; fi
}
armed() {
	if iptables -C DOCKER-USER -s 10.199.0.0/24 -j REJECT --reject-with icmp-port-unreachable 2>/dev/null; then
		echo yes
	else
		echo no
	fi
}

UNIT=/mnt/c/Users/steve/GolandProjects/bondvpn/bondvpn.service
install -m 0644 "$UNIT" /etc/systemd/system/bondvpn.service
systemctl daemon-reload

echo "== the unit file is valid =="
OUT=$(systemd-analyze verify /etc/systemd/system/bondvpn.service 2>&1)
# Ordering against docker.service is expected to be absent on a machine without
# Docker and is not an error.
OUT=$(echo "$OUT" | grep -v "docker.service" | grep -v '^$')
check "systemd-analyze reports nothing" "" "$OUT"

echo
echo "== it starts =="
systemctl start bondvpn
sleep 6
check "active" active "$(systemctl is-active bondvpn)"
check "kill switch armed" yes "$(armed)"
check "status agrees" 0 "$(
	bondvpn status >/dev/null 2>&1
	echo $?
)"

echo
echo "== restarting does not open a window =="
systemctl restart bondvpn
check "kill switch still armed immediately after restart" yes "$(armed)"
sleep 5
check "active again" active "$(systemctl is-active bondvpn)"
check "kill switch armed" yes "$(armed)"

echo
echo "== stopping the SERVICE leaves the clients protected =="
# Only `bondvpn stop` disarms. A stopped service must not mean an open gateway.
systemctl stop bondvpn
sleep 2
check "inactive" inactive "$(systemctl is-active bondvpn)"
check "kill switch STILL armed" yes "$(armed)"

echo
echo "== cleanup =="
systemctl disable bondvpn >/dev/null 2>&1
bondvpn stop >/dev/null 2>&1
rm -f /etc/systemd/system/bondvpn.service
systemctl daemon-reload
check "kill switch released by bondvpn stop" no "$(armed)"

echo
if [ "$FAILS" -eq 0 ]; then
	echo "ALL SYSTEMD CHECKS PASSED"
else
	echo "$FAILS CHECK(S) FAILED"
fi
exit "$FAILS"
