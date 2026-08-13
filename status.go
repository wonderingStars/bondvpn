package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Status is the integration surface. Keep it flat and stable - anything built
// on top of this (a dashboard, a monitor) depends on these names.
type Status struct {
	Generated  int64  `json:"generated"`
	Version    string `json:"version"`
	Bonded     int    `json:"bonded"`
	HashPolicy int    `json:"hash_policy"`
	NAT        bool   `json:"nat"`
	DNSForced  bool   `json:"dns_forced"`
	// Reported so a dashboard can show it. Purely informational: nothing in
	// the daemon acts on either field.
	UpdateAvailable bool             `json:"update_available"`
	LatestVersion   string           `json:"latest_version,omitempty"`
	Host            HostStatus       `json:"host"`
	KillSwitch      KillSwitchStatus `json:"killswitch"`
	Tunnels         []TunnelStatus   `json:"tunnels"`
	Clients         []ClientStatus   `json:"clients"`
	Problems        []string         `json:"problems"`
	// Warnings are degradations that do NOT fail health: the gateway still
	// protects and still routes, but something is not ideal. Kept apart from
	// Problems because /healthz gates on Problems alone - folding a warning in
	// there made the quickstart container permanently unhealthy on a provider
	// config that merely omitted PersistentKeepalive, which the docs call
	// "recommended", not required.
	Warnings []string `json:"warnings"`
}

type KillSwitchStatus struct {
	V4 bool `json:"v4"`
	V6 bool `json:"v6"`
}

type TunnelStatus struct {
	Iface        string   `json:"iface"`
	Up           bool     `json:"up"`
	InBond       bool     `json:"in_bond"`
	HandshakeAge int      `json:"handshake_age"`
	Endpoint     string   `json:"endpoint"`
	RxBytes      int64    `json:"rx_bytes"`
	TxBytes      int64    `json:"tx_bytes"`
	Pinned       []string `json:"pinned_clients"`
}

type ClientStatus struct {
	IP     string `json:"ip"`
	Mode   string `json:"mode"`
	Tunnel string `json:"tunnel"`
}

// buildStatus assembles the current picture, including what is wrong with it.
//
// `problems` is deliberately part of the API rather than something each caller
// re-derives. On the prototype the dashboard and the alerting computed "is it
// broken" separately and could disagree, which is the worst possible outcome
// for a health signal.
func buildStatus(cfg *Config, tunnels []*Tunnel, plan *Plan) *Status {
	s := &Status{
		Generated:  time.Now().Unix(),
		Version:    Version,
		Bonded:     len(plan.Bond),
		HashPolicy: readHashPolicy(),
		NAT:        natArmed(cfg.Clients),
		DNSForced:  dnsForced(cfg),
		Host:       readHost(cfg),
		KillSwitch: KillSwitchStatus{
			V4: killSwitchArmed(cfg.Clients),
			V6: ipv6Blocked(),
		},
		Problems: []string{},
		Warnings: []string{},
	}

	inBond := map[string]bool{}
	for _, name := range plan.Bond {
		inBond[name] = true
	}
	pinnedBy := map[string][]string{}
	for ip, iface := range plan.Pins {
		pinnedBy[iface] = append(pinnedBy[iface], ip)
	}

	for _, t := range tunnels {
		s.Tunnels = append(s.Tunnels, TunnelStatus{
			Iface: t.Name, Up: t.Up, InBond: inBond[t.Name],
			HandshakeAge: t.HandshakeAge, Endpoint: t.Endpoint,
			RxBytes: t.RxBytes, TxBytes: t.TxBytes,
			Pinned: pinnedBy[t.Name],
		})
		if !t.Up {
			s.Problems = append(s.Problems, fmt.Sprintf("%s is down", t.Name))
		} else if !t.Live(cfg.StaleAfter) {
			// HandshakeAge is -1 for a tunnel that has NEVER handshaken (a fresh
			// interface with an unreachable endpoint). Printing "-1s" reads as a
			// bug at exactly the moment someone is diagnosing a dead endpoint, so
			// say what is actually true instead.
			if t.HandshakeAge < 0 {
				s.Problems = append(s.Problems,
					fmt.Sprintf("%s is up but has never completed a handshake - check its endpoint and keys", t.Name))
			} else {
				s.Problems = append(s.Problems,
					fmt.Sprintf("%s is up but has not handshaken in %ds", t.Name, t.HandshakeAge))
			}
		} else if !inBond[t.Name] {
			s.Problems = append(s.Problems,
				fmt.Sprintf("%s is live but not carrying traffic", t.Name))
		}
	}

	for _, ip := range cfg.Pinned {
		s.Clients = append(s.Clients, ClientStatus{IP: ip, Mode: string(ModePin), Tunnel: plan.Pins[ip]})
	}
	for _, ip := range cfg.Bonded {
		s.Clients = append(s.Clients, ClientStatus{IP: ip, Mode: string(ModeBond), Tunnel: "bond"})
	}

	if len(plan.Bond) == 0 {
		s.Problems = append(s.Problems,
			"no live tunnels - all client traffic is blocked (the kill switch is holding)")
	}
	if !s.KillSwitch.V4 {
		s.Problems = append(s.Problems,
			"IPv4 kill switch is NOT armed - traffic could leave outside the tunnels")
	}
	if !s.KillSwitch.V6 {
		s.Problems = append(s.Problems, "IPv6 is not blocked")
	}
	// Its absence is invisible everywhere else: tunnels handshake, routing is
	// correct, counters move, and every request dies silently at the far end.
	if !s.NAT {
		s.Problems = append(s.Problems,
			"client traffic is not being translated onto the tunnels - the far end "+
				"drops any source address that is not the tunnel's own, so requests "+
				"will vanish with no error")
	}
	// Without the redirect a container configured with a public resolver leaks
	// every hostname it looks up, while its traffic stays correctly tunnelled
	// and nothing looks wrong.
	if cfg.DNS != "" && !s.DNSForced {
		s.Problems = append(s.Problems,
			"client DNS is NOT being redirected to "+cfg.DNS+" - containers can "+
				"leak every hostname they look up")
	}
	// A WARNING, not a problem. It degrades bond-mode load-spreading (usenet,
	// HTTP) to a single tunnel, but pinning does not use multipath hashing at
	// all, the kill switch and NAT are unaffected, and cmdRun already treats
	// ensureHashPolicy failing as non-fatal. Making it a problem meant the
	// quickstart container - which cannot set this sysctl without --privileged -
	// reported itself permanently unhealthy while its pinned workload ran fine.
	if s.HashPolicy != 1 {
		s.Warnings = append(s.Warnings,
			"fib_multipath_hash_policy is not 1 - bonded (not pinned) clients will "+
				"land on one tunnel. Set it on the host: sysctl -w net.ipv4.fib_multipath_hash_policy=1")
	}
	// Pinned clients collapsing onto one tunnel is the failure this product
	// exists to prevent, and it looks healthy because traffic still flows.
	if len(cfg.Pinned) > 1 && len(plan.Bond) > 1 && plan.DistinctPins() == 1 {
		s.Problems = append(s.Problems,
			"every pinned client is on the same tunnel - per-client separation is lost")
	}
	// A provider's file as downloaded fights this gateway for the routing table
	// and rewrites the host's resolver. The FATAL ones (Table = off missing, a
	// DNS = line) are problems - they break routing. The warnings (a missing
	// PersistentKeepalive) are degradations the docs call "recommended", so they
	// must not fail health: folding them into Problems 503'd the container on
	// Mullvad's own default config.
	if fatal, warn := checkTunnelConfigs(cfg); len(fatal) > 0 || len(warn) > 0 {
		s.Problems = append(s.Problems, fatal...)
		s.Warnings = append(s.Warnings, warn...)
	}
	return s
}

// serveStatus exposes the status document, and the interface built on it. It
// binds to whatever `listen` says, which should stay on loopback or the
// container bridge - never the LAN.
//
// NEITHER SURFACE IS AUTHENTICATED, and that is a deliberate consequence of the
// bind address rather than an oversight: the page is a read-only view of one
// machine's own routing, reachable only by something already on that machine or
// inside its container network. Anyone putting it on the LAN must put a login
// in front of it, which is what `listen` being a config setting is for.
func serveStatus(cfg *Config, get func() *Status) *http.Server {
	mux := http.NewServeMux()
	if cfg.Dashboard {
		mux.HandleFunc("/", dashboardHandler())
	}
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(get())
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		st := get()
		// Gated on Problems, NOT Warnings. A warning is a degradation the
		// gateway survives; failing health on one turned a working container
		// permanently unhealthy.
		if len(st.Problems) == 0 {
			w.WriteHeader(http.StatusOK)
			if len(st.Warnings) > 0 {
				_, _ = w.Write([]byte("ok (with warnings)\n" + strings.Join(st.Warnings, "\n") + "\n"))
			} else {
				_, _ = w.Write([]byte("ok\n"))
			}
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(strings.Join(st.Problems, "\n") + "\n"))
	})
	return &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

const hashPolicyPath = "/proc/sys/net/ipv4/fib_multipath_hash_policy"

func readHashPolicy() int {
	b, err := os.ReadFile(hashPolicyPath)
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return -1
	}
	return n
}

// ensureHashPolicy sets layer-4 hashing.
//
// MANDATORY, and the default is wrong. At policy 0 the kernel hashes on
// addresses only, so every connection to a single server lands on ONE tunnel -
// measured 400 of 400 usenet-shaped flows on one tunnel, against an even
// 131/138/131 split at policy 1. Torrents spread either way, so a torrent-only
// test passes and hides this completely.
func ensureHashPolicy() error {
	if readHashPolicy() == 1 {
		return nil
	}
	if err := os.WriteFile(hashPolicyPath, []byte("1\n"), 0o644); err != nil {
		return fmt.Errorf("could not set fib_multipath_hash_policy=1: %v", err)
	}
	return nil
}

func ipv6Blocked() bool {
	_, err := run(10*time.Second, "ip6tables", "-C", chain, "-j", "REJECT")
	return err == nil
}
