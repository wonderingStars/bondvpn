package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"
)

// run executes a host command with a hard deadline.
//
// EVERY external call goes through here, and the timeout is not optional. On
// the prototype an unbounded dhclient call hung the recovery script one line
// after it announced it was recovering, and an unbounded df against a dead NFS
// server hung the status collector so the dashboard simply stopped updating
// with no error anywhere. A bounded failure is reportable; a hang is not.
func run(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return out.String(), fmt.Errorf("%s timed out after %s", name, timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return out.String(), nil
}

// quiet runs a command whose failure is expected and uninteresting - deleting a
// rule that is not there, bringing down an interface that is already down.
func quiet(timeout time.Duration, name string, args ...string) {
	_, _ = run(timeout, name, args...)
}

// hostAddrs returns this machine's own IPv4 addresses, for the self-guard.
func hostAddrs() ([]net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []net.IP
	for _, i := range ifaces {
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if v4 := ipn.IP.To4(); v4 != nil && !v4.IsLoopback() {
					out = append(out, v4)
				}
			}
		}
	}
	return out, nil
}

// ifaceAddrs returns the IPv4 addresses on one interface. The self-guard needs
// exactly this rather than every address on the box: the host legitimately
// holds the container bridge's gateway address inside the clients subnet, so
// only the MANAGEMENT interface's addresses can indicate a misconfiguration.
func ifaceAddrs(name string) ([]net.IP, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}
	var out []net.IP
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if v4 := ipn.IP.To4(); v4 != nil {
				out = append(out, v4)
			}
		}
	}
	return out, nil
}

// defaultRouteIface reports the interface carrying the default route, used when
// wan_interface is "auto". Hardcoding this is one of the things that stranded
// the prototype when its network changed.
func defaultRouteIface() (string, error) {
	out, err := run(5*time.Second, "ip", "route", "show", "default")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "dev" && i+1 < len(fields) {
				return fields[i+1], nil
			}
		}
	}
	return "", fmt.Errorf("no default route found")
}

// ifaceNetwork returns the CIDR network an interface sits on, used when lan is
// "auto". Derived rather than configured so the kill switch's LAN exception
// follows the LAN when a router is replaced - when it did not, every container
// silently lost the NAS while routing and tunnels looked perfectly healthy.
func ifaceNetwork(name string) (string, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return "", err
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.To4() == nil {
			continue
		}
		return (&net.IPNet{IP: ipn.IP.Mask(ipn.Mask), Mask: ipn.Mask}).String(), nil
	}
	return "", fmt.Errorf("no IPv4 address on %s", name)
}

// resolve fills in every "auto" in the config against the live system.
func (c *Config) resolve() error {
	if c.WANIface == "auto" {
		iface, err := defaultRouteIface()
		if err != nil {
			return fmt.Errorf("cannot determine the WAN interface: %v", err)
		}
		c.WANIface = iface
	}
	if c.LAN == "auto" {
		lan, err := ifaceNetwork(c.WANIface)
		if err != nil {
			return fmt.Errorf("cannot determine the LAN from %s: %v", c.WANIface, err)
		}
		c.LAN = lan
	}
	return nil
}
