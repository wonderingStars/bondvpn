package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Routing tables and rule priorities.
//
// THE PRIORITY ORDER IS LOAD-BEARING. Destination exceptions must sit ABOVE the
// per-client pins. With them below, every container's replies to the LAN get
// policy-routed into a tunnel and every web UI on the box becomes unreachable -
// which looks like the applications being broken rather than the routing.
const (
	BondTable    = 51820
	PinTableBase = 51821 // 51821..51825, one per tunnel

	PrioSelf = 90  // to the container subnet    -> main
	PrioLAN  = 91  // to the LAN                 -> main
	PrioPin  = 95  // from a pinned client       -> its own tunnel
	PrioBond = 100 // from any client            -> the bond
)

// Plan is the routing that should exist, given the tunnels that are live.
type Plan struct {
	Live     []*Tunnel
	Bond     []string          // interfaces carrying the ECMP default route
	Pins     map[string]string // client IP -> interface
	PinTable map[string]int    // client IP -> table number
}

// BuildPlan decides what routing should look like right now.
//
// Pinned clients are assigned to live tunnels BY POSITION and wrap around, so
// losing a tunnel re-packs its clients onto the survivors rather than stranding
// them. With three tunnels and four pinned clients the fourth shares tunnel one;
// with one tunnel left, everyone shares it. That is deliberately better than
// leaving a client with no route at all.
func BuildPlan(cfg *Config, tunnels []*Tunnel) *Plan {
	live := liveTunnels(tunnels, cfg.StaleAfter)
	p := &Plan{
		Live:     live,
		Pins:     map[string]string{},
		PinTable: map[string]int{},
	}
	for _, t := range live {
		p.Bond = append(p.Bond, t.Name)
	}
	if len(live) == 0 {
		return p // nothing live: the kill switch is what protects us now
	}
	for i, ip := range cfg.Pinned {
		t := live[i%len(live)]
		p.Pins[ip] = t.Name
		p.PinTable[ip] = PinTableBase + (i % len(live))
	}
	return p
}

// Apply writes the plan into the kernel.
func (p *Plan) Apply(cfg *Config) error {
	if err := p.applyBondTable(); err != nil {
		return err
	}
	if err := p.applyPinTables(); err != nil {
		return err
	}
	return p.applyRules(cfg)
}

// applyBondTable installs one default route with a nexthop per live tunnel.
func (p *Plan) applyBondTable() error {
	table := fmt.Sprint(BondTable)
	quiet(5*time.Second, "ip", "route", "flush", "table", table)
	if len(p.Bond) == 0 {
		return nil
	}
	args := []string{"route", "replace", "default", "table", table}
	for _, iface := range p.Bond {
		args = append(args, "nexthop", "dev", iface, "weight", "1")
	}
	_, err := run(10*time.Second, "ip", args...)
	return err
}

// applyPinTables gives each live tunnel its own table with a single default.
func (p *Plan) applyPinTables() error {
	for i := 0; i < MaxTunnels; i++ {
		table := fmt.Sprint(PinTableBase + i)
		quiet(5*time.Second, "ip", "route", "flush", "table", table)
		if i < len(p.Live) {
			if _, err := run(10*time.Second, "ip", "route", "replace", "default",
				"dev", p.Live[i].Name, "table", table); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyRules rebuilds the policy rules from scratch, lowest priority first.
func (p *Plan) applyRules(cfg *Config) error {
	p.flushOurRules()

	// Destination exceptions FIRST, so replies to the container subnet and the
	// LAN keep using the main table. See the note on the constants above.
	if _, err := run(5*time.Second, "ip", "rule", "add", "to", cfg.Clients,
		"lookup", "main", "priority", fmt.Sprint(PrioSelf)); err != nil {
		return err
	}
	if _, err := run(5*time.Second, "ip", "rule", "add", "to", cfg.LAN,
		"lookup", "main", "priority", fmt.Sprint(PrioLAN)); err != nil {
		return err
	}

	for ip, table := range p.PinTable {
		if _, err := run(5*time.Second, "ip", "rule", "add", "from", ip,
			"lookup", fmt.Sprint(table), "priority", fmt.Sprint(PrioPin)); err != nil {
			return err
		}
	}

	if len(p.Bond) > 0 {
		if _, err := run(5*time.Second, "ip", "rule", "add", "from", cfg.Clients,
			"lookup", fmt.Sprint(BondTable), "priority", fmt.Sprint(PrioBond)); err != nil {
			return err
		}
	}
	return nil
}

// flushOurRules removes only the rules at our own priorities, so a rebuild
// cannot accumulate duplicates and cannot disturb anyone else's policy routing.
func (p *Plan) flushOurRules() {
	for _, prio := range []int{PrioSelf, PrioLAN, PrioPin, PrioBond} {
		// Deleting by priority removes one rule per call; loop until none are
		// left, bounded so a misbehaving ip cannot spin forever.
		for i := 0; i < 32; i++ {
			if _, err := run(5*time.Second, "ip", "rule", "del",
				"priority", fmt.Sprint(prio)); err != nil {
				break
			}
		}
	}
}

// Installed reports whether the rules this plan describes are in the kernel
// right now.
//
// Policy rules are no more ours than the firewall is: another tool, a script or
// a person can flush them, and the daemon would otherwise never notice, because
// it re-applies only when its PLAN changes and a flush does not change the
// plan. The symptom would be every client falling back to the main table and
// leaving through the ISP connection - the one outcome this product exists to
// prevent.
// It checks the ROUTES as well as the rules. Deleting an interface silently
// empties any table holding a route through it, while the rule pointing at that
// table survives - so a tunnel that goes away and comes back leaves its pinned
// client with a rule to an empty table and no way out at all. The plan does not
// change, the rules all look present, and that client alone is dead.
func (p *Plan) Installed() bool {
	out, err := run(5*time.Second, "ip", "rule", "show")
	if err != nil {
		// If we cannot tell, assume everything is present. Re-applying on every
		// failed read would churn the routing table for no reason.
		return true
	}
	if countOurRules(out) != p.expectedRules() {
		return false
	}

	if len(p.Bond) > 0 {
		out, err := run(5*time.Second, "ip", "route", "show", "table", fmt.Sprint(BondTable))
		if err == nil && strings.TrimSpace(out) == "" {
			return false
		}
	}
	for ip, table := range p.PinTable {
		out, err := run(5*time.Second, "ip", "route", "show", "table", fmt.Sprint(table))
		if err != nil {
			continue
		}
		if !pinTableOK(out, p.Pins[ip]) {
			return false
		}
	}
	return true
}

// pinTableOK reports whether a pin table actually routes through the interface
// the plan says it should. Split out from Installed so it can be tested without
// a kernel.
func pinTableOK(routeOutput, iface string) bool {
	return strings.Contains(routeOutput, "dev "+iface+" ")
}

func (p *Plan) expectedRules() int {
	n := 2 // the two destination exceptions
	n += len(p.PinTable)
	if len(p.Bond) > 0 {
		n++
	}
	return n
}

// countOurRules counts the rules at the priorities this product owns. Split out
// from Installed so the parsing is testable without a kernel.
func countOurRules(ipRuleOutput string) int {
	ours := map[string]bool{
		fmt.Sprintf("%d:", PrioSelf): true,
		fmt.Sprintf("%d:", PrioLAN):  true,
		fmt.Sprintf("%d:", PrioPin):  true,
		fmt.Sprintf("%d:", PrioBond): true,
	}
	n := 0
	for _, line := range strings.Split(ipRuleOutput, "\n") {
		if f := strings.Fields(line); len(f) > 0 && ours[f[0]] {
			n++
		}
	}
	return n
}

// Describe renders the plan for logs and the status endpoint.
//
// The pins are SORTED before rendering, and that is load-bearing, not tidiness.
// This string is the change signature the run loop compares each tick
// (main.go: `sig := plan.Describe()`); ranging a Go map here produces a
// different order almost every call, so an unchanged plan looked changed on
// ~40% of ticks. Each false change re-applied the whole plan - flushing the
// bond and pin tables and re-adding every rule - and in that window client
// packets fell through to the kill switch and were reset. Measured on the
// shipped three-pin config: 3 orderings over 6000 calls, resets every ~2.4s
// forever. Sorting makes identical plans render identically.
func (p *Plan) Describe() string {
	if len(p.Live) == 0 {
		return "no live tunnels - all client traffic is blocked"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "bonded %d tunnel(s): %s", len(p.Bond), strings.Join(p.Bond, " "))
	ips := make([]string, 0, len(p.Pins))
	for ip := range p.Pins {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	for _, ip := range ips {
		fmt.Fprintf(&b, "; %s pinned to %s", ip, p.Pins[ip])
	}
	return b.String()
}

// DistinctPins reports how many different tunnels the pinned clients are spread
// across. The leak test asserts this after a restore: a restore that quietly
// collapses every client onto one tunnel is the exact failure this product
// exists to prevent, and it looks healthy because traffic still flows.
func (p *Plan) DistinctPins() int {
	seen := map[string]bool{}
	for _, iface := range p.Pins {
		seen[iface] = true
	}
	return len(seen)
}
