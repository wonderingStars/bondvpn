package main

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// The settings API: add, list and remove tunnel configs from the browser.
//
// AUTHENTICATION, and why it is not optional here.
//
// The status page has always been readable by anyone who can reach the port,
// and that is a documented, deliberate trade: it exposes no secrets and someone
// who can reach the gateway can already see whether their traffic is flowing.
// Writing is a different thing entirely. An unauthenticated upload endpoint on
// a machine running as root would let anyone on the network add a tunnel
// pointing at a server they control and quietly take every packet the gateway
// carries. So mutations require a token, always, with no way to switch it off
// from the config file.
//
// The token is generated on first start and written next to the config with
// mode 0600. Whoever administers the box can read it; nobody else can. It is
// never sent to the browser - the browser sends it.
//
// Config CONTENTS are never served back. Listing returns names and endpoints
// only. A settings page that let you view an uploaded file would hand out the
// private keys of every tunnel to anyone who guessed the token once.

const tokenFileName = "admin-token"

// adminToken returns the token for this installation, creating it on first use.
//
// A Config with no path is refused rather than defaulted. filepath.Dir("") is
// ".", so the token would be written into whatever directory the process
// happened to start in - which, during this feature's own test run, dropped an
// admin-token file into the source tree and very nearly committed it. A secret
// whose location depends on the working directory is a secret that ends up
// somewhere nobody is watching.
// minTokenLen is the floor for a token someone chooses themselves. Generated
// ones are 64 hex characters; this only bounds how bad a hand-picked one may be.
const minTokenLen = 16

// tokenEnvVar lets the token be set at deploy time. This is the container-native
// answer to "how does the owner get the token on a NAS": it goes in the compose
// file, and there is nothing to discover afterwards.
const tokenEnvVar = "BONDVPN_ADMIN_TOKEN"

func adminToken(cfg *Config) (string, error) {
	// Chosen at deploy time wins, and nothing is written to disk.
	if t := strings.TrimSpace(os.Getenv(tokenEnvVar)); t != "" {
		if len(t) < minTokenLen {
			return "", fmt.Errorf("%s is %d characters; use at least %d, or unset it and let one be generated",
				tokenEnvVar, len(t), minTokenLen)
		}
		return t, nil
	}

	if strings.TrimSpace(cfg.path) == "" {
		return "", fmt.Errorf("no config path is known, so there is nowhere safe to keep the admin token")
	}
	path := filepath.Join(filepath.Dir(cfg.path), tokenFileName)

	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); len(t) >= minTokenLen {
			return t, nil
		}
		// Too short to be one of ours - a truncated write or a hand-edited
		// file. Replacing it is safer than trusting a weak token.
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("could not generate an admin token: %v", err)
	}
	token := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("could not write %s: %v", path, err)
	}

	// Printed IN FULL, once, at the moment it is created.
	//
	// The file is mode 0600 and owned by root, which is right for a secret and
	// useless to the owner of a NAS: they have no root shell, so a file they
	// cannot read is the same as no token at all. The startup log is the one
	// place every deployment can see - `docker logs`, Container Manager's log
	// pane, journalctl - so this is where a first install finds it.
	//
	// Only on creation. Printing it on every start would scatter the token
	// through logs that get pasted into forums and support threads.
	logf("")
	logf("  SETTINGS TOKEN (first run, shown once)")
	logf("  %s", token)
	logf("  Enter this on the dashboard to add tunnels. Kept in %s", path)
	logf("  Run `bondvpn token` to print it again, or delete that file for a new one.")
	logf("")
	return token, nil
}

// cmdToken prints the token for whoever can already read it. This exists so the
// answer to "where do I find it" is one command rather than a path, a
// permission problem and a support thread.
func cmdToken(cfg *Config) int {
	token, err := adminToken(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Println(token)
	return 0
}

// authorised does a constant-time comparison. String equality on a secret leaks
// its length and, given enough attempts, its contents.
func authorised(r *http.Request, token string) bool {
	given := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
	if given == "" {
		given = strings.TrimSpace(r.Header.Get("X-Bondvpn-Token"))
	}
	if given == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(given), []byte(token)) == 1
}

type tunnelInfo struct {
	Name     string `json:"name"`
	Iface    string `json:"iface"`
	Endpoint string `json:"endpoint"`
	Managed  bool   `json:"managed"` // in the tunnel dir, so removable from here
}

// registerSettings adds the settings endpoints to the mux.
//
// Everything here is a no-op unless `tunnel_dir` is set: without somewhere to
// put an uploaded file, the honest answer is that this installation is managed
// by hand and the page should say so rather than offering a button that cannot
// work.
func registerSettings(mux *http.ServeMux, cfg *Config, token string) {
	mux.HandleFunc("/api/tunnels", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// Readable without the token, like /status: it carries no secrets,
			// and the page needs it to render before anyone has unlocked it.
			writeJSON(w, http.StatusOK, listTunnels(cfg))
		case http.MethodPost:
			if !authorised(r, token) {
				writeErr(w, http.StatusUnauthorized, "unlock the settings page first")
				return
			}
			uploadTunnel(w, r, cfg)
		default:
			writeErr(w, http.StatusMethodNotAllowed, "use GET or POST")
		}
	})

	mux.HandleFunc("/api/tunnels/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeErr(w, http.StatusMethodNotAllowed, "use DELETE")
			return
		}
		if !authorised(r, token) {
			writeErr(w, http.StatusUnauthorized, "unlock the settings page first")
			return
		}
		deleteTunnel(w, r, cfg)
	})

	// Lets the page tell "wrong token" from "the daemon is old and has no
	// settings API", which are very different problems for a user to be in.
	mux.HandleFunc("/api/unlock", func(w http.ResponseWriter, r *http.Request) {
		if !authorised(r, token) {
			writeErr(w, http.StatusUnauthorized, "that token is not right")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"tunnel_dir": cfg.TunnelDir,
			"max":        MaxTunnels,
		})
	})
}

func listTunnels(cfg *Config) map[string]any {
	paths := cfg.tunnelPaths()
	out := make([]tunnelInfo, 0, len(paths))
	managed := map[string]bool{}
	for _, p := range cfg.dirTunnels() {
		managed[p] = true
	}
	for _, p := range paths {
		info := tunnelInfo{
			Name:    filepath.Base(p),
			Iface:   ifaceNameFor(p),
			Managed: managed[p],
		}
		// Endpoint only - which server a tunnel talks to is useful and not
		// secret. The file is never returned, and no key is ever read.
		info.Endpoint = endpointOf(p)
		out = append(out, info)
	}
	return map[string]any{
		"tunnels":    out,
		"tunnel_dir": cfg.TunnelDir,
		"max":        MaxTunnels,
		"editable":   cfg.TunnelDir != "",
	}
}

func uploadTunnel(w http.ResponseWriter, r *http.Request, cfg *Config) {
	if cfg.TunnelDir == "" {
		writeErr(w, http.StatusConflict,
			"this installation lists its tunnels in the config file, so they cannot be changed from here. Set tunnel_dir to manage them from the browser.")
		return
	}
	// Bounded before anything is read into memory: this runs as root and the
	// port may be on the LAN.
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "could not read the upload: "+err.Error())
		return
	}
	file, header, err := r.FormFile("config")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no file was attached")
		return
	}
	defer file.Close()

	name, err := safeName(header.Filename)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// The cap is Mullvad's per-account device limit and the daemon's own
	// ceiling; going over it silently would leave a config on disk that never
	// becomes a tunnel.
	existing := cfg.dirTunnels()
	dest := filepath.Join(cfg.TunnelDir, name)
	replacing := false
	for _, p := range existing {
		if p == dest {
			replacing = true
		}
	}
	if !replacing && len(cfg.tunnelPaths()) >= MaxTunnels {
		writeErr(w, http.StatusConflict,
			fmt.Sprintf("%d tunnels is the maximum; remove one first", MaxTunnels))
		return
	}

	raw := make([]byte, 0, 8*1024)
	buf := make([]byte, 4096)
	for {
		n, rerr := file.Read(buf)
		raw = append(raw, buf[:n]...)
		if rerr != nil {
			break
		}
		if len(raw) > 64*1024 {
			break // validateWGConfig will reject it with a readable message
		}
	}

	normalised, err := normaliseUpload(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Written via a temporary file in the same directory and renamed, so the
	// run loop can never see a half-written config and try to bring it up.
	tmp := dest + ".partial"
	if err := os.WriteFile(tmp, normalised, 0o600); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save it: "+err.Error())
		return
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		writeErr(w, http.StatusInternalServerError, "could not save it: "+err.Error())
		return
	}
	logf("settings: added tunnel %s", name)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"name":      name,
		"iface":     strings.TrimSuffix(name, ".conf"),
		"replaced":  replacing,
		"coming_up": "the tunnel is brought up on the next pass, within about 15 seconds",
	})
}

func deleteTunnel(w http.ResponseWriter, r *http.Request, cfg *Config) {
	if cfg.TunnelDir == "" {
		writeErr(w, http.StatusConflict, "this installation lists its tunnels in the config file")
		return
	}
	// Re-sanitised rather than trusted: this is a path from a URL.
	name, err := safeName(strings.TrimPrefix(r.URL.Path, "/api/tunnels/"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dest := filepath.Join(cfg.TunnelDir, name)

	// Only files this daemon manages. Refusing anything outside the tunnel
	// directory means a crafted request cannot turn DELETE into "remove any
	// file on the box as root".
	found := false
	for _, p := range cfg.dirTunnels() {
		if p == dest {
			found = true
		}
	}
	if !found {
		writeErr(w, http.StatusNotFound, "no such tunnel")
		return
	}
	if err := os.Remove(dest); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not remove it: "+err.Error())
		return
	}
	logf("settings: removed tunnel %s", name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": name})
}

// endpointOf reads just the Endpoint line. Deliberately its own tiny reader
// rather than handing the file to a general parser: the less of a file holding
// a private key that is read into memory by a web handler, the better.
func endpointOf(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if settingKey(sc.Text()) != "endpoint" {
			continue
		}
		_, v, _ := strings.Cut(sc.Text(), "=")
		return strings.TrimSpace(v)
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}
