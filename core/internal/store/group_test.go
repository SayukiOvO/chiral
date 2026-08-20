package store

import "testing"

// Groups exist so that "who gets this node" is decided once instead of once
// per subscriber. That makes them a permission layer, and the tests that
// matter for a permission layer are the ones about what happens when nobody
// has said anything yet, and what happens when the layer is taken away.

func groupFixture(t *testing.T) (*Store, Node, Profile) {
	t.Helper()
	st := testStore(t, storeTestKey)
	n, err := st.CreateNode("hk-1", "hash-hk")
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProfile("reality-vision")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindProfileNode(p.ID, n.ID); err != nil {
		t.Fatal(err)
	}
	return st, n, p
}

// A group is a class of subscriber, and a class that arrives holding the whole
// fleet is the failure this feature is meant to prevent — the operator's next
// action is to put people in it, not to audit what it already implies.
func TestANewGroupHoldsNothing(t *testing.T) {
	st, n, _ := groupFixture(t)
	g, err := st.CreateSubscriberGroup("trial", "")
	if err != nil {
		t.Fatal(err)
	}
	denied, err := st.GroupNodeDenies(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, no := denied[n.ID]; !no {
		t.Error("a new group was created already holding an existing node")
	}
	// And the other half: a node created afterwards is withheld too, or the
	// first node bought after the group existed would reach its whole
	// membership the moment it was bound.
	later, err := st.CreateNode("jp-1", "hash-jp")
	if err != nil {
		t.Fatal(err)
	}
	denied, _ = st.GroupNodeDenies(g.ID)
	if _, no := denied[later.ID]; !no {
		t.Error("a node created after the group was open to it")
	}
}

func TestAGroupDecidesForItsMembers(t *testing.T) {
	st, n, p := groupFixture(t)
	u, err := st.CreateUser(User{Name: "alice", Enabled: true}, "hash-a")
	if err != nil {
		t.Fatal(err)
	}
	g, _ := st.CreateSubscriberGroup("staff", "")
	if err := st.BindGroupProfile(g.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGroupNodeAccess(g.ID, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserGroup(u.ID, g.ID); err != nil {
		t.Fatal(err)
	}

	profiles, err := st.UserProfileIDs(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0] != p.ID {
		t.Errorf("profiles = %v, want the one the group grants", profiles)
	}
	// The other direction decides which credentials exist on the node; a group
	// grant missing here is a subscription naming a node that refuses it.
	users, err := st.ProfileUserIDs(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0] != u.ID {
		t.Errorf("profile holders = %v, want the group's member", users)
	}
	denied, _ := st.UserNodeDenies(u.ID)
	if _, no := denied[n.ID]; no {
		t.Error("the member is denied a node their group allows")
	}
}

// Joining discards the subscriber's own rows. Every (subscriber, node) pair is
// materialised as a denial the moment either is created, so a join that kept
// them would leave the member covered end to end by exceptions and the group
// deciding nothing at all.
func TestJoiningAGroupClearsWhatTheSubscriberHeld(t *testing.T) {
	st, n, p := groupFixture(t)
	u, _ := st.CreateUser(User{Name: "alice", Enabled: true}, "hash-a")
	if err := st.BindUserProfile(u.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	own, _ := st.UserOwnAccess(u.ID)
	if _, no := own.NodeDenies[n.ID]; !no {
		t.Fatal("fixture: the subscriber should start denied that node")
	}

	g, _ := st.CreateSubscriberGroup("staff", "")
	if err := st.SetUserGroup(u.ID, g.ID); err != nil {
		t.Fatal(err)
	}
	own, _ = st.UserOwnAccess(u.ID)
	if len(own.NodeDenies) != 0 || len(own.Profiles) != 0 {
		t.Errorf("personal rows survived the join: %+v", own)
	}
	if own.GroupID != g.ID {
		t.Errorf("group = %q, want %q", own.GroupID, g.ID)
	}
}

// An exception is the point of keeping per-subscriber rows at all: one person
// in the group who must not have one node, or must have one the others do not.
func TestAPersonalExceptionOutranksTheGroup(t *testing.T) {
	st, n, p := groupFixture(t)
	u, _ := st.CreateUser(User{Name: "alice", Enabled: true}, "hash-a")
	g, _ := st.CreateSubscriberGroup("staff", "")
	st.BindGroupProfile(g.ID, p.ID)
	st.SetGroupNodeAccess(g.ID, []string{n.ID}, nil, nil) // the group is denied this node
	st.SetUserGroup(u.ID, g.ID)

	denied, _ := st.UserNodeDenies(u.ID)
	if _, no := denied[n.ID]; !no {
		t.Fatal("fixture: the group should withhold the node")
	}
	if err := st.SetUserObjectAccess(u.ID, "node", n.ID, true, false); err != nil {
		t.Fatal(err)
	}
	denied, _ = st.UserNodeDenies(u.ID)
	if _, no := denied[n.ID]; no {
		t.Error("an allow exception did not override the group's denial")
	}

	// And the same in reverse: withheld from one member of a group that holds it.
	st.SetGroupNodeAccess(g.ID, nil, nil, nil)
	if err := st.SetUserObjectAccess(u.ID, "node", n.ID, false, false); err != nil {
		t.Fatal(err)
	}
	denied, _ = st.UserNodeDenies(u.ID)
	if _, no := denied[n.ID]; !no {
		t.Error("a deny exception did not override the group's grant")
	}

	// Clearing hands the decision back.
	if err := st.SetUserObjectAccess(u.ID, "node", n.ID, false, true); err != nil {
		t.Fatal(err)
	}
	denied, _ = st.UserNodeDenies(u.ID)
	if _, no := denied[n.ID]; no {
		t.Error("clearing the exception did not return the node to the group's answer")
	}
	own, _ := st.UserOwnAccess(u.ID)
	if len(own.NodeAllows) != 0 || len(own.NodeDenies) != 0 {
		t.Errorf("clearing left rows behind: %+v", own)
	}
}

// The console saves an end state, most of which is already what the group
// says. Written out per object, the first save would bury the group under a
// full set of exceptions and it would stop deciding anything.
func TestSavingWhatTheGroupAlreadySaysWritesNothing(t *testing.T) {
	st, n, _ := groupFixture(t)
	other, _ := st.CreateNode("jp-1", "hash-jp")
	u, _ := st.CreateUser(User{Name: "alice", Enabled: true}, "hash-a")
	g, _ := st.CreateSubscriberGroup("staff", "")
	st.SetGroupNodeAccess(g.ID, []string{other.ID}, nil, nil)
	st.SetUserGroup(u.ID, g.ID)

	// Exactly the group's answer: hk allowed, jp denied.
	if err := st.SetUserNodeAccess(u.ID, []string{other.ID}, nil, nil); err != nil {
		t.Fatal(err)
	}
	own, _ := st.UserOwnAccess(u.ID)
	if len(own.NodeDenies) != 0 || len(own.NodeAllows) != 0 {
		t.Errorf("agreeing with the group wrote exceptions: %+v", own)
	}
	// Disagreeing writes exactly one row, on the object that differs.
	if err := st.SetUserNodeAccess(u.ID, []string{n.ID}, nil, nil); err != nil {
		t.Fatal(err)
	}
	own, _ = st.UserOwnAccess(u.ID)
	if len(own.NodeDenies) != 1 || len(own.NodeAllows) != 1 {
		t.Errorf("want one denial and one allowance, got %+v", own)
	}
	denied, _ := st.UserNodeDenies(u.ID)
	if _, no := denied[n.ID]; !no {
		t.Error("the node the operator withheld is not withheld")
	}
	if _, no := denied[other.ID]; no {
		t.Error("the node the operator granted is still withheld")
	}
}

// Leaving a group must not be a way to acquire the whole fleet: after a join
// the personal tables hold only exceptions, so a member cut loose with those
// tables as-is would be denied nothing at all.
func TestLeavingAGroupKeepsWhatItDecided(t *testing.T) {
	st, n, p := groupFixture(t)
	jp, _ := st.CreateNode("jp-1", "hash-jp")
	u, _ := st.CreateUser(User{Name: "alice", Enabled: true}, "hash-a")
	g, _ := st.CreateSubscriberGroup("staff", "")
	st.BindGroupProfile(g.ID, p.ID)
	st.SetGroupNodeAccess(g.ID, []string{jp.ID}, nil, nil)
	st.SetUserGroup(u.ID, g.ID)

	if err := st.SetUserGroup(u.ID, ""); err != nil {
		t.Fatal(err)
	}
	denied, _ := st.UserNodeDenies(u.ID)
	if _, no := denied[jp.ID]; !no {
		t.Error("a node the group withheld became available by leaving it")
	}
	if _, no := denied[n.ID]; no {
		t.Error("a node the group allowed was lost by leaving it")
	}
	profiles, _ := st.UserProfileIDs(u.ID)
	if len(profiles) != 1 || profiles[0] != p.ID {
		t.Errorf("profiles = %v, want the grant the group had made", profiles)
	}
}

// Deleting a group is dissolving it, not publishing it.
func TestDeletingAGroupLeavesItsMembersWhereTheyWere(t *testing.T) {
	st, _, p := groupFixture(t)
	jp, _ := st.CreateNode("jp-1", "hash-jp")
	u, _ := st.CreateUser(User{Name: "alice", Enabled: true}, "hash-a")
	g, _ := st.CreateSubscriberGroup("staff", "")
	st.BindGroupProfile(g.ID, p.ID)
	st.SetGroupNodeAccess(g.ID, []string{jp.ID}, nil, nil)
	st.SetUserGroup(u.ID, g.ID)

	if err := st.DeleteSubscriberGroup(g.ID); err != nil {
		t.Fatal(err)
	}
	denied, _ := st.UserNodeDenies(u.ID)
	if _, no := denied[jp.ID]; !no {
		t.Error("deleting the group handed its members a node it withheld")
	}
	profiles, _ := st.UserProfileIDs(u.ID)
	if len(profiles) != 1 {
		t.Errorf("profiles = %v, want the group's grant to have been kept", profiles)
	}
	if id, _ := st.UserGroupID(u.ID); id != "" {
		t.Errorf("group = %q after deletion, want none", id)
	}
}

// A subscriber's own rule set wins over their group's, and ruleset_none is how
// they say "no rules" against a group that states one — NULL cannot mean both
// "inherit" and "none".
func TestTheRuleSetResolvesOwnThenGroup(t *testing.T) {
	st, _, _ := groupFixture(t)
	rs, err := st.CreateRuleset("acl4ssr", "standard", "")
	if err != nil {
		t.Fatal(err)
	}
	mine, err := st.CreateRuleset("mine", "standard", "")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := st.CreateUser(User{Name: "alice", Enabled: true}, "hash-a")
	g, _ := st.CreateSubscriberGroup("staff", "")
	if err := st.SetGroupRuleset(g.ID, rs.ID); err != nil {
		t.Fatal(err)
	}
	st.SetUserGroup(u.ID, g.ID)

	if got, _ := st.EffectiveRulesetID(u.ID); got != rs.ID {
		t.Errorf("ruleset = %q, want the group's %q", got, rs.ID)
	}
	if err := st.SetUserRuleset(u.ID, mine.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.EffectiveRulesetID(u.ID); got != mine.ID {
		t.Errorf("ruleset = %q, want the subscriber's own %q", got, mine.ID)
	}
	if err := st.SetUserRulesetNone(u.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.EffectiveRulesetID(u.ID); got != "" {
		t.Errorf("ruleset = %q, want none", got)
	}
}
