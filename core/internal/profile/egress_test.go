package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

// routingTags returns each rule's outboundTag in order, so a test can assert
// on priority rather than mere presence.
func routingTags(t *testing.T, cfg []byte) []string {
	t.Helper()
	var parsed struct {
		Routing struct {
			Rules []struct {
				OutboundTag string `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(parsed.Routing.Rules))
	for _, r := range parsed.Routing.Rules {
		out = append(out, r.OutboundTag)
	}
	return out
}

func egressRules(t *testing.T, cfg []byte, tag string) (domains, ips []string) {
	t.Helper()
	var parsed struct {
		Routing struct {
			Rules []struct {
				Domain      []string `json:"domain"`
				IP          []string `json:"ip"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, r := range parsed.Routing.Rules {
		if r.OutboundTag != tag {
			continue
		}
		domains = append(domains, r.Domain...)
		ips = append(ips, r.IP...)
	}
	return
}

// geosite and geoip go into separate rules sharing one outbound: conditions
// inside a single Xray rule are AND-ed, so one rule naming both would match
// nothing at all.
func TestGeoMatchesSplitIntoTwoRulesOnOneOutbound(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, n := realityProfile(t, svc, st)
	entitle(t, st, "alice", p.ID, nil)

	rule, err := st.CreateEgressRule(store.EgressRule{
		NodeID: n.ID, Label: "netflix", Enabled: true,
		Domains:    []string{"geosite:netflix", "domain:nflxvideo.net"},
		IPs:        []string{"geoip:netflix", "1.2.3.0/24"},
		TargetKind: store.EgressDirect,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := svc.AssembleNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	domains, ips := egressRules(t, cfg, "direct")
	if len(domains) != 2 || domains[0] != "geosite:netflix" {
		t.Errorf("domain rule carries %v", domains)
	}
	if len(ips) != 2 || ips[0] != "geoip:netflix" {
		t.Errorf("ip rule carries %v", ips)
	}
	// One outbound, two rules — never one rule with both conditions.
	var parsed struct {
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, r := range parsed.Routing.Rules {
		if r["outboundTag"] != "direct" {
			continue
		}
		if r["domain"] != nil && r["ip"] != nil {
			t.Fatalf("one rule names both domain and ip, which matches nothing: %v", r)
		}
	}
	_ = rule

}

// A geo rule that names a category the geodata does not contain loads as a
// category that does not exist: `xray -test` refuses it outright, which is
// the one place this class of typo can still be caught. Needs the .dat files
// the panel image ships beside its binary (deploy/README.md).
func TestARealKernelAcceptsGeoEgressRules(t *testing.T) {
	if xrayBin() == "" || !geoAssetsPresent() {
		t.Skip("no xray binary with geo assets; set XRAY_LOCATION_ASSET to run this")
	}
	svc, st, _ := newFixture(t)
	p, n := realityProfile(t, svc, st)
	entitle(t, st, "alice", p.ID, nil)
	if _, err := st.CreateEgressRule(store.EgressRule{
		NodeID: n.ID, Label: "netflix", Enabled: true,
		Domains:    []string{"geosite:netflix", "domain:nflxvideo.net"},
		IPs:        []string{"geoip:jp", "1.2.3.0/24"},
		TargetKind: store.EgressDirect,
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := svc.AssembleNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := (template.Xray{Bin: xrayBin()}).TestConfig(context.Background(), cfg); err != nil {
		t.Fatalf("a real kernel rejected geo egress rules: %v\n%s", err, cfg)
	}
}

// geoAssetsPresent reports whether the geodata files a geosite:/geoip: rule
// needs are where the kernel will look for them.
func geoAssetsPresent() bool {
	dirs := []string{os.Getenv("XRAY_LOCATION_ASSET")}
	if p, err := exec.LookPath(xrayBin()); err == nil {
		dirs = append(dirs, filepath.Dir(p))
	}
	dirs = append(dirs, "/usr/local/share/xray", "/usr/share/xray")
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "geosite.dat")); err == nil {
			return true
		}
	}
	return false
}

// Priority: a restricted-destination block must still outrank an egress rule
// (or a barred user reaches the network by way of the landing), and an egress
// rule must outrank the relay rules (which carry no destination condition and
// would otherwise swallow everything first).
func TestEgressSitsBetweenBlocksAndRelayRules(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, entry, exit, rl := twoNodeRelay(t, svc, st)
	entitle(t, st, "alice", p.ID, nil)
	if err := st.SetNodeRelayAccess(rl.ID, nil); err != nil {
		t.Fatal(err)
	}
	d, err := st.CreateRestrictedDestination("dn42", []string{"172.20.0.0/14"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetRestrictedDestinationNodes(d.ID, []string{entry.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateEgressRule(store.EgressRule{
		NodeID: entry.ID, Label: "netflix", Enabled: true,
		Domains: []string{"geosite:netflix"}, TargetKind: store.EgressDirect,
	}); err != nil {
		t.Fatal(err)
	}

	cfg, err := svc.AssembleNode(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	tags := routingTags(t, cfg)
	pos := func(want string) int {
		for i, tag := range tags {
			if tag == want {
				return i
			}
		}
		return -1
	}
	block, egress, relay := pos(template.BlackholeTag), pos("direct"), pos(RelayTag(rl.ID))
	if block < 0 || egress < 0 || relay < 0 {
		t.Fatalf("missing a rule: block=%d egress=%d relay=%d in %v", block, egress, relay, tags)
	}
	if !(block < egress && egress < relay) {
		t.Fatalf("wrong priority: block=%d egress=%d relay=%d in %v", block, egress, relay, tags)
	}
	_ = exit
}

// A rule landing on an external provider this node already relays for shares
// that one outbound: two outbounds to one provider would double the dials and
// split the picture of what the line carries.
func TestAnEgressLandingSharesAnExistingExitOutbound(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, n := realityProfile(t, svc, st)
	entitle(t, st, "alice", p.ID, nil)

	sub, err := st.CreateExternalSub("prov", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceExternalProxies(sub.ID, []store.ExternalProxy{{
		SubID: sub.ID, Name: "JP", Type: "trojan", Server: "203.0.113.9", Port: 443,
		Config:  "name: JP\ntype: trojan\nserver: 203.0.113.9\nport: 443\npassword: secret\n",
		Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	proxies, err := st.ExternalProxies(sub.ID)
	if err != nil || len(proxies) == 0 {
		t.Fatalf("no proxies stored: %v", err)
	}
	px := proxies[0]
	// Relayed through this node AND used as an egress landing.
	if err := st.SetExternalProxy(px.ID, n.ID, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateEgressRule(store.EgressRule{
		NodeID: n.ID, Label: "netflix", Enabled: true,
		Domains:    []string{"geosite:netflix"},
		TargetKind: store.EgressExternal, TargetProxyID: px.ID,
	}); err != nil {
		t.Fatal(err)
	}

	cfg, err := svc.AssembleNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, ob := range parsed.Outbounds {
		if ob.Tag == ExitTag(px.ID) {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("the provider's outbound appears %d times, want exactly 1", seen)
	}
	if d, _ := egressRules(t, cfg, ExitTag(px.ID)); len(d) != 1 || d[0] != "geosite:netflix" {
		t.Errorf("the egress rule does not point at the shared outbound: %v", d)
	}
}

// The live proof, and it is unambiguous in both directions because the source
// node cannot reach anything by itself: plainRelay gives it a blackhole as its
// default outbound. So traffic that arrives came through the other node, and
// traffic that does not arrive was never routed there.
//
// The match is on the NAME: a request to localhost:port is sent through the
// proxy as a name and the domain rule sees it, while the same server reached
// as 127.0.0.1:port carries no name at all and falls through to the blackhole.
// One destination, two ways of asking for it, opposite outcomes.
func TestEgressSendsOnlyMatchedTrafficThroughTheOtherNode(t *testing.T) {
	if xrayBin() == "" {
		t.Skip("no xray binary; this test needs real kernels")
	}
	svc, st, _ := newFixture(t)
	srcPort, dstPort := freePort(t), freePort(t)
	p, src, dst, rl := plainRelay(t, svc, st, srcPort, dstPort)
	alice := entitle(t, st, "alice", p.ID, nil)
	// The relay line is incidental here; disable it so only the egress rule
	// can move traffic between the two nodes.
	if err := st.UpdateNodeRelay(rl.ID, rl.Label, false, 1); err != nil {
		t.Fatal(err)
	}

	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "arrived")
	}))
	defer dest.Close()

	secret, err := template.Generate(template.GenUUID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateEgressRule(store.EgressRule{
		NodeID: src.ID, Label: "to-dst", Enabled: true,
		Domains:    []string{"domain:localhost"},
		TargetKind: store.EgressNode, TargetNodeID: dst.ID,
		TargetProfileID: p.ID, Secret: secret.Components[""],
	}); err != nil {
		t.Fatal(err)
	}

	srcCfg, err := svc.AssembleNode(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	dstCfg, err := svc.AssembleNode(dst.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The destination node must have accepted the dial credential, or the
	// egress outbound would authenticate as nobody.
	wantEmail := user.EgressStatsEmail(mustEgressID(t, st, src.ID), p.ID, dst.ID)
	if got := clientEmails(t, dstCfg); !contains(got, wantEmail) {
		t.Fatalf("the target node does not carry the egress credential %q; has %v", wantEmail, got)
	}
	startXray(t, "dst", dstCfg, dstPort)
	startXray(t, "src", srcCfg, srcPort)

	tmpl, err := st.ClientTemplates(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := svc.ClientContext(p.ID, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := st.FindCredential(alice.ID, p.ID, src.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	body, err := ctx.With(user.CredentialVars(cred)).Render(tmpl["xray-json"])
	if err != nil {
		t.Fatal(err)
	}
	var ob map[string]any
	if err := json.Unmarshal([]byte(body), &ob); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(ob)
	client := clientThrough(t, "client-egress", string(out), freePort(t))

	port := portOf(t, dest.URL)
	// Matched by name: routed to the egress outbound, out at the other node,
	// and the bytes come back.
	resp, err := client.Get("http://localhost:" + port + "/")
	if err != nil {
		t.Fatalf("matched traffic never arrived through the other node: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != "arrived" {
		t.Fatalf("matched request returned %q", got)
	}
	// The control: the same server asked for by address carries no name, so
	// the domain rule cannot match and the blackhole takes it. Without this
	// half, a node routing EVERYTHING through the landing would also pass.
	if resp, err := client.Get("http://127.0.0.1:" + port + "/"); err == nil {
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(got) == "arrived" {
			t.Fatal("unmatched traffic also went through the other node; the rule is matching more than its domains")
		}
	}
}

// mustEgressID returns the single egress rule on a node.
func mustEgressID(t *testing.T, st *store.Store, nodeID string) string {
	t.Helper()
	rules, err := st.EgressRulesOn(nodeID)
	if err != nil || len(rules) != 1 {
		t.Fatalf("want exactly one egress rule on %s, got %v (%v)", nodeID, len(rules), err)
	}
	return rules[0].ID
}

func portOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Port()
}
