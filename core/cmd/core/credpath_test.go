package main

import (
	"os"
	"path/filepath"
	"testing"
)

// systemd expands %d only inside unit directives. An EnvironmentFile is passed
// through verbatim, so the documented CHIRAL_TLS_CERT=%d/tls-cert reached the
// process as those literal characters and open() failed on a path that cannot
// exist — the panel would not start at all in direct-TLS mode.
func TestCredentialPathExpandsTheSystemdSpecifier(t *testing.T) {
	t.Setenv("CREDENTIALS_DIRECTORY", "/run/credentials/chiral-core.service")
	got := credentialPath("%d/tls-cert")
	want := filepath.Join("/run/credentials/chiral-core.service", "tls-cert")
	if got != want {
		t.Fatalf("credentialPath(%%d/tls-cert) = %q, want %q", got, want)
	}
}

// Ordinary paths are untouched, including one that merely contains a percent.
func TestCredentialPathLeavesOtherPathsAlone(t *testing.T) {
	t.Setenv("CREDENTIALS_DIRECTORY", "/run/credentials/x")
	for _, p := range []string{
		"/etc/chiral/tls/fullchain.pem",
		"",
		"relative/cert.pem",
		"/etc/weird%dname/cert.pem",
	} {
		if got := credentialPath(p); got != p {
			t.Errorf("credentialPath(%q) = %q, want it unchanged", p, got)
		}
	}
}

// Without LoadCredential there is nothing to resolve against. Keeping the
// literal makes the resulting error name the path that was actually tried,
// rather than an empty string or a silently wrong one.
func TestCredentialPathWithoutCredentialsDirectory(t *testing.T) {
	os.Unsetenv("CREDENTIALS_DIRECTORY")
	if got := credentialPath("%d/tls-cert"); got != "%d/tls-cert" {
		t.Fatalf("got %q, want the literal back", got)
	}
}
