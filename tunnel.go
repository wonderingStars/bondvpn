package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Tunnel is one WireGuard interface and what we know about it.
type Tunnel struct {
	Name         string // interface name, e.g. wg0
	ConfigPath   string
	Up           bool // the interface exists
	HandshakeAge int  // seconds since the last handshake, -1 if never
	Endpoint     string
	Address      string
	RxBytes      int64
	TxBytes      int64
}

// Live reports whether this tunnel should carry traffic. An interface that
// exists but has not handshaken recently is up in name only - it will blackhole
// whatever is routed into it.
func (t *Tunnel) Live(staleAfter time.Duration) bool {
	if !t.Up || t.HandshakeAge < 0 {
		return false
	}
	return time.Duration(t.HandshakeAge)*time.Second <= staleAfter
}

func ifaceNameFor(configPath string) string {
	return strings.TrimSuffix(filepath.Base(configPath), filepath.Ext(configPath))
}

// backend is worked out once, on the first tunnel operation, and reused. The
// probe creates and deletes an interface, which is cheap but not free, and the
// answer cannot change while the daemon runs without somebody loading a kernel
// module underneath it.
var backend = struct {
	once  bool
	value wgBackend
}{}

func currentBackend() wgBackend {
	if !backend.once {
		backend.value = detectBackend()
		backend.once = true
	}
	return backend.value
}

// readTunnels gathers the current state of every configured tunnel.
func readTunnels(cfg *Config) []*Tunnel {
	handshakes := readHandshakes()
	// wireguard-go keeps its state behind a control socket rather than in the
	// kernel, so `wg show` knows nothing about those tunnels.
	if currentBackend() == backendUserspace {
		for iface, ts := range uapiHandshakes() {
			handshakes[iface] = ts
		}
	}
	transfer := readTransfer()
	endpoints := readEndpoints()

	out := make([]*Tunnel, 0, len(cfg.Tunnels))
	for _, path := range cfg.Tunnels {
		name := ifaceNameFor(path)
		t := &Tunnel{Name: name, ConfigPath: path, HandshakeAge: -1}

		if _, err := run(3*time.Second, "ip", "link", "show", name); err == nil {
			t.Up = true
		}
		if ts, ok := handshakes[name]; ok && ts > 0 {
			t.HandshakeAge = int(time.Now().Unix() - ts)
		}
		if tr, ok := transfer[name]; ok {
			t.RxBytes, t.TxBytes = tr[0], tr[1]
		}
		t.Endpoint = endpoints[name]
		out = append(out, t)
	}
	return out
}

// readHandshakes parses `wg show all latest-handshakes`.
//
// The output is three fields: interface, PUBLIC KEY, unix timestamp. The
// timestamp is the THIRD. An earlier version of this logic elsewhere read the
// second field - the base64 public key - which Go would happily fail to parse
// but awk coerced to a truthy number, so a wait-for-handshake loop reported
// success instantly while nothing had connected at all.
func readHandshakes() map[string]int64 {
	out := map[string]int64{}
	text, err := run(10*time.Second, "wg", "show", "all", "latest-handshakes")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		ts, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil {
			continue
		}
		if ts > out[f[0]] {
			out[f[0]] = ts
		}
	}
	return out
}

// readTransfer parses `wg show all transfer`: interface, key, rx, tx.
func readTransfer() map[string][2]int64 {
	out := map[string][2]int64{}
	text, err := run(10*time.Second, "wg", "show", "all", "transfer")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		rx, err1 := strconv.ParseInt(f[2], 10, 64)
		tx, err2 := strconv.ParseInt(f[3], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		cur := out[f[0]]
		out[f[0]] = [2]int64{cur[0] + rx, cur[1] + tx}
	}
	return out
}

// readEndpoints parses `wg show all endpoints`.
func readEndpoints() map[string]string {
	out := map[string]string{}
	text, err := run(10*time.Second, "wg", "show", "all", "endpoints")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		out[f[0]] = f[2]
	}
	return out
}

// bringUp starts a tunnel, using whichever backend this machine has.
//
// The kernel is always preferred. wireguard-go is a fallback for kernels that
// have no WireGuard at all - a Synology NAS on DSM 7.3 runs kernel 4.4, which
// predates it by a decade - and it is slower, because packets cross the
// user/kernel boundary twice.
func bringUp(t *Tunnel) error {
	switch currentBackend() {
	case backendUserspace:
		return upUserspace(t)
	case backendNone:
		return fmt.Errorf("this machine has neither kernel WireGuard nor wireguard-go: " +
			"install wireguard-tools, or wireguard-go plus the tun module")
	default:
		// wg-quick reads the config from /etc/wireguard by name, so a config
		// living elsewhere is passed by path.
		_, err := run(45*time.Second, "wg-quick", "up", t.ConfigPath)
		return err
	}
}

func bringDown(t *Tunnel) {
	if currentBackend() == backendUserspace {
		downUserspace(t)
		return
	}
	quiet(30*time.Second, "wg-quick", "down", t.ConfigPath)
}

// RecoveryAction is what should be done about one tunnel right now.
type RecoveryAction string

const (
	RecoverNone    RecoveryAction = ""
	RecoverStart   RecoveryAction = "start"
	RecoverRestart RecoveryAction = "restart"
)

// How long a tunnel is left alone between recovery attempts. wg-quick takes a
// few seconds, and a dead endpoint stays dead - retrying harder fills the log
// and the CPU without making the far end answer any sooner.
const (
	startBackoff = 60 * time.Second
	cycleBackoff = 5 * time.Minute
)

// recoveryAction decides what to do about a tunnel, given how long it has been
// since we last touched it.
//
// Dropping a dead tunnel from the bond is only half the job. Without recovery
// the gateway degrades permanently on the first blip: a tunnel that goes down
// stays down until somebody notices, its clients re-pack onto the survivors,
// and the bond quietly runs at two thirds of its capacity forever.
//
// A tunnel that is up but has stopped handshaking is cycled rather than left
// alone, because the interface will happily blackhole everything routed into it
// while looking present. Cycling gets a much longer backoff than starting: it
// interrupts whatever is still flowing, so it must not be a hair trigger.
func recoveryAction(t *Tunnel, staleAfter, sinceLastAttempt time.Duration) RecoveryAction {
	if !t.Up {
		if sinceLastAttempt < startBackoff {
			return RecoverNone
		}
		return RecoverStart
	}
	// Twice the staleness window, so a tunnel is only cycled once it is well
	// past "the bond stopped using it", not at the first late handshake. A
	// tunnel that has never handshaken at all gets the same treatment.
	stale := t.HandshakeAge < 0 ||
		time.Duration(t.HandshakeAge)*time.Second > 2*staleAfter
	if stale {
		if sinceLastAttempt < cycleBackoff {
			return RecoverNone
		}
		return RecoverRestart
	}
	return RecoverNone
}

// recoverTunnels applies recoveryAction to every configured tunnel, recording
// when each was last touched so the backoffs mean something.
func recoverTunnels(cfg *Config, tunnels []*Tunnel, lastAttempt map[string]time.Time) {
	now := time.Now()
	for _, t := range tunnels {
		since := cycleBackoff * 2 // never touched: no backoff to serve
		if at, ok := lastAttempt[t.Name]; ok {
			since = now.Sub(at)
		}
		switch recoveryAction(t, cfg.StaleAfter, since) {
		case RecoverStart:
			lastAttempt[t.Name] = now
			logf("%s is down - starting it", t.Name)
			if err := bringUp(t); err != nil {
				logf("%s did not come up: %v", t.Name, err)
			}
		case RecoverRestart:
			lastAttempt[t.Name] = now
			logf("%s has not handshaken in %ds - cycling it", t.Name, t.HandshakeAge)
			bringDown(t)
			if err := bringUp(t); err != nil {
				logf("%s did not come back: %v", t.Name, err)
			}
		}
	}
}

// liveTunnels returns only those fit to carry traffic, in configured order so
// that pin assignment is stable between runs.
func liveTunnels(ts []*Tunnel, staleAfter time.Duration) []*Tunnel {
	var out []*Tunnel
	for _, t := range ts {
		if t.Live(staleAfter) {
			out = append(out, t)
		}
	}
	return out
}
