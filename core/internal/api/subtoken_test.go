package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

// A subscriber's link is not something to hand out again by accident.
//
// Reading it and replacing it were one call: the console had no read path, so
// showing an operator a link meant minting a new one, and "what is their link"
// and "break every client they have configured" were the same button. These
// tests are what keeps the two apart.

// tokenOf takes the secret out of a subscription URL. Slicing a fixed width
// off the end would make the negative assertions below pass for the wrong
// reason — a token parsed wrongly resolves to nobody either.
func tokenOf(url string) string {
	return url[strings.LastIndex(url, "/")+1:]
}

func subTokenFixture(t *testing.T) (*Server, store.User) {
	t.Helper()
	srv, st := portalFixture(t)
	token, hash := auth.NewSecret()
	u, err := st.CreateUserWithToken(store.User{Name: "alice", Enabled: true}, token, hash)
	if err != nil {
		t.Fatal(err)
	}
	return srv, u
}

func readSubToken(t *testing.T, srv *Server, id string) (string, bool) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/users/"+id+"/sub-token", nil)
	r.SetPathValue("id", id)
	srv.subToken(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET sub-token = %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		URL         string `json:"subscription_url"`
		Recoverable bool   `json:"recoverable"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got.URL, got.Recoverable
}

func TestReadingTheLinkDoesNotChangeIt(t *testing.T) {
	srv, u := subTokenFixture(t)

	first, ok := readSubToken(t, srv, u.ID)
	if !ok || first == "" {
		t.Fatalf("link not recoverable for a freshly created user: %q", first)
	}
	for i := 0; i < 3; i++ {
		again, _ := readSubToken(t, srv, u.ID)
		if again != first {
			t.Fatalf("read %d changed the link: %q -> %q", i+1, first, again)
		}
	}

	// And the link that comes back is the one the subscription route accepts.
	// A link that reads back but does not work would be worse than none.
	if _, err := srv.st.FindUserBySubTokenHash(auth.HashSecret(tokenOf(first))); err != nil {
		t.Fatalf("the link read back does not resolve to the user: %v", err)
	}
}

func TestResetIsTheOnlyThingThatReplacesTheLink(t *testing.T) {
	srv, u := subTokenFixture(t)
	before, _ := readSubToken(t, srv, u.ID)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/users/"+u.ID+"/sub-token", nil)
	r.SetPathValue("id", u.ID)
	srv.resetSubToken(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("POST sub-token = %d: %s", w.Code, w.Body.String())
	}

	after, ok := readSubToken(t, srv, u.ID)
	if !ok {
		t.Fatal("the link stopped being recoverable after a reset")
	}
	if after == before {
		t.Fatal("a reset left the old link in place")
	}
	// The new one resolves, so the negative assertion below is about the reset
	// rather than about a mis-parsed token.
	if _, err := srv.st.FindUserBySubTokenHash(auth.HashSecret(tokenOf(after))); err != nil {
		t.Fatalf("the new link does not resolve: %v", err)
	}
	// The old one must be dead, or a reset would not be a revocation.
	if _, err := srv.st.FindUserBySubTokenHash(auth.HashSecret(tokenOf(before))); err == nil {
		t.Fatal("the previous link still resolves after a reset")
	}
}
