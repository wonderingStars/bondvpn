package main

import (
	"strings"
	"testing"
)

const providerConf = `[Interface]
PrivateKey = qJ3vGQmVQxV0Jw3nZ0Zb0f8m4xX9qYy6l8v1QpQmXk8=
Address = 10.66.85.202/32
Table = off

[Peer]
PublicKey = xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=
AllowedIPs = 0.0.0.0/0
Endpoint = 185.213.154.67:51820
PersistentKeepalive = 25
`

// The conversion that catches people: a .conf carries BASE64 keys and the UAPI
// takes HEX. Feeding base64 straight through is accepted as a malformed key,
// and the tunnel then silently never handshakes - no error anywhere.
func TestUAPIConversion(t *testing.T) {
	settings, addrs, err := uapiFromConfig(providerConf)
	if err != nil {
		t.Fatalf("uapiFromConfig: %v", err)
	}

	if len(addrs) != 1 || addrs[0] != "10.66.85.202/32" {
		t.Errorf("addresses = %v", addrs)
	}

	// 32 bytes of key is 64 hex characters, and no base64 padding survives.
	for _, want := range []string{"private_key=", "endpoint=185.213.154.67:51820",
		"allowed_ip=0.0.0.0/0", "persistent_keepalive_interval=25"} {
		if !strings.Contains(settings, want) {
			t.Errorf("settings missing %q:\n%s", want, settings)
		}
	}
	if strings.Contains(settings, "=") && strings.Contains(settings, "qJ3vGQ") {
		t.Error("a base64 key reached the UAPI - it must be hex")
	}
	for _, line := range strings.Split(settings, "\n") {
		if v, ok := strings.CutPrefix(line, "private_key="); ok {
			if len(v) != 64 {
				t.Errorf("private_key is %d chars, want 64 hex", len(v))
			}
		}
	}

	// Table = off is for wg-quick and means nothing to the UAPI; passing it
	// through would be rejected as an unknown key.
	if strings.Contains(strings.ToLower(settings), "table") {
		t.Error("Table leaked into the UAPI settings")
	}
}

func TestUAPIRejectsBadKeys(t *testing.T) {
	bad := strings.Replace(providerConf, "PrivateKey = qJ3vGQmVQxV0Jw3nZ0Zb0f8m4xX9qYy6l8v1QpQmXk8=",
		"PrivateKey = not-a-key", 1)
	if _, _, err := uapiFromConfig(bad); err == nil {
		t.Error("a malformed key must be an error, not a tunnel that never connects")
	}

	short := strings.Replace(providerConf, "PrivateKey = qJ3vGQmVQxV0Jw3nZ0Zb0f8m4xX9qYy6l8v1QpQmXk8=",
		"PrivateKey = aGVsbG8=", 1)
	if _, _, err := uapiFromConfig(short); err == nil {
		t.Error("a key of the wrong length must be rejected")
	}
}

func TestUAPINeedsAPeer(t *testing.T) {
	if _, _, err := uapiFromConfig("[Interface]\nPrivateKey = aGVsbG8=\n"); err == nil {
		t.Error("a config with no [Peer] must be rejected")
	}
}

// UAPI will not resolve names. A provider config that names a host would
// otherwise produce a tunnel that never connects and never says why.
func TestEndpointResolution(t *testing.T) {
	got, err := resolveEndpoint("185.213.154.67:51820")
	if err != nil || got != "185.213.154.67:51820" {
		t.Errorf("an address should pass through unchanged: %q %v", got, err)
	}
	if _, err := resolveEndpoint("no-port"); err == nil {
		t.Error("an endpoint without a port must be an error")
	}
	if _, err := resolveEndpoint("this-host-does-not-exist.invalid:51820"); err == nil {
		t.Error("an unresolvable host must be an error rather than a silent failure")
	}
}

func TestBackendNames(t *testing.T) {
	if backendKernel.String() != "kernel" || backendNone.String() != "none" {
		t.Error("backend names are reported to the user; keep them plain")
	}
	if !strings.Contains(backendUserspace.String(), "wireguard-go") {
		t.Error("the userspace backend should name what is doing the work")
	}
}
