package main

import "time"

// Client DNS is redirected onto the tunnel resolver, not merely permitted.
//
// Blocking port 53 to the LAN stops the obvious leak and leaves a worse one:
// a container configured with a public resolver still leaks every hostname it
// looks up - correctly tunnelled traffic, plaintext queries, and nothing that
// looks broken. Asking users to configure DNS in every container turns a
// security property into a per-container setting that one forgotten compose
// file undoes.
//
// So the query is rewritten in the kernel instead. Whatever a container is
// configured to ask, it asks the tunnel's resolver, over the tunnel. There is
// nothing for a user to get wrong.
func applyDNSRedirect(cfg *Config) error {
	if cfg.DNS == "" {
		return nil
	}
	for _, proto := range []string{"udp", "tcp"} {
		if dnsRedirectArmed(cfg, proto) {
			continue
		}
		if _, err := run(10*time.Second, "iptables", "-t", "nat", "-A", "PREROUTING",
			"-s", cfg.Clients, "-p", proto, "--dport", "53",
			"-j", "DNAT", "--to-destination", cfg.DNS+":53"); err != nil {
			return err
		}
	}
	return nil
}

func dnsRedirectArmed(cfg *Config, proto string) bool {
	_, err := run(10*time.Second, "iptables", "-t", "nat", "-C", "PREROUTING",
		"-s", cfg.Clients, "-p", proto, "--dport", "53",
		"-j", "DNAT", "--to-destination", cfg.DNS+":53")
	return err == nil
}

// dnsForced reports whether both halves are in place. Checking only UDP would
// miss the TCP fallback that every resolver uses for large answers.
func dnsForced(cfg *Config) bool {
	if cfg.DNS == "" {
		return false
	}
	return dnsRedirectArmed(cfg, "udp") && dnsRedirectArmed(cfg, "tcp")
}

func removeDNSRedirect(cfg *Config) {
	if cfg.DNS == "" {
		return
	}
	for _, proto := range []string{"udp", "tcp"} {
		quiet(10*time.Second, "iptables", "-t", "nat", "-D", "PREROUTING",
			"-s", cfg.Clients, "-p", proto, "--dport", "53",
			"-j", "DNAT", "--to-destination", cfg.DNS+":53")
	}
}
