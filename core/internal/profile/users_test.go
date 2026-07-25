package profile

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

// entitle creates a user, grants them the profile, and returns them.
func entitle(t *testing.T, st *store.Store, name, profileID string, mut func(*store.User)) store.User {
	t.Helper()
	u := store.User{Name: name, Enabled: true}
	if mut != nil {
		mut(&u)
	}
	created, err := st.CreateUser(u, "hash-"+name)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindUserProfile(created.ID, profileID); err != nil {
		t.Fatal(err)
	}
	return created
}

func clientEmails(t *testing.T, cfg []byte) []string {
	t.Helper()
	var parsed struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Settings struct {
				Clients []struct {
					ID    string `json:"id"`
					Email string `json:"email"`
				} `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, in := range parsed.Inbounds {
		for _, c := range in.Settings.Clients {
			out = append(out, c.Email)
		}
	}
	return out
}

func TestEntitledUsersAppearInTheConfig(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, n := realityProfile(t, svc, st)
	entitle(t, st, "alice", p.ID, nil)
	entitle(t, st, "bob", p.ID, nil)

	cfg, err := svc.AssembleNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	emails := clientEmails(t, cfg)
	if len(emails) != 2 {
		t.Fatalf("expected both users in clients[], got %v", emails)
	}
	for _, want := range []string{"alice@", "bob@"} {
		found := false
		for _, e := range emails {
			if strings.HasPrefix(e, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no credential for %s in %v", want, emails)
		}
	}
}

// Strong isolation, end to end: the same user on two nodes must appear with
// two different secrets.
func TestSameUserGetsDifferentSecretsPerNode(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, n1 := realityProfile(t, svc, st)
	n2, err := st.CreateNode("frankfurt-1", "hash2")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindProfileNode(p.ID, n2.ID); err != nil {
		t.Fatal(err)
	}
	u := entitle(t, st, "alice", p.ID, nil)

	for _, id := range []string{n1.ID, n2.ID} {
		if _, err := svc.AssembleNode(id); err != nil {
			t.Fatal(err)
		}
	}
	creds, err := st.UserCredentials(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 2 {
		t.Fatalf("expected one credential per node, got %d", len(creds))
	}
	if creds[0].Secret == creds[1].Secret {
		t.Error("the same secret was issued for two nodes; strong isolation is broken")
	}
	if creds[0].Email == creds[1].Email {
		t.Error("stats keys collide across nodes")
	}
}

// Re-assembling must not churn secrets: a new secret would silently lock out
// every client the user had already configured.
func TestReassemblyKeepsTheSameSecret(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, n := realityProfile(t, svc, st)
	u := entitle(t, st, "alice", p.ID, nil)

	if _, err := svc.AssembleNode(n.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := st.UserCredentials(u.ID)
	if _, err := svc.AssembleNode(n.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := st.UserCredentials(u.ID)
	if before[0].Secret != after[0].Secret {
		t.Error("re-assembly re-minted the user's secret")
	}
}

// Quota, expiry and the operator switch each keep a user out of the config —
// that is how enforcement reaches the node.
func TestDisallowedUsersAreLeftOutOfTheConfig(t *testing.T) {
	past := time.Now().Add(-time.Hour).Unix()
	cases := []struct {
		name string
		mut  func(*store.User)
	}{
		{"disabled", func(u *store.User) { u.Enabled = false }},
		{"expired", func(u *store.User) { u.ExpiresAt = past }},
		{"over quota", func(u *store.User) { u.QuotaBytes = 100 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, st, _ := newFixture(t)
			p, n := realityProfile(t, svc, st)
			entitle(t, st, "alice", p.ID, tc.mut)
			entitle(t, st, "bob", p.ID, nil)

			if tc.name == "over quota" {
				// Push alice past her quota.
				if _, err := svc.AssembleNode(n.ID); err != nil {
					t.Fatal(err)
				}
				creds, _ := st.NodeCredentials(n.ID)
				for _, c := range creds {
					if strings.HasPrefix(c.Email, "alice@") {
						if err := st.AddCredentialTraffic(c.Email, 100, 100); err != nil {
							t.Fatal(err)
						}
					}
				}
			}

			cfg, err := svc.AssembleNode(n.ID)
			if err != nil {
				t.Fatal(err)
			}
			emails := clientEmails(t, cfg)
			for _, e := range emails {
				if strings.HasPrefix(e, "alice@") {
					t.Errorf("a %s user is still in the config: %v", tc.name, emails)
				}
			}
			if len(emails) != 1 || !strings.HasPrefix(emails[0], "bob@") {
				t.Errorf("the allowed user should remain, got %v", emails)
			}
		})
	}
}

// Revoking access must not destroy the credential: re-enabling would otherwise
// hand the user a different secret and break clients they had configured.
func TestRevokedUserKeepsTheirCredential(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, n := realityProfile(t, svc, st)
	u := entitle(t, st, "alice", p.ID, nil)

	if _, err := svc.AssembleNode(n.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := st.UserCredentials(u.ID)

	u.Enabled = false
	if err := st.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	cfg, err := svc.AssembleNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if emails := clientEmails(t, cfg); len(emails) != 0 {
		t.Errorf("disabled user still served: %v", emails)
	}

	u.Enabled = true
	if err := st.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AssembleNode(n.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := st.UserCredentials(u.ID)
	if after[0].Secret != before[0].Secret {
		t.Error("re-enabling issued a different secret; the user's clients would all break")
	}
}

// A user entitled to a profile gains every node bound to it, with no further
// action — that is the whole point of the binding model.
func TestBindingANodeExtendsEveryEntitledUser(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, _ := realityProfile(t, svc, st)
	entitle(t, st, "alice", p.ID, nil)

	fresh, err := st.CreateNode("osaka-1", "hash3")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindProfileNode(p.ID, fresh.ID); err != nil {
		t.Fatal(err)
	}
	cfg, err := svc.AssembleNode(fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if emails := clientEmails(t, cfg); len(emails) != 1 {
		t.Errorf("the new node did not pick up the entitled user: %v", emails)
	}
}

// The end the whole chain exists for: a config with real users that Xray takes.
func TestConfigWithUsersPassesXrayTest(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, n := realityProfile(t, svc, st)
	entitle(t, st, "alice", p.ID, nil)

	pv, err := svc.Preview(context.Background(), n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Tested {
		t.Skip("no xray binary; nothing to verify against")
	}
	if pv.TestError != "" {
		t.Fatalf("xray rejected a config with users: %s\n%s", pv.TestError, pv.Config)
	}
}

func TestStatsEmailIsStableAndSanitised(t *testing.T) {
	// ">>>" is Xray's stat-name separator; a name carrying it would corrupt
	// every reported key.
	got := user.StatsEmail("a>>>b", "pro file", "abc123")
	if strings.Contains(got, ">>>") {
		t.Errorf("separator survived sanitising: %q", got)
	}
	if strings.Contains(got, " ") {
		t.Errorf("whitespace survived sanitising: %q", got)
	}
	if !strings.HasSuffix(got, ".abc123") {
		t.Errorf("node id should make the key unique fleet-wide: %q", got)
	}
}
