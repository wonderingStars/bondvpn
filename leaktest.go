package main

import (
	"fmt"
	"time"
)

// LeakTest drops every tunnel and measures whether anything still escapes.
//
// WHY THIS SHIPS AS A USER-FACING COMMAND
// The product's central claim is "nothing leaks", and the source is closed. A
// runnable proof is how someone verifies that claim without reading the code -
// so this is a feature, not a test.
//
// WHY IT IS NOT THE SAME AS CHECKING THE RULES
// Asserting that a rule exists is a much weaker claim than asserting traffic is
// blocked. The prototype's kill switch had all the right rules present while
// containers reached the internet on the host's own address, because they were
// in the wrong chain. Only dropping the tunnels and measuring catches that.
//
// It is disruptive: roughly thirty seconds with no tunnels.
type LeakTest struct {
	Probe    func(target string) bool // returns true if the target was REACHED
	Restore  func() error
	Progress func(string)
}

// Run performs the test and returns the number of leaks found.
func (lt *LeakTest) Run(cfg *Config, tunnels []*Tunnel) (int, error) {
	say := lt.Progress
	if say == nil {
		say = func(string) {}
	}

	// However this ends - a failure, a panic, an assertion - the tunnels come
	// back. A test must never leave the box running without its VPN.
	defer func() {
		say("restoring")
		if err := lt.Restore(); err != nil {
			say(fmt.Sprintf("RESTORE FAILED: %v", err))
		}
	}()

	say("dropping every tunnel")
	for _, t := range tunnels {
		bringDown(t)
	}
	time.Sleep(2 * time.Second)

	// Three different layers. A leak can hide behind any one of them working
	// while the others fail: a DNS name, a raw IP over HTTP, and raw ICMP.
	targets := []string{
		"https://ipv4.icanhazip.com",
		"http://1.1.1.1",
		"icmp:1.1.1.1",
	}
	leaks := 0
	for _, target := range targets {
		if lt.Probe(target) {
			say(fmt.Sprintf("*** LEAK *** reached %s with no tunnels up", target))
			leaks++
		} else {
			say(fmt.Sprintf("blocked   %s", target))
		}
	}
	return leaks, nil
}

// RestoreTunnels brings the tunnels back and rebuilds the routing.
//
// TWO FAILURES THIS EXISTS TO AVOID, both found the hard way:
//
//  1. Rebuilding before the tunnels have handshaken bonds NOTHING and pins
//     every client to the first tunnel - every client out of one exit, which is
//     precisely the collapse this product prevents, and it looks healthy
//     because traffic still flows.
//
//  2. One attempt is not enough. A tunnel can handshake moments after the wait
//     gives up, so the rebuild is retried until the bond and the pins are
//     actually right.
func RestoreTunnels(cfg *Config, tunnels []*Tunnel, apply func(*Plan) error) error {
	for _, t := range tunnels {
		if err := bringUp(t); err != nil {
			// Keep going: one tunnel failing to start should not prevent the
			// others coming back.
			continue
		}
	}

	// Wait for real handshakes, reading the TIMESTAMP field.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		fresh := readTunnels(cfg)
		if len(liveTunnels(fresh, cfg.StaleAfter)) >= len(cfg.tunnelPaths()) {
			break
		}
		time.Sleep(2 * time.Second)
	}

	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		fresh := readTunnels(cfg)
		plan := BuildPlan(cfg, fresh)
		if err := apply(plan); err != nil {
			lastErr = err
		}
		// Success is not "it ran" - it is the bond being populated and the pins
		// being spread across as many tunnels as we have.
		wantPins := min(len(cfg.Pinned), len(plan.Bond))
		if len(plan.Bond) > 0 && plan.DistinctPins() >= wantPins {
			return nil
		}
		time.Sleep(4 * time.Second)
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("tunnels did not come back cleanly - check `bondvpn status`")
}
