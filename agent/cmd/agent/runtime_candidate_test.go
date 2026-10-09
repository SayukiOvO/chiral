package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCandidateEnvironmentIsExplicitAndNotPublic(t *testing.T) {
	t.Setenv("CHIRAL_RUNTIME_PROVIDER", runtimeProvider3XUICandidate)
	t.Setenv("CHIRAL_3XUI_URL", "http://127.0.0.1:2053")
	t.Setenv("CHIRAL_3XUI_TOKEN_FILE", "/run/secrets/candidate")
	t.Setenv("CHIRAL_3XUI_ALLOW_PUBLIC", "0")
	t.Setenv("CHIRAL_3XUI_ISOLATION", "")
	t.Setenv("CHIRAL_3XUI_CANDIDATE_USER_WRITES", "1")
	if _, err := runtimeConfigFromEnv(); err == nil {
		t.Fatal("candidate accepted without explicit isolated-instance guard")
	}
	t.Setenv("CHIRAL_3XUI_ISOLATION", "isolated-instance")
	if cfg, err := runtimeConfigFromEnv(); err != nil || !cfg.CandidateUserWrites {
		t.Fatal("explicit isolated candidate could not be configured")
	}
	t.Setenv("CHIRAL_3XUI_ALLOW_PUBLIC", "1")
	if _, err := runtimeConfigFromEnv(); err == nil {
		t.Fatal("candidate allowed a public administrative endpoint")
	}
}

func TestCandidateProvisionFileRequiresPrivateRegularBoundedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inbound.json")
	if err := os.WriteFile(path, []byte(`{"tag":"fixture"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if raw, err := readCandidateProvisionFile(path); err != nil || string(raw) != `{"tag":"fixture"}` {
		t.Fatal("private provisioning input was not read exactly")
	}
	link := filepath.Join(dir, "symlink.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readCandidateProvisionFile(link); err == nil {
		t.Fatal("symlink provision file was accepted")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCandidateProvisionFile(path); err == nil {
		t.Fatal("public provision file was accepted")
	}
}
