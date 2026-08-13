package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The update check.
//
// Once an hour BondVPN fetches a small signed file and compares the version in
// it with its own. If a newer release exists it says so, once, in the log and
// in `status`. That is the whole of it.
//
// # WHAT THIS DELIBERATELY DOES NOT DO, having previously done it
//
// An earlier version of this file was a licence heartbeat: miss it for 24 hours
// and the daemon withdrew the client routing and exited. That is indefensible in
// software people can compile themselves - the first fork would be the one with
// those lines deleted, and it would deservedly become the version everybody
// runs. Software that stops working because a server said so is not something
// you can ask a community to trust.
//
// So: nothing here can stop traffic, withdraw routing, or refuse to start. The
// worst case is a log line that fails to appear.
//
// It is also switchable off - `update_check: false` - and says so in the
// example config. A check you cannot decline is telemetry wearing a hat.
//
// The count is the useful side effect. Each installation asks once an hour, so
// the request count at the other end estimates how many are running, without
// anything being sent that identifies anybody: the request is a plain GET of a
// static file, identical from every machine, carrying no install ID and no
// address beyond the one every TCP connection needs.
const (
	updateKeyB64  = "rF8PJN9wpHzd9TjDLu8K0fuqT8Nzg4QYO1smJRoUqtA="
	checkEvery    = time.Hour
	updateTimeout = 15 * time.Second
)

// Where the signed file comes from, tried in order until one answers.
//
// The first is a host whose request count is visible, which is how the project
// knows whether anyone is using it. The second is the copy in the repository,
// and it is the reason the first can never matter: if that host is down,
// blocked, or retired tomorrow, the check still works and nobody notices.
var updateURLs = []string{
	"https://bondvpn-licence.bondvpn.workers.dev/license.json",
	"https://raw.githubusercontent.com/wonderingStars/bondvpn/master/license.json",
}

// updateKey verifies the file. A signature on an update notice matters: without
// it, anyone able to answer for that hostname could tell every installation
// that a "newer version" waits at an address of their choosing.
var updateKey = updateKeyB64

type signedDoc struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

// updatePayload is what the signed file carries. `status` is retained so old
// files still parse; nothing acts on it any more.
type updatePayload struct {
	Latest  string `json:"latest"`
	Status  string `json:"status"`
	Issued  int64  `json:"issued"`
	Message string `json:"message"`
}

// verifyUpdate checks the signature and returns the payload. Split from the
// fetching so the whole of it is testable without a network.
func verifyUpdate(raw []byte, pubKeyB64 string) (*updatePayload, error) {
	var doc signedDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("update file is not readable: %v", err)
	}
	payload, err := base64.StdEncoding.DecodeString(doc.Payload)
	if err != nil {
		return nil, fmt.Errorf("update payload is not valid base64: %v", err)
	}
	sig, err := base64.StdEncoding.DecodeString(doc.Signature)
	if err != nil {
		return nil, fmt.Errorf("update signature is not valid base64: %v", err)
	}
	key, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("built-in update key is unusable")
	}
	if !ed25519.Verify(ed25519.PublicKey(key), payload, sig) {
		return nil, fmt.Errorf("update signature does not verify")
	}
	var p updatePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("update payload is not readable: %v", err)
	}
	return &p, nil
}

func fetchUpdate() (*updatePayload, error) {
	var lastErr error
	for _, url := range updateURLs {
		p, err := fetchUpdateFrom(url)
		if err == nil {
			return p, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func fetchUpdateFrom(url string) (*updatePayload, error) {
	client := &http.Client{Timeout: updateTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}
	return verifyUpdate(body, updateKey)
}

// UpdateState is what the daemon and `status` report.
type UpdateState struct {
	Latest    string // the newest published version, "" if not known
	Available bool   // that version is newer than this build
	Checked   bool   // a check has succeeded at least once
}

// checkUpdate performs one check. Every failure is silent by design: a project
// that cannot reach the internet is not a project in trouble, and an update
// notice is not worth a warning in somebody's log every hour.
func checkUpdate() UpdateState {
	p, err := fetchUpdate()
	if err != nil || p == nil || p.Latest == "" {
		return UpdateState{}
	}
	return UpdateState{
		Latest:    p.Latest,
		Available: newerVersion(p.Latest, Version),
		Checked:   true,
	}
}

// newerVersion reports whether a is a later release than b.
//
// Compares numerically, field by field, because string comparison puts 1.10.0
// before 1.9.0 and would tell everyone running the newest build that they are
// out of date. A version that will not parse is treated as "not newer" - an
// update notice is not worth guessing about.
func newerVersion(a, b string) bool {
	as, bs := splitVersion(a), splitVersion(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		x, y := 0, 0
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func splitVersion(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	// Drop any pre-release or build suffix: 1.6.0-rc1 compares as 1.6.0.
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out []int
	for _, part := range strings.Split(v, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}
