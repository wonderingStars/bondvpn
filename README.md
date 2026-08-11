# BondVPN

Multi-tunnel WireGuard routing for a download stack. One static binary, no
runtime dependencies, fails closed.

Point your \*arr containers at it and each one gets a VPN exit that behaves the
way that application actually wants:

- **Pinned clients** get one tunnel each and stay on it. BitTorrent ties your
  identity to an address, so spreading a torrent client across tunnels leaves
  trackers and peers holding an address you are not reliably at. Measured
  against the same swarms: 1 connected seed at 0 B/s spread, against 7 seeds at
  2.05 MB/s pinned.
- **Bonded clients** are spread across every live tunnel. Usenet, indexers and
  direct HTTP open many independent connections to one server, each
  authenticating separately, so a rotating source address costs nothing and you
  get the sum of your tunnels.
- **Nothing leaks.** With every tunnel down, clients have no path out at all —
  and `bondvpn leak-test` proves it by measuring, not by asserting.

Free to use. Ships configured for three tunnels; up to five are supported.

## Install

Download the binary for your architecture from
[Releases](https://github.com/wonderingStars/bondvpn/releases), then:

```
sudo install -m 0755 bondvpn-linux-amd64 /usr/local/bin/bondvpn
sudo install -d /etc/bondvpn
sudo install -m 0600 config.example.yml /etc/bondvpn/config.yml
sudo install -m 0644 bondvpn.service /etc/systemd/system/
sudoedit /etc/bondvpn/config.yml
sudo systemctl enable --now bondvpn
```

Or run `sudo ./install.sh` from the directory holding the downloaded binary — it
does the same thing, picks the right architecture, and will not overwrite a
config you already have.

Verify what you downloaded against `SHA256SUMS` on the release:

```
sha256sum -c SHA256SUMS
```

Requires Linux, root, and `wg`, `wg-quick`, `ip`, `iptables`, `curl`.

### Your tunnel configs

A provider's file as downloaded is not safe to hand to this gateway. BondVPN
checks each one at startup and refuses to run rather than misbehave quietly, so
you find out immediately rather than a week later:

```ini
[Interface]
Table = off                 # REQUIRED. Otherwise wg-quick installs its own
                            # default route and fights BondVPN for the routing
                            # table — your traffic takes the wrong tunnel and
                            # nothing reports an error.
# DNS = 10.64.0.1           # REMOVE this line. wg-quick uses it to rewrite
                            # your machine's own resolver, and fails outright
                            # if resolvconf is not installed. BondVPN handles
                            # client DNS itself.
[Peer]
PersistentKeepalive = 25    # Recommended. WireGuard only rekeys when there is
                            # traffic, so an idle tunnel stops handshaking and
                            # gets dropped from the bond while perfectly
                            # healthy. This warns rather than refuses.
```

## Configure

Everything is documented in `config.example.yml`. The two settings that matter:

```yaml
clients: 172.20.0.0/24     # the network your containers sit on

routes:
  pin:                     # torrent clients: one tunnel each, permanently
    - 172.20.0.10
  bond:                    # usenet, indexers, direct HTTP: spread across all
    - 172.20.0.20
```

`clients` must be your containers' own network, never your LAN. BondVPN refuses
to start if you point it at your LAN, because that configuration routes the
host's own replies into a tunnel and the machine goes silent while looking
perfectly healthy.

### DNS is enforced, not suggested

`dns:` is required, and every DNS query leaving your client subnet is rewritten
in the kernel to that resolver, over the tunnels — whatever the container itself
is set to use.

Merely allowing DNS is not enough: a container pointed at a public resolver
leaks every hostname you look up while its traffic stays correctly tunnelled and
nothing looks broken. Configuring it per-container would make your privacy
depend on never forgetting a line in a compose file. There is nothing here for
you to get wrong.

## Use

```
bondvpn run          # the daemon; what systemd starts
bondvpn status       # current state as JSON, exit 1 if anything is wrong
bondvpn leak-test    # disruptive proof that nothing escapes
bondvpn stop         # tunnels down AND the kill switch removed
```

`status` is read-only and safe at any time. `stop` is the only command that
disarms the kill switch — restarting or killing the daemon deliberately leaves
it armed, so there is never a window where your clients route straight out of
your ISP connection.

### Status API

`run` serves the same document over HTTP on `listen` (loopback by default):

- `GET /status` — the full JSON document
- `GET /healthz` — 200 when healthy, 503 with the reasons when not

It exposes tunnel endpoints and client addresses and has no authentication of
its own. Put a reverse proxy in front of it if you want it on your LAN.

```json
{
  "version": "1.1.0",
  "bonded": 3,
  "hash_policy": 1,
  "nat": true,
  "dns_forced": true,
  "killswitch": { "v4": true, "v6": true },
  "tunnels": [
    { "iface": "wg0", "up": true, "in_bond": true, "handshake_age": 44,
      "pinned_clients": ["172.20.0.10"] }
  ],
  "problems": []
}
```

### The leak test

`leak-test` drops every tunnel for around thirty seconds, then probes a
hostname, a raw IP and ICMP from a throwaway network namespace attached to your
container bridge — kernel-wise indistinguishable from one of your containers, so
it is matched by exactly the same firewall rules. Then it restores the tunnels
and verifies your pinned clients came back spread across them.

Probing from the host would prove nothing: the kill switch sits in a
forward-side chain and never sees host traffic, so a host can reach the internet
perfectly legitimately while every container is sealed.

## What it reports as a problem

`status` and `/healthz` name the silent failures, not just the loud ones:

- a tunnel that is up but has not handshaken — it will blackhole whatever is
  routed into it, and "up" means very little on WireGuard
- every pinned client landing on the same tunnel, which looks healthy because
  traffic still flows
- `fib_multipath_hash_policy` not set to 1, without which every connection to a
  given server lands on one tunnel and bonding does nothing
- the kill switch not being armed
- your traffic not being translated onto the tunnels, which nothing else
  reveals: the tunnels handshake, the routing is correct, the counters move,
  and every request dies at the far end with no error anywhere
- your DNS not being redirected
- a tunnel config that has lost `Table = off`, gained a `DNS =` line, or never
  had a keepalive

And whatever it can repair, it repairs. The kill switch, the address
translation and the DNS redirect are re-checked continuously and reinstalled if
anything removes them — a firewall reload takes them out silently, and a kill
switch failing open is exactly the case where nobody is watching.

## Why three tunnels

Five WireGuard configs consumes an entire Mullvad device allowance, leaving none
for your phone or laptop. Past three tunnels a single home connection is usually
the bottleneck rather than the tunnels. Raise it if your line is fast enough —
`tunnels:` accepts up to five entries.

## Licence and support

Proprietary; see [LICENSE](LICENSE). Free to install and use on any number of
machines you own or administer. The source is not distributed, so this
repository holds the documentation and the releases only.

Bug reports and feature requests are welcome in
[Issues](https://github.com/wonderingStars/bondvpn/issues). Include the output
of `bondvpn status` and `bondvpn version`.

BondVPN routes traffic and blocks it when its tunnels are down. It is not a
guarantee of anonymity. Verify your own setup with `bondvpn leak-test` before
relying on it.
