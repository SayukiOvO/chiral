package api

import (
	"strings"
	"testing"
)

// Clash-family clients use the Content-Disposition filename as the profile's
// name, and several take it raw — an operator who set "MoonWX" saw `"MoonWX"`
// in their subscribers' clients, quotes and all. Neither form emitted here can
// put a quote in front of somebody's name.
func TestContentDispositionLeavesNoQuotesInTheName(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"MoonWX", "attachment; filename=MoonWX"},
		{"chiral", "attachment; filename=chiral"},
		{"my-nodes_2", "attachment; filename=my-nodes_2"},
		// Anything a token cannot hold goes out percent-encoded, which also
		// needs no quotes.
		{"Mai 的机场", "attachment; filename*=UTF-8''Mai%20%E7%9A%84%E6%9C%BA%E5%9C%BA"},
		{"a b", "attachment; filename*=UTF-8''a%20b"},
	} {
		got := contentDisposition(tc.name)
		if got != tc.want {
			t.Errorf("%q -> %q, want %q", tc.name, got, tc.want)
		}
		if strings.Contains(got, `"`) {
			t.Errorf("%q produced a quote: %s", tc.name, got)
		}
	}
}
