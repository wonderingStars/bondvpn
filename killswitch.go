package main

import (
	"fmt"
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
func applyKillSwitch(cfg *Config) error {
	if err := ensureChain(); err != nil {
		return err
	}
	purgeOurRules(cfg.Clients)

	// Inserted in reverse, each at position 1, so the list ends up in the order
	// documented above. Every allow lands above the catch-all, which is what
	// makes this fail closed rather than open.
	steps := [][]string{
		// The catch-all goes in first so it ends up last.
		{"-I", chain, "1", "-s", cfg.Clients, "-j", "REJECT",
			"--reject-with", "icmp-port-unreachable"},

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
		if _, err := run(10*time.Second, "iptables", "-I", chain, "1",
			"-s", cfg.Clients, "-d", cfg.LAN, "-p", proto, "--dport", "53",
			"-j", "REJECT", "--reject-with", "icmp-port-unreachable"); err != nil {
			return err
		}
	}

	return applyKillSwitch6()
}

// applyKillSwitch6 rejects ALL forwarded IPv6. The tunnels are IPv4-only, so
// any IPv6 path is by definition outside the VPN. No exceptions, no interface
// qualifier.
func applyKillSwitch6() error {
	if _, err := run(10*time.Second, "ip6tables", "-n", "-L", chain); err != nil {
		quiet(10*time.Second, "ip6tables", "-N", chain)
	}
	if _, err := run(10*time.Second, "ip6tables", "-C", chain, "-j", "REJECT"); err != nil {
		if _, err := run(10*time.Second, "ip6tables", "-A", chain, "-j", "REJECT"); err != nil {
			return err
		}
	}
	if _, err := run(10*time.Second, "ip6tables", "-C", "FORWARD", "-j", chain); err != nil {
		quiet(10*time.Second, "ip6tables", "-I", "FORWARD", "1", "-j", chain)
	}
	return nil
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

// killSwitchArmed reports whether the catch-all reject is present, which is the
// only rule whose absence means traffic can escape.
func killSwitchArmed(clients string) bool {
	_, err := run(10*time.Second, "iptables", "-C", chain, "-s", clients,
		"-j", "REJECT", "--reject-with", "icmp-port-unreachable")
	return err == nil
}

// removeKillSwitch tears the rules down. Only for `bondvpn stop`; nothing
// in the normal path should ever call this.
func removeKillSwitch(cfg *Config) {
	purgeOurRules(cfg.Clients)
	quiet(10*time.Second, "ip6tables", "-D", chain, "-j", "REJECT")
	fmt.Println("kill switch removed - client traffic is no longer restricted")
}
