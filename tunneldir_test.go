package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The NAS build tells people to drop .conf files into a shared folder over SMB.
// Those arrive exactly as the provider wrote them - a DNS line wg-quick would
// apply to the whole machine, and no `Table = off`, so the tunnel comes up,
// handshakes and carries nothing. The folder route must behave like the upload
// route or the instruction is a lie.
func TestFilesDroppedInTheFolderAreNormalised(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wg0.conf"), []byte(sampleWG), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.Tunnels = nil
	cfg.TunnelDir = dir

	paths := cfg.tunnelPaths()
	if len(paths) != 1 {
		t.Fatalf("found %d tunnels, want 1", len(paths))
	}

	body, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, line := range strings.Split(got, "\n") {
		if settingKey(line) == "dns" {
			t.Errorf("the DNS line survived: %q", line)
		}
	}
	if !strings.Contains(got, "Table = off") {
		t.Error("Table = off was not added, so the tunnel would carry nothing")
	}

	// The interface name must still come from the user's filename.
	if ifaceNameFor(paths[0]) != "wg0" {
		t.Errorf("interface is %q, want wg0", ifaceNameFor(paths[0]))
	}

	// And their own file must be untouched.
	orig, err := os.ReadFile(filepath.Join(dir, "wg0.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(orig) != sampleWG {
		t.Error("the user's own file was modified")
	}
}

// Rewriting the copy on every pass would churn the disk every fifteen seconds
// for the life of the daemon.
func TestNormalisedCopyIsNotRewrittenEveryPass(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "wg0.conf")
	if err := os.WriteFile(src, []byte(sampleWG), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.Tunnels = nil
	cfg.TunnelDir = dir

	first := cfg.tunnelPaths()
	info1, err := os.Stat(first[0])
	if err != nil {
		t.Fatal(err)
	}

	cfg.tunnelPaths() // a second pass
	info2, err := os.Stat(first[0])
	if err != nil {
		t.Fatal(err)
	}
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Error("the working copy was rewritten on an unchanged pass")
	}
}

// A file the daemon cannot use must be skipped and SAID, not silently ignored:
// a config sitting in the folder doing nothing, with no explanation anywhere,
// is the worst outcome for someone who thinks they have set it up.
func TestUnusableFilesAreSkippedNotFatal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shopping-list.conf"), []byte("milk, eggs"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "good.conf"), []byte(sampleWG), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.Tunnels = nil
	cfg.TunnelDir = dir

	paths := cfg.tunnelPaths()
	if len(paths) != 1 {
		t.Fatalf("got %d usable tunnels, want 1 (the rubbish one must be skipped, the good one kept)", len(paths))
	}
	if ifaceNameFor(paths[0]) != "good" {
		t.Errorf("kept %q, want good", ifaceNameFor(paths[0]))
	}
}

// The working directory lives inside the tunnel directory, so it must not be
// mistaken for a tunnel itself.
func TestWorkingCopiesAreNotPickedUpAsTunnels(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wg0.conf"), []byte(sampleWG), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.Tunnels = nil
	cfg.TunnelDir = dir

	cfg.tunnelPaths() // creates the working directory
	if got := len(cfg.tunnelPaths()); got != 1 {
		t.Errorf("second pass found %d tunnels, want 1 - the working copies are being counted twice", got)
	}
	if got := len(cfg.dirSources()); got != 1 {
		t.Errorf("dirSources found %d, want 1", got)
	}
}

// An appliance is meant to be installed empty and configured afterwards. If an
// empty tunnel directory were a fatal config error, a first install could not
// start the page you add tunnels from.
func TestEmptyTunnelDirIsAValidConfig(t *testing.T) {
	cfg := defaultConfig()
	cfg.Tunnels = nil
	cfg.TunnelDir = t.TempDir()
	cfg.Clients = "10.98.0.0/24"
	cfg.DNS = "10.64.0.1"

	for _, p := range cfg.Problems() {
		if strings.Contains(p, "no tunnels") {
			t.Errorf("an empty tunnel_dir was rejected: %q", p)
		}
	}
}
