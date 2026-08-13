package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The whole point of `check`: a config with several mistakes reports ALL of
// them, so fixing it is one edit rather than one edit per run.
func TestCheckReportsEveryProblemAtOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	// Four separate mistakes: no tunnels, a bad subnet, a pinned client that
	// cannot be inside it, and no dns.
	body := "clients: 172.20.0.1\nroutes:\n  pin:\n    - 10.0.0.5\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := cmdCheck(path, &out); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	got := out.String()
	for _, want := range []string{
		"no tunnels configured",
		"is not a CIDR",
		"dns is required",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("check output does not mention %q\n---\n%s", want, got)
		}
	}
	// Previously `dns is required` fired alone and hid the rest; if only one
	// FAIL line appears, that regression is back.
	if n := strings.Count(got, "FAIL"); n < 3 {
		t.Errorf("only %d FAIL lines - check should report every problem at once\n---\n%s", n, got)
	}
}

// A file that does not parse leaves nothing to check, and saying so beats a
// page of noise about a config nobody has written yet.
func TestCheckStopsWhenTheFileCannotBeRead(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.yml")
	var out bytes.Buffer
	if code := cmdCheck(missing, &out); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	got := out.String()
	// The path, not the OS's wording for it: "no such file" on Linux is "The
	// system cannot find the file specified" on the machine this is developed on.
	if !strings.Contains(got, missing) {
		t.Errorf("expected the missing file to be named:\n%s", got)
	}
	if !strings.Contains(got, "Nothing else can be checked") {
		t.Errorf("expected check to say why it stopped:\n%s", got)
	}
}

// The mistake that strands a remote box. It has to be reported by `check`, not
// discovered by running the daemon and losing the SSH session.
func TestCheckCatchesTheLockYourselfOutSubnet(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("the address checks need root")
	}
	addrs, err := hostAddrs()
	if err != nil || len(addrs) == 0 {
		t.Skip("no usable host address to build the case from")
	}
	own := addrs[0].String()
	subnet := own[:strings.LastIndex(own, ".")] + ".0/24"

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	wg := filepath.Join(dir, "wg0.conf")
	if err := os.WriteFile(wg, []byte("[Interface]\nTable = off\n[Peer]\nPersistentKeepalive = 25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := "tunnels:\n  - " + wg + "\nclients: " + subnet + "\ndns: 10.64.0.1\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := cmdCheck(path, &out)
	if code != 1 || !strings.Contains(out.String(), "would become unreachable") {
		t.Errorf("check did not refuse the host's own subnet (exit %d):\n%s", code, out.String())
	}
}

// Warnings are advice, not faults: a gateway with an unreadable disk still
// routes, so `check` must not tell people they cannot start.
func TestCheckWarningsDoNotFailTheRun(t *testing.T) {
	dir := t.TempDir()
	wg := filepath.Join(dir, "wg0.conf")
	// No PersistentKeepalive: a warning in checkTunnelConfigs, not a failure.
	if err := os.WriteFile(wg, []byte("[Interface]\nTable = off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yml")
	body := "tunnels:\n  - " + wg + "\nclients: 172.20.0.0/24\ndns: 10.64.0.1\n" +
		"disks:\n  - library /definitely/not/mounted\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := cmdCheck(path, &out)
	got := out.String()
	if !strings.Contains(got, "warn") {
		t.Errorf("expected warnings:\n%s", got)
	}
	if strings.Contains(got, "FAIL") && code == 0 {
		t.Errorf("inconsistent: FAIL lines but exit 0\n%s", got)
	}
	if code == 0 && !strings.Contains(got, "Safe to start") {
		t.Errorf("a warning-only run should still say it is safe to start:\n%s", got)
	}
}

// Validate now joins every problem; the single-problem case must stay readable
// rather than turning into a list of one.
func TestValidateFormatsOneProblemPlainly(t *testing.T) {
	c := defaultConfig()
	c.Tunnels = []string{"a.conf"}
	c.Clients = "172.20.0.0/24"
	err := c.Validate() // only dns missing
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "problems with this configuration") {
		t.Errorf("a single problem should not be wrapped in a list: %q", err)
	}

	c.Tunnels = nil
	err = c.Validate() // now dns AND tunnels
	if !strings.Contains(err.Error(), "2 problems") {
		t.Errorf("expected both problems counted, got %q", err)
	}
}

// A malformed LINE used to abort the parser, so the first bad line was the only
// thing anyone was told about - the exact failure `check` exists to prevent.
func TestCheckReportsSyntaxAndMeaningTogether(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	body := "clients: 172.20.0.0/24\n" +
		"dashbord: true\n" + // unknown setting
		"dashboard: maybe\n" + // not a boolean
		"disks:\n  - staging\n" // missing path
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := cmdCheck(path, &out); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	got := out.String()
	for _, want := range []string{
		`unknown setting "dashbord"`,
		`expected true or false, got "maybe"`,
		"disks want 'label /path'",
		"no tunnels configured", // a meaning problem, past all three syntax ones
		"dns is required",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("check output does not mention %q\n---\n%s", want, got)
		}
	}
}
