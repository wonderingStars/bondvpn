package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// WGConf is what we need to know about a WireGuard config before trusting it.
//
// A provider's file as downloaded is NOT safe to hand straight to this gateway,
// and both problems are silent. Neither shows up as an error the user can act
// on, so they are checked before anything starts.
type WGConf struct {
	Table     string // the Table= setting, lowercased ("" if absent)
	DNS       string // a DNS= line, which wg-quick applies to the HOST
	Keepalive bool   // whether any peer sets PersistentKeepalive
}

func parseWGConf(r io.Reader) *WGConf {
	c := &WGConf{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "table":
			c.Table = strings.ToLower(val)
		case "dns":
			c.DNS = val
		case "persistentkeepalive":
			c.Keepalive = val != "" && val != "0"
		}
	}
	return c
}

// Problems returns the reasons this config cannot be used as-is, and separately
// the things that merely deserve a warning.
func (c *WGConf) Problems(name string) (fatal []string, warn []string) {
	// Without Table = off, wg-quick installs its OWN default route and fwmark
	// rules for the tunnel. It then fights this gateway for control of the
	// routing table on every restart, and the symptom is traffic taking the
	// wrong tunnel rather than any error.
	if c.Table != "off" {
		fatal = append(fatal, fmt.Sprintf(
			"%s does not set `Table = off` - wg-quick would install its own routes "+
				"and fight this gateway for them. Add `Table = off` under [Interface].", name))
	}

	// A DNS= line makes wg-quick rewrite the HOST's resolver, which needs
	// resolvconf installed (it fails outright without it) and changes the
	// machine's own name resolution as a side effect of starting a tunnel.
	// BondVPN sends client DNS down the tunnel itself, so the line is not
	// needed and its effects are not wanted.
	if c.DNS != "" {
		fatal = append(fatal, fmt.Sprintf(
			"%s sets `DNS = %s`, which makes wg-quick rewrite this machine's own "+
				"resolver and fails outright without resolvconf installed. Remove the "+
				"line - BondVPN sends client DNS down the tunnel itself.", name, c.DNS))
	}

	// WireGuard only rekeys when there is traffic. On an idle tunnel the last
	// handshake ages past the staleness window and the tunnel gets dropped from
	// the bond while being perfectly healthy.
	if !c.Keepalive {
		warn = append(warn, fmt.Sprintf(
			"%s sets no PersistentKeepalive, so an idle tunnel stops handshaking and "+
				"will eventually be treated as dead. Add `PersistentKeepalive = 25` "+
				"under [Peer].", name))
	}
	return fatal, warn
}

// checkTunnelConfigs reads every configured tunnel and reports what is wrong.
func checkTunnelConfigs(cfg *Config) (fatal []string, warn []string) {
	for _, path := range cfg.tunnelPaths() {
		f, err := os.Open(path)
		if err != nil {
			fatal = append(fatal, fmt.Sprintf("cannot read %s: %v", path, err))
			continue
		}
		wc := parseWGConf(f)
		f.Close()
		ff, ww := wc.Problems(path)
		fatal = append(fatal, ff...)
		warn = append(warn, ww...)
	}
	return fatal, warn
}
