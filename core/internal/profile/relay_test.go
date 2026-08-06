package profile

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

// twoNodeRelay wires up the smallest arrangement the feature needs: one
// profile served on two nodes, and a line from the first to the second.
//
// Both nodes carry the same profile because that is the ordinary case — a
// fleet has one way in and several boxes — and because it is the case where a
// mistake is invisible: if the entry's outbound were built from the entry's
// own variables instead of the exit's, everything would still render and the
// line would quietly dial the wrong machine.
func twoNodeRelay(t *testing.T, svc *Service, st *store.Store, allow ...store.User) (store.Profile, store.Node, store.Node, store.NodeRelay) {
	t.Helper()
	p, entry := realityProfile(t, svc, st)
	exit, err := st.CreateNode("osaka-1", "join-hash-2")
	if err != nil {
		t.Fatal(err)
	}
	// Distinct addresses, so "which machine does the outbound point at" has an
	// answer a test can check.
	if err := st.UpdateNode(entry.ID, entry.Name, "", "entry.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateNode(exit.ID, exit.Name, "", "exit.example.com"); err != nil {
		t.Fatal(err)
	}
	entry, _ = st.GetNode(entry.ID)
	exit, _ = st.GetNode(exit.ID)
	if err := st.BindProfileNode(p.ID, exit.ID); err != nil {
		t.Fatal(err)
	}
	// The artefact the entry dials with: the same client template a
	// subscriber's Xray would use.
	if err := st.PutClientTemplate(p.ID, "xray-json", `{
	  "protocol": "vless",
	  "settings": { "vnext": [ { "address": "{{node.address}}", "port": {{port}},
	    "users": [ { "id": "{{user.uuid}}", "encryption": "none", "flow": "" } ] } ] },
	  "streamSettings": { "network": "tcp", "security": "reality",
	    "realitySettings": { "serverName": "{{sni}}", "fingerprint": "chrome",
	      "publicKey": "{{reality.public}}", "shortId": "{{shortId}}" } }
	}`); err != nil {
		t.Fatal(err)
	}
	rl, err := st.CreateNodeRelay(store.NodeRelay{
		EntryNodeID: entry.ID, ExitNodeID: exit.ID, ProfileID: p.ID,
		Label: "东京中转 → 大阪", Enabled: true, Secret: "11111111-2222-3333-4444-555555555555",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A new line is denied to everyone; say who may take it.
	denied := []string{}
	users, err := st.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		keep := false
		for _, a := range allow {
			if a.ID == u.ID {
				keep = true
			}
		}
		if !keep {
			denied = append(denied, u.ID)
		}
	}
	if err := st.SetNodeRelayAccess(rl.ID, denied); err != nil {
		t.Fatal(err)
	}
	return p, entry, exit, rl
}

// outboundsOf returns each outbound's tag and, for a vless one, the address it
// dials.
func outboundsOf(t *testing.T, cfg []byte) map[string]string {
	t.Helper()
	var parsed struct {
		Outbounds []struct {
			Tag      string `json:"tag"`
			Settings struct {
				VNext []struct {
					Address string `json:"address"`
				} `json:"vnext"`
			} `json:"settings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, ob := range parsed.Outbounds {
		addr := ""
		if len(ob.Settings.VNext) > 0 {
			addr = ob.Settings.VNext[0].Address
		}
		out[ob.Tag] = addr
	}
	return out
}

// rulesOf returns each routing rule's outboundTag and the emails it matches.
func rulesOf(t *testing.T, cfg []byte) map[string][]string {
	t.Helper()
	var parsed struct {
		Routing struct {
			Rules []struct {
				User        []string `json:"user"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, r := range parsed.Routing.Rules {
		out[r.OutboundTag] = r.User
	}
	return out
}

func TestTheEntryDialsTheExitAndTheExitAcceptsIt(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, entry, exit, rl := twoNodeRelay(t, svc, st)
	alice := entitle(t, st, "alice", p.ID, nil)
	if err := st.SetNodeRelayAccess(rl.ID, nil); err != nil {
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

	// The entry gained an outbound, and it points at the EXIT's address — not
	// its own, which is the mistake that would still render and still validate.
	obs := outboundsOf(t, entryCfg)
	addr, ok := obs[RelayTag(rl.ID)]
	if !ok {
		t.Fatalf("entry has no relay outbound; got %v", obs)
	}
	if addr != "exit.example.com" {
		t.Errorf("relay outbound dials %q, want the exit's address", addr)
	}

	// The exit gained a client for the line itself.
	want := user.RelayStatsEmail(rl.ID, p.ID, exit.ID)
	if got := clientEmails(t, exitCfg); !contains(got, want) {
		t.Errorf("exit does not carry the line's credential %q; has %v", want, got)
	}
	// And the entry did not: the line's credential belongs on the far end.
	if got := clientEmails(t, entryCfg); contains(got, want) {
		t.Errorf("entry carries the line's own credential, which is the exit's: %v", got)
	}

	// Alice got a second credential on the entry, and a rule sending it down
	// the line.
	relayEmail := user.StatsEmailForExit(alice.Name, alice.ID, p.ID, entry.ID, "r"+rl.ID)
	if got := clientEmails(t, entryCfg); !contains(got, relayEmail) {
		t.Errorf("entry has no relayed credential for alice; has %v", got)
	}
	rules := rulesOf(t, entryCfg)
	if got := rules[RelayTag(rl.ID)]; len(got) != 1 || got[0] != relayEmail {
		t.Errorf("relay rule matches %v, want exactly [%s]", got, relayEmail)
	}

	if xrayBin() == "" {
		t.Skip("no xray binary; skipping validation of the assembled configs")
	}
	x := template.Xray{Bin: xrayBin()}
	if err := x.TestConfig(context.Background(), entryCfg); err != nil {
		t.Errorf("entry config rejected by xray -test: %v\n%s", err, entryCfg)
	}
	if err := x.TestConfig(context.Background(), exitCfg); err != nil {
		t.Errorf("exit config rejected by xray -test: %v\n%s", err, exitCfg)
	}
}

// A line nobody may take must still get its outbound, and must NOT get a rule:
// a routing rule with an empty user list matches every user, so the "nobody"
// case and the "everybody" case are one character apart in the output.
func TestALineNobodyMayTakeRoutesNobody(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, entry, _, rl := twoNodeRelay(t, svc, st)
	entitle(t, st, "alice", p.ID, nil)

	cfg, err := svc.AssembleNode(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := outboundsOf(t, cfg)[RelayTag(rl.ID)]; !ok {
		t.Error("a line nobody may take lost its outbound; the operator can no longer see it is configured")
	}
	if got, ok := rulesOf(t, cfg)[RelayTag(rl.ID)]; ok {
		t.Errorf("a line nobody may take has a rule matching %v", got)
	}
}

// The line's own credential is not a subscriber's, and nothing must bill it.
// The entry already charged whoever authenticated there; counting the same
// bytes again at the exit would double-bill a gigabyte, once to a person and
// once to a machine.
func TestTheLinesOwnBytesAreNobodysBytes(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, _, exit, rl := twoNodeRelay(t, svc, st)
	alice := entitle(t, st, "alice", p.ID, nil)
	if _, err := svc.AssembleNode(exit.ID); err != nil {
		t.Fatal(err)
	}

	email := user.RelayStatsEmail(rl.ID, p.ID, exit.ID)
	creds, err := st.NodeCredentials(exit.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range creds {
		if c.Email == email {
			t.Fatal("the line's credential is in the credentials table; it belongs to no user")
		}
	}
	if err := st.AddCredentialTraffic(email, 1<<20, 1<<20); err != nil {
		t.Fatalf("reporting the line's own traffic failed: %v", err)
	}
	after, err := st.GetUser(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.UsedBytes != 0 {
		t.Errorf("the line's bytes were billed to a subscriber: used_bytes = %d", after.UsedBytes)
	}
}

// A relayed credential is priced by the line, because the line is what the
// subscriber picked and what the operator pays for.
func TestRelayedTrafficIsPricedByTheLine(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, entry, _, rl := twoNodeRelay(t, svc, st)
	alice := entitle(t, st, "alice", p.ID, nil)
	if err := st.SetNodeRelayAccess(rl.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateNodeRelay(rl.ID, rl.Label, true, 2.5); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AssembleNode(entry.ID); err != nil {
		t.Fatal(err)
	}

	email := user.StatsEmailForExit(alice.Name, alice.ID, p.ID, entry.ID, "r"+rl.ID)
	if err := st.AddCredentialTraffic(email, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	after, err := st.GetUser(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.UsedBytes != 5000 {
		t.Errorf("billed %d bytes, want 5000 (2000 × 2.5)", after.UsedBytes)
	}
	// The counter itself stays what the agent measured, so "how much moved"
	// and "how much it cost" remain separate questions.
	cred, err := st.FindCredentialForExit(alice.ID, p.ID, entry.ID, "", rl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cred.UpBytes+cred.DownBytes != 2000 {
		t.Errorf("counter reads %d, want the measured 2000", cred.UpBytes+cred.DownBytes)
	}
}

// Disabling a line takes it out of both configs, and re-enabling it must bring
// back the SAME credential — a new one would break every client already
// pointed at the line.
func TestDisablingALineKeepsItsCredential(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, entry, exit, rl := twoNodeRelay(t, svc, st)
	entitle(t, st, "alice", p.ID, nil)
	if err := st.SetNodeRelayAccess(rl.ID, nil); err != nil {
		t.Fatal(err)
	}
	before, err := svc.AssembleNode(entry.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := st.UpdateNodeRelay(rl.ID, rl.Label, false, rl.TrafficRate); err != nil {
		t.Fatal(err)
	}
	off, err := svc.AssembleNode(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := outboundsOf(t, off)[RelayTag(rl.ID)]; ok {
		t.Error("a disabled line still has an outbound on the entry")
	}
	offExit, err := svc.AssembleNode(exit.ID)
	if err != nil {
		t.Fatal(err)
	}
	if contains(clientEmails(t, offExit), user.RelayStatsEmail(rl.ID, p.ID, exit.ID)) {
		t.Error("a disabled line still has a client on the exit")
	}

	if err := st.UpdateNodeRelay(rl.ID, rl.Label, true, rl.TrafficRate); err != nil {
		t.Fatal(err)
	}
	again, err := svc.AssembleNode(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := outboundsOf(t, before), outboundsOf(t, again); a[RelayTag(rl.ID)] != b[RelayTag(rl.ID)] {
		t.Errorf("re-enabling changed the line: %q then %q", a[RelayTag(rl.ID)], b[RelayTag(rl.ID)])
	}
	// The credential is the thing that must not have moved.
	got, err := st.GetNodeRelay(rl.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret != rl.Secret {
		t.Error("re-enabling a line rotated its credential")
	}
}

// The rule that selects a line has to sit ahead of whatever the operator wrote,
// because routing is first-match and every skeleton in the wild ends with a
// catch-all. Sending relayed traffic straight out of the entry looks exactly
// like the line working — the connection succeeds, only the exit is wrong.
func TestTheRelayRuleOutranksTheOperatorsCatchAll(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, entry, _, rl := twoNodeRelay(t, svc, st)
	entitle(t, st, "alice", p.ID, nil)
	if err := st.SetNodeRelayAccess(rl.ID, nil); err != nil {
		t.Fatal(err)
	}
	n, err := st.GetNode(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	n.ConfigSkeleton = `{
	  "log": { "loglevel": "warning" },
	  "inbounds": [],
	  "outbounds": [ { "protocol": "freedom", "tag": "direct" } ],
	  "routing": { "rules": [ { "type": "field", "network": "tcp,udp", "outboundTag": "direct" } ] }
	}`
	if err := st.SetConfigSkeleton(n.ID, n.ConfigSkeleton); err != nil {
		t.Fatal(err)
	}
	cfg, err := svc.AssembleNode(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
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
	relayAt, directAt := -1, -1
	for i, r := range parsed.Routing.Rules {
		if r.OutboundTag == RelayTag(rl.ID) && relayAt < 0 {
			relayAt = i
		}
		if r.OutboundTag == "direct" && directAt < 0 {
			directAt = i
		}
	}
	if relayAt < 0 {
		t.Fatalf("no relay rule in %v", parsed.Routing.Rules)
	}
	if directAt >= 0 && relayAt > directAt {
		t.Errorf("the catch-all at %d swallows the relay rule at %d", directAt, relayAt)
	}
}

// A line whose exit no longer serves the profile cannot be assembled into
// something that works, and the panel has to say so rather than render a proxy
// that never connects.
func TestALineWithNoWayToDialIsRefusedAtAssembly(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, entry, _, _ := twoNodeRelay(t, svc, st)
	entitle(t, st, "alice", p.ID, nil)
	if err := st.DeleteClientTemplate(p.ID, "xray-json"); err != nil {
		t.Fatal(err)
	}
	_, err := svc.AssembleNode(entry.ID)
	if err == nil {
		t.Fatal("assembly succeeded with no template to dial the exit with")
	}
	if !strings.Contains(err.Error(), "xray-json") {
		t.Errorf("the error does not name what is missing: %v", err)
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
