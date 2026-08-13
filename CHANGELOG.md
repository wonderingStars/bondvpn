# Changelog

## 1.8.0

### Add a tunnel from the browser

The dashboard now has a settings panel: drop your provider's `.conf` file on it
and the tunnel is live within about fifteen seconds. No restart, no editing a
config file, no reading a setup guide first. This is the difference between a
gateway an engineer configures and an appliance somebody uses, and it is what
the NAS build needed to be worth installing.

**`tunnel_dir`** is the new setting behind it: point it at a directory and every
`*.conf` in it is a tunnel, re-read on every pass. Listing tunnels explicitly
still works and still takes precedence, so nothing about an existing install
changes. With no `tunnel_dir`, the settings panel says the tunnels are managed
in the config file rather than offering a button that cannot work.

**Uploads are normalised on the way in**, which is the whole reason "just drop
the file in" can be true. A config downloaded from Mullvad, IVPN, Proton or
AirVPN needs the same two edits every time, and both failures are silent:

- the provider's `DNS =` line is removed, because wg-quick applies it to the
  **whole machine** - on a NAS that breaks name resolution for every other
  service on the box. DNS is forced per-client instead.
- routing is disabled for the interface, without which wg-quick installs its own
  default route, fights the policy routing, and the tunnel comes up, handshakes,
  shows traffic counters and carries nothing.

`PersistentKeepalive` is added when absent, so an idle tunnel is not mistaken
for a dead one. The original file is never modified.

### Security

Writing requires a token. Always, with no config setting to turn it off.

Reading `/status` has always been open, and that is a considered trade: it
exposes no secrets. Writing is different. An unauthenticated upload endpoint on
a daemon running as root would let anyone who can reach the port add a tunnel
pointing at a server they control and take every packet the gateway carries.
The token is generated on first start and written to `admin-token` beside the
config, mode 0600.

Also, by construction:

- **Config contents are never served back.** The listing returns names and
  endpoints only. A settings page that let you view an uploaded file would hand
  the private key of every tunnel to anyone who guessed the token once.
- **Uploaded filenames are sanitised, not trusted.** A filename becomes a path
  under the tunnel directory on a process running as root; `../../etc/cron.d/x`
  is reduced to its base name. `DELETE` will only remove a file the daemon
  manages.
- Uploads are capped at 2 MB, validated as WireGuard configs before anything
  touches the disk, and written to a temporary file and renamed, so the run loop
  can never find a half-written config and try to bring it up.

### Also

Names are checked against the 15-character limit Linux puts on interface names,
with the limit in the message. Mullvad's own download is called something like
`mullvad-gb-lon-wg-001.conf`, which is over it - previously that config looked
perfectly good and silently failed to come up.

## 1.7.0

### It runs on a NAS

Everything in this release was found by running BondVPN on a Synology DS1019+
(DSM 7.3, kernel 4.4.302) and fixing whatever it hit. That box has no kernel
WireGuard, no multipath routing, an iptables without `--reject-with`, an
ip6tables that cannot load REJECT at all, and a noexec `/tmp`. It now works
there, within the limits the kernel imposes.

**WireGuard in userspace where the kernel has none.** The backend is detected at
startup by creating a probe interface, and falls back to `wireguard-go` over
`/dev/net/tun`. Slower than the kernel, and it is the difference between working
and not working on hardware nobody can recompile. `status` reports which backend
is in use.

**Bonding degrades honestly on a kernel without multipath.** ECMP is a kernel
feature; where `CONFIG_IP_ROUTE_MULTIPATH` is absent there is no userspace
substitute for it. A single tunnel now uses the plain `dev` route form rather
than a one-entry multipath route, which the old code emitted and that kernel
refused with EINVAL - so routing failed entirely and every client lost its
route. Where several tunnels are configured and multipath is refused, they all
take the first live tunnel instead of none, and `status` says so. **Pinning is
unaffected, and on a NAS pinning is the whole of what is available.**

**The firewall falls back instead of refusing to start.** Where `--reject-with`
is unsupported the kill switch uses DROP, and where ip6tables has no REJECT
target it tries DROP next. If neither can be installed, that is fatal only on a
machine that actually forwards IPv6 - on one that does not, there is nothing to
leak and refusing to start would be theatre.

### Fixed

- **The daemon re-armed the kill switch every fifteen seconds, forever,** on any
  machine that had to fall back to DROP. Both the armed-check and the IPv6 check
  looked for REJECT specifically, so they never saw the rule that was actually
  installed - and `status` carried an IPv6 problem no configuration could clear.
- **`stop` left the IPv6 rule behind** for the same reason: it only deleted the
  REJECT form.
- A tunnel that had never handshaken was logged as "has not handshaken in -1s"
  when cycled. `status` already said this properly; the recovery log did not.
- The advice to run `sysctl -w net.ipv4.fib_multipath_hash_policy=1` was
  printed on kernels where that file cannot exist. It now explains that the
  kernel has no multipath routing, which is a different problem with a different
  answer.

## 1.6.0

### Open source, AGPLv3

The source is published. Run it, read it, change it, share it, at home or at
work, at no charge. The obligation that matters is section 13: modify it, let
other people use the modified version over a network, and those users can ask
for your source. Running it unmodified, or modifying it for yourself, asks
nothing.

AGPL rather than MIT because it is the licence that keeps improvements coming
back without giving away the commercial position. A company building a paid
service on a modified copy either publishes their work or takes a commercial
licence, and almost all of them take the licence. See COMMERCIAL.md, which
spells out when a paid licence is NOT needed - which is most of the time.

CONTRIBUTING.md carries the condition dual licensing depends on: contributors
license their work for use under other terms while keeping their own copyright.

### The licence heartbeat is gone

It withdrew client routing and exited after 24 hours without a valid signed
status. That is indefensible in software people can compile themselves: the
first fork would be the one with those lines deleted, and it would deserve to
become the version everybody runs.

The same hourly request now reports whether a newer release exists, and nothing
more. It cannot stop, exit or unroute anything - a test fails if `update.go`
ever references `flushOurRules`, `os.Exit` or `Shutdown` - and `update_check:
false` turns it off entirely. `status` gained `update_available` and
`latest_version`; `licensed` is gone.

Version comparison is numeric per field, because string comparison puts 1.10.0
before 1.9.0 and would tell everyone on the newest build they were out of date.

## 1.5.2

A production-readiness review (32 reviewers, every finding adversarially
verified) surfaced defects across the daemon, the dashboard and the quickstart
container. The material ones are fixed here.

DAEMON
- **Pinned-route signature is now deterministic.** `Plan.Describe` rendered pins
  in Go map order, so the run loop saw an unchanged plan as changed on ~40% of
  ticks and re-applied the whole routing plan - flushing the tables and, in that
  window, resetting client connections, every ~2.4s forever on a three-pin
  setup. The pins are sorted before rendering.
- **Data race on the status document closed.** `current *Status` was written by
  the run loop and read by HTTP handler goroutines with no synchronisation; it
  now goes through a mutex. Verified under `go test -race`.
- **Warnings no longer fail health.** A missing `PersistentKeepalive` and an
  unset `fib_multipath_hash_policy` were folded into `problems`, so `/healthz`
  returned 503 forever on Mullvad's own default config and on any unprivileged
  container. `/status` now carries a separate `warnings` array; `/healthz` and
  `bondvpn status` gate on `problems` alone.
- A never-handshaken tunnel reported "has not handshaken in -1s"; it now says
  "has never completed a handshake".

DASHBOARD
- **Throughput is correct.** Rates were bytes-delta over wall-clock between 5s
  polls, but the daemon only refreshes the document every 15s, so the headline
  read 0 twice then ~3x once and the bars strobed. Rates are now delta over the
  server's own `generated` clock - accurate and immune to viewer clock skew.
- **Staleness is skew-proof.** "Stale" is now "generated has not advanced in N
  seconds of real time", not "generated vs the viewer's clock", which wrongly
  marked a healthy gateway frozen whenever the two clocks disagreed.
- Every panel dims when the feed is stale or unreachable - previously the
  protection and problems panels stayed bright, asserting "kill switch armed"
  over a frozen feed. A hung poll now aborts after 8s instead of leaving a
  stale-but-green screen for the browser's multi-minute TCP timeout.

QUICKSTART CONTAINER
- **Fails closed now.** `bondnet` had Docker's default masquerade on, so the
  client subnet had a working path out the host WAN whenever the kill switch was
  not armed - the seconds after a host reboot, before the daemon starts. This
  falsified the "nothing leaks" promise. Masquerade is disabled (bondvpn keeps
  its own NAT onto the tunnels), and the clients now wait for the gateway to be
  HEALTHY, not merely started.
- Each client gets its own `TORRENTING_PORT` (6881/6882/6883); without it the
  linuxserver image defaulted all three to 6881, so two of the three peer-port
  mappings hit no listener.
- `DNS` is configurable (`VPN_DNS`), so non-Mullvad providers work; it was
  hardcoded to Mullvad's 10.64.0.1, which is unrouted on other providers'
  tunnels and silently broke all name resolution.
- The healthcheck reads the port from the config instead of hardcoding 8099, so
  a changed `listen` no longer reports a working gateway as unhealthy.

DOCS AND CI
- Each release now attaches the install helpers (`config.example.yml`,
  `bondvpn.service`, `install.sh`), which the setup guide told users to download
  but were never uploaded; `install.sh` ships executable; the checksum step uses
  `--ignore-missing` so a single-arch download verifies cleanly.
- The `/status` sample in the README was three releases stale (1.2.0, missing
  fields); it now matches the shipped shape.
- The image workflow reads the tag/input through the environment and validates
  the version, closing a script-injection path into a `packages: write` job.

## 1.5.1

`bondvpn check` - validate the configuration and the machine, change nothing.

Getting a first install running was harder than it needed to be, for two
reasons that this fixes.

Errors arrived ONE AT A TIME, in an order that was not severity. `dns is
required` fired before the missing tunnels and before a clients subnet that
would have locked the operator out of their own box, hiding both. A config with
four mistakes took four edit-and-run cycles. The parser now reads the whole file
and collects every complaint, syntax and meaning together, so it is one edit.

And `status` was the wrong tool for the job it was being recommended for. It
reports on a gateway that is already running, so a mistyped path in `tunnels:`
showed up as "wg0 is down" - which reads as a VPN fault rather than as a typo.

`check` reads the config, every WireGuard file it names, the interfaces, the
listen address and the disks, and prints all of it at once. It touches nothing:
no rules, no routes, no interfaces brought up, so it is safe on a box already
carrying traffic, including one whose routing another tool owns. It exits 1 on
problems and 0 on warnings alone, which makes it usable in a provisioning
script.

Warnings are advice rather than faults: an unreadable disk or a missing
PersistentKeepalive still lets the gateway route, and the summary says "safe to
start". Two of them are worth the ink - `listen` on 0.0.0.0 publishes the API
and the interface to every network the machine is on with no login, and a
missing ip6tables means the IPv6 half of the kill switch cannot be armed.

Also new: a prebuilt container, `ghcr.io/wonderingstars/bondvpn`. It needs three
things from you - your provider's WireGuard files, where downloads land, and
where the finished media lives - and generates its own config around whatever
you mounted, so there is no config file to write for a standard install. A
config you mount yourself is used untouched. `docker-compose.quickstart.yml` and
`.env.example` in the repository are the whole of the setup.

## 1.5.0

BondVPN now ships an interface. Until this release it served JSON and nothing
else, and the screenshot in this README was the author's own separate dashboard
- which made the picture an advert for something you did not get.

`bondvpn run` serves one page at the root of `listen`, alongside the `/status`
and `/healthz` it already had. It shows the hero state (protected, degraded,
blocked or exposed), every tunnel with its handshake age, endpoint, pinned
clients and live throughput, the per-client routing table, every protection the
daemon maintains, the problem list, and a machine panel with CPU, memory and any
filesystems named under the new `disks:` setting.

Three decisions worth knowing about:

- **It is one file, compiled into the binary, and fetches nothing external.** A
  gateway whose kill switch is doing its job has no route out, so a page that
  pulled a font or a script from a CDN would break precisely when you opened it
  to find out what broke. A test fails if an absolute URL ever appears in it.
- **It shows only what `/status` carries.** No panel is fed by anything the API
  cannot answer for, which is how a dashboard avoids quietly inventing numbers.
- **There is no login, and `listen` is why.** The page is a read-only view of one
  machine's own routing, reachable from that machine or its container network.
  Putting `listen` on a LAN address publishes it to your whole network; put a
  reverse proxy with a login in front of it if you want that.

Set `dashboard: false` to serve the API alone. The status document gained a
`host` object (name, uptime, cores, load, memory, disks); everything already in
it is unchanged, so anything built on the old shape keeps working.

## 1.4.0

The hourly licence check now goes to a host whose request count is visible,
with the repository copy behind it as the fallback. Each installation checks in
once an hour, so requests in a day over 24 estimates how many are running -
which is the number downloads cannot give you. Downloads count people who took
the software once; this counts machines still running it today.

Nothing about the request changed: no identifiers, no cookies, no per-request
storage. The count comes from request metering rather than a log, so it answers
"how many" and never "who".

The ordering is the load-bearing part. If the counting host is down, blocked or
retired, installations carry on from the copy in the repository and nobody
notices - verified by blackholing the host and watching an install check in
anyway. A counter that can take the product down is a counter that eventually
will.

Only 1.4.0 and later check in there, so the figure is a floor that climbs as
people upgrade, and anyone blocking the host is invisible to it.

## 1.3.0

### Licence heartbeat

Installs fetch a signed status file from the public repository once an hour. If
no valid "active" status has been seen for 24 hours, the daemon withdraws the
client routing and exits.

The failure direction is the entire design, because this is a fail-closed
gateway and the daemon holds the kill switch:

- **Unreachable is not revoked.** Only a full day with no valid status stops
  anything, so GitHub outages and offline boxes are survivable. The countdown is
  logged on every check, long before it bites.
- **Expiry stops traffic, never protection.** The routing is withdrawn and the
  kill switch is left armed - a revoked install goes quiet, it never leaks.
- **The request carries no identifiers.** A plain GET of a static file: no
  install ID, no address, no telemetry. Documented in the public README rather
  than left to be found with tcpdump.
- The status file is Ed25519-signed and the public key is compiled in, so
  nobody can mint their own "active" by pointing the hostname somewhere else.

`status` reports `"licensed"`, and answers from the recorded heartbeat rather
than the network so that polling it costs nothing.

`tools/signlicense` produces the status file. The private key belongs nowhere
near either repository.

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
