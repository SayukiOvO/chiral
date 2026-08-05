package template

import (
	"encoding/json"
	"strings"
	"testing"
)

func srcCtx(vars map[string]string, secrets ...string) *Context {
	return NewContext(vars, secrets)
}

// profileTags drops the management API inbound Core injects, so a test can
// assert on what the profiles contributed without restating that contract.
func profileTags(t *testing.T, out []byte) []string {
	t.Helper()
	all, err := InboundTags(out)
	if err != nil {
		t.Fatal(err)
	}
	kept := []string{}
	for _, tag := range all {
		if tag != APIInboundTag {
			kept = append(kept, tag)
		}
	}
	return kept
}

func TestAssembleAppendsRenderedInbounds(t *testing.T) {
	out, err := AssembleNode(DefaultSkeleton, []InboundSource{
		{
			ProfileName: "p1",
			Template:    `{"tag":"{{tag}}","port":{{port}},"protocol":"vless"}`,
			Ctx:         srcCtx(map[string]string{"tag": "reality-in", "port": "443"}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tags := profileTags(t, out)
	if len(tags) != 1 || tags[0] != "reality-in" {
		t.Fatalf("got tags %v", tags)
	}
	// The skeleton's other sections must survive assembly.
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg["outbounds"]; !ok {
		t.Error("skeleton outbounds were dropped")
	}
	if _, ok := cfg["log"]; !ok {
		t.Error("skeleton log was dropped")
	}
}

func TestAssemblePreservesManualInbounds(t *testing.T) {
	skeleton := `{"log":{"loglevel":"warning"},
	  "inbounds":[{"tag":"manual","port":10800,"protocol":"socks"}],
	  "outbounds":[{"protocol":"freedom"}]}`
	out, err := AssembleNode(skeleton, []InboundSource{
		{ProfileName: "p", Template: `{"tag":"from-profile","port":443,"protocol":"vless"}`, Ctx: srcCtx(nil)},
	})
	if err != nil {
		t.Fatal(err)
	}
	tags := profileTags(t, out)
	if len(tags) != 2 || tags[0] != "manual" || tags[1] != "from-profile" {
		t.Errorf("expected the manual inbound kept and the profile one appended, got %v", tags)
	}
}

func TestAssembleMultipleProfiles(t *testing.T) {
	out, err := AssembleNode(DefaultSkeleton, []InboundSource{
		{ProfileName: "a", Template: `{"tag":"a","port":443,"protocol":"vless"}`, Ctx: srcCtx(nil)},
		{ProfileName: "b", Template: `{"tag":"b","port":8443,"protocol":"vless"}`, Ctx: srcCtx(nil)},
	})
	if err != nil {
		t.Fatal(err)
	}
	tags := profileTags(t, out)
	if len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Errorf("got %v", tags)
	}
}

func TestAssembleEmptySkeletonUsesDefault(t *testing.T) {
	out, err := AssembleNode("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out) {
		t.Fatal("default skeleton did not produce valid JSON")
	}
	var cfg map[string]json.RawMessage
	json.Unmarshal(out, &cfg)
	if _, ok := cfg["outbounds"]; !ok {
		t.Error("default skeleton should carry an outbound")
	}
}

// A template that renders to broken JSON must fail loudly at assembly, not
// produce a corrupt config that fails later with an opaque message.
func TestAssembleRejectsNonObjectRender(t *testing.T) {
	_, err := AssembleNode(DefaultSkeleton, []InboundSource{
		{ProfileName: "broken", Template: `{"tag": }`, Ctx: srcCtx(nil)},
	})
	if err == nil {
		t.Fatal("expected malformed rendered inbound to be rejected")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error should name the profile, got: %v", err)
	}
}

func TestAssembleReportsUndefinedVariableWithProfileName(t *testing.T) {
	_, err := AssembleNode(DefaultSkeleton, []InboundSource{
		{ProfileName: "tokyo-reality", Template: `{"tag":"{{missing}}"}`, Ctx: srcCtx(nil)},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "tokyo-reality") || !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should name both the profile and the variable, got: %v", err)
	}
}

func TestAssembleRejectsBadSkeleton(t *testing.T) {
	if _, err := AssembleNode(`not json`, nil); err == nil {
		t.Fatal("expected a bad skeleton to be rejected")
	}
	if _, err := AssembleNode(`{"inbounds": "not-an-array"}`, nil); err == nil {
		t.Fatal("expected a non-array inbounds to be rejected")
	}
}

// The server side legitimately uses secret variables; only client templates
// are restricted.
func TestAssembleAllowsSecretsInServerTemplate(t *testing.T) {
	out, err := AssembleNode(DefaultSkeleton, []InboundSource{
		{
			ProfileName: "p",
			Template:    `{"tag":"t","key":"{{reality.private}}"}`,
			Ctx:         srcCtx(map[string]string{"reality.private": "PRIV"}, "reality.private"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "PRIV") {
		t.Error("server-side render should include the private key")
	}
}

// Regression: JSON `null` unmarshals into a nil map without error, and
// assigning to a nil map panics. A stored `null` skeleton would have taken
// down every later assembly for that node.
func TestAssembleRejectsNullSkeleton(t *testing.T) {
	out, err := AssembleNode(`null`, []InboundSource{
		{ProfileName: "p", Template: `{"tag":"t"}`, Ctx: srcCtx(nil)},
	})
	if err == nil {
		t.Fatal("expected a null skeleton to be rejected, not panic")
	}
	if out != nil {
		t.Error("expected no output on error")
	}
	if !strings.Contains(err.Error(), "null") {
		t.Errorf("error should name the cause, got: %v", err)
	}
}

func TestAssembleRejectsNonObjectSkeletons(t *testing.T) {
	for _, skeleton := range []string{`null`, `[]`, `"a string"`, `42`, `true`} {
		if _, err := AssembleNode(skeleton, nil); err == nil {
			t.Errorf("skeleton %q should be rejected", skeleton)
		}
	}
}

// A relayed exit puts three things on the node and they have to agree: the
// provider as an outbound, the credentials that leave through it, and a rule
// tying the two together. Any one of them missing is a config that loads and
// sends the traffic somewhere else.
func TestARelayedExitBecomesAnOutboundAndARule(t *testing.T) {
	skeleton := `{"inbounds":[],"outbounds":[{"protocol":"freedom","tag":"direct"}],
		"routing":{"rules":[{"type":"field","network":"tcp,udp","outboundTag":"direct"}]}}`
	out, err := AssembleNodeWithExits(skeleton, nil, []ExitSource{{
		Tag:      "exit-abc",
		Outbound: `{"tag":"exit-abc","protocol":"vless","settings":{}}`,
		Emails:   []string{"a@p.n.abc", "b@p.n.abc"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
		Routing struct {
			Rules []struct {
				User        []string `json:"user"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, o := range cfg.Outbounds {
		tags = append(tags, o.Tag)
	}
	if len(tags) != 2 || tags[1] != "exit-abc" {
		t.Fatalf("outbounds = %v, want the operator's plus the exit", tags)
	}
	// Ahead of the operator's own catch-all, behind the management API.
	// Routing is first-match: "everything to direct" is the shape of every
	// skeleton in the wild and would swallow the relayed traffic, sending it
	// out of this node instead — which succeeds, and is wrong, and looks
	// identical to working.
	at := map[string]int{}
	for i, r := range cfg.Routing.Rules {
		at[r.OutboundTag] = i
		if r.OutboundTag == "exit-abc" && len(r.User) != 2 {
			t.Fatalf("the exit rule does not carry both credentials: %+v", r)
		}
	}
	for _, tag := range []string{"api", "exit-abc", "direct"} {
		if _, ok := at[tag]; !ok {
			t.Fatalf("no rule for %q: %+v", tag, cfg.Routing.Rules)
		}
	}
	if !(at["api"] < at["exit-abc"] && at["exit-abc"] < at["direct"]) {
		t.Fatalf("rule order is api < exit < operator's, got %v", at)
	}
}

// An exit nobody may use still gets its outbound, so the operator can see it is
// configured — but no rule. A rule with an empty user list matches EVERY user,
// which would send the whole node out through a provider nobody was granted.
func TestAnExitWithNoUsersGetsNoRule(t *testing.T) {
	out, err := AssembleNodeWithExits(DefaultSkeleton, nil, []ExitSource{{
		Tag: "exit-abc", Outbound: `{"tag":"exit-abc","protocol":"vless","settings":{}}`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Routing struct {
			Rules []struct {
				OutboundTag string `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, r := range cfg.Routing.Rules {
		if r.OutboundTag == "exit-abc" {
			t.Fatal("an exit with no permitted users got a rule matching everyone")
		}
	}
}
