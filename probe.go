package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The probe namespace is a stand-in client.
//
// A leak test that probes from the HOST proves nothing: the kill switch lives in
// a FORWARD-side chain and never sees host-originated traffic, so the host can
// reach the internet perfectly legitimately while every client is correctly
// sealed. To test the rules that actually matter, the probe has to enter the box
// the way a container does.
//
// So we build one: a veth pair with one end on the same bridge the clients sit
// on and the other in a throwaway network namespace holding an address inside
// the clients subnet. Kernel-wise that is indistinguishable from a container,
// which means it is matched by exactly the same rules - and it needs no
// container runtime, so the test works on any host.
const (
	probeNS = "bondvpn-probe"
	// Interface names are capped at 15 characters by the kernel, so these stay
	// short rather than matching the namespace name.
	probeVeth = "bvprobe"
	probePeer = "bvprobe-ns"
)

type probeEnv struct {
	addr   string
	bridge string
}

// setupProbe builds the namespace. Always pair with teardownProbe.
func setupProbe(cfg *Config) (*probeEnv, error) {
	bridge, gw, prefix, err := clientBridge(cfg.Clients)
	if err != nil {
		return nil, err
	}
	addr, err := freeClientAddr(cfg.Clients, gw)
	if err != nil {
		return nil, err
	}

	teardownProbe() // clear anything a previous crashed run left behind

	steps := [][]string{
		{"netns", "add", probeNS},
		{"link", "add", probeVeth, "type", "veth", "peer", "name", probePeer},
		{"link", "set", probeVeth, "master", bridge},
		{"link", "set", probeVeth, "up"},
		{"link", "set", probePeer, "netns", probeNS},
	}
	for _, args := range steps {
		if _, err := run(10*time.Second, "ip", args...); err != nil {
			teardownProbe()
			return nil, err
		}
	}
	inNS := [][]string{
		{"netns", "exec", probeNS, "ip", "link", "set", "lo", "up"},
		{"netns", "exec", probeNS, "ip", "addr", "add",
			fmt.Sprintf("%s/%d", addr, prefix), "dev", probePeer},
		{"netns", "exec", probeNS, "ip", "link", "set", probePeer, "up"},
		{"netns", "exec", probeNS, "ip", "route", "add", "default", "via", gw},
	}
	for _, args := range inNS {
		if _, err := run(10*time.Second, "ip", args...); err != nil {
			teardownProbe()
			return nil, err
		}
	}

	// The namespace gets the same resolver the clients are given, so the DNS
	// probe exercises the real path rather than the host's resolver.
	dns := cfg.DNS
	if dns == "" {
		dns = "1.1.1.1"
	}
	dir := filepath.Join("/etc/netns", probeNS)
	if err := os.MkdirAll(dir, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(dir, "resolv.conf"),
			[]byte("nameserver "+dns+"\n"), 0o644)
	}

	return &probeEnv{addr: addr, bridge: bridge}, nil
}

func teardownProbe() {
	quiet(10*time.Second, "ip", "netns", "del", probeNS)
	quiet(10*time.Second, "ip", "link", "del", probeVeth)
	_ = os.RemoveAll(filepath.Join("/etc/netns", probeNS))
}

// probe returns true if the target was REACHED.
//
// Classification is on the EXIT STATUS, never the output. curl asked for a
// status code prints "000" when it never connected AND exits non-zero, so
// treating the printed text as the answer turns every blocked probe into a
// mangled string - which on the prototype was read as nine leaks that did not
// exist and cost an afternoon.
func (p *probeEnv) probe(target string) bool {
	if host, ok := strings.CutPrefix(target, "icmp:"); ok {
		_, err := run(15*time.Second, "ip", "netns", "exec", probeNS,
			"ping", "-c", "1", "-W", "3", host)
		return err == nil
	}
	_, err := run(20*time.Second, "ip", "netns", "exec", probeNS,
		"curl", "-sS", "--max-time", "8", "-o", "/dev/null", target)
	return err == nil
}

// clientBridge finds the interface holding the clients subnet, which is the
// bridge the containers are attached to, plus its address and prefix length.
func clientBridge(clients string) (iface, gw string, prefix int, err error) {
	_, want, err := net.ParseCIDR(clients)
	if err != nil {
		return "", "", 0, err
	}
	ones, _ := want.Mask.Size()

	ifaces, err := net.Interfaces()
	if err != nil {
		return "", "", 0, err
	}
	for _, i := range ifaces {
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			if want.Contains(ipn.IP) {
				return i.Name, ipn.IP.String(), ones, nil
			}
		}
	}
	return "", "", 0, fmt.Errorf(
		"no interface holds an address in the clients subnet %s - "+
			"is the container network up?", clients)
}

// freeClientAddr picks an address in the clients subnet that nothing answers on.
//
// It works DOWN from the top of the range because container runtimes allocate
// upward from the bottom, and it pings each candidate rather than trusting the
// arithmetic: stealing an address a running container already holds would break
// that container's networking for the duration of the test.
func freeClientAddr(clients, gw string) (string, error) {
	_, netw, err := net.ParseCIDR(clients)
	if err != nil {
		return "", err
	}
	ones, bits := netw.Mask.Size()
	if bits-ones < 2 {
		return "", fmt.Errorf("clients subnet %s is too small to hold a probe address", clients)
	}

	base := netw.IP.To4()
	if base == nil {
		return "", fmt.Errorf("clients subnet %s is not IPv4", clients)
	}
	size := uint32(1) << uint(bits-ones)
	start := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])

	host, _ := hostAddrs()
	taken := map[string]bool{gw: true}
	for _, ip := range host {
		taken[ip.String()] = true
	}

	// Skip the broadcast address; try the eight below it.
	for off := size - 2; off > 0 && off > size-10; off-- {
		v := start + off
		cand := net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).String()
		if taken[cand] {
			continue
		}
		if _, err := run(5*time.Second, "ping", "-c", "1", "-W", "1", cand); err == nil {
			continue // something is already there
		}
		return cand, nil
	}
	return "", fmt.Errorf("no free address available in %s for the probe", clients)
}
