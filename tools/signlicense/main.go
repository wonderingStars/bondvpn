// Command signlicense produces the signed license.json that BondVPN installs
// check once an hour.
//
// The private key never goes in either repository. Keep it somewhere you will
// still have it in a year: losing it means you can never publish a new status,
// and every install falls out of grace a day later.
//
//	signlicense -key <base64 private key> -status active  > license.json
//	signlicense -keygen                                   # make a new pair
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	keygen := flag.Bool("keygen", false, "generate a new keypair and exit")
	key := flag.String("key", os.Getenv("BONDVPN_LICENSE_KEY"),
		"base64 ed25519 private key (or set BONDVPN_LICENSE_KEY)")
	latest := flag.String("latest", "", "the newest published version, e.g. 1.6.0")
	status := flag.String("status", "active", `retained so older builds still parse this file`)
	message := flag.String("message", "", "human-readable note, shown in the daemon log")
	flag.Parse()

	if *keygen {
		pub, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			fail(err)
		}
		fmt.Println("public  (embed in license.go):", base64.StdEncoding.EncodeToString(pub))
		fmt.Println("private (keep off both repos):", base64.StdEncoding.EncodeToString(priv))
		return
	}

	raw, err := base64.StdEncoding.DecodeString(*key)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		fail(fmt.Errorf("need a base64 ed25519 private key in -key or BONDVPN_LICENSE_KEY"))
	}

	body, err := json.Marshal(map[string]any{
		"latest":  *latest,
		"status":  *status,
		"issued":  time.Now().Unix(),
		"message": *message,
	})
	if err != nil {
		fail(err)
	}
	out, err := json.MarshalIndent(map[string]string{
		"payload":   base64.StdEncoding.EncodeToString(body),
		"signature": base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(raw), body)),
	}, "", "  ")
	if err != nil {
		fail(err)
	}
	fmt.Println(string(out))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
