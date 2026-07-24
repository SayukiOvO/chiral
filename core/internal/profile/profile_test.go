package profile

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
)

type fakePusher struct {
	pushed  map[string]int64
	failNow bool
}

func (f *fakePusher) PushConfig(nodeID string, version int64, _ []byte) error {
	if f.failNow {
		return io.ErrClosedPipe
	}
	if f.pushed == nil {
		f.pushed = map[string]int64{}
	}
	f.pushed[nodeID] = version
	return nil
}

func xrayBin() string {
	if p := os.Getenv("CHIRAL_XRAY_BIN"); p != "" {
		return p
	}
	if p, err := exec.LookPath("xray"); err == nil {
		return p
	}
	return ""
}

func newFixture(t *testing.T) (*Service, *store.Store, *fakePusher) {
	t.Helper()
	box, err := secret.NewBox("profile-test-key-0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	push := &fakePusher{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewService(st, template.Xray{Bin: xrayBin()}, push, logger), st, push
}

// realityProfile wires up a profile whose inbound is a genuine VLESS+REALITY
// inbound, with the key material coming from the generator.
func realityProfile(t *testing.T, svc *Service, st *store.Store) (store.Profile, store.Node) {
	t.Helper()
	p, err := st.CreateProfile("tokyo-reality")
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.CreateNode("tokyo-1", "join-hash")
	if err != nil {
		t.Fatal(err)
	}
	pid := sql.NullString{String: p.ID, Valid: true}

	if _, err := svc.GenerateVariable("reality", store.ScopeProfile, pid, sql.NullString{}, template.GenX25519); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GenerateVariable("shortId", store.ScopeProfile, pid, sql.NullString{}, template.GenShortID); err != nil {
		t.Fatal(err)
	}
	for name, val := range map[string]string{"port": "443", "sni": "www.microsoft.com"} {
		if _, err := st.PutVariable(store.Variable{
			Name: name, Scope: store.ScopeProfile, ProfileID: pid,
			Components: []store.Component{{Value: val}},
		}); err != nil {
			t.Fatal(err)
		}
	}

	p.InboundTemplate = `{
	  "tag": "reality-in",
	  "listen": "0.0.0.0",
	  "port": {{port}},
	  "protocol": "vless",
	  "settings": { "clients": [], "decryption": "none" },
	  "streamSettings": {
	    "network": "tcp",
	    "security": "reality",
	    "realitySettings": {
	      "target": "{{sni}}:443",
	      "serverNames": ["{{sni}}"],
	      "privateKey": "{{reality.private}}",
	      "shortIds": ["{{shortId}}"]
	    }
	  }
	}`
	p.ClientEntry = `{"id":"{{user.uuid}}","email":"{{user.email}}"}`
	if err := st.UpdateProfile(p); err != nil {
		t.Fatal(err)
	}
	if err := st.BindProfileNode(p.ID, n.ID); err != nil {
		t.Fatal(err)
	}
	return p, n
}

func TestAssembleProducesValidRealityConfig(t *testing.T) {
	svc, st, _ := newFixture(t)
	_, n := realityProfile(t, svc, st)

	cfg, err := svc.AssembleNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	tags, err := template.InboundTags(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0] != "reality-in" {
		t.Fatalf("unexpected inbounds: %v", tags)
	}
	// The generated private key must have been substituted, not left as a
	// placeholder.
	if strings.Contains(string(cfg), "{{") {
		t.Errorf("config still contains unrendered placeholders:\n%s", cfg)
	}

	if xrayBin() == "" {
		t.Skip("no xray binary; skipping validation of the assembled config")
	}
	if err := (template.Xray{Bin: xrayBin()}).TestConfig(context.Background(), cfg); err != nil {
		t.Errorf("assembled config rejected by xray -test: %v\n%s", err, cfg)
	}
}

func TestApplyStoresAndPushes(t *testing.T) {
	svc, st, push := newFixture(t)
	_, n := realityProfile(t, svc, st)

	version, err := svc.Apply(context.Background(), n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Errorf("expected version 1, got %d", version)
	}
	if push.pushed[n.ID] != 1 {
		t.Errorf("config was not pushed: %v", push.pushed)
	}
	stored, err := st.LatestConfig(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != 1 || !strings.Contains(stored.Config, "reality-in") {
		t.Errorf("stored config looks wrong: %+v", stored.Version)
	}
}

// A node that cannot be reached must still keep the new version, so heartbeat
// reconciliation can deliver it later.
func TestApplyStoresEvenWhenPushFails(t *testing.T) {
	svc, st, push := newFixture(t)
	_, n := realityProfile(t, svc, st)
	push.failNow = true

	version, err := svc.Apply(context.Background(), n.ID)
	if err != nil {
		t.Fatalf("a failed push should not fail Apply: %v", err)
	}
	stored, err := st.LatestConfig(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != version {
		t.Error("version was not persisted despite the push failing")
	}
}

// The whole point of validating panel-side: a broken render must never be
// stored or pushed.
func TestApplyRefusesInvalidConfig(t *testing.T) {
	if xrayBin() == "" {
		t.Skip("needs an xray binary to validate")
	}
	svc, st, push := newFixture(t)
	p, n := realityProfile(t, svc, st)

	p.InboundTemplate = strings.Replace(p.InboundTemplate,
		`"privateKey": "{{reality.private}}"`, `"privateKey": "not-a-valid-key"`, 1)
	if err := st.UpdateProfile(p); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Apply(context.Background(), n.ID); err == nil {
		t.Fatal("expected apply to refuse a config xray rejects")
	}
	if len(push.pushed) != 0 {
		t.Error("an invalid config was pushed")
	}
	if _, err := st.LatestConfig(n.ID); err == nil {
		t.Error("an invalid config was stored")
	}
}

func TestPreviewReportsTestOutcome(t *testing.T) {
	svc, st, _ := newFixture(t)
	_, n := realityProfile(t, svc, st)

	pv, err := svc.Preview(context.Background(), n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.InboundTags) != 1 {
		t.Errorf("expected one inbound, got %v", pv.InboundTags)
	}
	if xrayBin() != "" {
		if !pv.Tested {
			t.Error("preview should report that it validated")
		}
		if pv.TestError != "" {
			t.Errorf("valid config reported an error: %s", pv.TestError)
		}
	}
}

func TestNodeMetadataIsAvailableToTemplates(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, n := realityProfile(t, svc, st)
	p.InboundTemplate = `{"tag":"{{node.name}}","port":{{port}},"protocol":"vless"}`
	if err := st.UpdateProfile(p); err != nil {
		t.Fatal(err)
	}
	cfg, err := svc.AssembleNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	tags, _ := template.InboundTags(cfg)
	if len(tags) != 1 || tags[0] != "tokyo-1" {
		t.Errorf("node metadata not exposed: %v", tags)
	}
}

func TestGenerateVariableMarksSecrets(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, _ := st.CreateProfile("p")
	v, err := svc.GenerateVariable("reality", store.ScopeProfile,
		sql.NullString{String: p.ID, Valid: true}, sql.NullString{}, template.GenX25519)
	if err != nil {
		t.Fatal(err)
	}
	var sawSecretPrivate, sawPublicPlain bool
	for _, c := range v.Components {
		if c.Name == "private" && c.Secret {
			sawSecretPrivate = true
		}
		if c.Name == "public" && !c.Secret {
			sawPublicPlain = true
		}
	}
	if !sawSecretPrivate || !sawPublicPlain {
		t.Errorf("secrecy not carried from the generator: %+v", v.Components)
	}
}

func TestAssembleFailsOnProfileWithoutTemplate(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, _ := st.CreateProfile("empty")
	n, _ := st.CreateNode("n", "h")
	if err := st.BindProfileNode(p.ID, n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AssembleNode(n.ID); err == nil {
		t.Fatal("expected assembling with an empty profile template to fail")
	}
}
