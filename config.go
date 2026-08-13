package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the whole of the user-facing surface. Everything the prototype
// hardcoded lives here, because hardcoding any of it is what broke that system
// when a router was replaced and the LAN moved to a different subnet.
type Config struct {
	Tunnels []string // paths to WireGuard configs, 1..MaxTunnels

	// TunnelDir is a directory whose *.conf files are tunnels, re-read on every
	// pass. It is what lets a config be added by dropping in a file or
	// uploading one from the settings page, with no restart and no editing of
	// this file - the difference between a gateway an engineer configures and
	// an appliance somebody uses.
	TunnelDir string

	// path is where this config was loaded from. The admin token lives beside
	// it, so it has to be remembered rather than re-derived.
	path string

	Clients string // the container subnet this gateway serves

	// Per-client routing. Anything inside Clients not named here uses Default.
	Default RouteMode
	Pinned  []string
	Bonded  []string

	LAN        string // "auto" or a CIDR
	WANIface   string // "auto" or an interface name
	DNS        string // resolver forced on clients; must be tunnel-only
	StaleAfter time.Duration
	Listen     string

	// UpdateCheck fetches a signed file once an hour and reports whether a newer
	// release exists. On by default, and off with one line - a check you cannot
	// decline is telemetry wearing a hat. Nothing in the daemon acts on the
	// result: it cannot stop, block or degrade anything.
	UpdateCheck bool

	// Dashboard serves the built-in interface at the root of Listen. On by
	// default: a gateway you cannot see the state of is the thing this product
	// exists to fix. Turn it off and the binary serves only /status and
	// /healthz, which is what an installation with its own front end wants.
	Dashboard bool

	parseErrs []string

	// Disks are the filesystems the machine panel watches. Optional, and named
	// by the user because only they know which mount is the staging disk and
	// which is the library - guessing from /proc/mounts would put the docker
	// overlay on screen and leave the NFS export off it.
	Disks []Disk
}

type Disk struct {
	Label string
	Path  string
}

// parseErrs are the line-level complaints collected while reading the file.
// Kept separate from Problems() because they are about syntax rather than about
// what the settings mean, and `check` prints them under their own heading.
func (c *Config) ParseProblems() []string { return c.parseErrs }

type RouteMode string

const (
	// ModePin gives a client one tunnel and keeps it there. Correct for
	// BitTorrent: the protocol ties identity to the IP, so spreading one
	// client's connections across tunnels leaves trackers and peers holding an
	// address you are not reliably at. Measured: 1 connected seed at 0 B/s
	// bonded, versus 7 seeds at 2.05 MB/s pinned, against the same swarms.
	ModePin RouteMode = "pin"

	// ModeBond spreads a client's connections across every live tunnel. Correct
	// for usenet and HTTP, where many independent connections to one server
	// each authenticate separately and a rotating source costs nothing.
	ModeBond RouteMode = "bond"
)

// MaxTunnels is a ceiling, not a target. Five is Mullvad's per-account device
// limit, so five tunnels consumes the whole account and leaves nothing for a
// phone or laptop. The shipped example uses three deliberately.
const MaxTunnels = 5

// DefaultTunnels is what the example config ships with.
const DefaultTunnels = 3

func defaultConfig() *Config {
	return &Config{
		Default:     ModeBond,
		LAN:         "auto",
		WANIface:    "auto",
		StaleAfter:  180 * time.Second,
		Listen:      "127.0.0.1:8099",
		UpdateCheck: true,
		Dashboard:   true,
	}
}

// LoadConfig reads the config file. The format is a small, deliberate subset of
// YAML - scalars and "- " lists - parsed here rather than pulled from a module
// so the binary has no third-party code in it at all. For something that runs
// as root and holds VPN keys, an empty dependency tree is worth more than
// support for YAML's exotic corners.
func LoadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := defaultConfig()
	cfg.path = path
	var section string
	var perr []string

	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Text()
		text := strings.TrimSpace(raw)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}

		// A list item belongs to whatever key last opened a section.
		if strings.HasPrefix(text, "- ") {
			val := strings.TrimSpace(strings.TrimPrefix(text, "- "))
			val = trimComment(val)
			switch section {
			case "tunnels":
				cfg.Tunnels = append(cfg.Tunnels, val)
			case "pin":
				cfg.Pinned = append(cfg.Pinned, val)
			case "bond":
				cfg.Bonded = append(cfg.Bonded, val)
			case "disks":
				// "label /path" - the label is what the panel prints, so it is
				// the user's word for the mount rather than a derived one.
				name, path, ok := strings.Cut(val, " ")
				if !ok {
					perr = append(perr, fmt.Sprintf("line %d: disks want 'label /path'", line))
					continue
				}
				cfg.Disks = append(cfg.Disks, Disk{
					Label: strings.TrimSpace(name),
					Path:  strings.TrimSpace(path),
				})
			default:
				perr = append(perr, fmt.Sprintf("line %d: list item outside a known section", line))
			}
			continue
		}

		key, val, ok := strings.Cut(text, ":")
		if !ok {
			perr = append(perr, fmt.Sprintf("line %d: expected 'key: value'", line))
			continue
		}
		key = strings.TrimSpace(key)
		val = trimComment(strings.TrimSpace(val))

		// An empty value opens a section for the list items that follow.
		if val == "" {
			switch key {
			case "tunnels", "pin", "bond", "routes", "disks":
				section = key
			default:
				section = key
			}
			continue
		}
		section = ""

		switch key {
		case "clients":
			cfg.Clients = val
		case "default":
			cfg.Default = RouteMode(val)
		case "lan":
			cfg.LAN = val
		case "wan_interface":
			cfg.WANIface = val
		case "tunnel_dir":
			cfg.TunnelDir = val
		case "dns":
			cfg.DNS = val
		case "listen":
			cfg.Listen = val
		case "update_check":
			b, err := parseBool(val)
			if err != nil {
				perr = append(perr, fmt.Sprintf("line %d: %v", line, err))
				continue
			}
			cfg.UpdateCheck = b
		case "dashboard":
			b, err := parseBool(val)
			if err != nil {
				perr = append(perr, fmt.Sprintf("line %d: %v", line, err))
				continue
			}
			cfg.Dashboard = b
		case "stale_handshake":
			d, err := parseDuration(val)
			if err != nil {
				perr = append(perr, fmt.Sprintf("line %d: %v", line, err))
				continue
			}
			cfg.StaleAfter = d
		default:
			perr = append(perr, fmt.Sprintf("line %d: unknown setting %q", line, key))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// The config is returned even when it is wrong. `check` needs a partially
	// parsed document to go on reporting from - returning nil here is what made
	// the first malformed line the ONLY thing anyone was told about.
	cfg.parseErrs = perr
	return cfg, cfg.Validate()
}

// parseBool accepts what people actually write in a config file. Anything else
// is an error rather than a silent false - a misspelled "ture" quietly turning
// a feature off is the kind of thing nobody finds for months.
func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "on", "1":
		return true, nil
	case "false", "no", "off", "0":
		return false, nil
	}
	return false, fmt.Errorf("expected true or false, got %q", s)
}

func trimComment(s string) string {
	if i := strings.Index(s, " #"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(strings.Trim(s, `"'`))
}

func parseDuration(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	// Bare number means seconds, which is what people write.
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return 0, fmt.Errorf("cannot read %q as a duration", s)
}

// Validate refuses configurations that would break the box rather than merely
// misbehave. Each rule here is a failure that actually happened.
// Problems lists everything wrong with this config, rather than stopping at the
// first thing.
//
// Reporting one error at a time makes fixing a config an edit-run-edit-run
// cycle, and the order is not severity: with `dns` unset, that message used to
// fire before the missing tunnels and before the subnet that would lock you out
// of your own box, hiding both. Whoever is editing this file deserves the whole
// list in one go.
func (c *Config) Problems() []string {
	var p []string
	// A tunnel_dir counts as configuring tunnels even when it is empty right
	// now: an appliance is meant to start with none and have them added from
	// the settings page, and refusing to start would leave the user with no
	// page to add them from.
	if len(c.Tunnels) == 0 && c.TunnelDir == "" {
		p = append(p, "no tunnels configured - set tunnel_dir, or list at least one WireGuard config")
	}
	if len(c.Tunnels) > MaxTunnels {
		p = append(p, fmt.Sprintf("%d tunnels configured, the maximum is %d",
			len(c.Tunnels), MaxTunnels))
	}

	var clientNet *net.IPNet
	switch {
	case c.Clients == "":
		p = append(p, "clients subnet is required")
	default:
		_, n, err := net.ParseCIDR(c.Clients)
		if err != nil {
			p = append(p, fmt.Sprintf("clients %q is not a CIDR: %v", c.Clients, err))
		} else {
			clientNet = n
		}
	}

	switch c.Default {
	case ModePin, ModeBond:
	default:
		p = append(p, fmt.Sprintf("default route mode %q must be %q or %q",
			c.Default, ModeBond, ModePin))
	}

	for _, ip := range append(append([]string{}, c.Pinned...), c.Bonded...) {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			p = append(p, fmt.Sprintf("%q is not an IP address", ip))
			continue
		}
		// Only meaningful once the subnet itself parsed; otherwise every client
		// would be reported as outside a subnet that does not exist.
		if clientNet != nil && !clientNet.Contains(parsed) {
			p = append(p, fmt.Sprintf("%s is outside the clients subnet %s", ip, c.Clients))
		}
	}

	if c.LAN != "auto" {
		if _, _, err := net.ParseCIDR(c.LAN); err != nil {
			p = append(p, fmt.Sprintf("lan %q must be \"auto\" or a CIDR: %v", c.LAN, err))
		}
	}

	// Required, not optional. Every client's DNS is redirected to this address
	// over the tunnels; leaving it unset means containers resolve however they
	// happen to be configured, which leaks every hostname they look up while
	// their traffic stays correctly tunnelled and nothing looks wrong.
	switch {
	case c.DNS == "":
		p = append(p, "dns is required - set your provider's resolver "+
			"(Mullvad's is 10.64.0.1); client DNS is redirected there over the tunnels")
	case net.ParseIP(c.DNS) == nil:
		p = append(p, fmt.Sprintf("dns %q is not an IP address", c.DNS))
	}

	if c.Listen != "" {
		if _, _, err := net.SplitHostPort(c.Listen); err != nil {
			p = append(p, fmt.Sprintf("listen %q must be host:port: %v", c.Listen, err))
		}
	}

	for _, d := range c.Disks {
		if d.Label == "" || d.Path == "" {
			p = append(p, fmt.Sprintf("disk %q needs both a label and a path", d.Label+" "+d.Path))
		}
	}
	return p
}

func (c *Config) Validate() error {
	// Syntax first, then meaning. A line the parser could not read is the reason
	// the settings below it look wrong, so it belongs at the top of the list.
	p := append(append([]string{}, c.parseErrs...), c.Problems()...)
	switch len(p) {
	case 0:
		return nil
	case 1:
		return errors.New(p[0])
	default:
		return fmt.Errorf("%d problems with this configuration:\n  - %s",
			len(p), strings.Join(p, "\n  - "))
	}
}

// GuardAgainstSelf refuses to start when the clients subnet is the network this
// host is MANAGED on - so `mgmt` is the addresses of the WAN interface, not
// every address the host holds.
//
// The distinction matters and a live box is what taught it. The host ALWAYS
// holds an address inside the clients subnet: it is the container bridge's
// gateway, which is how the containers reach anything at all. Refusing on that
// address refuses every correct configuration. The catastrophe actually worth
// guarding against is different and specific - pointing clients at the LAN,
// which policy-routes the host's own replies into a tunnel. Inbound still
// arrives, replies vanish, and the box looks dead while being perfectly
// healthy. With no console attached that is an expensive afternoon, so it is
// refused outright rather than warned about.
func (c *Config) GuardAgainstSelf(mgmt []net.IP) error {
	_, clientNet, err := net.ParseCIDR(c.Clients)
	if err != nil {
		return err
	}
	for _, ip := range mgmt {
		if clientNet.Contains(ip) {
			return fmt.Errorf(
				"refusing to start: clients subnet %s contains this host's management "+
					"address %s - the host's own replies would be routed into a tunnel "+
					"and the box would become unreachable", c.Clients, ip)
		}
	}
	// The same failure reached from the other direction: an overlap means the
	// LAN exception and the client rules are describing the same addresses.
	if c.LAN != "auto" {
		if _, lanNet, err := net.ParseCIDR(c.LAN); err == nil {
			if clientNet.Contains(lanNet.IP) || lanNet.Contains(clientNet.IP) {
				return fmt.Errorf(
					"refusing to start: clients subnet %s overlaps the LAN %s - "+
						"the containers must sit on their own network", c.Clients, c.LAN)
			}
		}
	}
	return nil
}

// ModeFor reports how a given client address should be routed.
func (c *Config) ModeFor(ip string) RouteMode {
	for _, p := range c.Pinned {
		if p == ip {
			return ModePin
		}
	}
	for _, b := range c.Bonded {
		if b == ip {
			return ModeBond
		}
	}
	return c.Default
}
