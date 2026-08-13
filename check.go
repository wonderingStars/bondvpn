package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
)

// cmdCheck validates an installation without touching the kernel.
//
// WHY THIS EXISTS AS A COMMAND OF ITS OWN
// `status` was the thing people were told to run before starting, and it cannot
// do this job: it reports on a gateway that is already running, so a mistyped
// path in `tunnels:` shows up there as "wg0 is down" - which reads as a VPN
// problem rather than as a typo. Meanwhile every other error surfaced one at a
// time, in an order that was not severity, so a config with three mistakes took
// three edit-and-run cycles to fix.
//
// This reports EVERYTHING it can find in one pass, and changes nothing: no
// rules, no routes, no interfaces brought up. It is safe to run on a box that
// is already serving traffic, including one whose routing another tool owns.
func cmdCheck(path string, out io.Writer) int {
	var fails, warns int

	pass := func(format string, args ...any) {
		fmt.Fprintf(out, "  ok    %s\n", fmt.Sprintf(format, args...))
	}
	fail := func(format string, args ...any) {
		fails++
		fmt.Fprintf(out, "  FAIL  %s\n", fmt.Sprintf(format, args...))
	}
	warn := func(format string, args ...any) {
		warns++
		fmt.Fprintf(out, "  warn  %s\n", fmt.Sprintf(format, args...))
	}
	section := func(s string) { fmt.Fprintf(out, "\n%s\n", s) }

	fmt.Fprintf(out, "bondvpn %s - checking %s\n", Version, path)

	// ---- the file itself ---------------------------------------------------
	section("configuration")
	cfg, err := LoadConfig(path)
	if cfg == nil {
		// A parse or read failure means there is no config to examine, so
		// everything below would be noise about a file nobody has yet written.
		fail("%v", err)
		fmt.Fprintf(out, "\n1 problem. Nothing else can be checked until the file parses.\n")
		return 1
	}
	// Syntax and meaning are reported together, and the parser now reads the
	// whole file rather than stopping at the first bad line - so a config with
	// four mistakes is four lines of output and one edit, not four runs.
	for _, p := range cfg.ParseProblems() {
		fail("%s", p)
	}
	for _, p := range cfg.Problems() {
		fail("%s", p)
	}
	if len(cfg.ParseProblems()) == 0 && len(cfg.Problems()) == 0 {
		pass("%s parsed, every setting is valid", path)
	}

	// ---- what the tunnels need ---------------------------------------------
	section("wireguard configs")
	if len(cfg.Tunnels) == 0 {
		fail("none configured")
	} else {
		fatal, wrn := checkTunnelConfigs(cfg)
		for _, f := range fatal {
			fail("%s", f)
		}
		for _, w := range wrn {
			warn("%s", w)
		}
		if len(fatal) == 0 {
			pass("%d tunnel config(s) readable, with Table = off and no DNS line", len(cfg.Tunnels))
		}
	}

	// ---- the network this box is on ----------------------------------------
	section("this machine")
	if os.Geteuid() != 0 {
		// Not fatal: everything above is a file-reading exercise. Say so
		// plainly rather than reporting a false pass on the checks that need
		// the kernel.
		warn("not running as root - the interface and address checks below are skipped")
	} else {
		resolved := *cfg
		if err := resolved.resolve(); err != nil {
			fail("%v", err)
		} else {
			pass("WAN interface %s, LAN %s", resolved.WANIface, resolved.LAN)
			mgmt, err := ifaceAddrs(resolved.WANIface)
			switch {
			case err != nil:
				fail("cannot read addresses on %s: %v", resolved.WANIface, err)
			case !validCIDR(resolved.Clients):
				// The guard can only restate the parse failure already reported
				// above, in worse words. Saying nothing beats saying
				// "invalid CIDR address" a second time with no context.
			default:
				if err := resolved.GuardAgainstSelf(mgmt); err != nil {
					fail("%v", err)
				} else {
					pass("the clients subnet does not contain this host's own address")
				}
			}
		}
	}

	// How tunnels will actually be made, which decides which tools are needed.
	// A NAS whose kernel has no WireGuard needs wireguard-go and nothing else;
	// demanding wg-quick there would fail a machine that works perfectly well.
	switch currentBackend() {
	case backendKernel:
		pass("kernel WireGuard is available")
		for _, tool := range []string{"wg", "wg-quick", "ip", "iptables"} {
			if _, err := exec.LookPath(tool); err != nil {
				fail("%s is not installed - bondvpn cannot bring tunnels up without it", tool)
			}
		}
	case backendUserspace:
		pass("no kernel WireGuard, using wireguard-go over /dev/net/tun (slower than the kernel)")
		for _, tool := range []string{"ip", "iptables"} {
			if _, err := exec.LookPath(tool); err != nil {
				fail("%s is not installed - bondvpn cannot route without it", tool)
			}
		}
	default:
		fail("this machine has no way to create a WireGuard interface: no kernel " +
			"module, and wireguard-go is not installed. Install wireguard-tools, " +
			"or wireguard-go plus the tun module.")
	}
	if _, err := exec.LookPath("ip6tables"); err != nil {
		warn("ip6tables is not installed - the IPv6 side of the kill switch cannot be armed")
	}

	// ---- where the interface will be published -----------------------------
	section("status api and interface")
	host, _, err := net.SplitHostPort(cfg.Listen)
	switch {
	case err != nil:
		fail("listen %q must be host:port: %v", cfg.Listen, err)
	case host == "" || host == "0.0.0.0" || host == "::":
		// The page and the API have no login of their own. On every address is
		// the one setting that turns a local view into a public one.
		warn("listen is %s, which publishes the status API%s to every network this "+
			"machine is on, with no login. Use 127.0.0.1 and reach it over SSH "+
			"forwarding, or put a reverse proxy with a login in front.",
			cfg.Listen, dashboardAlso(cfg))
	case isLoopback(host):
		pass("listen %s is loopback-only%s", cfg.Listen, dashboardAlso(cfg))
	default:
		warn("listen %s is not loopback - anything that can reach %s gets the status "+
			"API%s without a login", cfg.Listen, host, dashboardAlso(cfg))
	}

	// ---- the machine panel -------------------------------------------------
	if len(cfg.Disks) > 0 {
		section("disks")
		for _, d := range cfg.Disks {
			if _, free, err := diskUsage(d.Path); err != nil {
				// A warning, not a failure: a missing mount does not stop the
				// gateway routing, it just leaves a gauge saying so.
				warn("%s: %s cannot be read (%v) - the machine panel will show it as "+
					"unreadable", d.Label, d.Path, err)
			} else {
				pass("%s: %s readable, %s free", d.Label, d.Path, human(free))
			}
		}
	}

	// ---- verdict -----------------------------------------------------------
	fmt.Fprintln(out)
	switch {
	case fails > 0:
		fmt.Fprintf(out, "%s and %s. Fix the failures before starting bondvpn.\n",
			plural(fails, "problem"), plural(warns, "warning"))
		return 1
	case warns > 0:
		fmt.Fprintf(out, "No problems, %s. Safe to start.\n", plural(warns, "warning"))
		return 0
	default:
		fmt.Fprintln(out, "No problems. Safe to start.")
		return 0
	}
}

func dashboardAlso(cfg *Config) string {
	if cfg.Dashboard {
		return " and the interface"
	}
	return ""
}

func validCIDR(s string) bool {
	_, _, err := net.ParseCIDR(s)
	return err == nil
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// human formats bytes for a person reading a terminal, not for a machine.
func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return strings.TrimSuffix(
		fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp]), ".0")
}
