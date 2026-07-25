package template

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func assemble(t *testing.T, skeleton string, sources []InboundSource) []byte {
	t.Helper()
	out, err := AssembleNode(skeleton, sources)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Without the api block, online user management and traffic stats do not fail
// loudly — they simply never work. Core injects it so an operator cannot
// forget it.
func TestAssemblyInjectsTheManagementAPI(t *testing.T) {
	out := assemble(t, DefaultSkeleton, nil)
	var cfg struct {
		API struct {
			Tag      string   `json:"tag"`
			Services []string `json:"services"`
		} `json:"api"`
		Stats  *map[string]any `json:"stats"`
		Policy struct {
			Levels map[string]struct {
				StatsUserUplink   bool `json:"statsUserUplink"`
				StatsUserDownlink bool `json:"statsUserDownlink"`
			} `json:"levels"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.API.Tag != APIHandlerTag {
		t.Errorf("api tag = %q", cfg.API.Tag)
	}
	if strings.Join(cfg.API.Services, ",") != "HandlerService,StatsService" {
		t.Errorf("api services = %v; both are required (users and stats)", cfg.API.Services)
	}
	if cfg.Stats == nil {
		t.Error("stats block missing; counters stay off without it")
	}
	// Per-user counters only exist if the policy level asks for them.
	if lvl, ok := cfg.Policy.Levels["0"]; !ok || !lvl.StatsUserUplink || !lvl.StatsUserDownlink {
		t.Errorf("level 0 must enable per-user stats, got %+v", cfg.Policy.Levels)
	}
}

func TestAPIInboundIsLoopbackOnly(t *testing.T) {
	out := assemble(t, DefaultSkeleton, nil)
	var cfg struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Listen   string `json:"listen"`
			Protocol string `json:"protocol"`
		} `json:"inbounds"`
	}
	json.Unmarshal(out, &cfg)
	for _, in := range cfg.Inbounds {
		if in.Tag != APIInboundTag {
			continue
		}
		// This endpoint has no authentication of its own.
		if in.Listen != "127.0.0.1" {
			t.Errorf("api inbound listens on %q; it must be loopback-only", in.Listen)
		}
		if in.Protocol != "dokodemo-door" {
			t.Errorf("api inbound protocol = %q", in.Protocol)
		}
		return
	}
	t.Fatal("no api inbound was injected")
}

// Routing is first-match: an operator's catch-all would otherwise swallow API
// traffic and break user management.
func TestAPIRouteComesFirst(t *testing.T) {
	skeleton := `{"log":{"loglevel":"warning"},
	  "outbounds":[{"protocol":"freedom","tag":"direct"}],
	  "routing":{"rules":[{"type":"field","network":"tcp,udp","outboundTag":"direct"}]}}`
	out := assemble(t, skeleton, nil)
	var cfg struct {
		Routing struct {
			Rules []struct {
				InboundTag  []string `json:"inboundTag"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	json.Unmarshal(out, &cfg)
	if len(cfg.Routing.Rules) != 2 {
		t.Fatalf("expected the operator's rule kept and ours added, got %d", len(cfg.Routing.Rules))
	}
	first := cfg.Routing.Rules[0]
	if first.OutboundTag != APIHandlerTag || len(first.InboundTag) != 1 || first.InboundTag[0] != APIInboundTag {
		t.Errorf("api rule is not first: %+v", cfg.Routing.Rules)
	}
	if cfg.Routing.Rules[1].OutboundTag != "direct" {
		t.Error("the operator's own rule was dropped")
	}
}

// An operator who wrote their own api block meant it; we must not fight them.
func TestExistingAPIBlockIsLeftAlone(t *testing.T) {
	skeleton := `{"log":{"loglevel":"warning"},
	  "api":{"tag":"myapi","services":["HandlerService"]},
	  "inbounds":[{"tag":"my-api-in","listen":"127.0.0.1","port":9999,"protocol":"dokodemo-door","settings":{"address":"127.0.0.1"}}],
	  "outbounds":[{"protocol":"freedom","tag":"direct"}],
	  "routing":{"rules":[{"type":"field","inboundTag":["my-api-in"],"outboundTag":"myapi"}]}}`
	out := assemble(t, skeleton, nil)
	var cfg struct {
		API struct {
			Tag string `json:"tag"`
		} `json:"api"`
		Inbounds []struct {
			Tag string `json:"tag"`
		} `json:"inbounds"`
	}
	json.Unmarshal(out, &cfg)
	if cfg.API.Tag != "myapi" {
		t.Errorf("operator's api tag was overwritten: %q", cfg.API.Tag)
	}
	for _, in := range cfg.Inbounds {
		if in.Tag == APIInboundTag {
			t.Error("injected a second api inbound alongside the operator's")
		}
	}
}

// The agent locates the endpoint this way rather than assuming APIPort.
func TestAPIAddress(t *testing.T) {
	out := assemble(t, DefaultSkeleton, nil)
	if got := APIAddress(out); got != "127.0.0.1:10085" {
		t.Errorf("APIAddress = %q", got)
	}

	custom := `{"api":{"tag":"myapi","services":["HandlerService"]},
	  "inbounds":[{"tag":"my-api-in","listen":"127.0.0.1","port":9999,"protocol":"dokodemo-door","settings":{"address":"127.0.0.1"}}],
	  "outbounds":[{"protocol":"freedom"}],
	  "routing":{"rules":[{"type":"field","inboundTag":["my-api-in"],"outboundTag":"myapi"}]}}`
	if got := APIAddress([]byte(custom)); got != "127.0.0.1:9999" {
		t.Errorf("operator's own api endpoint not found: %q", got)
	}

	if got := APIAddress([]byte(`{"inbounds":[]}`)); got != "" {
		t.Errorf("a config with no api should report no address, got %q", got)
	}
	if got := APIAddress([]byte(`not json`)); got != "" {
		t.Errorf("malformed config should report no address, got %q", got)
	}
}

// --- client splicing ---

func TestClientsAreSplicedIntoTheInbound(t *testing.T) {
	out := assemble(t, DefaultSkeleton, []InboundSource{{
		ProfileName: "p",
		Template:    `{"tag":"in","protocol":"vless","settings":{"clients":[],"decryption":"none"}}`,
		Ctx:         srcCtx(nil),
		Clients: []string{
			`{"id":"uuid-alice","email":"alice@p-n"}`,
			`{"id":"uuid-bob","email":"bob@p-n"}`,
		},
	}})
	emails := clientEmails(t, out, "in")
	if len(emails) != 2 || emails[0] != "alice@p-n" || emails[1] != "bob@p-n" {
		t.Errorf("got %v", emails)
	}
}

// A template author may pin a client by hand; assembly must not drop it.
func TestHandWrittenClientsArePreserved(t *testing.T) {
	out := assemble(t, DefaultSkeleton, []InboundSource{{
		ProfileName: "p",
		Template:    `{"tag":"in","protocol":"vless","settings":{"clients":[{"id":"manual","email":"ops@fixed"}]}}`,
		Ctx:         srcCtx(nil),
		Clients:     []string{`{"id":"uuid-alice","email":"alice@p-n"}`},
	}})
	emails := clientEmails(t, out, "in")
	if len(emails) != 2 || emails[0] != "ops@fixed" || emails[1] != "alice@p-n" {
		t.Errorf("got %v", emails)
	}
}

func TestNoClientsLeavesTheInboundUntouched(t *testing.T) {
	out := assemble(t, DefaultSkeleton, []InboundSource{{
		ProfileName: "p",
		Template:    `{"tag":"in","protocol":"vless","settings":{"clients":[],"decryption":"none"}}`,
		Ctx:         srcCtx(nil),
	}})
	if emails := clientEmails(t, out, "in"); len(emails) != 0 {
		t.Errorf("expected no clients, got %v", emails)
	}
}

func TestMalformedClientEntryIsRejected(t *testing.T) {
	_, err := AssembleNode(DefaultSkeleton, []InboundSource{{
		ProfileName: "tokyo-reality",
		Template:    `{"tag":"in","settings":{}}`,
		Ctx:         srcCtx(nil),
		Clients:     []string{`{"id": }`},
	}})
	if err == nil {
		t.Fatal("expected a malformed client entry to be rejected")
	}
	if !strings.Contains(err.Error(), "tokyo-reality") {
		t.Errorf("error should name the profile, got: %v", err)
	}
}

func clientEmails(t *testing.T, configJSON []byte, tag string) []string {
	t.Helper()
	var cfg struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Settings struct {
				Clients []struct {
					Email string `json:"email"`
				} `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, in := range cfg.Inbounds {
		if in.Tag != tag {
			continue
		}
		out := []string{}
		for _, c := range in.Settings.Clients {
			out = append(out, c.Email)
		}
		return out
	}
	t.Fatalf("no inbound tagged %q", tag)
	return nil
}

// The whole point is a config Xray accepts: assemble one with users and the
// injected API block, and let the real binary judge it.
func TestAssembledConfigWithUsersPassesXrayTest(t *testing.T) {
	x := testXray(t)
	out := assemble(t, DefaultSkeleton, []InboundSource{{
		ProfileName: "tokyo-reality",
		Template: `{"tag":"vless-in","listen":"0.0.0.0","port":{{port}},"protocol":"vless",
		  "settings":{"clients":[],"decryption":"none"}}`,
		Ctx: srcCtx(map[string]string{"port": "18443"}),
		Clients: []string{
			`{"id":"8673288c-253e-4a63-bfc0-2828a6203a97","email":"alice@tokyo-reality-tokyo-1"}`,
		},
	}})
	if err := x.TestConfig(context.Background(), out); err != nil {
		t.Fatalf("assembled config rejected by xray: %v\n%s", err, out)
	}
}
