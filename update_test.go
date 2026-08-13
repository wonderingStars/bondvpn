package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func signUpdate(t *testing.T, priv ed25519.PrivateKey, p updatePayload) []byte {
	t.Helper()
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(signedDoc{
		Payload:   base64.StdEncoding.EncodeToString(body),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A signature on an update notice matters: without it, anyone able to answer for
// that hostname could tell every installation that a newer version waits at an
// address of their choosing.
func TestUpdateSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	raw := signUpdate(t, priv, updatePayload{Latest: "9.9.9"})
	p, err := verifyUpdate(raw, pubB64)
	if err != nil {
		t.Fatalf("a correctly signed file must verify: %v", err)
	}
	if p.Latest != "9.9.9" {
		t.Errorf("latest = %q", p.Latest)
	}

	_, otherPriv, _ := ed25519.GenerateKey(nil)
	forged := signUpdate(t, otherPriv, updatePayload{Latest: "9.9.9"})
	if _, err := verifyUpdate(forged, pubB64); err == nil {
		t.Error("a file signed by another key must be rejected")
	}
	if _, err := verifyUpdate([]byte("not json"), pubB64); err == nil {
		t.Error("garbage must be rejected rather than treated as valid")
	}
}

// String comparison puts 1.10.0 before 1.9.0, which would tell everyone running
// the newest build that they are out of date.
func TestVersionComparison(t *testing.T) {
	cases := []struct {
		latest, running string
		want            bool
	}{
		{"1.6.0", "1.5.2", true},
		{"1.10.0", "1.9.0", true},
		{"1.5.2", "1.5.2", false},
		{"1.5.1", "1.5.2", false},
		{"2.0.0", "1.99.99", true},
		{"v1.6.0", "1.5.2", true},    // a leading v is common in tags
		{"1.6.0-rc1", "1.5.2", true}, // pre-release suffixes are ignored
		{"nonsense", "1.5.2", false}, // unparseable is never "newer"
		{"", "1.5.2", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.latest, c.running); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", c.latest, c.running, got, c.want)
		}
	}
}

// A counting endpoint must never become a way for the product to fail: if the
// first source is down, the next answers and nobody notices.
func TestUpdateSourcesFallBack(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	good := signUpdate(t, priv, updatePayload{Latest: "9.9.9", Message: "from the fallback"})

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer dead.Close()
	alive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(good)
	}))
	defer alive.Close()

	origURLs, origKey := updateURLs, updateKey
	defer func() { updateURLs, updateKey = origURLs, origKey }()
	updateURLs = []string{dead.URL, alive.URL}
	updateKey = base64.StdEncoding.EncodeToString(pub)

	p, err := fetchUpdate()
	if err != nil {
		t.Fatalf("a dead first source must fall through: %v", err)
	}
	if p.Message != "from the fallback" {
		t.Errorf("got %q", p.Message)
	}

	// Everything failing is a silent no-op, not an error the user sees.
	updateURLs = []string{dead.URL}
	if st := checkUpdate(); st.Checked || st.Available {
		t.Errorf("a failed check must report nothing, got %+v", st)
	}
}

// The check is compiled in, and it stays compiled in: no environment variable,
// config key or ldflags value can point it elsewhere, which is what makes a
// build from source count the same as a released binary.
func TestCheckInIsCompiledIn(t *testing.T) {
	if len(updateURLs) < 2 {
		t.Fatalf("expected a counting host and a fallback, got %v", updateURLs)
	}
	if !strings.Contains(updateURLs[0], "workers.dev") {
		t.Errorf("the counting host must be tried FIRST, got %q", updateURLs[0])
	}
	last := updateURLs[len(updateURLs)-1]
	if !strings.Contains(last, "raw.githubusercontent.com") {
		t.Errorf("the repository copy must remain the fallback, got %q", last)
	}
	for _, u := range updateURLs {
		if !strings.HasPrefix(u, "https://") {
			t.Errorf("%q is not https - the check could be rewritten in transit", u)
		}
	}
	if updateKey != updateKeyB64 {
		t.Error("verification must use the compiled-in key")
	}
}

// The whole point of the rewrite: this cannot stop anything. If a future edit
// gives the update path the power to withdraw routing or exit, the first fork
// will be the one with those lines deleted - and it will be the version people
// run.
func TestUpdateCheckCannotStopTheGateway(t *testing.T) {
	raw, err := os.ReadFile("update.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, forbidden := range []string{"flushOurRules", "os.Exit", "Shutdown"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("update.go references %q - the update check must not be able "+
				"to stop, exit or unroute anything", forbidden)
		}
	}
}

// Off by one config line, and the default is on.
func TestUpdateCheckIsOptOut(t *testing.T) {
	if !defaultConfig().UpdateCheck {
		t.Error("the update check should be on by default")
	}
	cfg, err := LoadConfig(writeConfig(t, goodConfig+"\nupdate_check: false\n"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.UpdateCheck {
		t.Error("update_check: false must turn it off")
	}
}
