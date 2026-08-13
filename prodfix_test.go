package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Describe is the change signature the run loop compares each tick. If it
// renders pins in Go map order it looks changed on ~40% of identical ticks and
// re-applies the whole plan, resetting client traffic in the flush window. It
// must be deterministic for a fixed plan.
func TestDescribeIsDeterministic(t *testing.T) {
	p := &Plan{
		Live: []*Tunnel{{Name: "wg0"}, {Name: "wg1"}, {Name: "wg2"}},
		Bond: []string{"wg0", "wg1", "wg2"},
		Pins: map[string]string{
			"10.99.0.10": "wg0", "10.99.0.11": "wg1", "10.99.0.12": "wg2",
			"10.99.0.13": "wg0", "10.99.0.14": "wg1",
		},
	}
	first := p.Describe()
	for i := 0; i < 2000; i++ {
		// Rebuild the map each iteration exactly as BuildPlan does every tick,
		// so insertion order cannot accidentally stabilise the range order.
		q := &Plan{Live: p.Live, Bond: p.Bond, Pins: map[string]string{}}
		for k, v := range p.Pins {
			q.Pins[k] = v
		}
		if got := q.Describe(); got != first {
			t.Fatalf("Describe not stable across map rebuilds:\n first: %q\n now:   %q", first, got)
		}
	}
	if !strings.Contains(first, "10.99.0.10 pinned to wg0") {
		t.Errorf("Describe lost its pin content: %q", first)
	}
}

// A missing PersistentKeepalive is a WARNING, not a Problem: the gateway still
// protects and routes, and /healthz must stay 200 or the container is unhealthy
// on Mullvad's own default config.
func TestKeepaliveMissingIsWarnNotProblem(t *testing.T) {
	dir := t.TempDir()
	wg := writeFile(t, dir, "wg0.conf", "[Interface]\nTable = off\n") // no keepalive
	cfg := defaultConfig()
	cfg.Tunnels = []string{wg}
	cfg.Clients = "10.99.0.0/24"
	cfg.DNS = "10.64.0.1"

	// A plan with the one tunnel live, so tunnel-liveness does not add a problem.
	plan := &Plan{Live: []*Tunnel{{Name: "wg0"}}, Bond: []string{"wg0"}, Pins: map[string]string{}}
	tunnels := []*Tunnel{{Name: "wg0", Up: true, HandshakeAge: 5}}
	s := buildStatus(cfg, tunnels, plan)

	joinedProblems := strings.Join(s.Problems, "|")
	joinedWarnings := strings.Join(s.Warnings, "|")
	if strings.Contains(joinedProblems, "PersistentKeepalive") {
		t.Errorf("keepalive appeared in Problems (would 503 /healthz): %q", joinedProblems)
	}
	if !strings.Contains(joinedWarnings, "PersistentKeepalive") {
		t.Errorf("keepalive should be a warning, warnings were: %q", joinedWarnings)
	}
}

// The hash policy being wrong degrades bonded load-spreading but breaks nothing
// pinning needs; it must be a warning so the unprivileged quickstart container
// (which cannot set the sysctl) is not permanently unhealthy.
func TestHashPolicyIsWarnNotProblem(t *testing.T) {
	cfg := defaultConfig()
	cfg.Tunnels = []string{"x"}
	cfg.Clients = "10.99.0.0/24"
	cfg.DNS = "10.64.0.1"
	plan := &Plan{Live: []*Tunnel{{Name: "wg0"}}, Bond: []string{"wg0"}, Pins: map[string]string{}}
	s := buildStatus(cfg, []*Tunnel{{Name: "wg0", Up: true, HandshakeAge: 5}}, plan)
	// On a dev box HashPolicy reads -1 (path absent), so it is != 1 and must be
	// a warning, never a problem.
	if s.HashPolicy != 1 {
		if strings.Contains(strings.Join(s.Problems, "|"), "fib_multipath_hash_policy") {
			t.Error("hash policy is in Problems; it must be a warning")
		}
		if !strings.Contains(strings.Join(s.Warnings, "|"), "fib_multipath_hash_policy") {
			t.Errorf("hash policy should be a warning, warnings: %v", s.Warnings)
		}
	}
}

// A never-handshaken tunnel has HandshakeAge -1; the message must not read
// "handshaken in -1s".
func TestNeverHandshakenMessage(t *testing.T) {
	cfg := defaultConfig()
	cfg.Clients = "10.99.0.0/24"
	cfg.DNS = "10.64.0.1"
	cfg.StaleAfter = 180 * time.Second
	plan := &Plan{Live: []*Tunnel{}, Bond: []string{}, Pins: map[string]string{}}
	s := buildStatus(cfg, []*Tunnel{{Name: "wg0", Up: true, HandshakeAge: -1}}, plan)
	joined := strings.Join(s.Problems, "|")
	if strings.Contains(joined, "-1s") {
		t.Errorf("problem still renders negative age: %q", joined)
	}
	if !strings.Contains(joined, "never completed a handshake") {
		t.Errorf("expected the never-handshaken wording, got: %q", joined)
	}
}

// The /status get() closure and refresh() touch `current` from different
// goroutines; hammer them together under -race.
func TestStatusServerConcurrentAccess(t *testing.T) {
	var mu sync.Mutex
	var current *Status
	get := func() *Status {
		mu.Lock()
		defer mu.Unlock()
		if current == nil {
			return &Status{Problems: []string{}, Warnings: []string{}}
		}
		return current
	}
	cfg := defaultConfig()
	srv := serveStatus(cfg, get)
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	stop := make(chan struct{})
	go func() {
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				mu.Lock()
				current = &Status{Version: "x", Generated: int64(i), Problems: []string{}, Warnings: []string{}}
				mu.Unlock()
				i++
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				resp, err := http.Get(ts.URL + "/status")
				if err == nil {
					resp.Body.Close()
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
