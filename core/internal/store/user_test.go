package store

import (
	"strings"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/secret"
)

func seedUser(t *testing.T, s *Store, name string) User {
	t.Helper()
	u, err := s.CreateUser(User{Name: name, Enabled: true}, "hash-"+name)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestUserRoundTrip(t *testing.T) {
	s := testStore(t, storeTestKey)
	u, err := s.CreateUser(User{
		Name: "alice", QuotaBytes: 1 << 30, ExpiresAt: 1800000000,
		RenewPeriod: 2592000, Enabled: true,
	}, "tokenhash")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetUser(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "alice" || got.QuotaBytes != 1<<30 || got.RenewPeriod != 2592000 {
		t.Errorf("did not round-trip: %+v", got)
	}
	if !got.Enabled {
		t.Error("enabled should persist")
	}
	if got.Active {
		t.Error("a new user should not start active")
	}
}

// The subscription token is a credential: only its hash is kept.
func TestSubTokenIsOnlyStoredAsAHash(t *testing.T) {
	s := testStore(t, storeTestKey)
	if _, err := s.CreateUser(User{Name: "alice", Enabled: true}, "the-hash"); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT sub_token_hash FROM users WHERE name = 'alice'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "the-hash" {
		t.Errorf("got %q", stored)
	}
	u, err := s.FindUserBySubTokenHash("the-hash")
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "alice" {
		t.Errorf("lookup by token hash returned %q", u.Name)
	}
	if u.SubToken != "" {
		t.Error("a loaded user must never carry the raw token")
	}
}

func TestResetSubTokenInvalidatesTheOldLink(t *testing.T) {
	s := testStore(t, storeTestKey)
	u := seedUser(t, s, "alice")
	if err := s.ResetSubToken(u.ID, "new-token", "new-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FindUserBySubTokenHash("hash-alice"); !IsNotFound(err) {
		t.Error("the old subscription link still resolves")
	}
	if _, err := s.FindUserBySubTokenHash("new-hash"); err != nil {
		t.Errorf("the new link does not resolve: %v", err)
	}
}

// Strong isolation: the same user on two nodes gets two different secrets.
func TestCredentialsAreDistinctPerAccessPoint(t *testing.T) {
	s := testStore(t, storeTestKey)
	u := seedUser(t, s, "alice")
	p, _ := s.CreateProfile("tokyo-reality")
	n1, _ := s.CreateNode("tokyo-1", "h1")
	n2, _ := s.CreateNode("frankfurt-1", "h2")

	c1, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n1.ID,
		Email: "alice@tokyo-reality-tokyo-1", Secret: "SECRET-ONE",
	})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n2.ID,
		Email: "alice@tokyo-reality-frankfurt-1", Secret: "SECRET-TWO",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c1.Secret == c2.Secret {
		t.Fatal("the same secret was reused across nodes; strong isolation is broken")
	}
	if c1.Email == c2.Email {
		t.Fatal("stats keys collide across nodes")
	}
}

// Assembly calls this on every render; re-minting a secret would lock the user
// out until they refetched their subscription.
func TestPutCredentialIsIdempotent(t *testing.T) {
	s := testStore(t, storeTestKey)
	u := seedUser(t, s, "alice")
	p, _ := s.CreateProfile("p")
	n, _ := s.CreateNode("n", "h")

	first, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID, Email: "alice@p-n", Secret: "ORIGINAL",
	})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID, Email: "alice@p-n", Secret: "REGENERATED",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Secret != "ORIGINAL" || again.ID != first.ID {
		t.Errorf("credential was re-minted: %+v", again)
	}
}

func TestCredentialSecretIsCiphertextOnDisk(t *testing.T) {
	s := testStore(t, storeTestKey)
	u := seedUser(t, s, "alice")
	p, _ := s.CreateProfile("p")
	n, _ := s.CreateNode("n", "h")
	c, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID,
		Email: "alice@p-n", Secret: "THE-USERS-UUID",
	})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.db.QueryRow(`SELECT secret FROM credentials WHERE id = ?`, c.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "THE-USERS-UUID") {
		t.Fatal("credential secret is stored in plaintext")
	}
	if !secret.IsEncrypted(raw) {
		t.Errorf("not encrypted: %q", raw)
	}
	back, err := s.FindCredential(u.ID, p.ID, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Secret != "THE-USERS-UUID" {
		t.Errorf("did not round-trip: %q", back.Secret)
	}
}

func TestDeletingUserCascadesToCredentials(t *testing.T) {
	s := testStore(t, storeTestKey)
	u := seedUser(t, s, "alice")
	p, _ := s.CreateProfile("p")
	n, _ := s.CreateNode("n", "h")
	if _, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID, Email: "alice@p-n", Secret: "x",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.BindUserProfile(u.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	creds, err := s.NodeCredentials(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 0 {
		t.Errorf("credentials outlived their user: %+v", creds)
	}
	ids, _ := s.ProfileUserIDs(p.ID)
	if len(ids) != 0 {
		t.Errorf("profile binding outlived the user: %v", ids)
	}
}

// Deleting a node must not leave credentials pointing at nothing — they are
// the clients[] entries for an inbound that no longer exists.
func TestDeletingNodeCascadesToCredentials(t *testing.T) {
	s := testStore(t, storeTestKey)
	u := seedUser(t, s, "alice")
	p, _ := s.CreateProfile("p")
	n, _ := s.CreateNode("n", "h")
	if _, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID, Email: "alice@p-n", Secret: "x",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNode(n.ID); err != nil {
		t.Fatal(err)
	}
	creds, err := s.UserCredentials(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(creds) != 0 {
		t.Errorf("credentials outlived their node: %+v", creds)
	}
}

func TestEmailIsUniqueFleetWide(t *testing.T) {
	s := testStore(t, storeTestKey)
	a := seedUser(t, s, "alice")
	b := seedUser(t, s, "bob")
	p, _ := s.CreateProfile("p")
	n, _ := s.CreateNode("n", "h")
	if _, err := s.PutCredential(Credential{
		UserID: a.ID, ProfileID: p.ID, NodeID: n.ID, Email: "clash@p-n", Secret: "x",
	}); err != nil {
		t.Fatal(err)
	}
	// Xray reports traffic under the email; two credentials sharing one would
	// silently merge two users' usage.
	if _, err := s.PutCredential(Credential{
		UserID: b.ID, ProfileID: p.ID, NodeID: n.ID, Email: "clash@p-n", Secret: "y",
	}); err == nil {
		t.Fatal("a duplicate stats email was accepted")
	}
}

func TestAddCredentialTrafficUpdatesBothLevels(t *testing.T) {
	s := testStore(t, storeTestKey)
	u := seedUser(t, s, "alice")
	p, _ := s.CreateProfile("p")
	n, _ := s.CreateNode("n", "h")
	if _, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID, Email: "alice@p-n", Secret: "x",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCredentialTraffic("alice@p-n", 100, 250); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCredentialTraffic("alice@p-n", 5, 5); err != nil {
		t.Fatal(err)
	}
	creds, _ := s.UserCredentials(u.ID)
	if creds[0].UpBytes != 105 || creds[0].DownBytes != 255 {
		t.Errorf("credential totals wrong: up=%d down=%d", creds[0].UpBytes, creds[0].DownBytes)
	}
	got, _ := s.GetUser(u.ID)
	if got.UsedBytes != 360 {
		t.Errorf("user total should be the sum of both directions, got %d", got.UsedBytes)
	}
}

// Traffic for a revoked credential is normal (in-flight when it was removed)
// and must not fail the whole report.
func TestTrafficForUnknownEmailIsIgnored(t *testing.T) {
	s := testStore(t, storeTestKey)
	if err := s.AddCredentialTraffic("ghost@nowhere", 10, 10); err != nil {
		t.Errorf("an unknown email should be ignored, got: %v", err)
	}
}

func TestNegativeTrafficIsRejected(t *testing.T) {
	s := testStore(t, storeTestKey)
	if err := s.AddCredentialTraffic("a@b", -1, 0); err == nil {
		t.Error("a negative delta should be rejected, not silently applied")
	}
}

func TestRenewZeroesUsage(t *testing.T) {
	s := testStore(t, storeTestKey)
	u := seedUser(t, s, "alice")
	p, _ := s.CreateProfile("p")
	n, _ := s.CreateNode("n", "h")
	if _, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID, Email: "alice@p-n", Secret: "x",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCredentialTraffic("alice@p-n", 500, 500); err != nil {
		t.Fatal(err)
	}
	if err := s.RenewUser(u.ID, 1900000000); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetUser(u.ID)
	if got.UsedBytes != 0 {
		t.Errorf("renewal should zero usage, got %d", got.UsedBytes)
	}
	if got.ExpiresAt != 1900000000 {
		t.Errorf("expiry not moved: %d", got.ExpiresAt)
	}
}

func TestUnbindProfileDropsCredentials(t *testing.T) {
	s := testStore(t, storeTestKey)
	u := seedUser(t, s, "alice")
	p, _ := s.CreateProfile("p")
	n, _ := s.CreateNode("n", "h")
	if err := s.BindUserProfile(u.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID, Email: "alice@p-n", Secret: "x",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UnbindUserProfile(u.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCredentialsForBinding(u.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	creds, _ := s.UserCredentials(u.ID)
	if len(creds) != 0 {
		t.Errorf("credentials survived the revoked entitlement: %+v", creds)
	}
}

func TestBindUserProfileIsIdempotent(t *testing.T) {
	s := testStore(t, storeTestKey)
	u := seedUser(t, s, "alice")
	p, _ := s.CreateProfile("p")
	for i := 0; i < 2; i++ {
		if err := s.BindUserProfile(u.ID, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	ids, _ := s.UserProfileIDs(u.ID)
	if len(ids) != 1 {
		t.Errorf("expected one binding, got %v", ids)
	}
}
