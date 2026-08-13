package store

import "testing"

// A pattern the routing rule silently never matches is a restriction that is
// not one — for an allowlist security feature, the validator's job is to
// refuse those, not to pass them through to a kernel that accepts anything.
func TestDomainLinesRefuseWhatXrayWouldNeverMatch(t *testing.T) {
	// Shapes operators actually type, normalised rather than refused.
	for input, want := range map[string]string{
		"dn42":            "dn42",
		".dn42":           "dn42",
		"*.dn42":          "dn42",
		"DN42":            "dn42",
		" corp.internal ": "corp.internal",
	} {
		got, err := ParseDomainLines(input)
		if err != nil {
			t.Errorf("%q refused: %v", input, err)
			continue
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("%q -> %v, want [%s]", input, got, want)
		}
	}
	// Shapes that parse, render, validate — and match nothing, ever.
	for _, bad := range []string{
		"dn42.",         // trailing dot: no real destination ends with one
		"..dn42",        // an empty label
		"a*.dn42",       // a literal asterisk
		"dn42,clearnet", // a comma is not a separator here
		"geosite:cn",    // a matcher prefix, not a suffix
		"172.20.0.1",    // an IP in the wrong box
	} {
		if got, err := ParseDomainLines(bad); err == nil {
			t.Errorf("%q accepted as %v; Xray would never match it", bad, got)
		}
	}
}

func TestCIDRLinesCanonicaliseBareIPs(t *testing.T) {
	got, err := ParseCIDRLines("172.20.0.1\n::ffff:10.1.2.3\nfd00::1\n172.20.0.0/14")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"172.20.0.1/32", "10.1.2.3/32", "fd00::1/128", "172.20.0.0/14"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: got %s, want %s", i, got[i], want[i])
		}
	}
	if _, err := ParseCIDRLines("not-an-ip"); err == nil {
		t.Error("garbage accepted as a CIDR")
	}
}
