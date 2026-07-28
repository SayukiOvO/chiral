package store

import "testing"

func configFixture(t *testing.T) (*Store, Node) {
	t.Helper()
	s := testStore(t, storeTestKey)
	n, err := s.CreateNode("tokyo-1", "join-hash")
	if err != nil {
		t.Fatal(err)
	}
	return s, n
}

func TestConfigVersionsAreNewestFirstAndCarryNoBodies(t *testing.T) {
	s, n := configFixture(t)
	for i := 0; i < 3; i++ {
		if _, err := s.InsertConfig(n.ID, `{"v":`+string(rune('0'+i))+`}`, ""); err != nil {
			t.Fatal(err)
		}
	}
	versions, err := s.ConfigVersions(n.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 3 {
		t.Fatalf("got %d versions, want 3", len(versions))
	}
	if versions[0].Version != 3 || versions[2].Version != 1 {
		t.Fatalf("versions are not newest-first: %+v", versions)
	}
}

func TestConfigAtReturnsThatVersionDecrypted(t *testing.T) {
	s, n := configFixture(t)
	if _, err := s.InsertConfig(n.ID, `{"first":true}`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertConfig(n.ID, `{"second":true}`, ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.ConfigAt(n.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config != `{"first":true}` {
		t.Fatalf("config = %q, want the first version's body", got.Config)
	}
}

// The sweep must never remove the version a reconnecting agent would be given,
// which is always the newest.
func TestPruneConfigsKeepsTheNewest(t *testing.T) {
	s, n := configFixture(t)
	for i := 0; i < ConfigHistoryDepth+5; i++ {
		if _, err := s.InsertConfig(n.ID, `{}`, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PruneConfigs(); err != nil {
		t.Fatal(err)
	}
	versions, _ := s.ConfigVersions(n.ID, 100)
	if len(versions) != ConfigHistoryDepth {
		t.Fatalf("kept %d versions, want %d", len(versions), ConfigHistoryDepth)
	}
	if versions[0].Version != int64(ConfigHistoryDepth+5) {
		t.Fatalf("newest version is %d; the prune dropped the one a reconnect would receive",
			versions[0].Version)
	}
	if _, err := s.LatestPushableConfig(n.ID); err != nil {
		t.Fatalf("no pushable config survived the prune: %v", err)
	}
}

// Each node's history is bounded independently — one busy node must not
// consume another's depth.
func TestPruneConfigsIsPerNode(t *testing.T) {
	s, a := configFixture(t)
	b, err := s.CreateNode("frankfurt-2", "join-hash-2")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < ConfigHistoryDepth+5; i++ {
		s.InsertConfig(a.ID, `{}`, "")
	}
	for i := 0; i < 3; i++ {
		s.InsertConfig(b.ID, `{}`, "")
	}
	if _, err := s.PruneConfigs(); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.ConfigVersions(b.ID, 100); len(v) != 3 {
		t.Fatalf("the quiet node kept %d versions, want all 3", len(v))
	}
}
