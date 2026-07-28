package profile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/store"
)

// probeFixture is a REALITY profile with an xray-json client template and one
// entitled user — the shape a node has to be in for its data path to be
// checkable at all.
func probeFixture(t *testing.T) (*Service, *store.Store, store.Node, store.Profile) {
	t.Helper()
	svc, st, _ := newFixture(t)
	p, n := realityProfile(t, svc, st)

	if err := st.PutClientTemplate(p.ID, "xray-json", `{
	  "protocol": "vless",
	  "settings": {"vnext": [{"address": "{{sni}}", "port": {{port}},
	    "users": [{"id": "{{user.uuid}}", "encryption": "none"}]}]},
	  "streamSettings": {"network": "tcp", "security": "reality",
	    "realitySettings": {"serverName": "{{sni}}", "publicKey": "{{reality.public}}",
	      "shortId": "{{shortId}}"}}
	}`); err != nil {
		t.Fatal(err)
	}

	u, err := st.CreateUser(store.User{Name: "mai", Enabled: true}, "sub-hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindUserProfile(u.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	// Assembly is what mints the per-user credential for this access point.
	if _, err := svc.AssembleNode(n.ID); err != nil {
		t.Fatal(err)
	}
	return svc, st, n, p
}

func TestProbeOutboundRendersARunnableClient(t *testing.T) {
	svc, _, n, _ := probeFixture(t)

	out, why := svc.ProbeOutbound(n.ID)
	if out == "" {
		t.Fatalf("no probe rendered: %s", why)
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(out), &probe); err != nil {
		t.Fatalf("the probe is not one JSON object: %v\n%s", err, out)
	}
	if probe["protocol"] != "vless" {
		t.Errorf("protocol = %v, want vless", probe["protocol"])
	}
	// The agent routes to it by name, so the tag is pinned rather than left to
	// whatever the template happened to say.
	if probe["tag"] != "probe-out" {
		t.Errorf("tag = %v, want probe-out", probe["tag"])
	}
	if strings.Contains(out, "{{") {
		t.Errorf("the probe still holds unrendered placeholders:\n%s", out)
	}
}

// The probe is shipped to a node and stored in the database. A template that
// reaches for a private key must fail loudly here rather than sealing one into
// a blob that travels — this is the same guard subscriptions get, and the probe
// is a second path out of the panel that needs it just as much.
func TestProbeOutboundRefusesToRenderASecret(t *testing.T) {
	svc, st, n, p := probeFixture(t)
	if err := st.PutClientTemplate(p.ID, "xray-json",
		`{"protocol":"vless","leak":"{{reality.private}}"}`); err != nil {
		t.Fatal(err)
	}
	out, why := svc.ProbeOutbound(n.ID)
	if out != "" {
		t.Fatalf("a template referencing a private key rendered anyway:\n%s", out)
	}
	if why == "" {
		t.Error("the refusal came with no explanation")
	}
}

// Every reason a node cannot be probed is a reason an operator needs in words.
// "Inconclusive" on its own sends somebody hunting; the fixes here are all
// one-liners once you know which one it is.
func TestEveryUnprobeableNodeExplainsItself(t *testing.T) {
	t.Run("no profile bound", func(t *testing.T) {
		svc, st, _ := newFixture(t)
		n, err := st.CreateNode("bare", "join-hash")
		if err != nil {
			t.Fatal(err)
		}
		out, why := svc.ProbeOutbound(n.ID)
		if out != "" || !strings.Contains(why, "no profile") {
			t.Fatalf("out=%q why=%q", out, why)
		}
	})

	t.Run("no xray-json template", func(t *testing.T) {
		svc, st, n, p := probeFixture(t)
		if err := st.DeleteClientTemplate(p.ID, "xray-json"); err != nil {
			t.Skipf("no way to remove a client template: %v", err)
		}
		out, why := svc.ProbeOutbound(n.ID)
		if out != "" || !strings.Contains(why, "xray-json") {
			t.Fatalf("out=%q why=%q", out, why)
		}
	})

	t.Run("no entitled user", func(t *testing.T) {
		svc, st, n, p := probeFixture(t)
		users, err := st.ListUsers()
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range users {
			if err := st.DeleteUser(u.ID); err != nil {
				t.Fatal(err)
			}
		}
		_ = p
		out, why := svc.ProbeOutbound(n.ID)
		if out != "" {
			t.Fatalf("rendered a probe with no users: %s", out)
		}
		if !strings.Contains(why, "no user") && !strings.Contains(why, "disabled") {
			t.Fatalf("why=%q", why)
		}
	})
}

// A disabled account is not a client that can get online, so testing as one
// would report a working node as broken.
func TestProbeSkipsDisabledUsers(t *testing.T) {
	svc, st, n, _ := probeFixture(t)
	users, err := st.ListUsers()
	if err != nil || len(users) == 0 {
		t.Fatalf("fixture has no users: %v", err)
	}
	u := users[0]
	u.Enabled = false
	if err := st.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	out, why := svc.ProbeOutbound(n.ID)
	if out != "" {
		t.Fatalf("rendered a probe as a disabled user:\n%s", out)
	}
	if !strings.Contains(why, "disabled") {
		t.Errorf("why = %q, want it to name the disabled account", why)
	}
}

// Apply stores the probe with the config version it was rendered alongside, so
// a reconnecting agent that gets version N gets N's probe and never a stale one.
func TestApplyStoresTheProbeWithItsConfigVersion(t *testing.T) {
	svc, st, n, _ := probeFixture(t)

	version, err := svc.Apply(t.Context(), n.ID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.ConfigAt(n.ID, version)
	if err != nil {
		t.Fatal(err)
	}
	if c.ProbeOutbound == "" {
		t.Fatal("the stored config version carries no probe")
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(c.ProbeOutbound), &probe); err != nil {
		t.Fatalf("the stored probe is not JSON: %v", err)
	}
	if probe["tag"] != "probe-out" {
		t.Errorf("stored probe tag = %v", probe["tag"])
	}
}

// CLAUDE.md §7: a config that fails validation is NEITHER stored NOR pushed.
// The hand-written route used to skip the check entirely, which made the rule
// true of the safe path and false of the dangerous one.
func TestAHandWrittenConfigMustPassTheSameGate(t *testing.T) {
	if xrayBin() == "" {
		t.Skip("no xray binary; validation cannot run")
	}
	svc, st, n, _ := probeFixture(t)

	before, _ := st.ConfigVersions(n.ID, 50)
	_, err := svc.ApplyRaw(t.Context(), n.ID, []byte(`{"inbounds":[{"protocol":"nonsense-protocol","port":443}]}`))
	if err == nil {
		t.Fatal("a config xray rejects was accepted")
	}
	after, _ := st.ConfigVersions(n.ID, 50)
	if len(after) != len(before) {
		t.Fatalf("a rejected config was stored anyway: %d versions before, %d after", len(before), len(after))
	}
}

// And the escape hatch still works for something valid, or it is not an escape
// hatch.
func TestAValidHandWrittenConfigIsStoredAndPushed(t *testing.T) {
	if xrayBin() == "" {
		t.Skip("no xray binary; validation cannot run")
	}
	svc, st, n, _ := probeFixture(t)

	version, err := svc.ApplyRaw(t.Context(), n.ID,
		[]byte(`{"inbounds":[],"outbounds":[{"protocol":"freedom","tag":"direct"}]}`))
	if err != nil {
		t.Fatalf("a valid config was refused: %v", err)
	}
	c, err := st.ConfigAt(n.ID, version)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Config, "freedom") {
		t.Errorf("stored config is not the one written: %s", c.Config)
	}
	// The probe comes from the profiles, not from the pasted bytes, so a
	// hand-configured node still has a canary that means something.
	if c.ProbeOutbound == "" {
		t.Error("a hand-written config left the node with no data-path probe")
	}
}
