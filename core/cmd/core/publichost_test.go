package main

import "testing"

// The agent dial address is derived from the public URL, and lands in a command
// an operator pastes onto another machine. A wrong answer here fails there.
func TestPublicHostStripsSchemePortAndPath(t *testing.T) {
	for raw, want := range map[string]string{
		"https://panel.example.com":     "panel.example.com",
		"https://panel.example.com/":    "panel.example.com",
		"https://panel.example.com:443": "panel.example.com",
		"http://panel.example.com":      "panel.example.com",
		// The case the first version got wrong: the panel's own port is not
		// 443 by default, and trimming only ":443" left it attached.
		"https://panel.example.com:26080":   "panel.example.com",
		"https://panel.example.com:26080/x": "panel.example.com",
		"panel.example.com":                 "panel.example.com",
		"panel.example.com:26080":           "panel.example.com",
		"https://[2001:db8::1]:26080":       "2001:db8::1",
		"":                                  "",
	} {
		if got := publicHost(raw); got != want {
			t.Errorf("publicHost(%q) = %q, want %q", raw, got, want)
		}
	}
}
