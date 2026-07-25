package store

import (
	"strings"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/secret"
)

func seedAdmin(t *testing.T, s *Store, name string, role auth.Role) Admin {
	t.Helper()
	hash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAdmin(name, hash, role)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAdminRoundTrip(t *testing.T) {
	s := testStore(t, storeTestKey)
	a := seedAdmin(t, s, "mai", auth.RoleSuperadmin)

	got, err := s.GetAdmin(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "mai" || got.Role != auth.RoleSuperadmin || got.Disabled {
		t.Errorf("did not round-trip: %+v", got)
	}
	if !auth.VerifyPassword("password123", got.PasswordHash) {
		t.Error("stored password does not verify")
	}
}

func TestUsernameIsUnique(t *testing.T) {
	s := testStore(t, storeTestKey)
	seedAdmin(t, s, "mai", auth.RoleOperator)
	if _, err := s.CreateAdmin("mai", "hash", auth.RoleViewer); err == nil {
		t.Error("a duplicate username was accepted")
	}
}

// The schema must reject a role the code does not know, so a bad write cannot
// create an account whose permissions are undefined.
func TestRoleIsConstrained(t *testing.T) {
	s := testStore(t, storeTestKey)
	if _, err := s.CreateAdmin("x", "hash", auth.Role("root")); err == nil {
		t.Error("an unknown role was accepted by the schema")
	}
}

// --- sessions ---

func TestSessionResolvesToItsAdmin(t *testing.T) {
	s := testStore(t, storeTestKey)
	a := seedAdmin(t, s, "mai", auth.RoleOperator)
	token, hash := auth.NewSecret()
	if err := s.CreateSession(hash, a.ID, SessionTTL); err != nil {
		t.Fatal(err)
	}

	got, err := s.AdminBySession(auth.HashSecret(token))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != a.ID {
		t.Errorf("resolved to the wrong admin: %+v", got)
	}
	// Only the hash is stored; the raw token must not appear in the table.
	var stored string
	s.db.QueryRow(`SELECT token_hash FROM admin_sessions LIMIT 1`).Scan(&stored)
	if stored == token {
		t.Error("the raw session token is stored")
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	s := testStore(t, storeTestKey)
	a := seedAdmin(t, s, "mai", auth.RoleOperator)
	_, hash := auth.NewSecret()
	if err := s.CreateSession(hash, a.ID, -time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdminBySession(hash); !IsNotFound(err) {
		t.Errorf("an expired session still resolved: %v", err)
	}
}

// Disabling an account must cut off whoever is already logged in, not wait for
// their session to lapse.
func TestDisabledAdminsSessionStopsWorking(t *testing.T) {
	s := testStore(t, storeTestKey)
	a := seedAdmin(t, s, "mai", auth.RoleOperator)
	_, hash := auth.NewSecret()
	if err := s.CreateSession(hash, a.ID, SessionTTL); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdminBySession(hash); err != nil {
		t.Fatal("setup: session should work")
	}
	if err := s.UpdateAdminRole(a.ID, auth.RoleOperator, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdminBySession(hash); !IsNotFound(err) {
		t.Error("a disabled admin's existing session still works")
	}
}

// Changing a password is how someone reacts to a suspected compromise, so it
// has to end every session for that account.
func TestPasswordChangeEndsSessions(t *testing.T) {
	s := testStore(t, storeTestKey)
	a := seedAdmin(t, s, "mai", auth.RoleSuperadmin)
	_, hash := auth.NewSecret()
	s.CreateSession(hash, a.ID, SessionTTL)

	newHash, _ := auth.HashPassword("a-different-password")
	if err := s.SetAdminPassword(a.ID, newHash); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdminBySession(hash); !IsNotFound(err) {
		t.Error("a session survived the password change")
	}
	got, _ := s.GetAdmin(a.ID)
	if !auth.VerifyPassword("a-different-password", got.PasswordHash) {
		t.Error("the new password does not verify")
	}
}

func TestDeletingAdminCascadesToSessions(t *testing.T) {
	s := testStore(t, storeTestKey)
	a := seedAdmin(t, s, "mai", auth.RoleOperator)
	_, hash := auth.NewSecret()
	s.CreateSession(hash, a.ID, SessionTTL)

	if err := s.DeleteAdmin(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdminBySession(hash); !IsNotFound(err) {
		t.Error("a session outlived its admin")
	}
}

func TestPruneSessionsDropsOnlyExpired(t *testing.T) {
	s := testStore(t, storeTestKey)
	a := seedAdmin(t, s, "mai", auth.RoleOperator)
	_, live := auth.NewSecret()
	_, dead := auth.NewSecret()
	s.CreateSession(live, a.ID, SessionTTL)
	s.CreateSession(dead, a.ID, -time.Hour)

	n, err := s.PruneSessions(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("pruned %d sessions, want 1", n)
	}
	if _, err := s.AdminBySession(live); err != nil {
		t.Error("the live session was pruned")
	}
}

func TestCountEnabledSuperadmins(t *testing.T) {
	s := testStore(t, storeTestKey)
	a := seedAdmin(t, s, "one", auth.RoleSuperadmin)
	seedAdmin(t, s, "two", auth.RoleSuperadmin)
	seedAdmin(t, s, "three", auth.RoleOperator)

	if n, _ := s.CountEnabledSuperadmins(); n != 2 {
		t.Errorf("got %d, want 2", n)
	}
	// A disabled superadmin is not a way in.
	s.UpdateAdminRole(a.ID, auth.RoleSuperadmin, true)
	if n, _ := s.CountEnabledSuperadmins(); n != 1 {
		t.Errorf("after disabling one, got %d, want 1", n)
	}
}

// --- audit ---

func TestAuditRecordsAndPages(t *testing.T) {
	s := testStore(t, storeTestKey)
	actor := auth.Identity{ID: "a1", Name: "mai", Role: auth.RoleSuperadmin}
	for i := 0; i < 5; i++ {
		if err := s.Audit(actor, "node.create", "node", "n1", "tokyo-1", ""); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := s.AuditPage(0, 3, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3, got %d", len(entries))
	}
	// Newest first, so the page reads like a log.
	if entries[0].ID <= entries[1].ID {
		t.Error("entries are not newest-first")
	}
	// Paging backwards by id must not repeat or skip.
	next, _ := s.AuditPage(entries[2].ID, 3, "", "")
	if len(next) != 2 || next[0].ID >= entries[2].ID {
		t.Errorf("paging is wrong: %+v", next)
	}
}

// The trail must keep naming someone after their account is gone.
func TestAuditKeepsTheActorNameAfterDeletion(t *testing.T) {
	s := testStore(t, storeTestKey)
	a := seedAdmin(t, s, "mai", auth.RoleSuperadmin)
	actor := auth.Identity{ID: a.ID, Name: a.Username, Role: a.Role}
	if err := s.Audit(actor, "user.delete", "user", "u1", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAdmin(a.ID); err != nil {
		t.Fatal(err)
	}
	entries, _ := s.AuditPage(0, 10, "", "")
	if len(entries) != 1 {
		t.Fatalf("the entry vanished with its actor: %+v", entries)
	}
	if entries[0].ActorName != "mai" {
		t.Errorf("the actor's name was lost: %+v", entries[0])
	}
}

func TestAuditFilters(t *testing.T) {
	s := testStore(t, storeTestKey)
	mai := auth.Identity{ID: "a1", Name: "mai"}
	bot := auth.Identity{ID: "a2", Name: "bot"}
	s.Audit(mai, "node.create", "node", "n1", "tokyo", "")
	s.Audit(bot, "user.create", "user", "u1", "alice", "")
	s.Audit(mai, "user.create", "user", "u2", "bob", "")

	byActor, _ := s.AuditPage(0, 10, "a1", "")
	if len(byActor) != 2 {
		t.Errorf("actor filter: got %d, want 2", len(byActor))
	}
	byAction, _ := s.AuditPage(0, 10, "", "user.create")
	if len(byAction) != 2 {
		t.Errorf("action filter: got %d, want 2", len(byAction))
	}
	both, _ := s.AuditPage(0, 10, "a1", "user.create")
	if len(both) != 1 {
		t.Errorf("combined filter: got %d, want 1", len(both))
	}
}

func TestPruneAuditRespectsRetention(t *testing.T) {
	s := testStore(t, storeTestKey)
	actor := auth.Identity{ID: "a1", Name: "mai"}
	s.Audit(actor, "recent", "", "", "", "")
	// Backdate one entry past the window.
	s.db.Exec(`UPDATE audit_log SET at = ? WHERE action = 'recent'`,
		time.Now().Add(-AuditRetention-time.Hour).Unix())
	s.Audit(actor, "fresh", "", "", "", "")

	n, err := s.PruneAudit(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	entries, _ := s.AuditPage(0, 10, "", "")
	if len(entries) != 1 || entries[0].Action != "fresh" {
		t.Errorf("wrong entry survived: %+v", entries)
	}
}

// --- alert targets ---

// A bot token is a credential like any other, so it must not sit in the
// database in the clear.
func TestAlertTargetConfigIsEncryptedAtRest(t *testing.T) {
	s := testStore(t, storeTestKey)
	const token = "123456:AA-secret-bot-token"
	target, err := s.CreateAlertTarget(AlertTelegram, "tg", token)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT config FROM alert_targets WHERE id = ?`, target.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "AA-secret-bot-token") {
		t.Fatal("the bot token is stored in plaintext")
	}
	if !secret.IsEncrypted(stored) {
		t.Errorf("not encrypted: %q", stored)
	}
	got, err := s.GetAlertTarget(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config != token {
		t.Errorf("config did not round-trip: %q", got.Config)
	}
}

func TestAlertTargetKindIsConstrained(t *testing.T) {
	s := testStore(t, storeTestKey)
	if _, err := s.CreateAlertTarget("carrier-pigeon", "x", "y"); err == nil {
		t.Error("the schema accepted an unknown target kind")
	}
}

// A first sighting is recorded as already-announced, so switching alerting on
// over an existing fleet does not fire a message per node.
func TestFirstSightingIsAlreadyAnnounced(t *testing.T) {
	s := testStore(t, storeTestKey)
	n, _ := s.CreateNode("tokyo-1", "h")
	now := time.Now()
	if err := s.ObserveNode(n.ID, true, now); err != nil {
		t.Fatal(err)
	}
	states, _ := s.NodeAlertStates()
	st := states[n.ID]
	if st.AnnouncedOnline != st.ObservedOnline {
		t.Errorf("a first sighting is pending announcement: %+v", st)
	}
}

// changed_at must move only when the observation actually differs, or the
// debounce would restart on every sweep and nothing would ever be announced.
func TestObserveKeepsChangedAtWhileSteady(t *testing.T) {
	s := testStore(t, storeTestKey)
	n, _ := s.CreateNode("tokyo-1", "h")
	base := time.Now()
	s.ObserveNode(n.ID, true, base)
	s.ObserveNode(n.ID, false, base.Add(time.Minute)) // the change
	states, _ := s.NodeAlertStates()
	changed := states[n.ID].ChangedAt

	s.ObserveNode(n.ID, false, base.Add(5*time.Minute)) // same state again
	states, _ = s.NodeAlertStates()
	if states[n.ID].ChangedAt != changed {
		t.Errorf("changed_at moved without a change: %d -> %d", changed, states[n.ID].ChangedAt)
	}
}
