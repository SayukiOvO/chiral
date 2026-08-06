package subscription

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

// relayFixture: one profile on two nodes, and a line from the first to the
// second, with the subscriber allowed all three.
func relayFixture(t *testing.T) (*store.Store, *Service, store.User, store.Node, store.Node, store.NodeRelay) {
	t.Helper()
	box, err := secret.NewBox("subscription-test-key-0123456789ab")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	entry, err := st.CreateNode("entry", "join-hash-1")
	if err != nil {
		t.Fatal(err)
	}
	exit, err := st.CreateNode("exit", "join-hash-2")
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProfile("alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []store.Node{entry, exit} {
		if err := st.BindProfileNode(p.ID, n.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutClientTemplate(p.ID, ClientXrayJSON,
		`{"tag":"{{node.display_name}}","address":"{{address}}","id":"{{user.uuid}}"}`); err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(store.User{Name: "sub", Enabled: true}, "sub-hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindUserProfile(u.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	rl, err := st.CreateNodeRelay(store.NodeRelay{
		EntryNodeID: entry.ID, ExitNodeID: exit.ID, ProfileID: p.ID,
		Label: "中转线", Enabled: true, Secret: "relay-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Credentials as assembly would have minted them: one direct per node,
	// plus one for the line on the entry.
	for _, n := range []store.Node{entry, exit} {
		if _, err := st.PutCredential(store.Credential{
			UserID: u.ID, ProfileID: p.ID, NodeID: n.ID,
			Email: "sub@" + n.Name, Secret: "uuid-" + n.Name,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.PutCredential(store.Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: entry.ID, ExitRelayID: rl.ID,
		Email: "sub@entry.relay", Secret: "uuid-relay",
	}); err != nil {
		t.Fatal(err)
	}
	// Everything is denied to a fresh subscriber; this fixture is about
	// rendering, so allow the lot.
	if err := st.SetUserNodeAccess(u.ID, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	return st, NewService(st, stubContexts{}), u, entry, exit, rl
}

// A relay is a THIRD line, not a replacement for either end: both nodes are
// ours and the subscriber may well be entitled to reach the exit directly too.
// What the line adds is a different way in to the same exit.
func TestARelayIsItsOwnLineAlongsideBothNodes(t *testing.T) {
	_, svc, u, _, _, _ := relayFixture(t)
	res, err := svc.Render(u, ClientXrayJSON, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Fragments != 3 {
		t.Fatalf("Fragments = %d, want 3 (entry, exit, the line): %s", res.Fragments, res.Body)
	}
	// The line carries its own name — the entry's belongs to its direct line
	// and the exit's to its own, so two proxies alike would make the document
	// ambiguous and the client's picker useless.
	if !strings.Contains(res.Body, `"tag":"中转线"`) {
		t.Errorf("the line is not named after itself:\n%s", res.Body)
	}
	// And its own credential, not the entry's direct one.
	if !strings.Contains(res.Body, `"id":"uuid-relay"`) {
		t.Errorf("the line does not carry its own credential:\n%s", res.Body)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("Skipped = %v", res.Skipped)
	}
}

func TestDenyingALineLeavesTheNodesAlone(t *testing.T) {
	st, svc, u, _, _, rl := relayFixture(t)
	if err := st.SetUserNodeAccess(u.ID, nil, nil, []string{rl.ID}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Render(u, ClientXrayJSON, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Fragments != 2 {
		t.Fatalf("Fragments = %d, want 2 — denying the line took a node with it: %s", res.Fragments, res.Body)
	}
	if strings.Contains(res.Body, "中转线") {
		t.Errorf("a denied line is still in the subscription:\n%s", res.Body)
	}
}

// Denying the ENTRY has to take its lines with it. A line through a box is
// that box's address plus a working credential on it, so carrying the line
// while "denying" the node would hand over exactly what was withheld.
func TestDenyingTheEntryTakesItsLinesWithIt(t *testing.T) {
	st, svc, u, entry, _, _ := relayFixture(t)
	if err := st.SetUserNodeAccess(u.ID, []string{entry.ID}, nil, nil); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Render(u, ClientXrayJSON, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Body, "中转线") {
		t.Errorf("the line survived its entry being denied:\n%s", res.Body)
	}
	if res.Fragments != 1 {
		t.Fatalf("Fragments = %d, want 1 (the exit, reached directly): %s", res.Fragments, res.Body)
	}
}

func TestDisablingALineRemovesItFromSubscriptions(t *testing.T) {
	st, svc, u, _, _, rl := relayFixture(t)
	if err := st.UpdateNodeRelay(rl.ID, rl.Label, false, 1); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Render(u, ClientXrayJSON, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Body, "中转线") {
		t.Errorf("a disabled line is still in the subscription:\n%s", res.Body)
	}
}

// The subscriber's list is one sequence, and a line takes its place in it like
// anything else.
func TestARelayObeysTheOperatorsOrder(t *testing.T) {
	st, svc, u, entry, exit, rl := relayFixture(t)
	if err := st.SetProxyOrder([]store.ProxyOrderEntry{
		{Kind: "relay", ID: rl.ID},
		{Kind: "node", ID: exit.ID},
		{Kind: "node", ID: entry.ID},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Render(u, ClientXrayJSON, "")
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Index(res.Body, "中转线")
	if first < 0 {
		t.Fatalf("the line is missing:\n%s", res.Body)
	}
	if e := strings.Index(res.Body, `"address":"203.0.113.`); e >= 0 && first > e {
		t.Errorf("the line was placed first but rendered after a node:\n%s", res.Body)
	}
}
