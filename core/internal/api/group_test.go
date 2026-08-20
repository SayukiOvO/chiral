package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/store"
)

// The group endpoints are where an operator hands access to a whole class of
// subscriber at once, so the tests here are about the two moments that change
// what people hold: the move into a group, and the grant made to one.

func TestAGroupGrantReachesItsMembers(t *testing.T) {
	srv, st, admin := egressFixture(t)
	h := srv.Handler()
	node, _ := st.CreateNode("hk-1", "hash-hk")
	prof, _ := st.CreateProfile("reality-vision")
	st.BindProfileNode(prof.ID, node.ID)
	u, _ := st.CreateUser(store.User{Name: "alice", Enabled: true}, "hash-a")

	w := do(t, h, "POST", "/api/groups", admin, map[string]string{"name": "staff"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status %d, want 201: %s", w.Code, w.Body)
	}
	var g groupView
	json.Unmarshal(w.Body.Bytes(), &g)

	// A group starts holding nothing, which is what makes putting people in it
	// safe to do before deciding what it gets.
	w = do(t, h, "GET", "/api/groups/"+g.ID+"/nodes", admin, nil)
	var access struct {
		Fleet []nodeAccessEntry `json:"fleet"`
	}
	json.Unmarshal(w.Body.Bytes(), &access)
	if len(access.Fleet) != 1 || access.Fleet[0].Allowed {
		t.Fatalf("a new group already holds a node: %+v", access.Fleet)
	}

	if w = do(t, h, "PUT", "/api/users/"+u.ID+"/group", admin,
		map[string]string{"group_id": g.ID}); w.Code != http.StatusNoContent {
		t.Fatalf("join: status %d, want 204: %s", w.Code, w.Body)
	}
	if w = do(t, h, "POST", "/api/groups/"+g.ID+"/profiles/"+prof.ID, admin, nil); w.Code != http.StatusNoContent {
		t.Fatalf("grant: status %d, want 204: %s", w.Code, w.Body)
	}
	if w = do(t, h, "PUT", "/api/groups/"+g.ID+"/nodes", admin,
		map[string]any{"denied_nodes": []string{}}); w.Code != http.StatusNoContent {
		t.Fatalf("open the node: status %d, want 204: %s", w.Code, w.Body)
	}

	w = do(t, h, "GET", "/api/users/"+u.ID+"/nodes", admin, nil)
	json.Unmarshal(w.Body.Bytes(), &access)
	if len(access.Fleet) != 1 {
		t.Fatalf("fleet = %+v", access.Fleet)
	}
	got := access.Fleet[0]
	if !got.Entitled || !got.Allowed {
		t.Errorf("the member does not hold the node their group grants: %+v", got)
	}
	if !got.FromGroup {
		t.Error("the decision is the group's and is not marked as inherited")
	}
}

// Joining discards what the subscriber held: their own rows would otherwise
// cover every object and the group would decide nothing. The console warns
// about it; this asserts the endpoint really does it.
func TestJoiningAGroupThroughTheAPIClearsPersonalRows(t *testing.T) {
	srv, st, admin := egressFixture(t)
	h := srv.Handler()
	node, _ := st.CreateNode("hk-1", "hash-hk")
	prof, _ := st.CreateProfile("reality-vision")
	st.BindProfileNode(prof.ID, node.ID)
	u, _ := st.CreateUser(store.User{Name: "alice", Enabled: true}, "hash-a")
	st.BindUserProfile(u.ID, prof.ID)

	// A credential minted under that grant, so the assertion below that they
	// are gone does not hold vacuously.
	if _, err := st.PutCredential(store.Credential{
		UserID: u.ID, ProfileID: prof.ID, NodeID: node.ID,
		Email: "alice@chiral", Secret: "a-uuid",
	}); err != nil {
		t.Fatal(err)
	}

	w := do(t, h, "POST", "/api/groups", admin, map[string]string{"name": "staff"})
	var g groupView
	json.Unmarshal(w.Body.Bytes(), &g)
	if w = do(t, h, "PUT", "/api/users/"+u.ID+"/group", admin,
		map[string]string{"group_id": g.ID}); w.Code != http.StatusNoContent {
		t.Fatalf("join: status %d: %s", w.Code, w.Body)
	}

	own, err := st.UserOwnAccess(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(own.Profiles) != 0 || len(own.NodeDenies) != 0 {
		t.Errorf("personal rows survived the join: %+v", own)
	}
	// And the credentials minted under the grant they no longer hold are gone,
	// or a client that already has one reconnects straight through the change.
	creds, err := st.UserCredentials(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 0 {
		t.Errorf("credentials survived a grant the subscriber no longer holds: %+v", creds)
	}
}

// A group with a rule set decides for members who have not chosen one, and a
// member can still say "none" against it — an empty choice means inherit, so
// without the third state they could not.
func TestARuleSetIsInheritedUntilTheMemberSaysOtherwise(t *testing.T) {
	srv, st, admin := egressFixture(t)
	h := srv.Handler()
	rs, _ := st.CreateRuleset("acl4ssr", "standard", "")
	u, _ := st.CreateUser(store.User{Name: "alice", Enabled: true}, "hash-a")
	w := do(t, h, "POST", "/api/groups", admin, map[string]string{"name": "staff"})
	var g groupView
	json.Unmarshal(w.Body.Bytes(), &g)
	do(t, h, "PUT", "/api/users/"+u.ID+"/group", admin, map[string]string{"group_id": g.ID})
	if w = do(t, h, "PUT", "/api/groups/"+g.ID+"/ruleset", admin,
		map[string]string{"ruleset_id": rs.ID}); w.Code != http.StatusNoContent {
		t.Fatalf("set group ruleset: status %d: %s", w.Code, w.Body)
	}

	w = do(t, h, "GET", "/api/users/"+u.ID, admin, nil)
	var view userView
	json.Unmarshal(w.Body.Bytes(), &view)
	if view.EffectiveRulesetID != rs.ID {
		t.Errorf("effective ruleset = %q, want the group's %q", view.EffectiveRulesetID, rs.ID)
	}
	if view.RulesetID != "" {
		t.Errorf("ruleset_id = %q, want the member's own choice to be empty", view.RulesetID)
	}

	if w = do(t, h, "PUT", "/api/users/"+u.ID+"/ruleset", admin,
		map[string]any{"none": true}); w.Code != http.StatusNoContent {
		t.Fatalf("excuse the member: status %d: %s", w.Code, w.Body)
	}
	w = do(t, h, "GET", "/api/users/"+u.ID, admin, nil)
	json.Unmarshal(w.Body.Bytes(), &view)
	if view.EffectiveRulesetID != "" {
		t.Errorf("effective ruleset = %q, want none", view.EffectiveRulesetID)
	}
}
