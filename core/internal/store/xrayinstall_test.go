package store

import (
	"testing"
	"time"
)

func installFixture(t *testing.T) (*Store, Node) {
	t.Helper()
	s := testStore(t, storeTestKey)
	n, err := s.CreateNode("tokyo-1", "join-hash")
	if err != nil {
		t.Fatal(err)
	}
	return s, n
}

func TestStartAndReadAnInstall(t *testing.T) {
	s, n := installFixture(t)
	now := time.Unix(1_700_000_000, 0)

	want := XrayInstall{
		NodeID:      n.ID,
		Version:     "26.9.1",
		DownloadURL: "https://example.invalid/Xray-linux-64.zip",
		SHA256:      "aa11c3685c71da0ffc71e511db50404609e7e963bb914b048f59a6a00af8930e",
		Activate:    true,
		Phase:       "UNSPECIFIED",
	}
	if err := s.StartXrayInstall(want, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.XrayInstallFor(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != want.Version || got.SHA256 != want.SHA256 || !got.Activate {
		t.Fatalf("read back %+v", got)
	}
	if got.StartedAt != now.Unix() {
		t.Errorf("started_at = %d, want %d", got.StartedAt, now.Unix())
	}
}

// A second attempt replaces the first rather than accumulating: the useful
// questions are "what is this node doing now" and "how did the last attempt
// end", and both are answered by one row.
func TestANewAttemptReplacesTheOldAndClearsItsMessage(t *testing.T) {
	s, n := installFixture(t)
	base := time.Unix(1_700_000_000, 0)

	if err := s.StartXrayInstall(XrayInstall{NodeID: n.ID, Version: "26.9.1", SHA256: "a", Phase: "UNSPECIFIED"}, base); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateXrayInstall(n.ID, "26.9.1", "ROLLED_BACK", "it exploded", base); err != nil {
		t.Fatal(err)
	}
	if err := s.StartXrayInstall(XrayInstall{NodeID: n.ID, Version: "26.10.1", SHA256: "b", Phase: "UNSPECIFIED"}, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	got, err := s.XrayInstallFor(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "26.10.1" {
		t.Fatalf("version = %q, want 26.10.1", got.Version)
	}
	if got.Message != "" {
		t.Errorf("the previous attempt's failure message survived: %q", got.Message)
	}
	if got.Phase != "UNSPECIFIED" {
		t.Errorf("phase = %q; the new attempt inherited the old one's outcome", got.Phase)
	}
}

// The guard that keeps a failed upgrade from being read as a success: a status
// for a superseded version must not land on the current attempt.
func TestAStatusForASupersededVersionIsRejected(t *testing.T) {
	s, n := installFixture(t)
	now := time.Unix(1_700_000_000, 0)

	if err := s.StartXrayInstall(XrayInstall{NodeID: n.ID, Version: "26.10.1", SHA256: "b", Phase: "DOWNLOADING"}, now); err != nil {
		t.Fatal(err)
	}
	// A late status from the attempt that came before.
	ok, err := s.UpdateXrayInstall(n.ID, "26.9.1", "ACTIVE", "running and answering", now)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a status for a superseded version was applied")
	}
	got, _ := s.XrayInstallFor(n.ID)
	if got.Phase != "DOWNLOADING" {
		t.Fatalf("phase = %q; the current attempt was stamped with the old one's outcome", got.Phase)
	}
}

func TestUpdateAppliesToTheMatchingVersion(t *testing.T) {
	s, n := installFixture(t)
	now := time.Unix(1_700_000_000, 0)
	if err := s.StartXrayInstall(XrayInstall{NodeID: n.ID, Version: "26.9.1", SHA256: "a", Phase: "UNSPECIFIED"}, now); err != nil {
		t.Fatal(err)
	}
	ok, err := s.UpdateXrayInstall(n.ID, "26.9.1", "ACTIVE", "running and answering", now.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("update: ok=%v err=%v", ok, err)
	}
	got, _ := s.XrayInstallFor(n.ID)
	if got.Phase != "ACTIVE" || got.Message != "running and answering" {
		t.Fatalf("read back %+v", got)
	}
	if got.UpdatedAt != now.Add(time.Minute).Unix() {
		t.Errorf("updated_at = %d", got.UpdatedAt)
	}
}

func TestDeletingANodeRemovesItsInstall(t *testing.T) {
	s, n := installFixture(t)
	if err := s.StartXrayInstall(XrayInstall{NodeID: n.ID, Version: "26.9.1", SHA256: "a", Phase: "UNSPECIFIED"}, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNode(n.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM node_xray_installs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%d install rows survived the node's deletion", count)
	}
}

func TestPlatformPersistsAndIsNotBlankedByAnOlderAgent(t *testing.T) {
	s, n := installFixture(t)
	changed, err := s.SetNodePlatform(n.ID, "linux/arm64")
	if err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v", changed, err)
	}
	got, _ := s.GetNode(n.ID)
	if got.Platform != "linux/arm64" {
		t.Fatalf("platform = %q", got.Platform)
	}
	// Same value again is not a change.
	if changed, _ := s.SetNodePlatform(n.ID, "linux/arm64"); changed {
		t.Error("an unchanged platform reported a change")
	}
	// An agent too old to report one must not blank it, or the node becomes
	// un-upgradable with no visible reason.
	if changed, _ := s.SetNodePlatform(n.ID, ""); changed {
		t.Error("an empty platform reported a change")
	}
	got, _ = s.GetNode(n.ID)
	if got.Platform != "linux/arm64" {
		t.Fatalf("platform was blanked to %q", got.Platform)
	}
}
