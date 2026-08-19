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

	"github.com/SayukiOvO/chiral/core/internal/kernel"
	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
	"github.com/SayukiOvO/chiral/core/internal/user"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

type fakePusher struct {
	pushed  map[string]int64
	failNow bool
	// userOps records the online add/remove frames Core sent, in order.
	userOps []*chiralv1.UserOp
	// offline nodes reject user ops, as a disconnected agent would.
	offline map[string]bool
}

func (f *fakePusher) SendUserOp(nodeID string, op *chiralv1.UserOp) error {
	if f.offline[nodeID] {
		return io.ErrClosedPipe
	}
	f.userOps = append(f.userOps, op)
	return nil
}

// opsFor returns the ops sent for one stats email.
func (f *fakePusher) opsFor(email string) []*chiralv1.UserOp {
	var out []*chiralv1.UserOp
	for _, op := range f.userOps {
		if op.GetEmail() == email {
			out = append(out, op)
		}
	}
	return out
}

func (f *fakePusher) PushConfig(nodeID string, version int64, _, _ []byte) error {
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
	return NewService(st, kernel.New("", template.Xray{Bin: xrayBin()}), push, push, user.NewService(st, logger), logger), st, push
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

// profileTags drops the management API inbound Core injects into every
// assembled config, so these tests can assert on what the profiles contributed.
func profileTags(t *testing.T, cfg []byte) []string {
	t.Helper()
	all, err := template.InboundTags(cfg)
	if err != nil {
		t.Fatal(err)
	}
	kept := []string{}
	for _, tag := range all {
		if tag != template.APIInboundTag {
			kept = append(kept, tag)
		}
	}
	return kept
}

func TestAssembleProducesValidRealityConfig(t *testing.T) {
	svc, st, _ := newFixture(t)
	_, n := realityProfile(t, svc, st)

	cfg, err := svc.AssembleNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	tags := profileTags(t, cfg)
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
	// One from the profile, plus the injected management API inbound.
	if len(pv.InboundTags) != 2 {
		t.Errorf("expected the profile inbound and the api inbound, got %v", pv.InboundTags)
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
	tags := profileTags(t, cfg)
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

// A client template had only node.name to work with, so every subscription
// named the box the way the operator does — which is normally the provider and
// the datacentre. The customer-facing name exists so that does not happen, and
// it was reachable from the portal and from nowhere else.
func TestDisplayNameIsAvailableToTemplates(t *testing.T) {
	svc, st, _ := newFixture(t)
	n, err := st.CreateNode("tokyo-provider-a", "hash-display")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateNode(n.ID, n.Name, "日本 · 东京 01", ""); err != nil {
		t.Fatal(err)
	}
	ctx, err := svc.contextFor("", n.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctx.Render("{{node.display_name}}")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got != "日本 · 东京 01" {
		t.Fatalf("display name = %q", got)
	}
}

// Unset must not fall back to the internal name: an operator who has not
// filled it in should publish an anonymous line, not their hosting
// arrangement.
func TestUnnamedNodeGetsANumberNotTheInternalName(t *testing.T) {
	svc, st, _ := newFixture(t)
	n, err := st.CreateNode("hetzner-fsn1-07", "hash-unnamed")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := svc.contextFor("", n.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctx.Render("{{node.display_name}}")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, n.Name) {
		t.Fatalf("leaked the internal name %q as %q", n.Name, got)
	}
	if got != "线路 01" {
		t.Fatalf("display name = %q, want a numbered line", got)
	}
}

// Re-scoping a variable must leave every rendered byte where it was.
//
// The scenario this guards: an operator creates the REALITY keypair global,
// later decides (correctly) that it belongs to one node, and moves it. If the
// move re-generated or re-sealed anything, the rendered public key would
// differ and every subscription holding the old one would stop handshaking —
// exactly the outage the move exists to avoid.
func TestMovingAVariableDoesNotChangeWhatRenders(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, n := realityProfile(t, svc, st)
	entitle(t, st, "alice", p.ID, nil)
	// realityProfile put `reality` and `shortId` at profile scope; the
	// server config and the client context both resolve them.
	if err := st.PutClientTemplate(p.ID, "xray-json",
		`{"protocol":"vless","settings":{"vnext":[{"address":"{{node.address}}","port":{{port}},"users":[{"id":"{{user.uuid}}"}]}]},`+
			`"streamSettings":{"security":"reality","realitySettings":{"publicKey":"{{reality.public}}","shortId":"{{shortId}}","serverName":"{{sni}}"}}}`); err != nil {
		t.Fatal(err)
	}
	before, err := svc.AssembleNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	clientBefore, err := svc.ClientContext(p.ID, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	pubBefore, err := clientBefore.Render("{{reality.public}}")
	if err != nil {
		t.Fatal(err)
	}

	// Move the keypair and the shortId down to the node.
	for _, name := range []string{"reality", "shortId"} {
		id, err := st.FindVariableID(store.ScopeProfile, p.ID, "", name)
		if err != nil {
			t.Fatalf("finding %s: %v", name, err)
		}
		if err := st.MoveVariable(id, store.ScopeNode, sql.NullString{}, sql.NullString{String: n.ID, Valid: true}); err != nil {
			t.Fatalf("moving %s: %v", name, err)
		}
	}

	after, err := svc.AssembleNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("the node config changed across a scope move:\n--- before\n%s\n--- after\n%s", before, after)
	}
	clientAfter, err := svc.ClientContext(p.ID, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	pubAfter, err := clientAfter.Render("{{reality.public}}")
	if err != nil {
		t.Fatal(err)
	}
	if pubBefore == "" || pubBefore != pubAfter {
		t.Fatalf("the public key clients hold changed across the move: %q -> %q", pubBefore, pubAfter)
	}
	// And it is really gone from the profile scope — the move moved, it did
	// not copy.
	if _, err := st.FindVariableID(store.ScopeProfile, p.ID, "", "reality"); err == nil {
		t.Fatal("reality is still at profile scope after the move")
	}
}

// The name must stay unique within a scope; a move that would create a twin
// is refused rather than leaving the render to pick one at random.
func TestMovingOntoAnExistingNameIsRefused(t *testing.T) {
	svc, st, _ := newFixture(t)
	p, n := realityProfile(t, svc, st)
	nid := sql.NullString{String: n.ID, Valid: true}
	if _, err := st.PutVariable(store.Variable{
		Name: "sni", Scope: store.ScopeNode, NodeID: nid,
		Components: []store.Component{{Value: "node.example"}},
	}); err != nil {
		t.Fatal(err)
	}
	id, err := st.FindVariableID(store.ScopeProfile, p.ID, "", "sni")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MoveVariable(id, store.ScopeNode, sql.NullString{}, nid); err == nil {
		t.Fatal("moving sni onto a node that already has one was allowed")
	}
	_ = svc
}
