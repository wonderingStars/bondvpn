package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const sampleWG = `[Interface]
PrivateKey = qJ8Xk3vN2mP5rT7wY1zA4bC6dE9fG0hI2jK4lM6nO8Q=
Address = 10.64.0.2/32
DNS = 10.64.0.1

[Peer]
PublicKey = aB3cD5eF7gH9iJ1kL3mN5oP7qR9sT1uV3wX5yZ7aB9c=
AllowedIPs = 0.0.0.0/0
Endpoint = 185.213.154.66:51820
`

func settingsTestConfig(t *testing.T) (*Config, string) {
	t.Helper()
	dir := t.TempDir()
	tunnelDir := filepath.Join(dir, "wg")
	if err := os.MkdirAll(tunnelDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.Tunnels = nil
	cfg.TunnelDir = tunnelDir
	cfg.path = filepath.Join(dir, "config.yml")
	return cfg, tunnelDir
}

func uploadRequest(t *testing.T, filename, body, token string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("config", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/tunnels", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// An upload endpoint on a daemon running as root, on a port that may be on the
// LAN, must not accept writes from anyone who can reach it. Without a token,
// anyone on the network could add a tunnel pointing at a server they control
// and take every packet the gateway carries.
func TestUploadRequiresTheToken(t *testing.T) {
	cfg, dir := settingsTestConfig(t)
	mux := http.NewServeMux()
	registerSettings(mux, cfg, "the-real-token")

	for _, tc := range []struct{ name, token string }{
		{"no token", ""},
		{"wrong token", "not-the-token"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, uploadRequest(t, "wg0.conf", sampleWG, tc.token))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", tc.name, rec.Code)
		}
	}
	if files, _ := os.ReadDir(dir); len(files) != 0 {
		t.Errorf("an unauthorised upload wrote %d file(s) to disk", len(files))
	}
}

// The uploaded filename becomes a path under the tunnel directory, and this
// daemon runs as root. A filename is attacker-controlled input.
func TestUploadRejectsPathTraversal(t *testing.T) {
	cfg, dir := settingsTestConfig(t)
	mux := http.NewServeMux()
	registerSettings(mux, cfg, "tok")

	for _, name := range []string{
		"../../../etc/cron.d/evil.conf",
		"..%2f..%2fetc%2fpasswd.conf",
		"/etc/systemd/system/evil.conf",
		"....//....//evil.conf",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, uploadRequest(t, name, sampleWG, "tok"))

		// Either refused outright, or reduced to a harmless base name - never
		// written outside the tunnel directory.
		for _, f := range collectFiles(t, dir) {
			if !strings.HasPrefix(f, dir) {
				t.Errorf("%q escaped the tunnel directory: wrote %s", name, f)
			}
		}
		if _, err := os.Stat("/etc/cron.d/evil.conf"); err == nil {
			t.Fatalf("%q wrote outside the tunnel directory", name)
		}
	}
}

func collectFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

// The two edits every provider's config needs, and the reason the "just drop
// the file in" promise can be true at all.
func TestUploadNormalisesAProviderConfig(t *testing.T) {
	cfg, dir := settingsTestConfig(t)
	mux := http.NewServeMux()
	registerSettings(mux, cfg, "tok")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, uploadRequest(t, "wg0.conf", sampleWG, "tok"))
	if rec.Code != http.StatusOK {
		t.Fatalf("upload failed: %d %s", rec.Code, rec.Body.String())
	}

	saved, err := os.ReadFile(filepath.Join(dir, "wg0.conf"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(saved)

	// wg-quick would apply this to the WHOLE machine, breaking name resolution
	// for every other service on a NAS.
	for _, line := range strings.Split(got, "\n") {
		if settingKey(line) == "dns" {
			t.Errorf("the DNS line survived: %q", line)
		}
	}
	// Without this the tunnel comes up, handshakes, and carries nothing.
	if !strings.Contains(got, "Table = off") {
		t.Error("Table = off was not added")
	}
	if !strings.Contains(got, "PersistentKeepalive") {
		t.Error("PersistentKeepalive was not added, so an idle tunnel will be dropped as dead")
	}
	// The parts that matter must survive intact.
	for _, want := range []string{
		"PrivateKey = qJ8Xk3vN2mP5rT7wY1zA4bC6dE9fG0hI2jK4lM6nO8Q=",
		"Endpoint = 185.213.154.66:51820",
		"AllowedIPs = 0.0.0.0/0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("upload lost %q", want)
		}
	}
}

// Table = off must land in [Interface]. In [Peer] it is silently ignored, which
// is the same failure as not adding it at all but much harder to see.
func TestTableOffLandsInTheInterfaceSection(t *testing.T) {
	out, err := normaliseUpload([]byte(sampleWG))
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	iface := strings.Index(text, "[Interface]")
	peer := strings.Index(text, "[Peer]")
	table := strings.Index(text, "Table = off")

	if table < iface || table > peer {
		t.Errorf("Table = off is not inside [Interface] (iface=%d table=%d peer=%d)", iface, table, peer)
	}
}

// Someone uploads the wrong thing from their Downloads folder. Saying so now
// beats a daemon that spends every pass trying to raise an interface from a PDF.
func TestUploadRejectsThingsThatAreNotConfigs(t *testing.T) {
	cases := map[string]string{
		"empty":          "",
		"not a config":   "hello, this is a text file",
		"no peer":        "[Interface]\nPrivateKey = x\nAddress = 10.0.0.1/32\n",
		"no private key": "[Interface]\nAddress = 10.0.0.1/32\n[Peer]\nPublicKey = y\n",
		"binary":         "\x00\x01\x02binary rubbish",
	}
	for name, body := range cases {
		if _, err := normaliseUpload([]byte(body)); err == nil {
			t.Errorf("%s: accepted something that is not a WireGuard config", name)
		}
	}
}

// Linux interface names stop at 15 characters, and the name comes from the
// filename - so Mullvad's own download name is silently fatal.
func TestLongProviderFilenameIsRefusedWithAReason(t *testing.T) {
	_, err := safeName("mullvad-gb-lon-wg-001.conf")
	if err == nil {
		t.Fatal("a 21-character name was accepted; the interface would fail to come up")
	}
	if !strings.Contains(err.Error(), "15") {
		t.Errorf("the error should say what the limit is, got: %v", err)
	}
}

func TestSafeNameAcceptsOrdinaryNames(t *testing.T) {
	for in, want := range map[string]string{
		"wg0.conf":     "wg0.conf",
		"WG0.conf":     "wg0.conf",
		"uk london":    "uk-london.conf",
		"se-mma-1":     "se-mma-1.conf",
		"nl.ams.wg":    "nl-ams-wg.conf",
		"/tmp/wg1.con": "wg1-con.conf",
	} {
		got, err := safeName(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q became %q, want %q", in, got, want)
		}
	}
}

// Serving a config back would hand out the private keys of every tunnel to
// anyone who guessed the token once.
func TestListingNeverReturnsKeys(t *testing.T) {
	cfg, dir := settingsTestConfig(t)
	if err := os.WriteFile(filepath.Join(dir, "wg0.conf"), []byte(sampleWG), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerSettings(mux, cfg, "tok")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tunnels", nil))
	body := rec.Body.String()

	if strings.Contains(body, "qJ8Xk3vN2mP5rT7wY1zA4bC6dE9fG0hI2jK4lM6nO8Q=") {
		t.Error("the listing returned a private key")
	}
	if strings.Contains(strings.ToLower(body), "privatekey") {
		t.Error("the listing mentions PrivateKey")
	}
	// The useful, non-secret parts should be there.
	if !strings.Contains(body, "wg0.conf") || !strings.Contains(body, "185.213.154.66:51820") {
		t.Errorf("the listing is missing the name or endpoint: %s", body)
	}
}

// DELETE takes a name from a URL. It must only ever remove a file this daemon
// manages, or it becomes "delete any file on the box, as root".
func TestDeleteOnlyTouchesTheTunnelDirectory(t *testing.T) {
	cfg, dir := settingsTestConfig(t)
	outside := filepath.Join(t.TempDir(), "important.conf")
	if err := os.WriteFile(outside, []byte("do not delete me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wg0.conf"), []byte(sampleWG), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerSettings(mux, cfg, "tok")

	req := httptest.NewRequest(http.MethodDelete, "/api/tunnels/"+outside, nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("a file outside the tunnel directory was removed: %v", err)
	}

	// The real one should still delete.
	req = httptest.NewRequest(http.MethodDelete, "/api/tunnels/wg0.conf", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("deleting a managed tunnel failed: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "wg0.conf")); err == nil {
		t.Error("the tunnel was not removed")
	}
}

func TestDeleteRequiresTheToken(t *testing.T) {
	cfg, dir := settingsTestConfig(t)
	if err := os.WriteFile(filepath.Join(dir, "wg0.conf"), []byte(sampleWG), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerSettings(mux, cfg, "tok")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/tunnels/wg0.conf", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "wg0.conf")); err != nil {
		t.Error("an unauthorised DELETE removed the tunnel")
	}
}

// Uploading is how tunnels get added, so the daemon must see them without a
// restart - otherwise the settings page appears to do nothing.
func TestUploadedTunnelAppearsInTheTunnelList(t *testing.T) {
	cfg, _ := settingsTestConfig(t)
	if got := len(cfg.tunnelPaths()); got != 0 {
		t.Fatalf("started with %d tunnels, want 0", got)
	}
	mux := http.NewServeMux()
	registerSettings(mux, cfg, "tok")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, uploadRequest(t, "wg0.conf", sampleWG, "tok"))
	if rec.Code != http.StatusOK {
		t.Fatalf("upload failed: %d %s", rec.Code, rec.Body.String())
	}

	paths := cfg.tunnelPaths()
	if len(paths) != 1 {
		t.Fatalf("the daemon sees %d tunnels after an upload, want 1", len(paths))
	}
	if ifaceNameFor(paths[0]) != "wg0" {
		t.Errorf("interface would be %q, want wg0", ifaceNameFor(paths[0]))
	}
}

// Five is the ceiling everywhere else; accepting a sixth would leave a config
// on disk that never becomes a tunnel.
func TestUploadStopsAtTheMaximum(t *testing.T) {
	cfg, dir := settingsTestConfig(t)
	for i := 0; i < MaxTunnels; i++ {
		name := filepath.Join(dir, string(rune('a'+i))+"wg.conf")
		if err := os.WriteFile(name, []byte(sampleWG), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	registerSettings(mux, cfg, "tok")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, uploadRequest(t, "onemore.conf", sampleWG, "tok"))
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d, want 409 when already at %d tunnels", rec.Code, MaxTunnels)
	}

	// Replacing one of the existing five must still work.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, uploadRequest(t, "awg.conf", sampleWG, "tok"))
	if rec.Code != http.StatusOK {
		t.Errorf("replacing an existing tunnel failed: %d %s", rec.Code, rec.Body.String())
	}
}

// The owner of a NAS has no root shell, so a token they can only get by
// reading a root-owned file is no token at all. Setting it at deploy time -
// in a compose file - removes the discovery problem entirely.
func TestTokenCanBeSetAtDeployTime(t *testing.T) {
	cfg, _ := settingsTestConfig(t)
	t.Setenv(tokenEnvVar, "a-token-chosen-in-the-compose-file")

	got, err := adminToken(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != "a-token-chosen-in-the-compose-file" {
		t.Errorf("got %q, want the value from %s", got, tokenEnvVar)
	}
	// Nothing should be written when the operator supplied one.
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfg.path), tokenFileName)); err == nil {
		t.Error("a token file was written even though one was supplied")
	}
}

// A token short enough to guess is worse than useless, because the page implies
// the write endpoint is protected.
func TestSuppliedTokenMustNotBeTrivial(t *testing.T) {
	cfg, _ := settingsTestConfig(t)
	t.Setenv(tokenEnvVar, "hunter2")

	if _, err := adminToken(cfg); err == nil {
		t.Error("a 7-character token was accepted")
	} else if !strings.Contains(err.Error(), "16") {
		t.Errorf("the error should state the minimum, got: %v", err)
	}
}

// The token is the only thing between the network and a root-owned write
// endpoint, so it must be generated, persistent, and not world-readable.
func TestAdminTokenIsStrongAndPrivate(t *testing.T) {
	cfg, _ := settingsTestConfig(t)

	tok, err := adminToken(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) < 32 {
		t.Errorf("token is %d characters, too short to resist guessing", len(tok))
	}

	again, err := adminToken(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if again != tok {
		t.Error("the token changed between calls; the settings page would lock out on every restart")
	}

	// Windows has no Unix permission bits - Go reports 0666 for every file - and
	// the daemon refuses to run anywhere but Linux anyway. Asserting there would
	// be a permanent red herring on a development machine.
	if runtime.GOOS == "windows" {
		t.Skip("file permissions are not meaningful on Windows; this daemon is Linux-only")
	}
	// A config with no known path must NOT fall back to the working directory.
	// It did, and the test run dropped an admin-token into the source tree.
	stray := defaultConfig()
	if _, err := adminToken(stray); err == nil {
		t.Error("a Config with no path produced a token; it would be written to the current directory")
	}
	if _, err := os.Stat(tokenFileName); err == nil {
		t.Errorf("%s was created in the working directory", tokenFileName)
	}

	info, err := os.Stat(filepath.Join(filepath.Dir(cfg.path), tokenFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("token file is mode %o; it must not be readable by other users", perm)
	}
}
