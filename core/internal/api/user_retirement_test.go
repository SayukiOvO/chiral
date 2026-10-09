package api

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/kernel"
	"github.com/SayukiOvO/chiral/core/internal/node"
	"github.com/SayukiOvO/chiral/core/internal/profile"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

// Use the actual offline Manager, rather than a successful enqueue stub: the
// running credential cannot be confirmed retired, so its local identity and
// secret must remain available for later reconciliation.
func offlineRetirementFixture(t *testing.T) (*Server, *store.Store, string, store.User, store.Profile, store.Credential) {
	t.Helper()
	srv, st := portalFixture(t)
	srv.mgr = node.NewManager(30*time.Second, srv.logger)
	srv.profiles = profile.NewService(st, kernel.New("", template.Xray{}),
		srv.mgr, srv.mgr, user.NewService(st, srv.logger), srv.logger)
	n, err := st.CreateNode("retirement-offline", "retirement-node-hash")
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProfile("retirement-profile")
	if err != nil {
		t.Fatal(err)
	}
	p.InboundTemplate = `{"tag":"retirement-in","protocol":"vless","settings":{"clients":[]}}`
	if err := st.UpdateProfile(p); err != nil {
		t.Fatal(err)
	}
	if err := st.BindProfileNode(p.ID, n.ID); err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(store.User{Name: "retirement-user", Enabled: true}, "retirement-token-hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindUserProfile(u.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserActive(u.ID, true); err != nil {
		t.Fatal(err)
	}
	u, err = st.GetUser(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := st.PutCredential(store.Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID,
		Email: "retirement-user@chiral", Secret: "retirement-original-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, st, seedAdmin(t, st), u, p, credential
}

func TestDeleteUserRetirementFailureDisablesButPreservesRecords(t *testing.T) {
	srv, st, admin, u, p, credential := offlineRetirementFixture(t)
	w := do(t, srv.Handler(), http.MethodDelete, "/api/users/"+u.ID, admin, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("unconfirmed retirement returned %d, want 409: %s", w.Code, w.Body)
	}
	got, err := st.GetUser(u.ID)
	if err != nil || got.Enabled || got.ID != u.ID || got.Name != u.Name || got.Active != u.Active {
		t.Fatalf("failed deletion did not preserve the disabled installed user: %+v, %v", got, err)
	}
	assertRetirementContextPreserved(t, st, u.ID, p.ID, credential)
}

func TestUnbindUserProfileRetirementFailurePreservesBindingAndCredential(t *testing.T) {
	srv, st, admin, u, p, credential := offlineRetirementFixture(t)
	w := do(t, srv.Handler(), http.MethodDelete, "/api/users/"+u.ID+"/profiles/"+p.ID, admin, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("unconfirmed retirement returned %d, want 409: %s", w.Code, w.Body)
	}
	got, err := st.GetUser(u.ID)
	if err != nil || !reflect.DeepEqual(got, u) {
		t.Fatalf("failed unbind changed the user: %+v, %v", got, err)
	}
	assertRetirementContextPreserved(t, st, u.ID, p.ID, credential)
}

func TestProfileDenyRetirementFailureRemainsConflictOnRepeatedRequest(t *testing.T) {
	srv, st, admin, u, p, credential := offlineRetirementFixture(t)
	for attempt := 0; attempt < 2; attempt++ {
		w := do(t, srv.Handler(), http.MethodPut, "/api/users/"+u.ID+"/profiles/"+p.ID, admin, map[string]string{"state": "deny"})
		if w.Code != http.StatusConflict {
			t.Fatalf("deny attempt %d returned %d, want 409: %s", attempt+1, w.Code, w.Body)
		}
		profiles, err := st.UserProfileIDs(u.ID)
		if err != nil || len(profiles) != 0 {
			t.Fatalf("authorized deny was not recorded: %v, %v", profiles, err)
		}
		credentials, err := st.UserCredentials(u.ID)
		if err != nil || len(credentials) != 1 || credentials[0] != credential {
			t.Fatal("repeated unconfirmed deny removed or replaced the original credential")
		}
	}
}

func TestGroupProfileRevocationKeepsPersonallyGrantedCredential(t *testing.T) {
	srv, st, admin, u, p, credential := offlineRetirementFixture(t)
	g, err := st.CreateSubscriberGroup("retirement-group", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserGroup(u.ID, g.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.BindGroupProfile(g.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	// Joining removes prior personal access rows; this explicit new personal
	// grant remains effective independently of the group's grant.
	if err := st.BindUserProfile(u.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	held, err := st.UserHoldsProfileWithoutGroup(u.ID, p.ID, g.ID)
	if err != nil || !held {
		t.Fatalf("personal grant preview failed: held=%v, error=%v", held, err)
	}
	w := do(t, srv.Handler(), http.MethodDelete, "/api/groups/"+g.ID+"/profiles/"+p.ID, admin, nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("group revoke despite retained personal grant returned %d, want 204: %s", w.Code, w.Body)
	}
	groupProfiles, err := st.GroupProfileIDs(g.ID)
	if err != nil || len(groupProfiles) != 0 {
		t.Fatalf("group grant was not revoked: %v, %v", groupProfiles, err)
	}
	assertRetirementContextPreserved(t, st, u.ID, p.ID, credential)
}

func assertRetirementContextPreserved(t *testing.T, st *store.Store, userID, profileID string, credential store.Credential) {
	t.Helper()
	profiles, err := st.UserProfileIDs(userID)
	if err != nil || !reflect.DeepEqual(profiles, []string{profileID}) {
		t.Fatalf("failed retirement lost the binding: %v, %v", profiles, err)
	}
	credentials, err := st.UserCredentials(userID)
	if err != nil || len(credentials) != 1 || !reflect.DeepEqual(credentials[0], credential) {
		// Do not print credential objects: a failure must not expose secrets.
		t.Fatalf("failed retirement lost or changed the original credential; count=%d, error=%v", len(credentials), err)
	}
}
