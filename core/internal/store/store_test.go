package store

import "testing"

// The API has always advertised a uniqueness check on node names; migration
// 0014 is what makes it real. Two boxes answering to the same name is a worse
// outcome than a refused rename: the name is the handle in the roster, in the
// alert that wakes somebody at 3am, and in the audit trail.
func TestNodeNamesAreUnique(t *testing.T) {
	s := testStore(t, storeTestKey)
	if _, err := s.CreateNode("tokyo-1", "hash-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateNode("tokyo-1", "hash-b"); err == nil {
		t.Fatal("a second node was created with a name already in use")
	} else if !IsConstraint(err) {
		t.Errorf("error is not recognisable as a conflict: %v", err)
	}

	// And a rename onto an existing name is refused the same way.
	other, err := s.CreateNode("osaka-1", "hash-c")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateNode(other.ID, "tokyo-1", "", ""); err == nil {
		t.Fatal("a node was renamed onto a name already in use")
	} else if !IsConstraint(err) {
		t.Errorf("rename error is not recognisable as a conflict: %v", err)
	}

	// The original is untouched by the refused rename.
	got, err := s.GetNode(other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "osaka-1" {
		t.Errorf("name = %q after a refused rename, want osaka-1", got.Name)
	}
}

// The same for a machine of our own: binding it to a profile should not be the
// moment every holder of that profile silently acquires it.
func TestANewNodeIsDeniedToExistingSubscribers(t *testing.T) {
	st := testStore(t, storeTestKey)
	alice, err := st.CreateUser(User{Name: "alice", Enabled: true}, "hash-a")
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.CreateNode("tokyo-01", "hash-n")
	if err != nil {
		t.Fatal(err)
	}
	denied, err := st.UserNodeDenies(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, no := denied[n.ID]; !no {
		t.Fatal("a new node was open to an existing subscriber")
	}
	// Somebody who arrives later is governed by the profiles they are granted,
	// not by nodes they have never been asked about.
	bob, err := st.CreateUser(User{Name: "bob", Enabled: true}, "hash-b")
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := st.UserNodeDenies(bob.ID); len(d) != 0 {
		t.Fatalf("a new subscriber started out with denials: %v", d)
	}
}
