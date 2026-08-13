package main

import (
	"testing"
	"time"
)

const stale = 180 * time.Second

// Without recovery the gateway degrades permanently on the first blip: a
// tunnel that drops stays dropped, and the bond runs at reduced capacity
// forever while reporting itself healthy about the tunnels that remain.
func TestRecoveryStartsADownTunnel(t *testing.T) {
	down := &Tunnel{Name: "wg1", Up: false}
	if got := recoveryAction(down, stale, time.Hour); got != RecoverStart {
		t.Errorf("a down tunnel should be started, got %q", got)
	}
}

func TestRecoveryLeavesAHealthyTunnelAlone(t *testing.T) {
	ok := &Tunnel{Name: "wg0", Up: true, HandshakeAge: 20}
	if got := recoveryAction(ok, stale, time.Hour); got != RecoverNone {
		t.Errorf("a healthy tunnel must not be touched, got %q", got)
	}
}

// An interface that is up but silent blackholes whatever is routed into it, so
// it is cycled - but only once it is well past the point where the bond stopped
// using it, because cycling interrupts anything still flowing.
func TestRecoveryCyclesASilentTunnel(t *testing.T) {
	justStale := &Tunnel{Name: "wg0", Up: true, HandshakeAge: 200}
	if got := recoveryAction(justStale, stale, time.Hour); got != RecoverNone {
		t.Errorf("a barely-stale tunnel must not be cycled yet, got %q", got)
	}
	longGone := &Tunnel{Name: "wg0", Up: true, HandshakeAge: 400}
	if got := recoveryAction(longGone, stale, time.Hour); got != RecoverRestart {
		t.Errorf("a long-silent tunnel should be cycled, got %q", got)
	}
	never := &Tunnel{Name: "wg2", Up: true, HandshakeAge: -1}
	if got := recoveryAction(never, stale, time.Hour); got != RecoverRestart {
		t.Errorf("a tunnel that never handshook should be cycled, got %q", got)
	}
}

// Retrying every pass would fill the log and the CPU without making a dead
// endpoint answer any sooner.
func TestRecoveryBacksOff(t *testing.T) {
	down := &Tunnel{Name: "wg1", Up: false}
	if got := recoveryAction(down, stale, 5*time.Second); got != RecoverNone {
		t.Errorf("a tunnel started 5s ago must be left alone, got %q", got)
	}
	silent := &Tunnel{Name: "wg0", Up: true, HandshakeAge: 400}
	if got := recoveryAction(silent, stale, 90*time.Second); got != RecoverNone {
		t.Errorf("cycling needs the longer backoff, got %q", got)
	}
	if got := recoveryAction(silent, stale, 6*time.Minute); got != RecoverRestart {
		t.Errorf("after the cycle backoff it should cycle, got %q", got)
	}
}
