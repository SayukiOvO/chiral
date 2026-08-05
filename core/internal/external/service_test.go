package external

import (
	"context"
	"errors"
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

// fragmentBodies is what these tests assert on. Fragments now carries each
// body with the place the operator gave it; the placement has its own tests.
func (s *Service) fragmentBodies(denied map[string]struct{}, chainName func(string) string) ([]string, error) {
	fs, err := s.Fragments(denied, chainName)
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Body)
	}
	return out, err
}

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
	if err := st.SetExternalProxy(before[0].ID, node.ID, "", true); err != nil {
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
	st.SetExternalProxy(proxies[0].ID, node.ID, "", true)

	frags, err := svc.fragmentBodies(nil, func(id string) string {
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
	st.SetExternalProxy(proxies[0].ID, node.ID, "", true)

	frags, err := svc.fragmentBodies(nil, func(string) string { return "" })
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

	st.SetExternalProxy(proxies[0].ID, "", "", false)
	frags, _ := svc.fragmentBodies(nil, func(string) string { return "" })
	if len(frags) != 1 {
		t.Fatalf("a disabled proxy was carried: %v", frags)
	}

	st.UpdateExternalSub(sub.ID, sub.Name, sub.URL, false)
	frags, _ = svc.fragmentBodies(nil, func(string) string { return "" })
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

	frags, err := svc.fragmentBodies(denied, func(string) string { return "" })
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
	frags, _ = svc.fragmentBodies(denied, func(string) string { return "" })
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
	frags, err := svc.fragmentBodies(nil, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 {
		t.Fatalf("fragments = %d, want the external one", len(frags))
	}
}

// The arrangement the chain exists for and could not express: a relay bought
// from one provider, an exit from another, no fleet node in between.
func TestAnExternalCanBeChainedThroughAnotherExternal(t *testing.T) {
	st, svc := fixture(t)
	srv, _ := serve(t, "proxies:\n"+
		"  - {name: Relay, type: vless, server: r.example.com, port: 443, uuid: u1}\n"+
		"  - {name: Exit, type: vless, server: e.example.com, port: 443, uuid: u2}\n")
	sub, _ := st.CreateExternalSub("provider", srv.URL, "")
	if err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	p, _ := st.ExternalProxies(sub.ID)
	relay, exit := p[0], p[1]
	if err := st.SetExternalProxy(exit.ID, "", relay.ID, true); err != nil {
		t.Fatal(err)
	}

	frags, err := svc.fragmentBodies(nil, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 2 {
		t.Fatalf("fragments = %d, want both:\n%v", len(frags), frags)
	}
	var exitFrag string
	for _, f := range frags {
		if strings.Contains(f, "e.example.com") {
			exitFrag = f
		}
	}
	if !strings.Contains(exitFrag, `dialer-proxy: "Relay"`) {
		t.Errorf("exit does not dial through the relay:\n%s", exitFrag)
	}
}

// Denying the relay must take the exit with it, exactly as denying a fleet
// relay does. Emitting the exit unchained would send the subscriber straight at
// the provider — the one thing the setting prevents.
func TestDenyingARelayDropsWhatChainsThroughIt(t *testing.T) {
	st, svc := fixture(t)
	srv, _ := serve(t, "proxies:\n"+
		"  - {name: Relay, type: vless, server: r.example.com, port: 443, uuid: u1}\n"+
		"  - {name: Exit, type: vless, server: e.example.com, port: 443, uuid: u2}\n")
	sub, _ := st.CreateExternalSub("provider", srv.URL, "")
	svc.Refresh(context.Background(), sub.ID)
	p, _ := st.ExternalProxies(sub.ID)
	relay, exit := p[0], p[1]
	st.SetExternalProxy(exit.ID, "", relay.ID, true)

	frags, err := svc.fragmentBodies(map[string]struct{}{relay.ID: {}}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 0 {
		t.Fatalf("the exit survived its relay being denied:\n%v", frags)
	}
	blocked, err := svc.Blocked(map[string]struct{}{relay.ID: {}}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := blocked[exit.ID]
	if !ok || ref.ID != relay.ID || !ref.External {
		t.Fatalf("the console would not have been told why: %+v", blocked)
	}
}

// Three deep, and the break is at the far end. A single pass over the list can
// resolve this only if it happens to visit in the right order; the exit must go
// whichever order it is visited in.
func TestABreakPropagatesAlongTheWholeChain(t *testing.T) {
	st, svc := fixture(t)
	srv, _ := serve(t, "proxies:\n"+
		"  - {name: A, type: vless, server: a.example.com, port: 443, uuid: u1}\n"+
		"  - {name: B, type: vless, server: b.example.com, port: 443, uuid: u2}\n"+
		"  - {name: C, type: vless, server: c.example.com, port: 443, uuid: u3}\n")
	node, _ := st.CreateNode("relay", "hash-chain-deep")
	sub, _ := st.CreateExternalSub("provider", srv.URL, "")
	svc.Refresh(context.Background(), sub.ID)
	p, _ := st.ExternalProxies(sub.ID)
	// C -> B -> A -> fleet node.
	st.SetExternalProxy(p[0].ID, node.ID, "", true)
	st.SetExternalProxy(p[1].ID, "", p[0].ID, true)
	st.SetExternalProxy(p[2].ID, "", p[1].ID, true)

	frags, err := svc.fragmentBodies(nil, func(id string) string {
		if id == node.ID {
			return "东京 01"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 3 {
		t.Fatalf("a whole intact chain did not render: %v", frags)
	}

	// Now the fleet node is not in this subscription. All three must go.
	frags, err = svc.fragmentBodies(nil, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 0 {
		t.Fatalf("the chain survived losing its far end:\n%v", frags)
	}
}

// A chain that loops has no rendering, so it must not be storable. Refusing the
// write is the only place an operator finds out; the alternative is two proxies
// silently missing from every subscription.
func TestAChainCannotLoop(t *testing.T) {
	st, svc := fixture(t)
	srv, _ := serve(t, "proxies:\n"+
		"  - {name: A, type: vless, server: a.example.com, port: 443, uuid: u1}\n"+
		"  - {name: B, type: vless, server: b.example.com, port: 443, uuid: u2}\n")
	sub, _ := st.CreateExternalSub("provider", srv.URL, "")
	svc.Refresh(context.Background(), sub.ID)
	p, _ := st.ExternalProxies(sub.ID)

	if err := st.SetExternalProxy(p[0].ID, "", p[0].ID, true); !errors.Is(err, store.ErrChainCycle) {
		t.Errorf("a proxy chained through itself was accepted: %v", err)
	}
	if err := st.SetExternalProxy(p[1].ID, "", p[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetExternalProxy(p[0].ID, "", p[1].ID, true); !errors.Is(err, store.ErrChainCycle) {
		t.Errorf("A->B->A was accepted: %v", err)
	}
}

// One setting, two kinds of value. Switching a proxy from a fleet relay to an
// external one must not leave both recorded.
func TestTheTwoChainTargetsAreExclusive(t *testing.T) {
	st, svc := fixture(t)
	srv, _ := serve(t, "proxies:\n"+
		"  - {name: A, type: vless, server: a.example.com, port: 443, uuid: u1}\n"+
		"  - {name: B, type: vless, server: b.example.com, port: 443, uuid: u2}\n")
	node, _ := st.CreateNode("relay", "hash-exclusive")
	sub, _ := st.CreateExternalSub("provider", srv.URL, "")
	svc.Refresh(context.Background(), sub.ID)
	p, _ := st.ExternalProxies(sub.ID)

	if err := st.SetExternalProxy(p[1].ID, node.ID, "", true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetExternalProxy(p[1].ID, "", p[0].ID, true); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetExternalProxy(p[1].ID)
	if got.ChainNodeID != "" {
		t.Errorf("the fleet target survived being replaced: %q", got.ChainNodeID)
	}
	if got.ChainProxyID != p[0].ID {
		t.Errorf("chain proxy = %q", got.ChainProxyID)
	}
	if err := st.SetExternalProxy(p[1].ID, node.ID, p[0].ID, true); err == nil {
		t.Error("both targets at once was accepted")
	}
}

// Reading the relation from the node's end must agree with reading it from the
// subscriber's. They are the same rows.
func TestProxyAccessReadsBothWays(t *testing.T) {
	st, svc := fixture(t)
	srv, _ := serve(t, "proxies:\n  - {name: A, type: vless, server: a.example.com, port: 443, uuid: u1}\n")
	sub, _ := st.CreateExternalSub("provider", srv.URL, "")
	svc.Refresh(context.Background(), sub.ID)
	p, _ := st.ExternalProxies(sub.ID)
	alice, err := st.CreateUser(store.User{Name: "alice", Enabled: true}, "hash-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.CreateUser(store.User{Name: "bob", Enabled: true}, "hash-bob")
	if err != nil {
		t.Fatal(err)
	}

	if err := st.SetExternalProxyAccess(p[0].ID, []string{alice.ID}); err != nil {
		t.Fatal(err)
	}
	from, _ := st.UserExternalDenies(alice.ID)
	if _, ok := from[p[0].ID]; !ok {
		t.Error("the user side does not see the denial the node side wrote")
	}
	if others, _ := st.UserExternalDenies(bob.ID); len(others) != 0 {
		t.Errorf("denying alice touched bob: %v", others)
	}
	// And writing from the user side is visible from the node side.
	if err := st.SetUserNodeAccess(bob.ID, nil, []string{p[0].ID}); err != nil {
		t.Fatal(err)
	}
	back, _ := st.ExternalProxyDenies(p[0].ID)
	if _, ok := back[bob.ID]; !ok {
		t.Error("the node side does not see the denial the user side wrote")
	}
}

// The provider's name is a label with the account's remaining traffic in it.
// The operator gets to call the node something else, and that is what reaches
// the subscriber — while the provider's name stays put underneath, because it
// is what a refresh matches on.
func TestARenamedProxyKeepsItsIdentityAcrossARefresh(t *testing.T) {
	st, svc := fixture(t)
	srv, next := serve(t,
		"proxies:\n  - {name: 'HK 01 | 82%', type: vless, server: hk.example.com, port: 443, uuid: u1}\n",
		"proxies:\n  - {name: 'HK 01 | 61%', type: vless, server: hk.example.com, port: 443, uuid: u1}\n")
	sub, _ := st.CreateExternalSub("provider", srv.URL, "")
	if err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	p, _ := st.ExternalProxies(sub.ID)
	if err := st.RenameExternalProxy(p[0].ID, "香港 · 中继"); err != nil {
		t.Fatal(err)
	}

	frags, err := svc.fragmentBodies(nil, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 {
		t.Fatalf("fragments = %d", len(frags))
	}
	if !strings.Contains(frags[0], `name: "香港 · 中继"`) {
		t.Errorf("the chosen name did not reach the subscription:\n%s", frags[0])
	}
	// Exactly one name key, or the document is unloadable.
	if n := strings.Count(frags[0], "\nname:") + strings.Count(frags[0], "name: "); n != 1 {
		t.Errorf("expected one name key, found %d:\n%s", n, frags[0])
	}
	if strings.Contains(frags[0], "82%") {
		t.Errorf("the provider's label leaked into the subscription:\n%s", frags[0])
	}

	next()
	if err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := st.ExternalProxies(sub.ID)
	if len(after) != 1 || after[0].ID != p[0].ID {
		t.Fatalf("the row was replaced rather than updated: %+v", after)
	}
	if after[0].DisplayName != "香港 · 中继" {
		t.Errorf("the rename was lost on refresh: %q", after[0].DisplayName)
	}
	if after[0].Name != "HK 01 | 61%" {
		t.Errorf("the provider's name stopped tracking the provider: %q", after[0].Name)
	}
}

// A dialer-proxy has to name what the document defines. Rename the relay and
// the chain must follow, or every client refuses the whole configuration.
func TestAChainFollowsARename(t *testing.T) {
	st, svc := fixture(t)
	srv, _ := serve(t, "proxies:\n"+
		"  - {name: Relay, type: vless, server: r.example.com, port: 443, uuid: u1}\n"+
		"  - {name: Exit, type: vless, server: e.example.com, port: 443, uuid: u2}\n")
	sub, _ := st.CreateExternalSub("provider", srv.URL, "")
	svc.Refresh(context.Background(), sub.ID)
	p, _ := st.ExternalProxies(sub.ID)
	st.SetExternalProxy(p[1].ID, "", p[0].ID, true)
	if err := st.RenameExternalProxy(p[0].ID, "中继 A"); err != nil {
		t.Fatal(err)
	}

	frags, err := svc.fragmentBodies(nil, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	var names, dialers []string
	for _, f := range frags {
		for _, line := range strings.Split(f, "\n") {
			if strings.HasPrefix(line, "name: ") {
				names = append(names, strings.Trim(strings.TrimPrefix(line, "name: "), `"`))
			}
			if strings.HasPrefix(line, "dialer-proxy: ") {
				dialers = append(dialers, strings.Trim(strings.TrimPrefix(line, "dialer-proxy: "), `"`))
			}
		}
	}
	if len(dialers) != 1 {
		t.Fatalf("dialers = %v", dialers)
	}
	found := false
	for _, n := range names {
		if n == dialers[0] {
			found = true
		}
	}
	if !found {
		t.Fatalf("dialer-proxy %q names nothing the document defines: %v", dialers[0], names)
	}
	if dialers[0] != "中继 A" {
		t.Errorf("the chain kept the old name: %q", dialers[0])
	}
}

// A node arriving is not a decision about who gets it.
//
// Left open, a provider adding a node to their list hands it to every
// subscriber on the next refresh — silently, with nothing having asked. Denied
// on arrival it is inert until somebody says otherwise.
func TestANewExternalNodeReachesNobodyUntilSaidOtherwise(t *testing.T) {
	st, svc := fixture(t)
	alice, err := st.CreateUser(store.User{Name: "alice", Enabled: true}, "hash-a")
	if err != nil {
		t.Fatal(err)
	}
	srv, next := serve(t,
		"proxies:\n  - {name: A, type: vless, server: a.example.com, port: 443, uuid: u1}\n",
		"proxies:\n"+
			"  - {name: A, type: vless, server: a.example.com, port: 443, uuid: u1}\n"+
			"  - {name: B, type: vless, server: b.example.com, port: 443, uuid: u2}\n")
	sub, _ := st.CreateExternalSub("provider", srv.URL, "")
	if err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	p, _ := st.ExternalProxies(sub.ID)
	denied, _ := st.UserExternalDenies(alice.ID)
	if _, no := denied[p[0].ID]; !no {
		t.Fatal("a new node was open to an existing subscriber")
	}

	// The operator says yes to A. Then the provider adds B.
	if err := st.SetUserNodeAccess(alice.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	next()
	if err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := st.ExternalProxies(sub.ID)
	var a, b store.ExternalProxy
	for _, x := range after {
		if x.Name == "A" {
			a = x
		} else {
			b = x
		}
	}
	denied, _ = st.UserExternalDenies(alice.ID)
	if _, no := denied[a.ID]; no {
		t.Error("a refresh took back a node the operator had allowed")
	}
	if _, no := denied[b.ID]; !no {
		t.Error("the node the provider added arrived open")
	}
}

// A subscriber who arrives after the nodes do starts with nothing either.
// Profiles were already opt-in, but external nodes had nothing in front of
// them, so "nobody has decided yet" and "hand them everything we buy from
// anyone" were the same state.
func TestANewSubscriberStartsWithNoExternalNodes(t *testing.T) {
	st, svc := fixture(t)
	srv, _ := serve(t, "proxies:\n"+
		"  - {name: A, type: vless, server: a.example.com, port: 443, uuid: u1}\n"+
		"  - {name: B, type: vless, server: b.example.com, port: 443, uuid: u2}\n")
	sub, _ := st.CreateExternalSub("provider", srv.URL, "")
	if err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(store.User{Name: "newcomer", Enabled: true}, "hash-n")
	if err != nil {
		t.Fatal(err)
	}
	denied, _ := st.UserExternalDenies(u.ID)
	if len(denied) != 2 {
		t.Fatalf("new subscriber could reach %d of 2 external nodes unasked", 2-len(denied))
	}
	// And their subscription really is empty rather than merely marked so.
	frags, err := svc.fragmentBodies(denied, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 0 {
		t.Fatalf("fragments = %v", frags)
	}
}
