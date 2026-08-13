package main

import (
	"strings"
	"testing"
	"time"
)

func tunnelSet(names ...string) []*Tunnel {
	out := make([]*Tunnel, 0, len(names))
	for _, n := range names {
		out = append(out, &Tunnel{Name: n, ConfigPath: "/etc/wireguard/" + n + ".conf",
			Up: true, HandshakeAge: 10})
	}
	return out
}

func testConfig(pinned ...string) *Config {
	return &Config{
		Clients:    "172.20.0.0/24",
		Default:    ModeBond,
		Pinned:     pinned,
		LAN:        "192.168.1.0/24",
		StaleAfter: 180 * time.Second,
	}
}

func TestBuildPlanSpreadsPins(t *testing.T) {
	cfg := testConfig("172.20.0.10", "172.20.0.11", "172.20.0.12")
	plan := BuildPlan(cfg, tunnelSet("wg0", "wg1", "wg2"))

	if len(plan.Bond) != 3 {
		t.Fatalf("bond has %d tunnels, want 3", len(plan.Bond))
	}
	if got := plan.DistinctPins(); got != 3 {
		t.Fatalf("pins landed on %d tunnels, want 3 - this is the collapse the "+
			"whole product exists to prevent", got)
	}
	if plan.PinTable["172.20.0.10"] != PinTableBase {
		t.Errorf("first pin got table %d, want %d",
			plan.PinTable["172.20.0.10"], PinTableBase)
	}
	if plan.PinTable["172.20.0.12"] != PinTableBase+2 {
		t.Errorf("third pin got table %d, want %d",
			plan.PinTable["172.20.0.12"], PinTableBase+2)
	}
}

// More clients than tunnels must share, never go unrouted. An unrouted client
// is a client with no VPN.
func TestBuildPlanWrapsWhenClientsOutnumberTunnels(t *testing.T) {
	cfg := testConfig("172.20.0.10", "172.20.0.11", "172.20.0.12", "172.20.0.13")
	plan := BuildPlan(cfg, tunnelSet("wg0", "wg1", "wg2"))

	if len(plan.Pins) != 4 {
		t.Fatalf("%d clients routed, want 4 - one was left with no tunnel", len(plan.Pins))
	}
	if plan.Pins["172.20.0.13"] != "wg0" {
		t.Errorf("the fourth client landed on %q, want it wrapped onto wg0",
			plan.Pins["172.20.0.13"])
	}
}

// Losing a tunnel must re-pack its clients onto the survivors.
func TestBuildPlanRepacksAfterTunnelLoss(t *testing.T) {
	cfg := testConfig("172.20.0.10", "172.20.0.11", "172.20.0.12")
	tunnels := tunnelSet("wg0", "wg1", "wg2")
	tunnels[1].HandshakeAge = 9000 // wg1 has gone quiet

	plan := BuildPlan(cfg, tunnels)
	if len(plan.Bond) != 2 {
		t.Fatalf("bond has %d tunnels, want 2 - a stale tunnel is still carrying traffic",
			len(plan.Bond))
	}
	for ip, iface := range plan.Pins {
		if iface == "wg1" {
			t.Errorf("%s is still pinned to the dead tunnel wg1", ip)
		}
	}
	if len(plan.Pins) != 3 {
		t.Errorf("%d clients routed after the loss, want 3", len(plan.Pins))
	}
}

// An interface that is up but has not handshaken will blackhole traffic. Time
// since handshake is the only honest liveness signal.
func TestStaleTunnelIsNotLive(t *testing.T) {
	stale := &Tunnel{Name: "wg0", Up: true, HandshakeAge: 400}
	if stale.Live(180 * time.Second) {
		t.Error("a tunnel silent for 400s must not be treated as live")
	}
	never := &Tunnel{Name: "wg1", Up: true, HandshakeAge: -1}
	if never.Live(180 * time.Second) {
		t.Error("a tunnel that has never handshaken must not be treated as live")
	}
	down := &Tunnel{Name: "wg2", Up: false, HandshakeAge: 5}
	if down.Live(180 * time.Second) {
		t.Error("an interface that does not exist must not be treated as live")
	}
	fresh := &Tunnel{Name: "wg3", Up: true, HandshakeAge: 30}
	if !fresh.Live(180 * time.Second) {
		t.Error("a recently handshaken tunnel must be live")
	}
}

func TestNoLiveTunnelsMeansNoRoutes(t *testing.T) {
	cfg := testConfig("172.20.0.10")
	tunnels := tunnelSet("wg0", "wg1")
	for _, tn := range tunnels {
		tn.Up = false
	}
	plan := BuildPlan(cfg, tunnels)

	if len(plan.Bond) != 0 || len(plan.Pins) != 0 {
		t.Fatal("with nothing live there must be no routes at all - the kill " +
			"switch is what protects clients here")
	}
	if !strings.Contains(plan.Describe(), "blocked") {
		t.Errorf("Describe should say traffic is blocked, got %q", plan.Describe())
	}
}

// The status document has to name the silent failures, not just the loud ones.
func TestStatusReportsCollapsedPins(t *testing.T) {
	cfg := testConfig("172.20.0.10", "172.20.0.11")
	tunnels := tunnelSet("wg0", "wg1")
	plan := &Plan{
		Live: tunnels,
		Bond: []string{"wg0", "wg1"},
		Pins: map[string]string{"172.20.0.10": "wg0", "172.20.0.11": "wg0"},
	}

	st := buildStatus(cfg, tunnels, plan)
	if !hasProblem(st, "same tunnel") {
		t.Errorf("collapsed pins were not reported as a problem: %v", st.Problems)
	}
}

// Missing NAT is the one failure with no other symptom: the tunnels handshake,
// the routing is correct, the counters move, and every request dies at the far
// end. Found on a live test bed, where a pinned client got no answer through a
// tunnel that looked perfect.
func TestStatusReportsMissingNAT(t *testing.T) {
	cfg := testConfig("172.20.0.10")
	tunnels := tunnelSet("wg0")

	st := buildStatus(cfg, tunnels, BuildPlan(cfg, tunnels))
	if st.NAT {
		t.Skip("a masquerade rule for this subnet already exists on this host")
	}
	if !hasProblem(st, "not being translated onto the tunnels") {
		t.Errorf("missing client NAT was not reported: %v", st.Problems)
	}
}

func TestStatusReportsDeadTunnel(t *testing.T) {
	cfg := testConfig("172.20.0.10")
	tunnels := tunnelSet("wg0", "wg1")
	tunnels[1].Up = false

	st := buildStatus(cfg, tunnels, BuildPlan(cfg, tunnels))
	if !hasProblem(st, "wg1 is down") {
		t.Errorf("a down tunnel was not reported: %v", st.Problems)
	}
	if st.Bonded != 1 {
		t.Errorf("bonded = %d, want 1", st.Bonded)
	}
}

// A flush of the policy rules does not change the plan, so the daemon has to
// compare against the kernel rather than against its own intentions.
func TestInstalledRuleCounting(t *testing.T) {
	cfg := testConfig("172.20.0.10", "172.20.0.11")
	plan := BuildPlan(cfg, tunnelSet("wg0", "wg1"))

	// self + LAN + two pins + the bond
	if got := plan.expectedRules(); got != 5 {
		t.Fatalf("expected 5 rules for this plan, got %d", got)
	}

	full := `0:	from all lookup local
90:	from all to 172.20.0.0/24 lookup main
91:	from all to 192.168.1.0/24 lookup main
95:	from 172.20.0.10 lookup 51821
95:	from 172.20.0.11 lookup 51822
100:	from 172.20.0.0/24 lookup 51820
32766:	from all lookup main`
	if got := countOurRules(full); got != 5 {
		t.Errorf("counted %d of our rules in a complete table, want 5", got)
	}

	flushed := `0:	from all lookup local
32766:	from all lookup main
32767:	from all lookup default`
	if got := countOurRules(flushed); got != 0 {
		t.Errorf("counted %d of our rules in a flushed table, want 0", got)
	}

	// Someone else's policy routing at a priority we do not own must not be
	// counted as ours, or a partial flush would look complete.
	foreign := `0:	from all lookup local
90:	from all to 172.20.0.0/24 lookup main
200:	from 10.0.0.5 lookup 99
32766:	from all lookup main`
	if got := countOurRules(foreign); got != 1 {
		t.Errorf("counted %d, want 1 - another tool's rules are not ours", got)
	}
}

// Deleting an interface empties the table that routed through it while the rule
// pointing at that table survives. Counting rules alone would call that healthy
// and leave one pinned client with no way out - which is exactly what happened
// on the test bed when a tunnel was killed and recovered.
func TestPinTableMustRouteThroughItsTunnel(t *testing.T) {
	if !pinTableOK("default dev wg1 scope link \n", "wg1") {
		t.Error("a table with a default through wg1 must be accepted")
	}
	if pinTableOK("", "wg1") {
		t.Error("an EMPTY table must not count as installed - this is the bug")
	}
	if pinTableOK("default dev wg2 scope link \n", "wg1") {
		t.Error("a table pointing at the wrong tunnel must not be accepted")
	}
}

func hasProblem(st *Status, substr string) bool {
	for _, p := range st.Problems {
		if strings.Contains(p, substr) {
			return true
		}
	}
	return false
}
