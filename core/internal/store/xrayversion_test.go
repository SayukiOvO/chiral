package store

import "testing"

func versionFixture(t *testing.T) (*Store, Node) {
	t.Helper()
	s := testStore(t, storeTestKey)
	n, err := s.CreateNode("tokyo-1", "join-token-hash")
	if err != nil {
		t.Fatal(err)
	}
	return s, n
}

func TestHelloSeedsBothVersions(t *testing.T) {
	s, n := versionFixture(t)
	if err := s.UpdateHello(n.ID, "203.0.113.7", "0.1.0", "26.3.27"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.XrayVersion != "26.3.27" || got.XrayInstalledVersion != "26.3.27" {
		t.Fatalf("running/installed = %q/%q, want both 26.3.27", got.XrayVersion, got.XrayInstalledVersion)
	}
}

// The steady state is "nothing changed", and heartbeats arrive every few
// seconds per node against a handle pinned to one connection.
func TestUnchangedVersionsDoNotWrite(t *testing.T) {
	s, n := versionFixture(t)
	if changed, err := s.SetXrayVersions(n.ID, "26.3.27", "26.3.27"); err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v", changed, err)
	}
	changed, err := s.SetXrayVersions(n.ID, "26.3.27", "26.3.27")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("an identical heartbeat reported a change")
	}
}

// The whole reason there are two columns: a swap moves one and not the other,
// and that gap is what says the new kernel is not actually serving yet.
func TestASwapWithoutARestartIsVisible(t *testing.T) {
	s, n := versionFixture(t)
	if _, err := s.SetXrayVersions(n.ID, "26.3.27", "26.9.1"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetNode(n.ID)
	if got.XrayVersion != "26.3.27" {
		t.Errorf("running = %q, want the old 26.3.27", got.XrayVersion)
	}
	if got.XrayInstalledVersion != "26.9.1" {
		t.Errorf("installed = %q, want the new 26.9.1", got.XrayInstalledVersion)
	}
}

// A Core newer than its agents is the normal state of a panel whose job is
// rolling upgrades out. An agent that reports neither field must not blank what
// Hello already established.
func TestAnOlderAgentCannotBlankWhatHelloRecorded(t *testing.T) {
	s, n := versionFixture(t)
	if err := s.UpdateHello(n.ID, "", "0.1.0", "26.3.27"); err != nil {
		t.Fatal(err)
	}
	changed, err := s.SetXrayVersions(n.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("an empty heartbeat reported a change")
	}
	got, _ := s.GetNode(n.ID)
	if got.XrayVersion != "26.3.27" || got.XrayInstalledVersion != "26.3.27" {
		t.Fatalf("versions were blanked to %q/%q", got.XrayVersion, got.XrayInstalledVersion)
	}
}

// A stopped kernel genuinely has no running version, and that has to be
// recordable — otherwise a node that crashed on the new binary keeps reading
// as though it is happily running it.
func TestAStoppedKernelClearsTheRunningVersion(t *testing.T) {
	s, n := versionFixture(t)
	if _, err := s.SetXrayVersions(n.ID, "26.3.27", "26.3.27"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetXrayVersions(n.ID, "", "26.9.1"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetNode(n.ID)
	if got.XrayVersion != "" {
		t.Errorf("running = %q, want empty for a kernel that is not running", got.XrayVersion)
	}
	if got.XrayInstalledVersion != "26.9.1" {
		t.Errorf("installed = %q, want 26.9.1", got.XrayInstalledVersion)
	}
}
