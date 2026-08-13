package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// The kill switch lives in DOCKER-USER, not FORWARD.
//
// Docker PREPENDS its own ACCEPT rules to FORWARD, so anything appended there
// never matches container traffic. This was measured on the prototype: with
// every tunnel down, a container still reached the internet on the host's ISP
// address while the FORWARD reject counter read exactly 0. The kill switch was
// decorative, and every test that only asserted "the rule exists" passed.
const chain = "DOCKER-USER"

// applyKillSwitch installs the fail-closed rules for the client subnet.
//
// Final order, top to bottom:
//
//	1  REJECT  clients -> LAN:53      no DNS to the LAN resolver
//	2  ACCEPT  clients established
//	3  ACCEPT  clients -> clients
//	4  ACCEPT  clients -> LAN
//	5  ACCEPT  clients out via wg+    the only way out
//	6  REJECT  clients -> anywhere    catch-all
//
// "REJECT" there means whatever rejectArgs() settles on for this machine.

// blockAction is how traffic is refused: REJECT with an ICMP error where the
// firewall supports it, DROP where it does not.
//
// Found on a Synology NAS. DSM 7.3 ships iptables 1.8.3 with no --reject-with,
// so arming the kill switch failed outright and the daemon exited before
// creating a single tunnel - on a box that was otherwise perfectly capable of
// running this. DROP is the same protection with worse manners: clients time
// out instead of being told immediately.
//
// Probed once, by trying it in a scratch chain rather than parsing a version
// string. Builds vary in what they include far more than version numbers
// suggest.
var blockAction = struct {
	once bool
	args []string
}{}

func rejectArgs() []string {
	if blockAction.once {
		return blockAction.args
	}
	blockAction.once = true
	blockAction.args = []string{"-j", "REJECT", "--reject-with", "icmp-port-unreachable"}

	const probe = "BONDVPN-PROBE"
	quiet(5*time.Second, "iptables", "-F", probe)
	quiet(5*time.Second, "iptables", "-X", probe)
	if _, err := run(5*time.Second, "iptables", "-N", probe); err != nil {
		return blockAction.args // cannot tell; assume the better behaviour
	}
	_, err := run(5*time.Second, "iptables", append([]string{"-A", probe},
		blockAction.args...)...)
	if err != nil {
		blockAction.args = []string{"-j", "DROP"}
	}
	quiet(5*time.Second, "iptables", "-F", probe)
	quiet(5*time.Second, "iptables", "-X", probe)
	return blockAction.args
}

func applyKillSwitch(cfg *Config) error {
	if err := ensureChain(); err != nil {
		return err
	}
	purgeOurRules(cfg.Clients)
	block := rejectArgs()

	// Inserted in reverse, each at position 1, so the list ends up in the order
	// documented above. Every allow lands above the catch-all, which is what
	// makes this fail closed rather than open.
	steps := [][]string{
		// The catch-all goes in first so it ends up last.
		append([]string{"-I", chain, "1", "-s", cfg.Clients}, block...),

		// MUST be qualified by out-interface. Unqualified this accepts
		// everything and the catch-all below never fires - and `iptables -L -n`
		// hides interface columns, so the broken version looks identical to the
		// correct one unless you pass -v.
		{"-I", chain, "1", "-s", cfg.Clients, "-o", "wg+", "-j", "ACCEPT"},

		{"-I", chain, "1", "-s", cfg.Clients, "-d", cfg.LAN, "-j", "ACCEPT"},
		{"-I", chain, "1", "-s", cfg.Clients, "-d", cfg.Clients, "-j", "ACCEPT"},
		{"-I", chain, "1", "-s", cfg.Clients, "-m", "conntrack",
			"--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	}
	for _, args := range steps {
		if _, err := run(10*time.Second, "iptables", args...); err != nil {
			return err
		}
	}

	// No DNS to the LAN resolver, above everything else.
	//
	// Containers need the LAN for local services, and that exception also opens
	// port 53 on the router - so an app pointed at a LAN resolver leaks every
	// hostname it looks up while the traffic itself stays correctly tunnelled
	// and nothing looks wrong. Blocking it here makes "DNS goes down the
	// tunnel" structural rather than a setting someone can undo.
	for _, proto := range []string{"udp", "tcp"} {
		dnsBlock := append([]string{"-I", chain, "1", "-s", cfg.Clients,
			"-d", cfg.LAN, "-p", proto, "--dport", "53"}, block...)
		if _, err := run(10*time.Second, "iptables", dnsBlock...); err != nil {
			return err
		}
	}

	return applyKillSwitch6()
}

// applyKillSwitch6 rejects ALL forwarded IPv6. The tunnels are IPv4-only, so
// any IPv6 path is by definition outside the VPN. No exceptions, no interface
// qualifier.
// It falls back the same way the IPv4 side does, and then further: some builds
// have no REJECT target for IPv6 at all. DSM's ip6tables 1.8.3 cannot load it -
// "Couldn't load target `REJECT'" - so DROP is tried next.
//
// If neither works, whether that matters depends on the machine. A box with
// IPv6 forwarding switched off cannot route IPv6 between interfaces however the
// firewall is configured, so there is nothing to leak and refusing to start
// would be theatre. A box that IS forwarding IPv6 and cannot block it is a
// genuine hole, and that stays fatal.
func applyKillSwitch6() error {
	if _, err := run(10*time.Second, "ip6tables", "-n", "-L", chain); err != nil {
		quiet(10*time.Second, "ip6tables", "-N", chain)
	}

	armed := false
	for _, action := range [][]string{{"-j", "REJECT"}, {"-j", "DROP"}} {
		if _, err := run(10*time.Second, "ip6tables",
			append([]string{"-C", chain}, action...)...); err == nil {
			armed = true
			break
		}
		if _, err := run(10*time.Second, "ip6tables",
			append([]string{"-A", chain}, action...)...); err == nil {
			armed = true
			break
		}
	}
	if !armed {
		if ipv6Forwarding() {
			return fmt.Errorf("ip6tables can neither REJECT nor DROP, and this " +
				"machine forwards IPv6 - client traffic could leave over IPv6 " +
				"outside the tunnels")
		}
		// Nothing to block: the kernel will not forward IPv6 regardless.
		return nil
	}

	if _, err := run(10*time.Second, "ip6tables", "-C", "FORWARD", "-j", chain); err != nil {
		quiet(10*time.Second, "ip6tables", "-I", "FORWARD", "1", "-j", chain)
	}
	return nil
}

// ipv6Forwarding reports whether this kernel will route IPv6 between interfaces
// at all. When it will not, an unarmed IPv6 kill switch is not a leak.
func ipv6Forwarding() bool {
	b, err := os.ReadFile("/proc/sys/net/ipv6/conf/all/forwarding")
	if err != nil {
		// Cannot tell - assume the worse case, which is that it forwards.
		return true
	}
	return strings.TrimSpace(string(b)) == "1"
}

// ensureChain creates DOCKER-USER when Docker has not yet. The kill switch has
// to work even if the daemon starts later, otherwise there is a window at boot
// with no protection at all.
func ensureChain() error {
	if _, err := run(10*time.Second, "iptables", "-n", "-L", chain); err != nil {
		if _, err := run(10*time.Second, "iptables", "-N", chain); err != nil {
			return err
		}
	}
	if _, err := run(10*time.Second, "iptables", "-C", "FORWARD", "-j", chain); err != nil {
		if _, err := run(10*time.Second, "iptables", "-I", "FORWARD", "1", "-j", chain); err != nil {
			return err
		}
	}
	return nil
}

// purgeOurRules deletes every rule in the chain mentioning our client subnet
// before we re-insert.
//
// Insert-only was a real bug: the rules are written with the CURRENT LAN, so
// when the LAN changed the old exception became an orphan nothing could remove,
// and every restart stacked another set on top. It opened no hole because it
// sat below the catch-all, but an unauditable kill switch is one nobody can
// trust.
func purgeOurRules(clients string) {
	for i := 0; i < 64; i++ {
		out, err := run(10*time.Second, "iptables", "-L", chain, "--line-numbers", "-n")
		if err != nil {
			return
		}
		num := ""
		for _, line := range strings.Split(out, "\n") {
			if !strings.Contains(line, clients) {
				continue
			}
			if f := strings.Fields(line); len(f) > 0 {
				num = f[0]
			}
			break
		}
		if num == "" {
			return
		}
		if _, err := run(10*time.Second, "iptables", "-D", chain, num); err != nil {
			return
		}
	}
}

// killSwitchArmed reports whether the catch-all is present, which is the only
// rule whose absence means traffic can escape.
//
// It has to look for the SAME form applyKillSwitch installed. Checking only for
// REJECT on a machine that had to fall back to DROP reports "not armed" forever:
// status shows a permanent problem, and repairFirewall re-arms an already-armed
// kill switch every fifteen seconds for the life of the daemon.
func killSwitchArmed(clients string) bool {
	args := append([]string{"-C", chain, "-s", clients}, rejectArgs()...)
	_, err := run(10*time.Second, "iptables", args...)
	return err == nil
}

// removeKillSwitch tears the rules down. Only for `bondvpn stop`; nothing
// in the normal path should ever call this.
func removeKillSwitch(cfg *Config) {
	purgeOurRules(cfg.Clients)
	// Both forms, since we do not know which one this machine could install.
	quiet(10*time.Second, "ip6tables", "-D", chain, "-j", "REJECT")
	quiet(10*time.Second, "ip6tables", "-D", chain, "-j", "DROP")
	fmt.Println("kill switch removed - client traffic is no longer restricted")
}
