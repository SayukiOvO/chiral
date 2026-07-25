package profile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/store"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// activeUser sets a user up so their credentials are installed on the node,
// which is the state enforcement has to move them out of.
func activeUser(t *testing.T, svc *Service, st *store.Store, name, profileID, nodeID string) store.User {
	t.Helper()
	u := entitle(t, st, name, profileID, nil)
	if _, err := svc.AssembleNode(nodeID); err != nil { // mints credentials
		t.Fatal(err)
	}
	if _, err := svc.SyncUser(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := st.GetUser(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.Active {
		t.Fatal("setup: user should be active")
	}
	return fresh
}

func TestSyncInstallsAnEntitledUser(t *testing.T) {
	svc, st, push := newFixture(t)
	p, n := realityProfile(t, svc, st)
	u := entitle(t, st, "alice", p.ID, nil)
	if _, err := svc.AssembleNode(n.ID); err != nil {
		t.Fatal(err)
	}

	changed, err := svc.SyncUser(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected the first sync to push something")
	}
	creds, _ := st.UserCredentials(u.ID)
	ops := push.opsFor(creds[0].Email)
	if len(ops) != 1 || ops[0].GetKind() != chiralv1.UserOpKind_USER_OP_KIND_ADD {
		t.Fatalf("expected one ADD, got %+v", ops)
	}
	if ops[0].GetInboundTag() != "reality-in" {
		t.Errorf("op names the wrong inbound: %q", ops[0].GetInboundTag())
	}
	// The account payload is what the kernel installs; it must carry the
	// user's own secret.
	if len(ops[0].GetAccountJson()) == 0 {
		t.Error("ADD carries no account payload")
	}
	got, _ := st.GetUser(u.ID)
	if !got.Active {
		t.Error("user should be recorded as active after a successful push")
	}
}

func TestSyncIsANoOpWhenNothingChanged(t *testing.T) {
	svc, st, push := newFixture(t)
	p, n := realityProfile(t, svc, st)
	u := activeUser(t, svc, st, "alice", p.ID, n.ID)
	before := len(push.userOps)

	changed, err := svc.SyncUser(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if changed || len(push.userOps) != before {
		t.Error("a sweep over an unchanged user should send nothing")
	}
}

func TestDisablingAUserRemovesThemOnline(t *testing.T) {
	svc, st, push := newFixture(t)
	p, n := realityProfile(t, svc, st)
	u := activeUser(t, svc, st, "alice", p.ID, n.ID)
	creds, _ := st.UserCredentials(u.ID)

	u.Enabled = false
	if err := st.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncUser(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	ops := push.opsFor(creds[0].Email)
	last := ops[len(ops)-1]
	if last.GetKind() != chiralv1.UserOpKind_USER_OP_KIND_REMOVE {
		t.Errorf("expected a REMOVE, got %v", last.GetKind())
	}
	got, _ := st.GetUser(u.ID)
	if got.Active {
		t.Error("user should no longer be recorded as active")
	}
}

func TestGoingOverQuotaCutsAccessOff(t *testing.T) {
	svc, st, push := newFixture(t)
	p, n := realityProfile(t, svc, st)
	u := activeUser(t, svc, st, "alice", p.ID, n.ID)
	u.QuotaBytes = 1000
	if err := st.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	creds, _ := st.UserCredentials(u.ID)

	// Under quota: nothing happens.
	if err := st.AddCredentialTraffic(creds[0].Email, 400, 400); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.EnforceQuotas(context.Background()); err != nil || n != 0 {
		t.Fatalf("under quota should change nothing, got n=%d err=%v", n, err)
	}

	// Over quota: cut off.
	if err := st.AddCredentialTraffic(creds[0].Email, 200, 200); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnforceQuotas(context.Background()); err != nil {
		t.Fatal(err)
	}
	ops := push.opsFor(creds[0].Email)
	if ops[len(ops)-1].GetKind() != chiralv1.UserOpKind_USER_OP_KIND_REMOVE {
		t.Error("an over-quota user should be removed from the nodes")
	}
}

func TestExpiryCutsAccessOff(t *testing.T) {
	svc, st, push := newFixture(t)
	p, n := realityProfile(t, svc, st)
	u := activeUser(t, svc, st, "alice", p.ID, n.ID)
	u.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	if err := st.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	creds, _ := st.UserCredentials(u.ID)

	if _, err := svc.EnforceQuotas(context.Background()); err != nil {
		t.Fatal(err)
	}
	ops := push.opsFor(creds[0].Email)
	if ops[len(ops)-1].GetKind() != chiralv1.UserOpKind_USER_OP_KIND_REMOVE {
		t.Error("an expired user should be removed from the nodes")
	}
}

// Renewal rolls the user into the next period and restores access in the same
// sweep, rather than leaving them cut off until someone notices.
func TestRenewalRestoresAccess(t *testing.T) {
	svc, st, push := newFixture(t)
	p, n := realityProfile(t, svc, st)
	u := activeUser(t, svc, st, "alice", p.ID, n.ID)
	creds, _ := st.UserCredentials(u.ID)

	u.QuotaBytes = 1000
	u.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	u.RenewPeriod = 3600
	if err := st.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	if err := st.AddCredentialTraffic(creds[0].Email, 900, 900); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.EnforceQuotas(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetUser(u.ID)
	if got.ExpiresAt <= time.Now().Unix() {
		t.Errorf("expiry was not rolled forward: %d", got.ExpiresAt)
	}
	if got.UsedBytes != 0 {
		t.Errorf("renewal should reset usage, got %d", got.UsedBytes)
	}
	if !got.Active {
		t.Error("a renewed user should still be active")
	}
	// They were never cut off, so no REMOVE should have gone out.
	for _, op := range push.opsFor(creds[0].Email) {
		if op.GetKind() == chiralv1.UserOpKind_USER_OP_KIND_REMOVE {
			t.Error("a user renewed in the same sweep should not be removed first")
		}
	}
}

// A panel that was down for several periods must not leave the user with an
// expiry still in the past.
func TestRenewalCatchesUpWholePeriods(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, n := realityProfile(t, svc, st)
	u := activeUser(t, svc, st, "alice", p.ID, n.ID)
	u.ExpiresAt = time.Now().Add(-10 * time.Hour).Unix()
	u.RenewPeriod = 3600
	if err := st.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnforceQuotas(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetUser(u.ID)
	if got.ExpiresAt <= time.Now().Unix() {
		t.Errorf("expiry is still in the past after renewal: %d", got.ExpiresAt)
	}
}

// If a node did not accept its operation, the user must not be recorded as
// synced — otherwise the difference is never retried and a ban silently
// applies to only part of the fleet.
func TestPartialFailureIsRetriedNextSweep(t *testing.T) {
	svc, st, push := newFixture(t)
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

	// One of the two nodes is unreachable.
	push.offline = map[string]bool{n2.ID: true}
	if _, err := svc.SyncUser(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetUser(u.ID)
	if got.Active {
		t.Fatal("a partially delivered sync must not be recorded as complete")
	}

	// Once it is reachable, the next sweep finishes the job.
	push.offline = nil
	if _, err := svc.SyncUser(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetUser(u.ID)
	if !got.Active {
		t.Error("the retry should have completed the sync")
	}
}

// A user with no credentials anywhere has nothing to push; the state should
// still settle so sweeps stop reconsidering them.
func TestUserWithNoCredentialsSettles(t *testing.T) {
	svc, st, push := newFixture(t)
	u, err := st.CreateUser(store.User{Name: "nobody", Enabled: true}, "h")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncUser(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	if len(push.userOps) != 0 {
		t.Error("nothing should have been pushed")
	}
	got, _ := st.GetUser(u.ID)
	if !got.Active {
		t.Error("state should settle rather than being reconsidered forever")
	}
}

func TestInboundTagForReportsAMissingTag(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, n := realityProfile(t, svc, st)
	p.InboundTemplate = `{"protocol":"vless","port":443}` // no tag
	if err := st.UpdateProfile(p); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.InboundTagFor(p.ID, n.ID); err == nil {
		t.Error("an inbound with no tag cannot be targeted by a user op and should be reported")
	}
}

// --- regressions from the M3 adversarial review ---

// The stored config is what a reconnecting agent replays, so it — not just the
// running kernel — has to reflect who is entitled. Otherwise an online ban is
// undone by the next restart, and a deleted user comes back with no row left
// to revoke them.
func TestStoredConfigDropsARevokedUser(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, n := realityProfile(t, svc, st)
	u := entitle(t, st, "alice", p.ID, nil)

	if _, err := svc.Apply(context.Background(), n.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := st.LatestConfig(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.Config, "alice.") {
		t.Fatal("setup: the user should be in the stored config")
	}

	// Revoke and re-apply, the way the API handlers now do.
	u.Enabled = false
	if err := st.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	if errs := svc.ApplyUserNodes(context.Background(), []string{n.ID}); len(errs) != 0 {
		t.Fatalf("re-assembly failed: %v", errs)
	}
	stored, err = st.LatestConfig(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.Config, "alice.") {
		t.Error("a revoked user is still in the stored config; a restart would restore their access")
	}
}

// The credentials record where a user is installed, so the nodes must be read
// before those rows are dropped.
func TestNodesForUserIsReadableBeforeDeletion(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, n1 := realityProfile(t, svc, st)
	n2, err := st.CreateNode("frankfurt-1", "h2")
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

	ids, err := svc.NodesForUser(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected both nodes, got %v", ids)
	}

	// After deletion there is nothing left to read — which is exactly why the
	// caller must capture it first.
	if err := st.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	if after, _ := svc.NodesForUser(u.ID); len(after) != 0 {
		t.Errorf("credentials outlived the user: %v", after)
	}
}

func TestNodesForUserProfileNarrowsToOneEntitlement(t *testing.T) {
	svc, st, _ := newFixture(t)
	p1, n1 := realityProfile(t, svc, st)
	p2, err := st.CreateProfile("second")
	if err != nil {
		t.Fatal(err)
	}
	p2.InboundTemplate = `{"tag":"second-in","listen":"0.0.0.0","port":8443,"protocol":"vless","settings":{"clients":[],"decryption":"none"}}`
	p2.ClientEntry = `{"id":"{{user.uuid}}","email":"{{user.email}}"}`
	if err := st.UpdateProfile(p2); err != nil {
		t.Fatal(err)
	}
	n2, err := st.CreateNode("frankfurt-1", "h2")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindProfileNode(p2.ID, n2.ID); err != nil {
		t.Fatal(err)
	}
	u := entitle(t, st, "alice", p1.ID, nil)
	if err := st.BindUserProfile(u.ID, p2.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{n1.ID, n2.ID} {
		if _, err := svc.AssembleNode(id); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := svc.NodesForUserProfile(u.ID, p2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != n2.ID {
		t.Errorf("expected only the second profile's node, got %v", ids)
	}
}

// Removing one entitlement must not disturb the user's other access points.
func TestRemoveUserFromProfileTargetsOnlyThatProfile(t *testing.T) {
	svc, st, push := newFixture(t)
	p1, n1 := realityProfile(t, svc, st)
	u := entitle(t, st, "alice", p1.ID, nil)
	if _, err := svc.AssembleNode(n1.ID); err != nil {
		t.Fatal(err)
	}
	creds, _ := st.UserCredentials(u.ID)

	if err := svc.RemoveUserFromProfile(context.Background(), u.ID, p1.ID); err != nil {
		t.Fatal(err)
	}
	ops := push.opsFor(creds[0].Email)
	if len(ops) != 1 || ops[0].GetKind() != chiralv1.UserOpKind_USER_OP_KIND_REMOVE {
		t.Fatalf("expected one REMOVE, got %+v", ops)
	}

	// A profile the user is not in yields nothing.
	push.userOps = nil
	if err := svc.RemoveUserFromProfile(context.Background(), u.ID, "some-other-profile"); err != nil {
		t.Fatal(err)
	}
	if len(push.userOps) != 0 {
		t.Errorf("touched an unrelated profile: %+v", push.userOps)
	}
}

func TestUniqueDropsDuplicateNodes(t *testing.T) {
	got := unique([]string{"a", "b", "a", "c", "b"})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("got %v", got)
	}
}
