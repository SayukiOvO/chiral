package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"testing"

	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"

	"github.com/SayukiOvO/chiral/core/internal/kernel"
	"github.com/SayukiOvO/chiral/core/internal/profile"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

// An egress rule is a routing rule an operator writes by hand, which means it
// is a rule an operator gets wrong: a mistyped geosite category, an IP range
// off by a prefix, a landing chosen before the profile it needs existed. For a
// while the only thing the console could do to an existing rule was enable it,
// disable it, or delete it — so the whole of "fix the typo" was "delete it and
// build it again", losing its position in a first-match-wins list. These tests
// hold the edit path open at the HTTP layer, where the console meets it.

type silentPush struct{}

func (silentPush) PushConfig(string, int64, []byte, []byte) error { return nil }
func (silentPush) SendUserOp(string, *chiralv1.UserOp) error      { return nil }

// egressFixture is portalFixture plus the assembly service the mutations call
// into. No Xray binary is resolved, so Apply pushes without pre-validation —
// what is under test here is the handler and the row it leaves behind.
func egressFixture(t *testing.T) (*Server, *store.Store, string) {
	t.Helper()
	srv, st := portalFixture(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv.logger = logger
	srv.profiles = profile.NewService(st, kernel.New("", template.Xray{}),
		silentPush{}, silentPush{}, user.NewService(st, logger), logger)
	return srv, st, seedAdmin(t, st)
}

func TestAnEgressRuleCanBeEditedInPlace(t *testing.T) {
	srv, st, admin := egressFixture(t)
	h := srv.Handler()
	node, err := st.CreateNode("hk-1", "hash-hk")
	if err != nil {
		t.Fatal(err)
	}

	w := do(t, h, "POST", "/api/nodes/"+node.ID+"/egress", admin, map[string]string{
		"label": "streaming", "domains": "geosite:netflx", "target_kind": "direct",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status %d, want 201: %s", w.Code, w.Body)
	}
	var created egressView
	json.Unmarshal(w.Body.Bytes(), &created)

	// The typo, corrected — the case the console could not express at all.
	w = do(t, h, "PUT", "/api/egress/"+created.ID, admin, map[string]any{
		"label": "streaming", "domains": "geosite:netflix", "ips": "geoip:netflix",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update: status %d, want 200: %s", w.Code, w.Body)
	}
	got, err := st.GetEgressRule(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Domains) != 1 || got.Domains[0] != "geosite:netflix" {
		t.Errorf("domains = %v, want the corrected category", got.Domains)
	}
	if len(got.IPs) != 1 || got.IPs[0] != "geoip:netflix" {
		t.Errorf("ips = %v, want the added one", got.IPs)
	}
}

// A rule left with nothing to match renders as an Xray rule carrying no
// destination condition, which matches every connection on the node. The
// create path has always refused that; the edit path is a second way in.
func TestAnEditCannotEmptyEveryMatch(t *testing.T) {
	srv, st, admin := egressFixture(t)
	h := srv.Handler()
	node, _ := st.CreateNode("hk-1", "hash-hk")

	w := do(t, h, "POST", "/api/nodes/"+node.ID+"/egress", admin, map[string]string{
		"label": "streaming", "domains": "geosite:netflix", "target_kind": "direct",
	})
	var created egressView
	json.Unmarshal(w.Body.Bytes(), &created)

	w = do(t, h, "PUT", "/api/egress/"+created.ID, admin, map[string]any{
		"domains": "", "ips": "",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 — an empty rule takes the whole node with it", w.Code)
	}
	got, _ := st.GetEgressRule(created.ID)
	if len(got.Domains) != 1 {
		t.Errorf("the refused edit still landed: domains = %v", got.Domains)
	}
}

// Moving the landing is the other half of editing, and the half that has a
// credential attached: the rule dials a fleet node under a secret minted for
// it. Moving away must not leave the rule holding one.
func TestMovingTheLandingToDirectDropsTheCredential(t *testing.T) {
	srv, st, admin := egressFixture(t)
	h := srv.Handler()
	entry, _ := st.CreateNode("hk-1", "hash-hk")
	exit, _ := st.CreateNode("jp-1", "hash-jp")
	prof, err := st.CreateProfile("reality-vision")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindProfileNode(prof.ID, exit.ID); err != nil {
		t.Fatal(err)
	}

	w := do(t, h, "POST", "/api/nodes/"+entry.ID+"/egress", admin, map[string]string{
		"label": "streaming", "domains": "geosite:netflix",
		"target_kind": "node", "target_node_id": exit.ID, "target_profile_id": prof.ID,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status %d, want 201: %s", w.Code, w.Body)
	}
	var created egressView
	json.Unmarshal(w.Body.Bytes(), &created)
	first, _ := st.GetEgressRule(created.ID)
	if first.Secret == "" {
		t.Fatal("a fleet landing was created without a dial credential")
	}

	// Same landing, different match: the credential must survive, or every
	// edit would force the far node to re-accept a new one.
	do(t, h, "PUT", "/api/egress/"+created.ID, admin, map[string]any{
		"domains": "geosite:disney", "target_kind": "node",
		"target_node_id": exit.ID, "target_profile_id": prof.ID,
	})
	same, _ := st.GetEgressRule(created.ID)
	if same.Secret != first.Secret {
		t.Error("the credential was rotated by an edit that did not move the landing")
	}

	w = do(t, h, "PUT", "/api/egress/"+created.ID, admin, map[string]any{
		"target_kind": "direct",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("move: status %d, want 200: %s", w.Code, w.Body)
	}
	moved, _ := st.GetEgressRule(created.ID)
	if moved.TargetKind != store.EgressDirect {
		t.Errorf("target_kind = %q, want direct", moved.TargetKind)
	}
	if moved.Secret != "" {
		t.Error("a direct landing kept the credential it dialled the exit with")
	}
	if moved.TargetNodeID != "" || moved.TargetProfileID != "" {
		t.Errorf("the former landing is still recorded: %+v", moved)
	}
}

// The loop check belongs to the landing, not to the create path — an edit is
// the easier way to build a cycle, because the second rule already exists.
func TestAnEditCannotPointARuleBackAtItsOwnNode(t *testing.T) {
	srv, st, admin := egressFixture(t)
	h := srv.Handler()
	a, _ := st.CreateNode("hk-1", "hash-hk")
	b, _ := st.CreateNode("jp-1", "hash-jp")
	prof, _ := st.CreateProfile("reality-vision")
	st.BindProfileNode(prof.ID, a.ID)
	st.BindProfileNode(prof.ID, b.ID)

	// a → b already exists.
	do(t, h, "POST", "/api/nodes/"+a.ID+"/egress", admin, map[string]string{
		"label": "to-jp", "domains": "geosite:netflix",
		"target_kind": "node", "target_node_id": b.ID, "target_profile_id": prof.ID,
	})
	// A rule on b, edited to land on a, closes the ring.
	w := do(t, h, "POST", "/api/nodes/"+b.ID+"/egress", admin, map[string]string{
		"label": "to-somewhere", "domains": "geosite:disney", "target_kind": "direct",
	})
	var onB egressView
	json.Unmarshal(w.Body.Bytes(), &onB)

	w = do(t, h, "PUT", "/api/egress/"+onB.ID, admin, map[string]any{
		"target_kind": "node", "target_node_id": a.ID, "target_profile_id": prof.ID,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 — the edit closes a routing loop", w.Code)
	}
	still, _ := st.GetEgressRule(onB.ID)
	if still.TargetKind != store.EgressDirect {
		t.Errorf("the refused move landed anyway: %+v", still)
	}
}
