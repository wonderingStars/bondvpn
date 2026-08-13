package main

import (
	"strings"
	"testing"
)

// A single tunnel gets the plain form, not a one-entry multipath route.
//
// Measured on a Synology DS1019+ (kernel 4.4.302, no CONFIG_IP_ROUTE_MULTIPATH):
// `nexthop dev eth0 weight 1` fails with EINVAL while `dev eth0` succeeds. Every
// nexthop variant was tried - with and without scope global, one nexthop and
// two - and all of them were refused. So on that hardware the one-tunnel case
// only works if it never says "nexthop".
func TestBondRouteUsesPlainFormForOneTunnel(t *testing.T) {
	args := bondRouteArgs("51820", []string{"wg0"})
	joined := strings.Join(args, " ")

	if strings.Contains(joined, "nexthop") {
		t.Errorf("a single tunnel must not use multipath syntax: %q", joined)
	}
	if joined != "route replace default dev wg0 table 51820" {
		t.Errorf("got %q", joined)
	}
}

// The sysctl advice is unfollowable where the file does not exist, and the NAS
// reported exactly that: "set fib_multipath_hash_policy=1" on a kernel with no
// such file and no multipath support to configure.
func TestMultipathWarningDoesNotAdviseAMissingSysctl(t *testing.T) {
	const absent = -1 // readHashPolicy returns -1 when the file is not there

	w := multipathWarning(false, absent)
	if strings.Contains(w, "sysctl") {
		t.Errorf("must not advise a sysctl that cannot exist: %q", w)
	}
	if !strings.Contains(w, "no multipath routing") {
		t.Errorf("must say why bonding is unavailable: %q", w)
	}

	if w := multipathWarning(true, 1); !strings.Contains(w, "no multipath routing") {
		t.Errorf("a failed multipath route must be reported even if the sysctl reads 1: %q", w)
	}
	if w := multipathWarning(false, 0); !strings.Contains(w, "sysctl") {
		t.Errorf("a settable-but-unset policy should still give the fix: %q", w)
	}
	if w := multipathWarning(false, 1); w != "" {
		t.Errorf("a healthy host should warn about nothing: %q", w)
	}
}

func TestBondRouteUsesMultipathForSeveral(t *testing.T) {
	args := bondRouteArgs("51820", []string{"wg0", "wg1", "wg2"})
	joined := strings.Join(args, " ")

	for _, iface := range []string{"wg0", "wg1", "wg2"} {
		if !strings.Contains(joined, "nexthop dev "+iface+" weight 1") {
			t.Errorf("%s missing from %q", iface, joined)
		}
	}
	if strings.Count(joined, "nexthop") != 3 {
		t.Errorf("expected three nexthops: %q", joined)
	}
}
