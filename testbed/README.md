# Test bed

Runs the daemon end to end against three real WireGuard tunnels without needing
a VPN account, a spare machine, or anything outside a Linux box with root.

`testbed.sh` builds the miniature: three tunnels that genuinely handshake (each
peering with a fake VPN server in its own network namespace), three stand-in
containers on a bridge, and a NAT path straight out to the internet.

That last part is deliberate and it is the point. If the clients cannot reach
the internet even with BondVPN stopped, a passing leak test proves nothing at
all — it would pass just as happily on a box with no connectivity. The open leak
path is the control, and `verify.sh` checks it first.

Each fake server answers `http://203.0.113.1/` with a different body, so a client
request proves which tunnel actually carried it. Interface byte counters would
only show that something moved.

## Run it

```
sudo ./testbed.sh          # build it
sudo ./verify.sh           # 20 checks, exits non-zero on any failure
sudo ./testbed.sh down     # tear it down
```

`verify.sh` installs nothing: it expects `bondvpn` on the PATH and reads
`/etc/bondvpn/config.yml`, which `testbed.sh` writes. Needs `wg`, `wg-quick`,
`ip`, `iptables`, `curl` and `python3`.

It is disruptive to the host's networking — it creates and deletes namespaces,
bridges, iptables rules and routing rules, and it runs `bondvpn stop` at the
end. Do not run it on a machine that is doing anything you care about.

## What it covers

1. control: the clients reach the internet before BondVPN starts
2. the daemon starts and stays up
3. three tunnels bonded, hash policy 1, kill switch armed, client NAT installed,
   pinned clients on distinct tunnels, no problems reported
4. traffic takes the exact tunnel it was pinned to
5. `leak-test` reports nothing escaping
6. the restore puts the pins back across all three tunnels
7. SIGTERM leaves the kill switch armed
8. with the tunnels down, every client is blocked
9. `stop` removes the kill switch and the NAT rule, and the clients get out again

## One trap worth knowing

The identity responders take about ten seconds to bind. Probing before that
reads exactly like "the tunnel does not work", and it cost one wrong diagnosis
before the wait in step 0 existed.
