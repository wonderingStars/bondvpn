package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// A tunnel directory is what makes "drop your provider's files in and you are
// done" true.
//
// Listing every config by hand in the config file is fine for a gateway someone
// built deliberately. It is the wrong shape for a NAS appliance, where the whole
// promise is that adding a tunnel means putting a file somewhere - or uploading
// it from the settings page - and nothing else. The directory is re-read on
// every pass of the run loop, so a config added at 11:00 is carrying traffic by
// 11:00:15 without a restart.

// tunnelPaths is the list the rest of the daemon should use: the explicit
// `tunnels:` entries first, then anything in `tunnel_dir`, deduplicated and
// capped.
//
// Explicit entries come first so that a hand-built gateway keeps its interface
// naming and ordering stable when someone later points a directory at it.
func (c *Config) tunnelPaths() []string {
	seen := map[string]bool{}
	var out []string

	add := func(p string) {
		if p == "" || seen[p] || len(out) >= MaxTunnels {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	for _, p := range c.Tunnels {
		add(p)
	}
	for _, p := range c.dirTunnels() {
		add(p)
	}
	return out
}

// dirTunnels lists *.conf in the tunnel directory, sorted, so interface
// assignment is stable between runs. An unreadable or missing directory is not
// an error here: it is reported once by `check` and by status, and a transient
// failure must not tear down tunnels that are already up.
func (c *Config) dirTunnels() []string {
	if c.TunnelDir == "" {
		return nil
	}
	entries, err := os.ReadDir(c.TunnelDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		out = append(out, filepath.Join(c.TunnelDir, e.Name()))
	}
	sort.Strings(out)
	return out
}

// safeName is the only naming rule an uploaded file gets to influence.
//
// Two independent reasons this is strict rather than polite:
//
//   - The name becomes a path under the tunnel directory. "../../etc/cron.d/x"
//     arriving as a filename in a multipart upload is the oldest trick there is,
//     and this daemon runs as root.
//   - The name becomes the network interface name, and Linux caps that at 15
//     characters. Mullvad's own download is called something like
//     "mullvad-gb-lon-wg-001.conf", which is over the limit - so a config that
//     looks perfectly good silently fails to come up.
//
// Anything unusable is rejected with a reason rather than quietly mangled: a
// tunnel that came up under a name the user did not choose is worse than one
// that refused to be added.
var safeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

const maxIfaceName = 15

func safeName(uploaded string) (string, error) {
	// Take the base name only. Browsers send bare filenames, but nothing stops
	// a hand-crafted request from sending a path, and filepath.Base is what
	// makes "../.." inert rather than clever.
	name := filepath.Base(filepath.ToSlash(uploaded))
	name = strings.TrimSuffix(name, ".conf")
	name = strings.ToLower(strings.TrimSpace(name))
	// Providers use dots and spaces freely; those are fine to translate because
	// the result is still recognisably the file the user uploaded.
	name = strings.NewReplacer(" ", "-", ".", "-").Replace(name)

	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("that file has no usable name")
	}
	if !safeNameRe.MatchString(name) {
		return "", fmt.Errorf("name %q must be letters, digits, dashes or underscores", name)
	}
	if len(name) > maxIfaceName {
		return "", fmt.Errorf("name %q is %d characters; Linux interface names stop at %d, so rename it to something shorter",
			name, len(name), maxIfaceName)
	}
	return name + ".conf", nil
}
