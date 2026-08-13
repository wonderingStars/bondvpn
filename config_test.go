package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const goodConfig = `
tunnels:
  - /etc/wireguard/wg0.conf
  - /etc/wireguard/wg1.conf
  - /etc/wireguard/wg2.conf

clients: 172.20.0.0/24
default: bond

routes:
  pin:
    - 172.20.0.10
    - 172.20.0.11
  bond:
    - 172.20.0.20

dns: 10.64.0.1   # inline comment
lan: auto
stale_handshake: 180
listen: 127.0.0.1:8099
`

func TestLoadConfig(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, goodConfig))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Tunnels) != DefaultTunnels {
		t.Errorf("tunnels = %d, want %d", len(cfg.Tunnels), DefaultTunnels)
	}
	if cfg.Clients != "172.20.0.0/24" {
		t.Errorf("clients = %q", cfg.Clients)
	}
	if len(cfg.Pinned) != 2 || cfg.Pinned[0] != "172.20.0.10" {
		t.Errorf("pinned = %v", cfg.Pinned)
	}
	if len(cfg.Bonded) != 1 || cfg.Bonded[0] != "172.20.0.20" {
		t.Errorf("bonded = %v", cfg.Bonded)
	}
	if cfg.DNS != "10.64.0.1" {
		t.Errorf("dns = %q - inline comment not stripped", cfg.DNS)
	}
	if cfg.StaleAfter != 180*time.Second {
		t.Errorf("stale_handshake = %v", cfg.StaleAfter)
	}
	if cfg.ModeFor("172.20.0.10") != ModePin {
		t.Error("a pinned client did not come back as pinned")
	}
	if cfg.ModeFor("172.20.0.99") != ModeBond {
		t.Error("an unlisted client should fall back to the default mode")
	}
}

// The shipped example must actually load, and must ship three tunnels.
func TestExampleConfigLoads(t *testing.T) {
	cfg, err := LoadConfig("config.example.yml")
	if err != nil {
		t.Fatalf("the shipped example config does not parse: %v", err)
	}
	if len(cfg.Tunnels) != DefaultTunnels {
		t.Errorf("example ships %d tunnels, want %d", len(cfg.Tunnels), DefaultTunnels)
	}
	if len(cfg.Tunnels) > MaxTunnels {
		t.Errorf("example exceeds the %d tunnel maximum", MaxTunnels)
	}
}

func TestConfigRejections(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{
			name: "no tunnels",
			body: "clients: 172.20.0.0/24\n",
			want: "no tunnels",
		},
		{
			name: "too many tunnels",
			body: "tunnels:\n  - a.conf\n  - b.conf\n  - c.conf\n  - d.conf\n" +
				"  - e.conf\n  - f.conf\nclients: 172.20.0.0/24\n",
			want: "maximum is 5",
		},
		{
			name: "clients is not a CIDR",
			body: "tunnels:\n  - a.conf\nclients: 172.20.0.1\n",
			want: "not a CIDR",
		},
		{
			name: "pinned client outside the subnet",
			body: "tunnels:\n  - a.conf\nclients: 172.20.0.0/24\npin:\n  - 10.0.0.5\n",
			want: "outside the clients subnet",
		},
		{
			// Without it, containers resolve however they happen to be
			// configured, leaking every hostname they look up while their
			// traffic stays correctly tunnelled.
			name: "no dns",
			body: "tunnels:\n  - a.conf\nclients: 172.20.0.0/24\n",
			want: "dns is required",
		},
		{
			name: "unknown setting",
			body: "tunnels:\n  - a.conf\nclients: 172.20.0.0/24\nspeed: fast\n",
			want: "unknown setting",
		},
		{
			name: "bad route mode",
			body: "tunnels:\n  - a.conf\nclients: 172.20.0.0/24\ndefault: sometimes\n",
			want: "must be",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(writeConfig(t, tc.body))
			if err == nil {
				t.Fatal("expected this config to be rejected")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error was %q, expected it to mention %q", err, tc.want)
			}
		})
	}
}

// The self-guard is the difference between a misconfiguration and a box you
// have to walk over to with a keyboard.
func TestGuardAgainstSelf(t *testing.T) {
	cfg := &Config{Clients: "192.168.1.0/24", LAN: "auto"}
	err := cfg.GuardAgainstSelf([]net.IP{net.ParseIP("192.168.1.6")})
	if err == nil {
		t.Fatal("a clients subnet holding this host's management address must be refused")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("the error should explain the consequence, got %q", err)
	}

	cfg = &Config{Clients: "172.20.0.0/24", LAN: "auto"}
	if err := cfg.GuardAgainstSelf([]net.IP{net.ParseIP("192.168.1.6")}); err != nil {
		t.Errorf("a separate container subnet must be accepted, got %v", err)
	}

	cfg = &Config{Clients: "192.168.0.0/16", LAN: "192.168.1.0/24"}
	if err := cfg.GuardAgainstSelf(nil); err == nil {
		t.Error("a clients subnet overlapping the LAN must be refused")
	}
}

// Regression, found on a live gateway: the host ALWAYS holds the container
// bridge's gateway address inside the clients subnet, because that address is
// how the containers reach anything. An earlier version checked every address
// on the box and so refused to start on a perfectly correct configuration.
func TestGuardAllowsTheBridgeGatewayAddress(t *testing.T) {
	cfg := &Config{Clients: "10.99.0.0/24", LAN: "192.168.1.0/24"}
	// 10.99.0.1 is on the bridge; only the management address is passed in.
	if err := cfg.GuardAgainstSelf([]net.IP{net.ParseIP("192.168.1.6")}); err != nil {
		t.Fatalf("the bridge gateway address must not block startup: %v", err)
	}
}
