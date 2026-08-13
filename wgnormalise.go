package main

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// normaliseUpload turns a config downloaded from a VPN provider into one this
// daemon can actually use.
//
// Every provider's file needs the same two edits, and every user who has not
// read the setup guide gets the same two failures:
//
//   - "DNS = ..." makes wg-quick rewrite the resolver for the WHOLE machine.
//     On a NAS that breaks name resolution for every other service on the box.
//     DNS is forced per-client by the daemon instead, which is both narrower and
//     harder to leak around.
//   - Without "Table = off", wg-quick installs its own default route, which
//     fights the policy routing this daemon depends on. The tunnel comes up,
//     handshakes, shows traffic counters, and carries nothing - the single most
//     confusing failure in this product.
//
// PersistentKeepalive is added when missing because WireGuard only rekeys when
// there is traffic: an idle tunnel goes quiet, gets marked stale, and is dropped
// from service while being perfectly healthy.
//
// The result is returned rather than written, so callers can validate before
// anything touches the disk.
func normaliseUpload(src []byte) ([]byte, error) {
	if err := validateWGConfig(src); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	// Careful with the wording here: any literal "Table = off" in this header
	// is indistinguishable from the real setting to anything scanning the file,
	// including this project's own tests.
	out.WriteString("# Added through the BondVPN settings page.\n")
	out.WriteString("# The provider's DNS line is removed and routing is disabled for this\n")
	out.WriteString("# interface, so the daemon can policy-route it. Your original is untouched.\n")

	hasKeepalive := false
	sc := bufio.NewScanner(bytes.NewReader(src))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		key := settingKey(line)

		switch key {
		case "dns", "table":
			// Dropped, with a note so the file explains itself to whoever reads
			// it next rather than looking like the provider never sent one.
			out.WriteString("# removed by BondVPN: " + strings.TrimSpace(line) + "\n")
			continue
		case "persistentkeepalive":
			hasKeepalive = true
		}

		out.WriteString(line + "\n")

		// Immediately after [Interface] so it cannot land in the [Peer] section,
		// where it would be silently ignored.
		if strings.EqualFold(strings.TrimSpace(line), "[Interface]") {
			out.WriteString("Table = off\n")
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("could not read that file: %v", err)
	}

	if !hasKeepalive {
		out.WriteString("\n# Added by BondVPN so an idle tunnel keeps handshaking and is not\n")
		out.WriteString("# mistaken for a dead one.\nPersistentKeepalive = 25\n")
	}
	return out.Bytes(), nil
}

// settingKey returns the lower-cased key of a "Key = value" line, or "" for
// blanks, comments and section headers.
func settingKey(line string) string {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "[") {
		return ""
	}
	k, _, ok := strings.Cut(t, "=")
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(k))
}

// validateWGConfig rejects anything that is not a WireGuard config before it is
// written anywhere.
//
// The failure this prevents is not subtle: someone uploads the wrong file from
// their Downloads folder, it lands in the tunnel directory, and the daemon
// spends every pass trying to bring up an interface from a PDF. Saying so at
// upload time costs one function and saves a support thread.
func validateWGConfig(src []byte) error {
	if len(src) == 0 {
		return fmt.Errorf("that file is empty")
	}
	if len(src) > 64*1024 {
		return fmt.Errorf("that file is %d KB; a WireGuard config is well under 64 KB, so this is probably the wrong file",
			len(src)/1024)
	}
	if !utf8ish(src) {
		return fmt.Errorf("that looks like a binary file, not a WireGuard config")
	}

	text := string(src)
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "[interface]") {
		return fmt.Errorf("no [Interface] section - this is not a WireGuard config")
	}
	if !strings.Contains(lower, "[peer]") {
		return fmt.Errorf("no [Peer] section - a tunnel needs a server to connect to")
	}

	var hasKey, hasPeerKey, hasAddress bool
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		switch settingKey(sc.Text()) {
		case "privatekey":
			hasKey = true
		case "publickey":
			hasPeerKey = true
		case "address":
			hasAddress = true
		}
	}
	if !hasKey {
		return fmt.Errorf("no PrivateKey - if your provider gave you a config without one, you need to generate the key first")
	}
	if !hasPeerKey {
		return fmt.Errorf("no PublicKey under [Peer] - the config is incomplete")
	}
	if !hasAddress {
		return fmt.Errorf("no Address under [Interface] - the tunnel has no address to use")
	}
	return nil
}

// utf8ish reports whether the bytes look like text. A NUL byte is the giveaway
// for the wrong file being uploaded; real configs never contain one.
func utf8ish(b []byte) bool {
	return !bytes.ContainsRune(b, 0)
}
