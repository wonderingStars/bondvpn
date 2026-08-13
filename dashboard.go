package main

import (
	_ "embed"
	"net/http"
)

// The interface, compiled into the binary.
//
// EMBEDDED rather than installed alongside, because everything else about this
// product is one static file you drop on a box: an interface that lives in
// /usr/share and can go missing, fall out of step with the binary, or be edited
// into something that lies about the gateway is a support burden the product
// does not need. One file in, one file out.
//
//go:embed dashboard.html
var dashboardHTML []byte

// dashboardHandler serves that page and nothing else.
//
// It is registered at "/", which in Go's ServeMux is the catch-all - so it must
// reject everything it is not, or a typo'd path would be answered with the
// dashboard and a 200, which is how a missing route hides.
func dashboardHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// No caching. An upgraded binary carries a new page, and a browser
		// holding the old one would show an interface that disagrees with the
		// document it is rendering.
		w.Header().Set("Cache-Control", "no-store")
		// The page is self-contained, so it never needs to load anything from
		// anywhere: say so, and a compromised or misconfigured deployment
		// cannot turn it into a beacon.
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(dashboardHTML)
	}
}
