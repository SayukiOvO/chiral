package api

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/portal"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

func TestPortalNodeJSONSchemaIsAnExplicitAllowlist(t *testing.T) {
	assertJSONFields := func(value any, want []string) {
		t.Helper()
		typeOf := reflect.TypeOf(value)
		got := make([]string, 0, typeOf.NumField())
		for i := 0; i < typeOf.NumField(); i++ {
			got = append(got, strings.Split(typeOf.Field(i).Tag.Get("json"), ",")[0])
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("portal JSON fields = %v, want strict allowlist %v", got, want)
		}
	}
	assertJSONFields(portal.Node{}, []string{"id", "name", "index", "availability", "traffic_24h"})
	assertJSONFields(portal.TrafficPoint{}, []string{"at", "up_bytes", "down_bytes"})
}

// What the portal must never send.
//
// The whitelist in portal.Node is only as good as the fact that nobody swaps
// it for the admin view "with a few fields removed". This test asserts against
// the response bytes rather than the struct, so it fails if someone reaches
// that conclusion later — and it seeds every forbidden field with a
// recognisable value first, so an assertion cannot pass merely because the
// field happened to be empty.
func TestPortalMeNeverExposesFleetDetail(t *testing.T) {
	srv, st := portalFixture(t)
	u, token := seedSubscriber(t, srv, st, "sub@example.com")

	// A node with every sensitive field populated.
	n, err := st.CreateNode("bwh-lax-3", "join-hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateHello(n.ID, "198.51.100.77", "agent-9.9.9", "xray-26.3.27-secret"); err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProfile("tokyo-reality")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindProfileNode(p.ID, n.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.BindUserProfile(u.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCredential(store.Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID,
		Email:  "sub.u1@p1.n1",
		Secret: "11111111-2222-3333-4444-555555555555",
	}); err != nil {
		t.Fatal(err)
	}

	w := do(t, srv.Handler(), "GET", "/api/portal/me", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", w.Code, w.Body)
	}
	body := w.Body.String()

	forbidden := map[string]string{
		"bwh-lax-3":                            "the internal node name, which encodes provider and datacentre",
		"198.51.100.77":                        "the node's address",
		"agent-9.9.9":                          "the agent version",
		"xray-26.3.27-secret":                  "the Xray version, i.e. which detection signatures apply",
		"11111111-2222-3333-4444-555555555555": "the user's own credential secret",
		"sub.u1@p1.n1":                         "the Xray stats key, which encodes internal ids",
		"tokyo-reality":                        "the profile name",
	}
	for value, what := range forbidden {
		if strings.Contains(body, value) {
			t.Errorf("the portal response contains %s (%q):\n%s", what, value, body)
		}
	}

	// Field names too, in case a value happens to be empty in some future
	// fixture but the key is being serialised regardless. Matched with the
	// colon so a key check cannot be satisfied by a string VALUE that happens
	// to spell the same word — "status":"active" is not an `active` field.
	for _, key := range []string{
		"public_ip", "hostname", "agent_version", "xray_version",
		"runtime_provider", "runtime_mode", "runtime_health", "runtime_version", "runtime_capabilities", "runtime_contract_digest", "runtime_error", "runtime_observed_at", "runtime_xray_state", "runtime_xray_version",
		"cpu_percent", "mem_used_bytes", "disk_total_bytes",
		"net_tx_bps", "config_version", "last_seen_at",
		"profile_ids", "credentials", "enabled", "active",
	} {
		if strings.Contains(body, `"`+key+`":`) {
			t.Errorf("the portal response carries the field %q:\n%s", key, body)
		}
	}
}

// The node list is derived from the user's own entitlements, never the fleet
// filtered client-side — otherwise one devtools tab would show every node.
func TestPortalNodesAreOnlyTheUsersOwn(t *testing.T) {
	srv, st := portalFixture(t)
	u, token := seedSubscriber(t, srv, st, "sub@example.com")

	// One node the user can reach, one they cannot.
	mine, _ := st.CreateNode("mine", "h1")
	theirs, _ := st.CreateNode("theirs", "h2")
	p, _ := st.CreateProfile("granted")
	other, _ := st.CreateProfile("not-granted")
	st.BindProfileNode(p.ID, mine.ID)
	st.BindProfileNode(other.ID, theirs.ID)
	st.BindUserProfile(u.ID, p.ID)
	// Both nodes were created after the subscriber, so both are withheld from
	// them by default. Grant the one they are supposed to have: the portal
	// lists what the subscription carries, and a withheld node is carried by
	// neither.
	if err := st.SetUserNodeAccess(u.ID, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	w := do(t, srv.Handler(), "GET", "/api/portal/me", token, nil)
	body := w.Body.String()

	if !strings.Contains(body, mine.ID) {
		t.Errorf("the user's own node is missing from their list:\n%s", body)
	}
	if strings.Contains(body, theirs.ID) {
		t.Errorf("a node the user has no access to appears in their list:\n%s", body)
	}
}

// A user with no profiles gets a coherent page rather than an error: this is
// the first screen a self-registered account sees.
func TestPortalMeWorksWithNoProfiles(t *testing.T) {
	srv, st := portalFixture(t)
	_, token := seedSubscriber(t, srv, st, "fresh@example.com")

	w := do(t, srv.Handler(), "GET", "/api/portal/me", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d for an unprovisioned account, want 200: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"no_access"`) {
		t.Errorf("an account with no profiles is not reported as no_access:\n%s", w.Body)
	}
}

// Withholding a node hides it from a subscription; it does not revoke the
// credential. So a withheld node still has one, and a portal that listed nodes
// from profiles alone showed the subscriber a node their client never receives
// — with an availability light beside it. With groups this stopped being
// hypothetical: a group withholds every node that exists when it is created.
func TestPortalDoesNotListAWithheldNode(t *testing.T) {
	srv, st := portalFixture(t)
	u, token := seedSubscriber(t, srv, st, "sub@example.com")
	granted, _ := st.CreateNode("granted", "h1")
	withheld, _ := st.CreateNode("withheld", "h2")
	p, _ := st.CreateProfile("granted")
	st.BindProfileNode(p.ID, granted.ID)
	st.BindProfileNode(p.ID, withheld.ID)
	st.BindUserProfile(u.ID, p.ID)
	if err := st.SetUserNodeAccess(u.ID, []string{withheld.ID}, nil, nil); err != nil {
		t.Fatal(err)
	}

	body := do(t, srv.Handler(), "GET", "/api/portal/me", token, nil).Body.String()
	if !strings.Contains(body, granted.ID) {
		t.Errorf("the node the subscriber holds is missing:\n%s", body)
	}
	if strings.Contains(body, withheld.ID) {
		t.Errorf("a withheld node is listed in the portal:\n%s", body)
	}
}
