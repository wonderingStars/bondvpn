package main

import "time"

// Client traffic has to leave wearing the tunnel's own address.
//
// WireGuard's cryptokey routing at the far end only accepts source addresses
// inside the peer's AllowedIPs - your provider hands you one address, and a
// packet arriving from 172.20.0.10 is not it. Without this translation the
// tunnels handshake, the routing is correct, the counters move, `status` reports
// no problems, and every client request vanishes at the far end with no error
// anywhere. It was found by watching a pinned client get no answer through a
// tunnel that looked perfect.
//
// One rule covers every tunnel: `wg+` matches wg0, wg1, wg2 and so on, so
// tunnels coming and going need no changes here.
func applyNAT(cfg *Config) error {
	if natArmed(cfg.Clients) {
		return nil
	}
	_, err := run(10*time.Second, "iptables", "-t", "nat", "-A", "POSTROUTING",
		"-s", cfg.Clients, "-o", "wg+", "-j", "MASQUERADE")
	return err
}

// natArmed reports whether the translation rule is present. It is checked on
// every status pass because its absence is invisible in every other signal.
func natArmed(clients string) bool {
	_, err := run(10*time.Second, "iptables", "-t", "nat", "-C", "POSTROUTING",
		"-s", clients, "-o", "wg+", "-j", "MASQUERADE")
	return err == nil
}

func removeNAT(cfg *Config) {
	quiet(10*time.Second, "iptables", "-t", "nat", "-D", "POSTROUTING",
		"-s", cfg.Clients, "-o", "wg+", "-j", "MASQUERADE")
}
