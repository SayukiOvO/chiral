package template

import (
	"encoding/json"
	"strings"
	"testing"
)

func srcCtx(vars map[string]string, secrets ...string) *Context {
	return NewContext(vars, secrets)
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
	tags, err := InboundTags(out)
	if err != nil {
		t.Fatal(err)
	}
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
	tags, _ := InboundTags(out)
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
	tags, _ := InboundTags(out)
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
