package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func testStatus() *Status {
	return &Status{
		Generated: 1, Version: Version, Bonded: 3, HashPolicy: 1, NAT: true,
		DNSForced: true, UpdateAvailable: false,
		KillSwitch: KillSwitchStatus{V4: true, V6: true},
		Tunnels:    []TunnelStatus{{Iface: "wg0", Up: true, InBond: true}},
		Problems:   []string{},
	}
}

func dashServer(t *testing.T, on bool) *httptest.Server {
	t.Helper()
	cfg := defaultConfig()
	cfg.Dashboard = on
	srv := serveStatus(cfg, testStatus)
	return httptest.NewServer(srv.Handler)
}

func TestDashboardServedAtRoot(t *testing.T) {
	ts := dashServer(t, true)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store - an upgraded binary must not "+
			"be rendered by a cached page from the old one", cc)
	}
}

// The page is registered at "/", which is Go's catch-all. Without the guard in
// dashboardHandler a mistyped path would answer 200 with the dashboard, which
// is exactly how a missing route hides - as it did behind nginx once already.
func TestDashboardDoesNotSwallowUnknownPaths(t *testing.T) {
	ts := dashServer(t, true)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /nope = %d, want 404", resp.StatusCode)
	}
}

func TestDashboardCanBeTurnedOff(t *testing.T) {
	ts := dashServer(t, false)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET / with dashboard off = %d, want 404", resp.StatusCode)
	}
}

// Both surfaces must keep working with the page in front of them - the API is
// what anything else is built on.
func TestStatusAndHealthzStillServed(t *testing.T) {
	ts := dashServer(t, true)
	defer ts.Close()

	for _, path := range []string{"/status", "/healthz"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// The gateway this runs on may have no route out at all - that is the kill
// switch working. A page that pulled a font or a script from the internet would
// break precisely when someone opens it to find out what went wrong.
func TestDashboardIsSelfContained(t *testing.T) {
	page := string(dashboardHTML)
	for _, bad := range []string{"http://", "https://", "//cdn", "<script src", "@import"} {
		if strings.Contains(page, bad) {
			t.Errorf("dashboard.html contains %q - it must not fetch anything external", bad)
		}
	}
	// Only relative same-origin fetches.
	for _, m := range regexp.MustCompile(`fetch\(\s*"([^"]*)"`).FindAllStringSubmatch(page, -1) {
		if strings.HasPrefix(m[1], "/") || strings.Contains(m[1], "://") {
			t.Errorf("fetch(%q) is not relative - it breaks behind a path prefix", m[1])
		}
	}
}

// Dark only, by decision. A light branch creeping back in is a regression.
func TestDashboardHasNoLightMode(t *testing.T) {
	if strings.Contains(string(dashboardHTML), "prefers-color-scheme: light") ||
		strings.Contains(string(dashboardHTML), "prefers-color-scheme:light") {
		t.Error("dashboard.html has a light-mode branch; this interface is dark only")
	}
}

func TestParseBool(t *testing.T) {
	for _, in := range []string{"true", "yes", "on", "1", "TRUE"} {
		if v, err := parseBool(in); err != nil || !v {
			t.Errorf("parseBool(%q) = %v, %v; want true, nil", in, v, err)
		}
	}
	for _, in := range []string{"false", "no", "off", "0"} {
		if v, err := parseBool(in); err != nil || v {
			t.Errorf("parseBool(%q) = %v, %v; want false, nil", in, v, err)
		}
	}
	// A typo must not read as false and silently disable something.
	if _, err := parseBool("ture"); err == nil {
		t.Error("parseBool(\"ture\") should be an error, not false")
	}
}
