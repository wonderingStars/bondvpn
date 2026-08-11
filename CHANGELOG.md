# Changelog

## 1.2.0

The daemon now recovers from things that break after it started, rather than
only from things that were wrong when it started.

### Tunnels that die are brought back

Dropping a dead tunnel from the bond was only half the job: it stayed dead until
somebody noticed, its clients re-packed onto the survivors, and the bond ran at
reduced capacity indefinitely while reporting itself healthy about the tunnels
that remained. A tunnel that is down is now started again, and one that is up
but has stopped handshaking - which blackholes everything routed into it while
looking present - is cycled. Both are backed off (60s and 5 minutes) so a dead
endpoint is not hammered, and cycling gets the longer wait because it interrupts
whatever is still flowing.

### Routing repairs itself, including the tables

Policy rules were only re-applied when the PLAN changed, so anything that
flushed them - another tool, a script, a person - went unnoticed and every
client fell back to the main table and out through the ISP connection.

The check covers routes as well as rules, and that distinction was found the
hard way: deleting an interface silently empties the table holding a route
through it while the rule pointing at that table survives. A tunnel that went
away and came back left its pinned client with a rule to an empty table and no
way out at all - the plan unchanged, every rule present, and exactly one client
dead. Caught on the test bed by killing a tunnel and watching that client stay
silent after everything else recovered.

## 1.1.0

Everything here came from asking what a first real user would hit that the
synthetic test bed could not show.

### Client DNS is redirected, not merely permitted

Every DNS query leaving the client subnet is rewritten in the kernel to the
resolver in `dns:`, over the tunnels, whatever the container is configured to
use. Blocking port 53 to the LAN only stopped the obvious leak: a container
pointed at a public resolver still leaked every hostname it looked up, with its
traffic correctly tunnelled and nothing looking wrong. Asking users to set DNS
in every container turns a security property into a per-container setting that
one forgotten compose file undoes.

`dns:` is now required. `status` gained `"dns_forced"` and reports its absence.

### The kill switch repairs itself

It was armed once at startup and never checked again. A firewalld or ufw
reload, an `iptables-restore`, or another tool's cleanup drops the rules, and
the failure direction is open - which is precisely the case where nobody is
reading `status`. The daemon now checks the kill switch, the client NAT and the
DNS redirect on every pass and reinstalls whatever has gone missing.

### Provider configs are checked before anything starts

A WireGuard file as downloaded is not safe to hand to this gateway, and both
problems are silent:

- no `Table = off` - wg-quick installs its own default route and fwmark rules
  and fights the gateway for the routing table on every restart. The symptom is
  traffic taking the wrong tunnel, not an error.
- a `DNS =` line - wg-quick rewrites the *host's* resolver as a side effect of
  starting a tunnel, and fails outright when resolvconf is not installed.

Both are now refused at startup with the exact edit to make, and reported by
`status`. A missing `PersistentKeepalive` warns rather than refuses: WireGuard
only rekeys when there is traffic, so an idle tunnel stops handshaking and gets
dropped from the bond while being perfectly healthy.

### Also

- `status` reports the running `"version"`.
- `stop` removes the DNS redirect alongside the kill switch and the NAT rule.
- CI builds, vets and tests on every push.

## 1.0.1

Client traffic left down the correct tunnel wearing the container's own private
source address, and WireGuard's cryptokey routing at the far end discards
anything that is not the address the provider issued. The tunnels handshook,
the routing was correct, the counters moved, `status` reported no problems, and
every request died at the far end. BondVPN now installs the translation itself,
re-asserts it, removes it on `stop`, and reports `"nat"`.

## 1.0.0

First release. Multi-tunnel WireGuard routing with per-client pinning or
bonding, a fail-closed kill switch armed before the tunnels come up, a status
and health API, and `leak-test` to prove nothing escapes.
