package main

import (
	"strings"
	"testing"
)

// A provider's file as downloaded. Handing this straight to the gateway is what
// most first-time users will do.
const mullvadStyleConf = `[Interface]
PrivateKey = aGVsbG8gdGhlcmU=
Address = 10.66.85.202/32
DNS = 10.64.0.1

[Peer]
PublicKey = c29tZSBwdWJsaWMga2V5
AllowedIPs = 0.0.0.0/0
Endpoint = 185.213.154.67:51820
`

const goodConf = `[Interface]
PrivateKey = aGVsbG8gdGhlcmU=
Address = 10.66.85.202/32
Table = off

[Peer]
PublicKey = c29tZSBwdWJsaWMga2V5
AllowedIPs = 0.0.0.0/0
Endpoint = 185.213.154.67:51820
PersistentKeepalive = 25
`

func TestProviderConfigIsRefused(t *testing.T) {
	c := parseWGConf(strings.NewReader(mullvadStyleConf))
	fatal, warn := c.Problems("wg1.conf")

	if len(fatal) != 2 {
		t.Fatalf("expected two fatal problems (Table and DNS), got %v", fatal)
	}
	joined := strings.Join(fatal, " | ")
	if !strings.Contains(joined, "Table = off") {
		t.Errorf("a config without Table = off must be refused: %v", fatal)
	}
	if !strings.Contains(joined, "resolvconf") {
		t.Errorf("a DNS line must be refused and explain why: %v", fatal)
	}
	if len(warn) != 1 || !strings.Contains(warn[0], "PersistentKeepalive") {
		t.Errorf("a missing keepalive must warn: %v", warn)
	}
}

func TestGoodConfigPasses(t *testing.T) {
	c := parseWGConf(strings.NewReader(goodConf))
	fatal, warn := c.Problems("wg0.conf")
	if len(fatal) != 0 {
		t.Errorf("a correct config must be accepted, got %v", fatal)
	}
	if len(warn) != 0 {
		t.Errorf("a correct config must not warn, got %v", warn)
	}
}

// `Table = off` written any of the ways a person might write it.
func TestTableParsingIsForgiving(t *testing.T) {
	for _, form := range []string{"Table = off", "Table=off", "table = OFF", "  Table   =   off  "} {
		c := parseWGConf(strings.NewReader("[Interface]\n" + form + "\n"))
		if c.Table != "off" {
			t.Errorf("%q parsed as %q, want \"off\"", form, c.Table)
		}
	}
}

// PersistentKeepalive = 0 means disabled, which is the same as absent.
func TestZeroKeepaliveCountsAsNone(t *testing.T) {
	c := parseWGConf(strings.NewReader("[Peer]\nPersistentKeepalive = 0\n"))
	if c.Keepalive {
		t.Error("PersistentKeepalive = 0 must not count as keepalive")
	}
}
