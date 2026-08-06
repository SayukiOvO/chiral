package subscription

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
)

// --- client detection ---

// assembleBodies is what these tests assert on: they are about how fragments
// are joined for each client, not about where the operator put them. The
// ordering has its own tests.
func (s *Service) assembleBodies(u store.User, token, client string, bodies []string) Result {
	placed := make([]placedFragment, 0, len(bodies))
	for i, b := range bodies {
		placed = append(placed, placedFragment{body: b, order: i + 1})
	}
	return s.assemble(u, token, client, placed)
}

func TestExplicitClientWins(t *testing.T) {
	r := httptest.NewRequest("GET", "/sub/tok?client=clash", nil)
	r.Header.Set("User-Agent", "v2rayN/6.0")
	if got := DetectClient(r); got != ClientClash {
		t.Errorf("?client= should win over the UA, got %q", got)
	}
}

func TestUnknownExplicitClientFallsBackToTheUA(t *testing.T) {
	r := httptest.NewRequest("GET", "/sub/tok?client=nonsense", nil)
	r.Header.Set("User-Agent", "Stash/2.0")
	if got := DetectClient(r); got != ClientStash {
		t.Errorf("got %q", got)
	}
}

func TestUserAgentDetection(t *testing.T) {
	cases := map[string]string{
		"clash-verge/1.5":       ClientClash,
		"mihomo/1.18":           ClientClash,
		"ClashMetaForAndroid":   ClientClash,
		"Stash/2.6.0 (like -)":  ClientStash,
		"v2rayN/6.31":           ClientVlessURI,
		"v2rayNG/1.8":           ClientVlessURI,
		"NekoBox/1.0":           ClientVlessURI,
		"Mozilla/5.0 (Firefox)": ClientXrayJSON,
		"":                      ClientXrayJSON,
	}
	for ua, want := range cases {
		if got := ClientForUserAgent(ua); got != want {
			t.Errorf("%q -> %q, want %q", ua, got, want)
		}
	}
}

// Stash reports a UA that also mentions clash in some builds, so the more
// specific match has to be tried first.
func TestStashBeatsClashWhenBothMatch(t *testing.T) {
	if got := ClientForUserAgent("Stash/2.0 clash-compatible"); got != ClientStash {
		t.Errorf("got %q, want stash", got)
	}
}

// --- assembly ---

func TestVlessURIsAreOnePerLine(t *testing.T) {
	r := (&Service{}).assembleBodies(store.User{}, "", ClientVlessURI, []string{"vless://a@h:443#one", "vless://b@h:443#two"})
	if r.Body != "vless://a@h:443#one\nvless://b@h:443#two" {
		t.Errorf("got %q", r.Body)
	}
	if !strings.HasPrefix(r.ContentType, "text/plain") {
		t.Errorf("content type %q", r.ContentType)
	}
}

func TestXrayJSONIsAValidDocument(t *testing.T) {
	r := (&Service{}).assembleBodies(store.User{}, "", ClientXrayJSON, []string{`{"tag":"a","protocol":"vless"}`, `{"tag":"b","protocol":"vless"}`})
	var parsed struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err := jsonUnmarshal(r.Body, &parsed); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, r.Body)
	}
	if len(parsed.Outbounds) != 2 || parsed.Outbounds[0].Tag != "a" {
		t.Errorf("got %+v", parsed.Outbounds)
	}
}

func TestXrayJSONWithOneFragmentHasNoTrailingComma(t *testing.T) {
	r := (&Service{}).assembleBodies(store.User{}, "", ClientXrayJSON, []string{`{"tag":"only"}`})
	var parsed map[string]any
	if err := jsonUnmarshal(r.Body, &parsed); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, r.Body)
	}
}

func TestEmptySubscriptionIsStillValid(t *testing.T) {
	// A user entitled to nothing must not receive a broken file.
	r := (&Service{}).assembleBodies(store.User{}, "", ClientXrayJSON, nil)
	var parsed map[string]any
	if err := jsonUnmarshal(r.Body, &parsed); err != nil {
		t.Errorf("empty xray subscription is not valid JSON: %v\n%s", err, r.Body)
	}
	if r.Fragments != 0 {
		t.Errorf("fragments = %d", r.Fragments)
	}
}

func TestClashDocumentHasProxiesAndAGroup(t *testing.T) {
	r := (&Service{}).assembleBodies(store.User{}, "", ClientClash, []string{
		"name: tokyo-1\ntype: vless\nserver: 203.0.113.9\nport: 443",
		"name: frankfurt-1\ntype: vless\nserver: 198.51.100.7\nport: 443",
	})
	if !strings.HasPrefix(r.Body, "proxies:\n") {
		t.Fatalf("missing proxies section:\n%s", r.Body)
	}
	// Each entry must be a list item, with its continuation lines aligned
	// under it, or the YAML is silently wrong.
	if !strings.Contains(r.Body, "  - name: tokyo-1\n    type: vless\n") {
		t.Errorf("proxy entry is not indented as a list item:\n%s", r.Body)
	}
	// Without a group, clash clients have nothing to select.
	if !strings.Contains(r.Body, "proxy-groups:") ||
		!strings.Contains(r.Body, "      - tokyo-1\n") ||
		!strings.Contains(r.Body, "      - frankfurt-1\n") {
		t.Errorf("proxy group missing or incomplete:\n%s", r.Body)
	}
}

func TestClashGroupIsOmittedWhenThereAreNoProxies(t *testing.T) {
	r := (&Service{}).assembleBodies(store.User{}, "", ClientClash, nil)
	if strings.Contains(r.Body, "proxy-groups:") {
		t.Errorf("an empty subscription should not declare an empty group:\n%s", r.Body)
	}
}

func TestYamlNameHandlesBothForms(t *testing.T) {
	if got := yamlName("name: tokyo-1\ntype: vless"); got != "tokyo-1" {
		t.Errorf("block form: %q", got)
	}
	if got := yamlName(`{name: tokyo-1, type: vless}`); got != "tokyo-1" {
		t.Errorf("inline form: %q", got)
	}
	if got := yamlName(`name: "quoted name"`); got != "quoted name" {
		t.Errorf("quoted: %q", got)
	}
	if got := yamlName("type: vless"); got != "" {
		t.Errorf("no name should yield empty, got %q", got)
	}
}

// The client context must not carry secrets — a template referencing one has
// to fail rather than render it into somebody's subscription.
func TestClientContextRefusesSecrets(t *testing.T) {
	ctx := template.NewContext(map[string]string{
		"reality.private": "PRIVATE-KEY",
		"reality.public":  "PUBLIC-KEY",
	}, []string{"reality.private"}).ForClient()

	if _, err := ctx.Render(`pbk: {{reality.private}}`); err == nil {
		t.Fatal("a client template referencing a private key must fail")
	}
	got, err := ctx.Render(`pbk: {{reality.public}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "pbk: PUBLIC-KEY" {
		t.Errorf("got %q", got)
	}
}

func jsonUnmarshal(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

// --- regressions from the M3 adversarial review ---

// A clash proxy entry routinely nests (reality-opts, ws-opts). Flattening
// every line to one depth reparents those keys onto the proxy itself: still
// valid YAML, but a different and broken config.
func TestNestedProxyOptionsKeepTheirStructure(t *testing.T) {
	fragment := strings.Join([]string{
		"name: tokyo-1",
		"type: vless",
		"server: 203.0.113.9",
		"port: 443",
		"reality-opts:",
		"  public-key: PUBKEY",
		"  short-id: a1fcb027",
		"client-fingerprint: chrome",
	}, "\n")
	body := (&Service{}).assembleBodies(store.User{}, "", ClientClash, []string{fragment}).Body

	// The nested keys must stay deeper than the key that introduces them.
	depth := func(needle string) int {
		for _, l := range strings.Split(body, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), needle) {
				return len(l) - len(strings.TrimLeft(l, " "))
			}
		}
		return -1
	}
	opts, pub := depth("reality-opts:"), depth("public-key:")
	if opts < 0 || pub < 0 {
		t.Fatalf("keys missing from output:\n%s", body)
	}
	if pub <= opts {
		t.Errorf("nested key was flattened to the parent's depth (reality-opts=%d public-key=%d):\n%s",
			opts, pub, body)
	}
	// A sibling of reality-opts must not be swallowed into it.
	if fp := depth("client-fingerprint:"); fp != opts {
		t.Errorf("sibling key ended up at the wrong depth (%d, want %d):\n%s", fp, opts, body)
	}
}

// Templates may be written with their own leading indentation; the entry
// should still be anchored correctly under the list item.
func TestIndentedFragmentIsReanchored(t *testing.T) {
	fragment := "    name: tokyo-1\n    type: vless\n    reality-opts:\n      public-key: K"
	body := (&Service{}).assembleBodies(store.User{}, "", ClientClash, []string{fragment}).Body
	if !strings.Contains(body, "  - name: tokyo-1\n") {
		t.Errorf("entry not anchored as a list item:\n%s", body)
	}
	if !strings.Contains(body, "    type: vless\n") {
		t.Errorf("sibling key at the wrong depth:\n%s", body)
	}
	if !strings.Contains(body, "      public-key: K\n") {
		t.Errorf("nested key at the wrong depth:\n%s", body)
	}
}

func TestBlankLinesInFragmentsAreDropped(t *testing.T) {
	body := (&Service{}).assembleBodies(store.User{}, "", ClientClash, []string{"name: a\n\ntype: vless\n"}).Body
	if strings.Contains(body, "\n\n") {
		t.Errorf("blank line survived into the document:\n%s", body)
	}
}

// Latency-based selection alongside the manual one, not instead of it.
//
// The manual group must stay: a subscriber who has worked out which node is
// good for them — often for reasons a latency probe cannot see, like which one
// their bank tolerates — must not have that quietly replaced.
func TestClashOffersBothManualAndAutomaticGroups(t *testing.T) {
	r := (&Service{}).assembleBodies(store.User{}, "", ClientClash, []string{
		"name: tokyo-1\ntype: vless\nserver: 203.0.113.9\nport: 443",
		"name: frankfurt-1\ntype: vless\nserver: 198.51.100.7\nport: 443",
	})

	if !strings.Contains(r.Body, "  - name: Chiral\n    type: select\n") {
		t.Errorf("the manual group is gone:\n%s", r.Body)
	}
	if !strings.Contains(r.Body, "type: url-test") {
		t.Errorf("no automatic group:\n%s", r.Body)
	}
	// Nested, so "auto" is a choice inside the one control the user already
	// knows about rather than a second control they have to discover.
	manual := r.Body[strings.Index(r.Body, "  - name: Chiral\n"):]
	if end := strings.Index(manual, "  - name: Chiral 自动"); end >= 0 {
		manual = manual[:end]
	}
	if !strings.Contains(manual, "      - Chiral 自动\n") {
		t.Errorf("the automatic group is not a member of the manual one:\n%s", manual)
	}
	// Every node belongs to both.
	for _, name := range []string{"tokyo-1", "frankfurt-1"} {
		if strings.Count(r.Body, "      - "+name+"\n") != 2 {
			t.Errorf("%s does not appear in both groups:\n%s", name, r.Body)
		}
	}
	// A url-test group with no probe URL silently never tests anything.
	if !strings.Contains(r.Body, "url: "+autoTestURL) {
		t.Errorf("the automatic group has no test URL:\n%s", r.Body)
	}
	if !strings.Contains(r.Body, "interval: 300") {
		t.Errorf("the automatic group has no interval:\n%s", r.Body)
	}
}

// --- degradation: one bad access point must not blank the list ---

// stubContexts stands in for the profile service. Each profile gets a context
// carrying one secret component, so a template that reaches for it fails the
// same way it would in production.
type stubContexts struct{}

func (stubContexts) ClientContext(profileID, nodeID string) (*template.Context, error) {
	return template.NewContext(map[string]string{
		"reality.private": "PRIVATE-KEY",
		"reality.public":  "PUBLIC-KEY",
		"address":         "203.0.113." + nodeID[len(nodeID)-1:],
		// Always present in production, where it is node metadata rather than
		// an operator's variable — and it is what a relayed line overrides to
		// give itself a name of its own.
		"node.display_name": "node-" + nodeID[len(nodeID)-1:],
	}, []string{"reality.private"}).ForClient(), nil
}

// twoProfileFixture gives one user two entitled profiles on one node, each with
// its own xray-json template, and returns the store, service and user.
func twoProfileFixture(t *testing.T) (*store.Store, *Service, store.User) {
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

	n, err := st.CreateNode("node1", "join-hash")
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(store.User{Name: "sub", Enabled: true}, "sub-hash")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		p, err := st.CreateProfile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.BindProfileNode(p.ID, n.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.BindUserProfile(u.ID, p.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.PutClientTemplate(p.ID, ClientXrayJSON,
			`{"tag":"`+name+`","address":"{{address}}","pbk":"{{reality.public}}"}`); err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutCredential(store.Credential{
			UserID: u.ID, ProfileID: p.ID, NodeID: n.ID,
			Email: name + "@node1", Secret: "uuid-" + name,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// The user is created after the node, so they start denied it — a new
	// subscriber holds nothing until somebody says otherwise. These tests are
	// about how a subscription renders, so the fixture says otherwise.
	if err := st.SetUserNodeAccess(u.ID, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	return st, NewService(st, stubContexts{}), u
}

// One typo in one profile used to blank the entire subscription: every healthy
// access point the customer was entitled to vanished with it, for every user
// bound to that profile. A degraded list beats an empty one.
func TestOneUnrenderableProfileDoesNotTakeTheOthersDown(t *testing.T) {
	st, svc, u := twoProfileFixture(t)

	profiles, err := st.UserProfileIDs(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(profiles)
	// Break exactly one, by pointing it at a component the client context
	// deliberately strips.
	if err := st.PutClientTemplate(profiles[0], ClientXrayJSON,
		`{"leak":"{{reality.private}}"}`); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Render(u, ClientXrayJSON, "")
	if err != nil {
		t.Fatalf("a broken template failed the whole subscription: %v", err)
	}
	if res.Fragments != 1 {
		t.Fatalf("Fragments = %d, want 1 — the healthy profile's fragment was lost too", res.Fragments)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("Skipped = %v, want exactly the broken profile", res.Skipped)
	}
	if !strings.Contains(strings.Join(res.Skipped, " "), profiles[0]) {
		t.Errorf("the skip does not name the profile that failed: %v", res.Skipped)
	}
	if strings.Contains(res.Body, "PRIVATE-KEY") {
		t.Fatalf("a secret leaked into the served body:\n%s", res.Body)
	}
}

// When nothing renders, the caller must be able to tell that apart from a
// legitimately empty entitlement — the handler turns one of them into a non-2xx.
func TestAllProfilesBrokenReportsZeroFragmentsAndWhy(t *testing.T) {
	st, svc, u := twoProfileFixture(t)
	profiles, err := st.UserProfileIDs(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, pid := range profiles {
		if err := st.PutClientTemplate(pid, ClientXrayJSON, `{"leak":"{{reality.private}}"}`); err != nil {
			t.Fatal(err)
		}
	}
	res, err := svc.Render(u, ClientXrayJSON, "")
	if err != nil {
		t.Fatalf("Render returned an error instead of an empty result: %v", err)
	}
	if res.Fragments != 0 {
		t.Fatalf("Fragments = %d, want 0", res.Fragments)
	}
	if len(res.Skipped) != len(profiles) {
		t.Errorf("Skipped = %v, want one entry per profile", res.Skipped)
	}
}

// Clash-family clients take the profile's name from the download filename, so
// this is what every subscriber reads in their client. Left as the software's
// own name it puts "chiral" on all of their screens.
func TestSubscriptionFilenameFollowsTheSetting(t *testing.T) {
	st, svc, u := twoProfileFixture(t)
	if got := svc.assembleBodies(u, "", ClientClash, []string{"name: a"}).Filename; got != "chiral" {
		t.Fatalf("default filename = %q", got)
	}
	if err := st.SetSetting(store.SettingSubscriptionName, "Mai 的机场"); err != nil {
		t.Fatal(err)
	}
	// No extension: the client shows this string verbatim in its profile
	// list, and ".yaml" in every subscriber's list is not a thing the operator
	// asked for.
	for _, tc := range []struct{ client, want string }{
		{ClientClash, "Mai 的机场"},
		{ClientXrayJSON, "Mai 的机场"},
		{ClientVlessURI, "Mai 的机场"},
	} {
		got := svc.assembleBodies(u, "", tc.client, []string{"name: a"}).Filename
		if got != tc.want {
			t.Errorf("%s filename = %q, want %q", tc.client, got, tc.want)
		}
	}
}

// The name goes into a Content-Disposition header and then onto somebody's
// disk. A quote would end the header's quoted string early and a slash would
// write outside the directory the client meant; spaces and emoji are a label a
// person chose and are left alone.
func TestSubscriptionFilenameIsSafe(t *testing.T) {
	st, svc, u := twoProfileFixture(t)
	for _, tc := range []struct{ set, want string }{
		{`a"b`, "ab"},
		{`../../etc/passwd`, "....etcpasswd"},
		{"back\\slash", "backslash"},
		{"  spaced  ", "spaced"},
		{"🇭🇰 香港机场", "🇭🇰 香港机场"},
		{"", "chiral"},
	} {
		if err := st.SetSetting(store.SettingSubscriptionName, tc.set); err != nil {
			t.Fatal(err)
		}
		if got := svc.assembleBodies(u, "", ClientClash, []string{"name: a"}).Filename; got != tc.want {
			t.Errorf("%q -> %q, want %q", tc.set, got, tc.want)
		}
	}
}

// Entitlement is per profile, so granting one gives every node bound to it.
// That is the right default and it left no way to say "this person, not that
// box" — an operator who wanted one had to split the profile and keep the
// copies in step by hand.
func TestDeniedNodesLeaveTheSubscription(t *testing.T) {
	st, svc, u := twoProfileFixture(t)

	before, err := svc.Render(u, ClientXrayJSON, "")
	if err != nil {
		t.Fatal(err)
	}
	if before.Fragments == 0 {
		t.Fatal("fixture produced nothing to deny")
	}

	nodes, err := st.ListNodes()
	if err != nil || len(nodes) == 0 {
		t.Fatalf("no nodes: %v", err)
	}
	if err := st.SetUserNodeAccess(u.ID, []string{nodes[0].ID}, nil, nil); err != nil {
		t.Fatal(err)
	}
	after, err := svc.Render(u, ClientXrayJSON, "")
	if err == nil && after.Fragments != 0 {
		t.Fatalf("denied node still present: %d fragments", after.Fragments)
	}
}

// A subscriber holds nothing until somebody says otherwise — including nodes
// that already existed when they were created. The opposite default meant a
// person added on Tuesday silently acquired every machine bought before then.
func TestANewSubscriberHoldsNothingYet(t *testing.T) {
	box, err := secret.NewBox("subscription-test-key-0123456789ab")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	n, err := st.CreateNode("node1", "join-hash")
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(store.User{Name: "newcomer", Enabled: true}, "hash-n")
	if err != nil {
		t.Fatal(err)
	}
	denied, err := st.UserNodeDenies(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, no := denied[n.ID]; !no {
		t.Fatal("a fresh subscriber already held a node nobody granted them")
	}
}

// The console shows the whole list and the operator ticks boxes, so a write
// describes an end state. Setting it twice must not accumulate.
func TestSettingAccessReplacesRatherThanAdds(t *testing.T) {
	st, _, u := twoProfileFixture(t)
	nodes, _ := st.ListNodes()
	id := nodes[0].ID

	if err := st.SetUserNodeAccess(u.ID, []string{id}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserNodeAccess(u.ID, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	denied, _ := st.UserNodeDenies(u.ID)
	if len(denied) != 0 {
		t.Fatalf("clearing left %v", denied)
	}
}

// The one arrangement.
//
// Placed entries come out in the operator's order, and anything never placed
// follows them rather than jumping the queue — a node added this morning
// belongs at the end of the list, not in the middle of it.
func TestTheOperatorsOrderIsWhatComesOut(t *testing.T) {
	got := orderFragments([]placedFragment{
		{body: "new-b", order: 0, tie: "b"},
		{body: "third", order: 3, tie: "x"},
		{body: "first", order: 1, tie: "x"},
		{body: "new-a", order: 0, tie: "a"},
		{body: "second", order: 2, tie: "x"},
	})
	want := []string{"first", "second", "third", "new-a", "new-b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}
