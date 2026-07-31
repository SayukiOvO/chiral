package external

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

func fixture(t *testing.T) (*store.Store, *Service) {
	t.Helper()
	box, err := secret.NewBox("external-test-key-0123456789abcd")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, NewService(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func serve(t *testing.T, bodies ...string) (*httptest.Server, func()) {
	t.Helper()
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(bodies[i]))
	}))
	t.Cleanup(srv.Close)
	return srv, func() { i++ }
}

func TestRefreshStoresProxies(t *testing.T) {
	st, svc := fixture(t)
	srv, _ := serve(t, "proxies:\n"+
		"  - {name: HK, type: vless, server: hk.example.com, port: 443, uuid: u1}\n"+
		"  - {name: JP, type: trojan, server: jp.example.com, port: 8443, password: p}\n")

	sub, err := st.CreateExternalSub("provider", srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got, err := st.ExternalProxies(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "HK" || got[1].Name != "JP" {
		t.Fatalf("proxies = %+v", got)
	}
	// Order is the provider's, not the database's.
	if got[0].Ord != 0 || got[1].Ord != 1 {
		t.Errorf("order lost: %d %d", got[0].Ord, got[1].Ord)
	}
}

// Providers rename constantly — remaining traffic and expiry dates go in the
// label. A chain setting that detached every time they edited a string would
// be the kind of thing nobody thinks to re-check.
func TestChainSurvivesARename(t *testing.T) {
	st, svc := fixture(t)
	srv, next := serve(t,
		"proxies:\n  - {name: 'HK 01 | 82%', type: vless, server: hk.example.com, port: 443, uuid: u1}\n",
		"proxies:\n  - {name: 'HK 01 | 61%', type: vless, server: hk.example.com, port: 443, uuid: u1}\n")

	node, err := st.CreateNode("relay", "hash-relay")
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := st.CreateExternalSub("provider", srv.URL, "")
	if err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := st.ExternalProxies(sub.ID)
	if err := st.SetExternalProxy(before[0].ID, node.ID, true); err != nil {
		t.Fatal(err)
	}

	next()
	if err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := st.ExternalProxies(sub.ID)
	if len(after) != 1 {
		t.Fatalf("proxies = %d", len(after))
	}
	if after[0].Name != "HK 01 | 61%" {
		t.Errorf("name not refreshed: %q", after[0].Name)
	}
	if after[0].ChainNodeID != node.ID {
		t.Fatalf("chain lost across the rename: %q", after[0].ChainNodeID)
	}
}

// The stored copy is what renders; an unreachable provider degrades to the
// nodes it last gave rather than to none.
func TestFailedRefreshKeepsWhatWorked(t *testing.T) {
	st, svc := fixture(t)
	srv, _ := serve(t, "proxies:\n  - {name: HK, type: vless, server: hk.example.com, port: 443, uuid: u1}\n")
	sub, _ := st.CreateExternalSub("provider", srv.URL, "")
	if err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	srv.Close()

	if err := svc.Refresh(context.Background(), sub.ID); err == nil {
		t.Fatal("a dead provider refreshed successfully")
	}
	got, _ := st.ExternalProxies(sub.ID)
	if len(got) != 1 {
		t.Fatalf("proxies were dropped on a failed refresh: %+v", got)
	}
	after, _ := st.GetExternalSub(sub.ID)
	if after.LastError == "" {
		t.Error("the failure was not recorded")
	}
	if after.Body == "" {
		t.Error("the working body was cleared")
	}
}

func TestFragmentsCarryTheChain(t *testing.T) {
	st, svc := fixture(t)
	srv, _ := serve(t, "proxies:\n"+
		"  - {name: HK, type: vless, server: hk.example.com, port: 443, uuid: u1}\n"+
		"  - {name: JP, type: vless, server: jp.example.com, port: 443, uuid: u2}\n")
	node, _ := st.CreateNode("relay", "hash-relay2")
	sub, _ := st.CreateExternalSub("provider", srv.URL, "")
	if err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	proxies, _ := st.ExternalProxies(sub.ID)
	st.SetExternalProxy(proxies[0].ID, node.ID, true)

	frags, err := svc.Fragments(nil, func(id string) string {
		if id == node.ID {
			return "日本 · 东京 01"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 2 {
		t.Fatalf("fragments = %d", len(frags))
	}
	if !strings.Contains(frags[0], `dialer-proxy: "日本 · 东京 01"`) {
		t.Errorf("chained proxy has no dialer:\n%s", frags[0])
	}
	if strings.Contains(frags[1], "dialer-proxy") {
		t.Errorf("unchained proxy grew one:\n%s", frags[1])
	}
}

// A dialer-proxy naming something the document does not define makes the whole
// configuration unloadable. Dropping the proxy is the only safe answer —
// emitting it unchained would quietly send the subscriber straight at the
// provider, which is the one thing the setting exists to prevent.
func TestAProxyWhoseChainIsMissingIsLeftOut(t *testing.T) {
	st, svc := fixture(t)
	srv, _ := serve(t, "proxies:\n  - {name: HK, type: vless, server: hk.example.com, port: 443, uuid: u1}\n")
	node, _ := st.CreateNode("relay", "hash-relay3")
	sub, _ := st.CreateExternalSub("provider", srv.URL, "")
	svc.Refresh(context.Background(), sub.ID)
	proxies, _ := st.ExternalProxies(sub.ID)
	st.SetExternalProxy(proxies[0].ID, node.ID, true)

	frags, err := svc.Fragments(nil, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 0 {
		t.Fatalf("emitted a proxy chained through a node that is not here:\n%v", frags)
	}
}

func TestDisabledSourcesAndProxiesAreLeftOut(t *testing.T) {
	st, svc := fixture(t)
	srv, _ := serve(t, "proxies:\n"+
		"  - {name: A, type: vless, server: a.example.com, port: 443, uuid: u1}\n"+
		"  - {name: B, type: vless, server: b.example.com, port: 443, uuid: u2}\n")
	sub, _ := st.CreateExternalSub("provider", srv.URL, "")
	svc.Refresh(context.Background(), sub.ID)
	proxies, _ := st.ExternalProxies(sub.ID)

	st.SetExternalProxy(proxies[0].ID, "", false)
	frags, _ := svc.Fragments(nil, func(string) string { return "" })
	if len(frags) != 1 {
		t.Fatalf("a disabled proxy was carried: %v", frags)
	}

	st.UpdateExternalSub(sub.ID, sub.Name, sub.URL, false)
	frags, _ = svc.Fragments(nil, func(string) string { return "" })
	if len(frags) != 0 {
		t.Fatalf("a disabled source was carried: %v", frags)
	}
}

// A single node someone sent over chat: no URL, just the link.
func TestPastedBodyNeedsNoURL(t *testing.T) {
	st, svc := fixture(t)
	sub, err := st.CreateExternalSub("a friend", "",
		"vless://uuid@friend.example.com:443?security=tls&sni=x#Friend")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got, _ := st.ExternalProxies(sub.ID)
	if len(got) != 1 || got[0].Name != "Friend" {
		t.Fatalf("proxies = %+v", got)
	}
}

// A subscriber can be denied an individual external node, and the denial
// survives a refresh — the rows are rebuilt every time, so an id that changed
// would take the operator's decision with it.
func TestDeniedExternalProxiesAreLeftOut(t *testing.T) {
	st, svc := fixture(t)
	srv, next := serve(t,
		"proxies:\n"+
			"  - {name: A, type: vless, server: a.example.com, port: 443, uuid: u1}\n"+
			"  - {name: B, type: vless, server: b.example.com, port: 443, uuid: u2}\n",
		"proxies:\n"+
			"  - {name: 'A | 61%', type: vless, server: a.example.com, port: 443, uuid: u1}\n"+
			"  - {name: B, type: vless, server: b.example.com, port: 443, uuid: u2}\n")
	sub, _ := st.CreateExternalSub("provider", srv.URL, "")
	if err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	proxies, _ := st.ExternalProxies(sub.ID)
	denied := map[string]struct{}{proxies[0].ID: {}}

	frags, err := svc.Fragments(denied, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 || strings.Contains(frags[0], "a.example.com") {
		t.Fatalf("denied proxy was carried: %v", frags)
	}

	// After a refresh that renames it, the same id must still be the same node.
	next()
	if err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := st.ExternalProxies(sub.ID)
	var still bool
	for _, p := range after {
		if p.ID == proxies[0].ID && p.Server == "a.example.com" {
			still = true
		}
	}
	if !still {
		t.Fatal("the proxy's identity did not survive a refresh, so the denial would have been lost")
	}
	frags, _ = svc.Fragments(denied, func(string) string { return "" })
	if len(frags) != 1 {
		t.Fatalf("denial did not survive the refresh: %v", frags)
	}
}

// A subscriber whose only access is external is not a subscriber with no
// access. The count that decides whether a subscription is empty is taken
// before the external proxies are merged unless something says otherwise, and
// then the panel refuses to serve a list it is holding.
func TestExternalOnlySubscriptionIsNotEmpty(t *testing.T) {
	st, svc := fixture(t)
	srv, _ := serve(t, "proxies:\n  - {name: A, type: vless, server: a.example.com, port: 443, uuid: u1}\n")
	sub, _ := st.CreateExternalSub("provider", srv.URL, "")
	if err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	frags, err := svc.Fragments(nil, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 {
		t.Fatalf("fragments = %d, want the external one", len(frags))
	}
}
