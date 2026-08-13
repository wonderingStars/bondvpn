package main

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Userspace WireGuard, for kernels that have none.
//
// A Synology NAS is the case that forced this: DSM 7.3 ships kernel 4.4, which
// predates in-kernel WireGuard by a decade, and Synology does not build the
// module. The same is true of plenty of appliance and NAS firmware, and of
// containers on hosts that lack it.
//
// wireguard-go implements the protocol entirely in userspace over /dev/net/tun.
// It is slower than the kernel - packets cross the boundary twice - which is
// fine on a domestic line and wrong for a 10 Gbit one. So it is a FALLBACK,
// never a preference: if the kernel can do it, the kernel does it.
//
// It is spawned as a separate process rather than linked in, which keeps go.mod
// empty. For something that runs as root beside VPN keys, an empty dependency
// tree is worth more than the convenience of one binary.

// wgBackend is how tunnels get created on this machine.
type wgBackend int

const (
	backendKernel    wgBackend = iota // ip link add ... type wireguard
	backendUserspace                  // wireguard-go, over /dev/net/tun
	backendNone                       // neither is available
)

func (b wgBackend) String() string {
	switch b {
	case backendKernel:
		return "kernel"
	case backendUserspace:
		return "userspace (wireguard-go)"
	}
	return "none"
}

// uapiDir is where wireguard-go puts its control sockets. It is not
// configurable in wireguard-go, so it is not configurable here either.
const uapiDir = "/var/run/wireguard"

// detectBackend works out how tunnels can be made, by trying rather than by
// guessing. Reading /sys/module/wireguard would miss a module that is present
// but unusable, and modprobe output varies by distribution; creating an
// interface and deleting it again answers the question exactly.
func detectBackend() wgBackend {
	probe := "bvprobe0"
	quiet(5*time.Second, "ip", "link", "del", probe)
	if _, err := run(5*time.Second, "ip", "link", "add", probe, "type", "wireguard"); err == nil {
		quiet(5*time.Second, "ip", "link", "del", probe)
		return backendKernel
	}
	if _, err := exec.LookPath("wireguard-go"); err != nil {
		return backendNone
	}
	// /dev/net/tun may exist only after the module is loaded, which is the
	// normal state of affairs on a NAS: the device node appears on demand.
	if !tunAvailable() {
		quiet(10*time.Second, "modprobe", "tun")
	}
	if !tunAvailable() {
		return backendNone
	}
	return backendUserspace
}

func tunAvailable() bool {
	st, err := os.Stat("/dev/net/tun")
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// upUserspace brings a tunnel up without the kernel module: spawn wireguard-go
// for the interface, configure it over the control socket, then address and
// route it the way wg-quick would have.
//
// This deliberately does NOT use wg-quick even when it exists. wg-quick would
// install its own default route and fwmark rules and then fight this gateway
// for the routing table - the same reason `Table = off` is demanded of every
// config file.
func upUserspace(t *Tunnel) error {
	conf, err := os.ReadFile(t.ConfigPath)
	if err != nil {
		return err
	}
	iface := t.Name

	// wireguard-go daemonises and creates /var/run/wireguard/<iface>.sock.
	if _, err := run(30*time.Second, "wireguard-go", iface); err != nil {
		return fmt.Errorf("wireguard-go could not create %s: %v", iface, err)
	}

	sock := uapiSocket(iface)
	if err := waitForSocket(sock, 10*time.Second); err != nil {
		return err
	}

	settings, addrs, err := uapiFromConfig(string(conf))
	if err != nil {
		return err
	}
	if err := uapiSet(sock, settings); err != nil {
		return err
	}

	// The address and MTU are wg-quick's job, and it is not running.
	for _, a := range addrs {
		if _, err := run(5*time.Second, "ip", "addr", "add", a, "dev", iface); err != nil {
			return fmt.Errorf("could not address %s: %v", iface, err)
		}
	}
	if _, err := run(5*time.Second, "ip", "link", "set", "mtu", "1420", "up", "dev", iface); err != nil {
		return fmt.Errorf("could not bring %s up: %v", iface, err)
	}
	return nil
}

func downUserspace(t *Tunnel) {
	// Removing the interface makes wireguard-go exit on its own.
	quiet(10*time.Second, "ip", "link", "del", t.Name)
	_ = os.Remove(uapiSocket(t.Name))
}

func uapiSocket(iface string) string { return uapiDir + "/" + iface + ".sock" }

func waitForSocket(path string, within time.Duration) error {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("wireguard-go did not create %s", path)
}

// uapiFromConfig turns a wg-quick style config into the cross-platform UAPI
// text wireguard-go expects, plus the addresses to put on the interface.
//
// The conversion that catches people: a .conf carries base64 keys, and UAPI
// takes HEX. Feeding base64 straight through is accepted as a malformed key and
// the tunnel silently never handshakes.
func uapiFromConfig(conf string) (settings string, addrs []string, err error) {
	var b strings.Builder
	inPeer := false

	for _, raw := range strings.Split(conf, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.EqualFold(line, "[peer]") {
			b.WriteString("public_key=") // filled by the PublicKey line below
			inPeer = true
			continue
		}
		if strings.HasPrefix(line, "[") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)

		switch key {
		case "privatekey":
			h, err := keyToHex(val)
			if err != nil {
				return "", nil, fmt.Errorf("PrivateKey: %v", err)
			}
			b.WriteString("private_key=" + h + "\n")
		case "listenport":
			if _, err := strconv.Atoi(val); err == nil {
				b.WriteString("listen_port=" + val + "\n")
			}
		case "address":
			for _, a := range strings.Split(val, ",") {
				a = strings.TrimSpace(a)
				if a != "" {
					addrs = append(addrs, a)
				}
			}
		case "publickey":
			h, err := keyToHex(val)
			if err != nil {
				return "", nil, fmt.Errorf("PublicKey: %v", err)
			}
			b.WriteString(h + "\n")
		case "presharedkey":
			h, err := keyToHex(val)
			if err != nil {
				return "", nil, fmt.Errorf("PresharedKey: %v", err)
			}
			b.WriteString("preshared_key=" + h + "\n")
		case "endpoint":
			ep, err := resolveEndpoint(val)
			if err != nil {
				return "", nil, err
			}
			b.WriteString("endpoint=" + ep + "\n")
		case "allowedips":
			for _, a := range strings.Split(val, ",") {
				a = strings.TrimSpace(a)
				if a != "" {
					b.WriteString("allowed_ip=" + a + "\n")
				}
			}
		case "persistentkeepalive":
			if _, err := strconv.Atoi(val); err == nil {
				b.WriteString("persistent_keepalive_interval=" + val + "\n")
			}
		}
	}
	if !inPeer {
		return "", nil, fmt.Errorf("config has no [Peer] section")
	}
	return b.String(), addrs, nil
}

// resolveEndpoint turns host:port into address:port. UAPI will not resolve
// names, and a provider config that names a host would otherwise produce a
// tunnel that never connects and never says why.
func resolveEndpoint(ep string) (string, error) {
	host, port, err := net.SplitHostPort(ep)
	if err != nil {
		return "", fmt.Errorf("Endpoint %q: %v", ep, err)
	}
	if net.ParseIP(host) != nil {
		return ep, nil
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("Endpoint %q does not resolve: %v", ep, err)
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			return net.JoinHostPort(v4.String(), port), nil
		}
	}
	return net.JoinHostPort(ips[0].String(), port), nil
}

func keyToHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", fmt.Errorf("not valid base64")
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("expected a 32-byte key, got %d bytes", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

// uapiSet writes a configuration to wireguard-go's control socket.
func uapiSet(sock, settings string) error {
	c, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		return fmt.Errorf("could not reach wireguard-go at %s: %v", sock, err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))

	if _, err := fmt.Fprintf(c, "set=1\n%s\n", settings); err != nil {
		return err
	}
	buf := make([]byte, 4096)
	n, err := c.Read(buf)
	if err != nil {
		return err
	}
	// The reply is errno=0 on success. Anything else is the reason.
	reply := strings.TrimSpace(string(buf[:n]))
	if !strings.Contains(reply, "errno=0") {
		return fmt.Errorf("wireguard-go rejected the configuration: %s", reply)
	}
	return nil
}

// uapiHandshakes reads last-handshake times from every userspace tunnel, in the
// same shape readHandshakes gets from `wg show all latest-handshakes`.
func uapiHandshakes() map[string]int64 {
	out := map[string]int64{}
	entries, err := os.ReadDir(uapiDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".sock")
		if name == e.Name() {
			continue
		}
		if ts := uapiLastHandshake(uapiDir + "/" + e.Name()); ts > 0 {
			out[name] = ts
		}
	}
	return out
}

func uapiLastHandshake(sock string) int64 {
	c, err := net.DialTimeout("unix", sock, 3*time.Second)
	if err != nil {
		return 0
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprint(c, "get=1\n\n"); err != nil {
		return 0
	}
	buf := make([]byte, 16384)
	n, _ := c.Read(buf)
	var latest int64
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "last_handshake_time_sec="); ok {
			if ts, err := strconv.ParseInt(v, 10, 64); err == nil && ts > latest {
				latest = ts
			}
		}
	}
	return latest
}
