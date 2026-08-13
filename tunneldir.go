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

// workingDirName holds the normalised copies. Hidden, and inside the tunnel
// directory so it travels with it and needs no second mount or setting.
const workingDirName = ".bondvpn"

// dirTunnels lists *.conf in the tunnel directory, sorted, so interface
// assignment is stable between runs. An unreadable or missing directory is not
// an error here: it is reported once by `check` and by status, and a transient
// failure must not tear down tunnels that are already up.
//
// Each file is returned as a NORMALISED WORKING COPY rather than the original.
//
// The settings page normalises what it uploads, but a directory can also be
// filled by dropping files into a shared folder over SMB - which is exactly what
// the NAS build tells people to do - and those arrive as the provider wrote
// them: with a DNS line that wg-quick would apply to the whole machine, and
// without `Table = off`, so the tunnel comes up, handshakes and carries nothing.
// Normalising on discovery makes both routes behave the same.
//
// The user's own file is never modified. Anything unusable is skipped with a
// reason in the log rather than silently ignored: a config that is present but
// not running, with nothing said about it, is the worst of both.
func (c *Config) dirTunnels() []string {
	if c.TunnelDir == "" {
		return nil
	}
	entries, err := os.ReadDir(c.TunnelDir)
	if err != nil {
		return nil
	}

	work := filepath.Join(c.TunnelDir, workingDirName)
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		src := filepath.Join(c.TunnelDir, e.Name())
		copyPath, err := normalisedCopy(src, work)
		if err != nil {
			warnOnce(src, err)
			continue
		}
		out = append(out, copyPath)
	}
	sort.Strings(out)
	return out
}

// dirSources lists the user's OWN files - what they dropped in or uploaded -
// as opposed to the normalised copies the daemon runs from. The settings page
// works in these terms: removing a tunnel has to remove the file the user put
// there, not the copy, or it reappears on the next pass.
func (c *Config) dirSources() []string {
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

// normalisedCopy returns the path of a usable copy of src, creating or
// refreshing it only when the original has changed. Rewriting it on every pass
// would churn the disk every fifteen seconds for no reason.
func normalisedCopy(src, workDir string) (string, error) {
	name, err := safeName(filepath.Base(src))
	if err != nil {
		return "", err
	}
	dest := filepath.Join(workDir, name)

	si, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if di, err := os.Stat(dest); err == nil && !si.ModTime().After(di.ModTime()) {
		return dest, nil
	}

	raw, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	normalised, err := normaliseUpload(raw)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return "", err
	}
	// Written and renamed so a pass of the run loop cannot read a half-written
	// config and try to bring it up.
	tmp := dest + ".partial"
	if err := os.WriteFile(tmp, normalised, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return dest, nil
}

// warnOnce keeps a rejected file from filling the log with the same line every
// fifteen seconds, while still saying it once and again if the file changes.
var warned = map[string]string{}

func warnOnce(path string, err error) {
	msg := err.Error()
	if warned[path] == msg {
		return
	}
	warned[path] = msg
	logf("ignoring %s: %s", filepath.Base(path), msg)
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
