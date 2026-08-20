package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

// The portal introduces a second kind of subject into a panel that had one.
// The tests that matter are not "does login work" but "can either subject
// reach the other's routes", because that is the failure that would not
// announce itself.

func portalFixture(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	box, err := secret.NewBox("portal-test-key-0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	srv := &Server{
		st:      st,
		limiter: newLimiter(1024),
		portal:  PortalConfig{Mode: PortalOpen, PublicURL: "https://panel.example"},
		logger:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
	return srv, st
}

// seedSubscriber creates a proxy user with a portal login and returns a live
// session token.
func seedSubscriber(t *testing.T, srv *Server, st *store.Store, email string) (store.User, string) {
	t.Helper()
	hash, err := auth.HashPassword("subscriber-password")
	if err != nil {
		t.Fatal(err)
	}
	token, tokenHash := auth.NewSecret()
	// A generated name, as self-registration produces: never derived from the
	// address, so a test cannot accidentally assert on a name that leaked one.
	u, err := st.CreateUserWithToken(store.User{Name: newSubscriberName(), Enabled: true}, token, tokenHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUserAccount(u.ID, email, hash); err != nil {
		t.Fatal(err)
	}
	session, sessionHash := auth.NewSecret()
	if err := st.CreatePortalSession(sessionHash, u.ID, store.PortalSessionTTL); err != nil {
		t.Fatal(err)
	}
	return u, session
}

func seedAdmin(t *testing.T, st *store.Store) string {
	t.Helper()
	hash, err := auth.HashPassword("admin-password")
	if err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateAdmin("mai", hash, auth.RoleSuperadmin)
	if err != nil {
		t.Fatal(err)
	}
	token, tokenHash := auth.NewSecret()
	if err := st.CreateSession(tokenHash, a.ID, store.SessionTTL); err != nil {
		t.Fatal(err)
	}
	return token
}

func do(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		raw, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(raw))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// A subscriber's token must not open a single operator route. This is the
// test that would catch someone adding "user" to auth.rank(), or resolving
// portal tokens through the admin path.
func TestPortalTokenIsRefusedByEveryAdminRoute(t *testing.T) {
	srv, st := portalFixture(t)
	u, portalToken := seedSubscriber(t, srv, st, "sub@example.com")
	h := srv.Handler()

	adminRoutes := []struct{ method, path string }{
		{"GET", "/api/nodes"},
		{"GET", "/api/users"},
		{"GET", "/api/users/" + u.ID},
		{"GET", "/api/users/" + u.ID + "/devices"},
		{"GET", "/api/profiles"},
		{"GET", "/api/variables"},
		{"GET", "/api/traffic"},
		{"GET", "/api/audit"},
		{"GET", "/api/alerts"},
		{"GET", "/api/whoami"},
		{"GET", "/api/mfa"},
		{"GET", "/api/admins"},
		{"GET", "/api/groups"},
		// The worst one: a fully rendered config.json, REALITY private keys
		// and every user's credential in the clear.
		{"GET", "/api/nodes/anything/config/preview"},
	}
	for _, rt := range adminRoutes {
		w := do(t, h, rt.method, rt.path, portalToken, nil)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status %d with a portal token, want 401", rt.method, rt.path, w.Code)
		}
	}
}

// And the reverse: an operator's session is not a subscriber's session. It
// carries no user id, so admitting it would mean answering for an arbitrary
// user or panicking.
func TestAdminTokenIsRefusedByEveryPortalRoute(t *testing.T) {
	srv, st := portalFixture(t)
	adminToken := seedAdmin(t, st)
	h := srv.Handler()

	portalRoutes := []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/portal/me", nil},
		{"POST", "/api/portal/logout", nil},
		{"POST", "/api/portal/password", map[string]string{"current_password": "x", "new_password": "yyyyyyyy"}},
	}
	for _, rt := range portalRoutes {
		w := do(t, h, rt.method, rt.path, adminToken, rt.body)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status %d with an admin token, want 401", rt.method, rt.path, w.Code)
		}
	}
}

// The environment token is a superadmin, and it is the most likely thing to be
// tried against the portal by accident.
func TestEnvTokenIsRefusedByThePortal(t *testing.T) {
	srv, _ := portalFixture(t)
	srv.adminToken = "env-token-value"
	h := srv.Handler()

	if w := do(t, h, "GET", "/api/portal/me", "env-token-value", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("status %d with the env token, want 401", w.Code)
	}
}

func TestPortalSessionReachesItsOwnRoutes(t *testing.T) {
	srv, st := portalFixture(t)
	_, portalToken := seedSubscriber(t, srv, st, "sub@example.com")
	h := srv.Handler()

	w := do(t, h, "GET", "/api/portal/me", portalToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d for the portal's own route, want 200: %s", w.Code, w.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["account"]; !ok {
		t.Error("no account in the response")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("one person's account state was returned without Cache-Control: no-store")
	}
}

// With the portal off the routes must not exist at all, so upgrading a panel
// that never asked for one does not quietly grow a public surface.
func TestPortalRoutesAreAbsentWhenOff(t *testing.T) {
	srv, _ := portalFixture(t)
	srv.portal = PortalConfig{Mode: PortalOff}
	h := srv.Handler()

	for _, path := range []string{"/api/portal/me", "/api/portal/login", "/api/portal/config"} {
		w := do(t, h, "GET", path, "", nil)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: status %d with the portal off, want 404", path, w.Code)
		}
	}
}

// Registration must answer identically whether or not the address is taken.
// A 409 here would be a public list of the operator's customers.
func TestRegistrationDoesNotRevealExistingAddresses(t *testing.T) {
	srv, st := portalFixture(t)
	seedSubscriber(t, srv, st, "taken@example.com")
	h := srv.Handler()

	fresh := do(t, h, "POST", "/api/portal/register", "",
		map[string]string{"email": "new@example.com", "password": "a-good-password"})
	taken := do(t, h, "POST", "/api/portal/register", "",
		map[string]string{"email": "taken@example.com", "password": "a-good-password"})

	if fresh.Code != taken.Code {
		t.Errorf("status %d for a new address and %d for a taken one", fresh.Code, taken.Code)
	}
	if fresh.Body.String() != taken.Body.String() {
		t.Errorf("bodies differ:\n new:   %s\n taken: %s", fresh.Body, taken.Body)
	}
}

func TestRegistrationIsRefusedWhenClosed(t *testing.T) {
	srv, _ := portalFixture(t)
	srv.portal.Mode = PortalClosed
	h := srv.Handler()

	w := do(t, h, "POST", "/api/portal/register", "",
		map[string]string{"email": "new@example.com", "password": "a-good-password"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d with registration closed, want 403", w.Code)
	}
	// Login must still work in closed mode; that is the difference from off.
	if w := do(t, h, "POST", "/api/portal/login", "", map[string]string{}); w.Code == http.StatusNotFound {
		t.Error("login is unroutable in closed mode")
	}
}

func TestRegistrationRequiresTheInviteCode(t *testing.T) {
	srv, _ := portalFixture(t)
	srv.portal.InviteCode = "let-me-in"
	h := srv.Handler()

	if w := do(t, h, "POST", "/api/portal/register", "",
		map[string]string{"email": "a@example.com", "password": "a-good-password"}); w.Code != http.StatusForbidden {
		t.Errorf("status %d without the code, want 403", w.Code)
	}
	if w := do(t, h, "POST", "/api/portal/register", "",
		map[string]string{"email": "a@example.com", "password": "a-good-password", "invite_code": "let-me-in"}); w.Code != http.StatusAccepted {
		t.Errorf("status %d with the correct code, want 202", w.Code)
	}
}

// A self-registered account gets nothing until an operator grants it, and the
// generated name must not be derived from the email address — users.name is
// UNIQUE and feeds credentials.email and {{user.email}}.
func TestNewRegistrationIsInertAndAnonymous(t *testing.T) {
	srv, st := portalFixture(t)
	h := srv.Handler()

	if w := do(t, h, "POST", "/api/portal/register", "",
		map[string]string{"email": "mai@example.com", "password": "a-good-password"}); w.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202", w.Code)
	}
	a, err := st.UserAccountByEmail("mai@example.com")
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.GetUser(a.UserID)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := st.UserProfileIDs(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 0 {
		t.Errorf("a new registration was granted %d profiles", len(profiles))
	}
	if bytes.Contains([]byte(u.Name), []byte("mai")) {
		t.Errorf("users.name %q is derived from the email address; it reaches "+
			"credentials.email and the subscriber's own config", u.Name)
	}
}

// Two different addresses must not collide on the generated name.
func TestRegistrationNamesDoNotCollide(t *testing.T) {
	srv, st := portalFixture(t)
	h := srv.Handler()

	for _, email := range []string{"mai@example.com", "mai@example.org"} {
		if w := do(t, h, "POST", "/api/portal/register", "",
			map[string]string{"email": email, "password": "a-good-password"}); w.Code != http.StatusAccepted {
			t.Fatalf("%s: status %d, want 202", email, w.Code)
		}
	}
	for _, email := range []string{"mai@example.com", "mai@example.org"} {
		if _, err := st.UserAccountByEmail(email); err != nil {
			t.Errorf("%s was not created: %v", email, err)
		}
	}
}

// Addresses are compared case-insensitively, so signing up twice with
// different capitalisation is one account, not two.
func TestEmailIsCaseInsensitive(t *testing.T) {
	srv, st := portalFixture(t)
	_, _ = seedSubscriber(t, srv, st, "mai@example.com")
	h := srv.Handler()

	w := do(t, h, "POST", "/api/portal/login", "",
		map[string]string{"email": "MAI@Example.COM", "password": "subscriber-password"})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d signing in with different capitalisation, want 200", w.Code)
	}
}

// Suspended service must not become a locked door: the person needs to sign in
// to find out why their subscription stopped.
func TestASuspendedSubscriberCanStillSignIn(t *testing.T) {
	srv, st := portalFixture(t)
	u, _ := seedSubscriber(t, srv, st, "sub@example.com")
	u.Enabled = false
	if err := st.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	w := do(t, h, "POST", "/api/portal/login", "",
		map[string]string{"email": "sub@example.com", "password": "subscriber-password"})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d for a suspended subscriber, want 200", w.Code)
	}
}

// Disabling portal access, on the other hand, must take effect immediately
// rather than when the 30-day session expires.
func TestDisablingPortalAccessKillsLiveSessions(t *testing.T) {
	srv, st := portalFixture(t)
	u, token := seedSubscriber(t, srv, st, "sub@example.com")
	h := srv.Handler()

	if w := do(t, h, "GET", "/api/portal/me", token, nil); w.Code != http.StatusOK {
		t.Fatalf("status %d before disabling, want 200", w.Code)
	}
	if err := st.SetUserAccountDisabled(u.ID, true); err != nil {
		t.Fatal(err)
	}
	if w := do(t, h, "GET", "/api/portal/me", token, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d after disabling portal access, want 401", w.Code)
	}
}
