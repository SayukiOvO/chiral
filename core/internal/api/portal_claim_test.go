package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

// Claim links are how an operator hands an existing subscriber a way in, and
// how somebody without SMTP recovers a password. Both make the link a
// credential that can take over an account, so the tests here are mostly about
// what it must NOT be able to do.

func seedClaimLink(t *testing.T, st *store.Store, userID string) string {
	t.Helper()
	token, hash := auth.NewSecret()
	if err := st.CreatePortalChallenge(hash, userID, store.PortalPurposeClaim, "",
		store.PortalClaimTTL); err != nil {
		t.Fatal(err)
	}
	return token
}

func TestClaimCreatesAnAccountAndSignsIn(t *testing.T) {
	srv, st := portalFixture(t)
	u, err := st.CreateUserWithToken(store.User{Name: "alice", Enabled: true}, "tok", "hash")
	if err != nil {
		t.Fatal(err)
	}
	token := seedClaimLink(t, st, u.ID)
	h := srv.Handler()

	w := do(t, h, "POST", "/api/portal/claim", "", map[string]string{
		"token": token, "email": "alice@example.com", "password": "a-good-password",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", w.Code, w.Body)
	}
	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["token"] == nil {
		t.Error("claiming did not return a session")
	}
	if _, err := st.UserAccountByEmail("alice@example.com"); err != nil {
		t.Errorf("no account was created: %v", err)
	}
}

// The link is one-time. Left spendable twice, a link forwarded or logged
// somewhere would keep working after the person had used it.
func TestAClaimLinkCannotBeUsedTwice(t *testing.T) {
	srv, st := portalFixture(t)
	u, _ := st.CreateUserWithToken(store.User{Name: "alice", Enabled: true}, "tok", "hash")
	token := seedClaimLink(t, st, u.ID)
	h := srv.Handler()

	first := do(t, h, "POST", "/api/portal/claim", "", map[string]string{
		"token": token, "email": "alice@example.com", "password": "a-good-password",
	})
	if first.Code != http.StatusOK {
		t.Fatalf("first claim: status %d, want 200", first.Code)
	}
	second := do(t, h, "POST", "/api/portal/claim", "", map[string]string{
		"token": token, "email": "attacker@example.com", "password": "another-password",
	})
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("second claim: status %d, want 401", second.Code)
	}
}

// The important one. Once an account exists, the link acts as a password
// reset — and must not be able to move the address. Otherwise a leaked or
// forwarded link lets someone repoint the account at their own inbox and then
// hold it permanently through password recovery.
func TestAResetLinkCannotChangeTheEmailAddress(t *testing.T) {
	srv, st := portalFixture(t)
	u, _ := seedSubscriber(t, srv, st, "victim@example.com")
	token := seedClaimLink(t, st, u.ID)
	h := srv.Handler()

	w := do(t, h, "POST", "/api/portal/claim", "", map[string]string{
		"token": token, "email": "attacker@example.com", "password": "a-good-password",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: a reset link moved the account's address", w.Code)
	}
	a, err := st.UserAccount(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Email != "victim@example.com" {
		t.Fatalf("address is now %q; the account was hijacked", a.Email)
	}
}

// Resetting the password without touching the address is the legitimate use,
// and it must drop the old sessions — whoever knew the previous password
// should not still be signed in.
func TestAResetLinkSetsThePasswordAndDropsOldSessions(t *testing.T) {
	srv, st := portalFixture(t)
	u, oldSession := seedSubscriber(t, srv, st, "mai@example.com")
	token := seedClaimLink(t, st, u.ID)
	h := srv.Handler()

	if w := do(t, h, "GET", "/api/portal/me", oldSession, nil); w.Code != http.StatusOK {
		t.Fatalf("the old session was not valid to begin with: %d", w.Code)
	}
	w := do(t, h, "POST", "/api/portal/claim", "", map[string]string{
		"token": token, "password": "a-brand-new-password",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", w.Code, w.Body)
	}
	if w := do(t, h, "GET", "/api/portal/me", oldSession, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("the old session still works after a password reset: %d", w.Code)
	}
	// The new password must actually be in effect.
	login := do(t, h, "POST", "/api/portal/login", "", map[string]string{
		"email": "mai@example.com", "password": "a-brand-new-password",
	})
	if login.Code != http.StatusOK {
		t.Errorf("status %d signing in with the new password, want 200", login.Code)
	}
}

// A challenge minted for one purpose must not satisfy another.
func TestAResetChallengeIsNotAClaimChallenge(t *testing.T) {
	srv, st := portalFixture(t)
	u, _ := seedSubscriber(t, srv, st, "mai@example.com")

	token, hash := auth.NewSecret()
	if err := st.CreatePortalChallenge(hash, u.ID, store.PortalPurposeReset, "",
		store.PortalResetTTL); err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	w := do(t, h, "POST", "/api/portal/claim", "", map[string]string{
		"token": token, "password": "a-good-password",
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d: a password_reset challenge was spent as a claim", w.Code)
	}
}

// The lookup exists so the claim page can say who the link is for. It must
// return the operator-chosen proxy name, not the email address — otherwise a
// leaked link discloses an address as well.
func TestClaimLookupReturnsTheNameNotTheAddress(t *testing.T) {
	srv, st := portalFixture(t)
	u, _ := seedSubscriber(t, srv, st, "private@example.com")
	token := seedClaimLink(t, st, u.ID)
	h := srv.Handler()

	w := do(t, h, "POST", "/api/portal/claim/lookup", "", map[string]string{"token": token})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !json.Valid(w.Body.Bytes()) {
		t.Fatal("response is not JSON")
	}
	for _, leak := range []string{"private@example.com", "private"} {
		if strings.Contains(body, leak) {
			t.Errorf("the lookup response leaks %q: %s", leak, body)
		}
	}
}

func TestClaimRejectsAnUnknownToken(t *testing.T) {
	srv, _ := portalFixture(t)
	h := srv.Handler()

	for _, path := range []string{"/api/portal/claim", "/api/portal/claim/lookup"} {
		w := do(t, h, "POST", path, "", map[string]string{
			"token": "not-a-real-token", "email": "a@example.com", "password": "a-good-password",
		})
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d for an unknown token, want 401", path, w.Code)
		}
	}
}
