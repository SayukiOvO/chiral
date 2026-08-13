package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

// restrictedRules pulls the blackhole rules out of an assembled config.
func restrictedRules(t *testing.T, cfg []byte) []struct {
	IP     []string
	Domain []string
	User   []string
} {
	t.Helper()
	var parsed struct {
		Routing struct {
			Rules []struct {
				IP          []string `json:"ip"`
				Domain      []string `json:"domain"`
				User        []string `json:"user"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		t.Fatal(err)
	}
	var out []struct {
		IP     []string
		Domain []string
		User   []string
	}
	for _, r := range parsed.Routing.Rules {
		if r.OutboundTag == template.BlackholeTag {
			out = append(out, struct {
				IP     []string
				Domain []string
				User   []string
			}{r.IP, r.Domain, r.User})
		}
	}
	return out
}

func TestOnlyTheBarredAreBarredAndOnlyWhereItApplies(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, n := realityProfile(t, svc, st)
	alice := entitle(t, st, "alice", p.ID, nil)
	bob := entitle(t, st, "bob", p.ID, nil)

	d, err := st.CreateRestrictedDestination("dn42", []string{"172.20.0.0/14"}, []string{"dn42"})
	if err != nil {
		t.Fatal(err)
	}

	// Not scoped anywhere yet: no rules, anywhere.
	cfg, err := svc.AssembleNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := restrictedRules(t, cfg); len(got) != 0 {
		t.Fatalf("an unscoped destination produced rules: %+v", got)
	}

	// Scoped to the node, alice allowed: only bob's email is barred.
	if err := st.SetRestrictedDestinationNodes(d.ID, []string{n.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRestrictedDestinationAllows(d.ID, []string{alice.ID}); err != nil {
		t.Fatal(err)
	}
	cfg, err = svc.AssembleNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	rules := restrictedRules(t, cfg)
	if len(rules) != 2 {
		t.Fatalf("want an ip rule and a domain rule, got %+v", rules)
	}
	bobEmail := user.StatsEmail(bob.Name, bob.ID, p.ID, n.ID)
	aliceEmail := user.StatsEmail(alice.Name, alice.ID, p.ID, n.ID)
	for _, r := range rules {
		if len(r.User) != 1 || r.User[0] != bobEmail {
			t.Errorf("rule bars %v, want exactly [%s]", r.User, bobEmail)
		}
		for _, u := range r.User {
			if u == aliceEmail {
				t.Errorf("alice is allowed and still barred: %v", r.User)
			}
		}
	}

	// Everyone allowed: the rules must VANISH, not shrink to an empty user
	// list — an empty list matches every user, and "nobody barred" and
	// "everybody barred" must never be one omission apart.
	if err := st.SetRestrictedDestinationAllows(d.ID, []string{alice.ID, bob.ID}); err != nil {
		t.Fatal(err)
	}
	cfg, err = svc.AssembleNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := restrictedRules(t, cfg); len(got) != 0 {
		t.Fatalf("nobody is barred and rules still exist: %+v", got)
	}

	if xrayBin() == "" {
		t.Skip("no xray binary; skipping validation")
	}
	if err := st.SetRestrictedDestinationAllows(d.ID, []string{alice.ID}); err != nil {
		t.Fatal(err)
	}
	cfg, err = svc.AssembleNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := (template.Xray{Bin: xrayBin()}).TestConfig(context.Background(), cfg); err != nil {
		t.Errorf("config with restricted rules rejected by xray -test: %v\n%s", err, cfg)
	}
}

// The block must outrank the relay rule. Both match on the user's email, and
// the relay rule matches their EVERY destination — behind it, a barred user's
// restricted traffic slips down the line to the exit, where only the line's
// machine credential is visible and enforcement is no longer possible.
func TestABlockOutranksTheRelayRule(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, entry, exit, rl := twoNodeRelay(t, svc, st)
	alice := entitle(t, st, "alice", p.ID, nil)
	if err := st.SetNodeRelayAccess(rl.ID, nil); err != nil {
		t.Fatal(err)
	}
	d, err := st.CreateRestrictedDestination("dn42", []string{"172.20.0.0/14"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Scoped to the EXIT only. The entry must inherit the rules because its
	// line lands there — this is the leak test.
	if err := st.SetRestrictedDestinationNodes(d.ID, []string{exit.ID}); err != nil {
		t.Fatal(err)
	}

	cfg, err := svc.AssembleNode(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Routing struct {
			Rules []struct {
				OutboundTag string   `json:"outboundTag"`
				User        []string `json:"user"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		t.Fatal(err)
	}
	blockAt, relayAt := -1, -1
	for i, r := range parsed.Routing.Rules {
		if r.OutboundTag == template.BlackholeTag && blockAt < 0 {
			blockAt = i
		}
		if r.OutboundTag == RelayTag(rl.ID) && relayAt < 0 {
			relayAt = i
		}
	}
	if blockAt < 0 {
		t.Fatal("the entry of a line landing on a scoped exit carries no block rule")
	}
	if relayAt >= 0 && blockAt > relayAt {
		t.Fatalf("the relay rule at %d outranks the block at %d; a barred user's restricted traffic escapes down the line", relayAt, blockAt)
	}
	// And the barred set includes alice's RELAY credential, the one that
	// would have carried the traffic into the line.
	relayEmail := user.StatsEmailForExit(alice.Name, alice.ID, p.ID, entry.ID, "r"+rl.ID)
	found := false
	for _, r := range parsed.Routing.Rules {
		if r.OutboundTag == template.BlackholeTag {
			for _, u := range r.User {
				if u == relayEmail {
					found = true
				}
			}
		}
	}
	if !found {
		t.Errorf("the block does not bar alice's relay credential %s", relayEmail)
	}
}

// Live proof with two kernels: a barred user's traffic to the restricted
// range dies at the ENTRY of a relay line, while an allowed user's traverses
// the line and arrives. The destination is scoped to the EXIT — the entry
// enforces it purely through inheritance, which is the leak this feature had
// to not have.
func TestRestrictedTrafficDiesAtTheEntryOfALine(t *testing.T) {
	if xrayBin() == "" {
		t.Skip("no xray binary; this test needs real kernels")
	}
	svc, st, _ := newFixture(t)
	entryPort, exitPort := freePort(t), freePort(t)
	p, entry, exit, rl := plainRelay(t, svc, st, entryPort, exitPort)
	alice := entitle(t, st, "alice", p.ID, nil)
	bob := entitle(t, st, "bob", p.ID, nil)
	if err := st.SetNodeRelayAccess(rl.ID, nil); err != nil {
		t.Fatal(err)
	}

	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "arrived")
	}))
	defer dest.Close()

	// The whole loopback net is "the special network", which the destination
	// server happens to live in — the same shape as DN42 behind an exit.
	d, err := st.CreateRestrictedDestination("loop", []string{"127.0.0.0/8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetRestrictedDestinationNodes(d.ID, []string{exit.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRestrictedDestinationAllows(d.ID, []string{alice.ID}); err != nil {
		t.Fatal(err)
	}

	entryCfg, err := svc.AssembleNode(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	exitCfg, err := svc.AssembleNode(exit.ID)
	if err != nil {
		t.Fatal(err)
	}
	startXray(t, "exit", exitCfg, exitPort)
	startXray(t, "entry", entryCfg, entryPort)

	tmpl, err := st.ClientTemplates(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := svc.ClientContext(p.ID, entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	dial := func(u store.User) *http.Client {
		cred, err := st.FindCredentialForExit(u.ID, p.ID, entry.ID, "", rl.ID)
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
		return clientThrough(t, "client-"+u.Name, string(out), freePort(t))
	}

	// Alice is allowed: through the line, out at the exit, into the range.
	resp, err := dial(alice).Get(dest.URL)
	if err != nil {
		t.Fatalf("the allowed user could not reach the restricted range: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != "arrived" {
		t.Fatalf("allowed user's request returned %q", got)
	}

	// Bob is barred: same line, same inbound, same destination — dead at the
	// entry. A blackhole swallows rather than refuses, so what comes back is
	// a gateway failure, never the destination's body.
	if resp, err := dial(bob).Get(dest.URL); err == nil {
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(got), "arrived") {
			t.Fatalf("the barred user reached the restricted range through the line (status %d)", resp.StatusCode)
		}
	}
}
