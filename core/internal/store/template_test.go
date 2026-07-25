package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/secret"
)

func testStore(t *testing.T, key string) *Store {
	t.Helper()
	box, err := secret.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(t.TempDir(), "test.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

const storeTestKey = "store-test-key-0123456789abcdef"

func TestVariableRoundTripWithSecret(t *testing.T) {
	s := testStore(t, storeTestKey)
	p, err := s.CreateProfile("tokyo-reality")
	if err != nil {
		t.Fatal(err)
	}
	in := Variable{
		Name:      "reality",
		Scope:     ScopeProfile,
		ProfileID: sql.NullString{String: p.ID, Valid: true},
		Generator: "x25519",
		Components: []Component{
			{Name: "private", Value: "PRIVATE-HALF", Secret: true},
			{Name: "public", Value: "PUBLIC-HALF"},
		},
	}
	saved, err := s.PutVariable(in)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.GetVariable(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Component{}
	for _, c := range got.Components {
		byName[c.Name] = c
	}
	if byName["private"].Value != "PRIVATE-HALF" || !byName["private"].Secret {
		t.Errorf("secret component did not round-trip: %+v", byName["private"])
	}
	if byName["public"].Value != "PUBLIC-HALF" || byName["public"].Secret {
		t.Errorf("public component did not round-trip: %+v", byName["public"])
	}
}

// The point of encryption at rest: the raw database must not contain the key.
func TestSecretComponentIsCiphertextOnDisk(t *testing.T) {
	s := testStore(t, storeTestKey)
	v, err := s.PutVariable(Variable{
		Name:       "reality",
		Scope:      ScopeGlobal,
		Components: []Component{{Name: "private", Value: "SUPER-SECRET-KEY", Secret: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.db.QueryRow(
		`SELECT value FROM variable_components WHERE variable_id = ? AND component = 'private'`, v.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "SUPER-SECRET-KEY") {
		t.Fatal("secret is stored in plaintext")
	}
	if !secret.IsEncrypted(stored) {
		t.Errorf("stored value is not marked encrypted: %q", stored)
	}
}

func TestWrongSecretKeyIsReportedNotSilentlyWrong(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	box1, _ := secret.NewBox(storeTestKey)
	s1, err := Open(path, box1)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s1.PutVariable(Variable{
		Name:       "reality",
		Scope:      ScopeGlobal,
		Components: []Component{{Name: "private", Value: "V", Secret: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s1.Close()

	box2, _ := secret.NewBox("a-totally-different-key-9876543210")
	s2, err := Open(path, box2)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err := s2.GetVariable(v.ID); err == nil {
		t.Fatal("expected an error reading with the wrong key, not a silent wrong value")
	}
}

func TestPutVariableReplacesComponents(t *testing.T) {
	s := testStore(t, storeTestKey)
	v, err := s.PutVariable(Variable{
		Name:       "grp",
		Scope:      ScopeGlobal,
		Components: []Component{{Name: "a", Value: "1"}, {Name: "b", Value: "2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	v.Components = []Component{{Name: "a", Value: "updated"}}
	if _, err := s.PutVariable(v); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetVariable(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Components) != 1 || got.Components[0].Value != "updated" {
		t.Errorf("components were not replaced: %+v", got.Components)
	}
}

func TestVariableNameUniquePerScope(t *testing.T) {
	s := testStore(t, storeTestKey)
	if _, err := s.PutVariable(Variable{Name: "port", Scope: ScopeGlobal,
		Components: []Component{{Value: "443"}}}); err != nil {
		t.Fatal(err)
	}
	_, err := s.PutVariable(Variable{Name: "port", Scope: ScopeGlobal,
		Components: []Component{{Value: "8443"}}})
	if err == nil {
		t.Fatal("expected a duplicate global variable name to be rejected")
	}
}

// Same name in different scopes is fine — that is what shadowing means.
func TestSameNameInDifferentScopes(t *testing.T) {
	s := testStore(t, storeTestKey)
	n, err := s.CreateNode("tokyo-1", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutVariable(Variable{Name: "port", Scope: ScopeGlobal,
		Components: []Component{{Value: "443"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutVariable(Variable{Name: "port", Scope: ScopeNode,
		NodeID:     sql.NullString{String: n.ID, Valid: true},
		Components: []Component{{Value: "8443"}}}); err != nil {
		t.Fatalf("node-scoped variable with the same name should be allowed: %v", err)
	}
}

func TestDeletingNodeCascadesToItsVariables(t *testing.T) {
	s := testStore(t, storeTestKey)
	n, err := s.CreateNode("tokyo-1", "hash")
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.PutVariable(Variable{Name: "address", Scope: ScopeNode,
		NodeID:     sql.NullString{String: n.ID, Valid: true},
		Components: []Component{{Value: "203.0.113.9"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNode(n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetVariable(v.ID); err == nil {
		t.Error("node-scoped variable outlived its node")
	}
	var orphans int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM variable_components WHERE variable_id = ?`, v.ID).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Errorf("%d orphaned components left behind", orphans)
	}
}

func TestDeletingProfileCascades(t *testing.T) {
	s := testStore(t, storeTestKey)
	p, _ := s.CreateProfile("p")
	n, _ := s.CreateNode("n", "h")
	if _, err := s.PutVariable(Variable{Name: "sni", Scope: ScopeProfile,
		ProfileID:  sql.NullString{String: p.ID, Valid: true},
		Components: []Component{{Value: "example.com"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutClientTemplate(p.ID, "xray-json", "{}"); err != nil {
		t.Fatal(err)
	}
	if err := s.BindProfileNode(p.ID, n.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProfile(p.ID); err != nil {
		t.Fatal(err)
	}
	vs, err := s.ProfileVariables(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Errorf("profile variables outlived the profile: %d", len(vs))
	}
	ids, err := s.ProfileNodeIDs(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("bindings outlived the profile: %v", ids)
	}
}

func TestScopeCheckRejectsMismatchedOwner(t *testing.T) {
	s := testStore(t, storeTestKey)
	p, _ := s.CreateProfile("p")
	// Global scope must not carry an owner.
	_, err := s.PutVariable(Variable{Name: "bad", Scope: ScopeGlobal,
		ProfileID:  sql.NullString{String: p.ID, Valid: true},
		Components: []Component{{Value: "x"}}})
	if err == nil {
		t.Error("a global variable with a profile owner should be rejected by the schema")
	}
}

func TestProfileBindingsRoundTrip(t *testing.T) {
	s := testStore(t, storeTestKey)
	p, _ := s.CreateProfile("p")
	a, _ := s.CreateNode("a", "h1")
	b, _ := s.CreateNode("b", "h2")
	for _, id := range []string{a.ID, b.ID} {
		if err := s.BindProfileNode(p.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	// Binding twice must not error or duplicate.
	if err := s.BindProfileNode(p.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	ids, err := s.ProfileNodeIDs(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 bound nodes, got %v", ids)
	}
	back, err := s.NodeProfileIDs(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || back[0] != p.ID {
		t.Errorf("reverse lookup wrong: %v", back)
	}
	if err := s.UnbindProfileNode(p.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	ids, _ = s.ProfileNodeIDs(p.ID)
	if len(ids) != 1 || ids[0] != b.ID {
		t.Errorf("unbind wrong: %v", ids)
	}
}

func TestProfileUpdateAndClientTemplates(t *testing.T) {
	s := testStore(t, storeTestKey)
	p, _ := s.CreateProfile("p")
	p.InboundTemplate = `{"tag":"{{tag}}"}`
	p.ClientEntry = `{"id":"{{user.uuid}}"}`
	if err := s.UpdateProfile(p); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProfile(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.InboundTemplate != p.InboundTemplate || got.ClientEntry != p.ClientEntry {
		t.Errorf("profile did not round-trip: %+v", got)
	}
	if err := s.PutClientTemplate(p.ID, "clash", "yaml-v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutClientTemplate(p.ID, "clash", "yaml-v2"); err != nil {
		t.Fatal(err)
	}
	tmpls, err := s.ClientTemplates(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tmpls["clash"] != "yaml-v2" {
		t.Errorf("client template not replaced: %v", tmpls)
	}
}

// --- regressions from the M2 adversarial review ---

// The whole at-rest threat model turns on this: a rendered config contains the
// very private keys variable_components encrypts, so storing it in the clear
// would hand every node's key material to anyone holding the database file.
func TestRenderedConfigIsCiphertextOnDisk(t *testing.T) {
	s := testStore(t, storeTestKey)
	n, _ := s.CreateNode("tokyo-1", "hash")
	const rendered = `{"inbounds":[{"privateKey":"REALITY-PRIVATE-KEY-HERE"}]}`
	c, err := s.InsertConfig(n.ID, rendered)
	if err != nil {
		t.Fatal(err)
	}

	var raw string
	if err := s.db.QueryRow(`SELECT config FROM node_configs WHERE node_id = ? AND version = ?`,
		n.ID, c.Version).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "REALITY-PRIVATE-KEY-HERE") {
		t.Fatal("rendered config is stored in plaintext; a leaked database exposes every node's keys")
	}
	if !secret.IsEncrypted(raw) {
		t.Errorf("stored config is not encrypted: %q", raw)
	}

	got, err := s.LatestConfig(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config != rendered {
		t.Errorf("config did not round-trip: %q", got.Config)
	}
	pushable, err := s.LatestPushableConfig(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pushable.Config != rendered {
		t.Errorf("pushable config did not round-trip: %q", pushable.Config)
	}
}

// A skeleton can hold credentials of its own (an outbound to an upstream
// proxy), so it gets the same treatment.
func TestSkeletonIsCiphertextOnDisk(t *testing.T) {
	s := testStore(t, storeTestKey)
	n, _ := s.CreateNode("tokyo-1", "hash")
	const skeleton = `{"outbounds":[{"password":"UPSTREAM-PROXY-PASSWORD"}]}`
	if err := s.SetConfigSkeleton(n.ID, skeleton); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.db.QueryRow(`SELECT config_skeleton FROM nodes WHERE id = ?`, n.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "UPSTREAM-PROXY-PASSWORD") {
		t.Fatal("skeleton is stored in plaintext")
	}
	got, err := s.GetNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigSkeleton != skeleton {
		t.Errorf("skeleton did not round-trip: %q", got.ConfigSkeleton)
	}
}

// A config sealed for one version must not open under another: the AAD binds
// each ciphertext to its own row.
func TestConfigCiphertextIsBoundToItsVersion(t *testing.T) {
	s := testStore(t, storeTestKey)
	n, _ := s.CreateNode("n", "h")
	if _, err := s.InsertConfig(n.ID, `{"v":1}`); err != nil {
		t.Fatal(err)
	}
	c2, err := s.InsertConfig(n.ID, `{"v":2}`)
	if err != nil {
		t.Fatal(err)
	}
	var v1 string
	if err := s.db.QueryRow(`SELECT config FROM node_configs WHERE node_id=? AND version=1`, n.ID).Scan(&v1); err != nil {
		t.Fatal(err)
	}
	// Move v1's ciphertext onto v2's row, as an attacker with write access would.
	if _, err := s.db.Exec(`UPDATE node_configs SET config=? WHERE node_id=? AND version=?`, v1, n.ID, c2.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LatestConfig(n.ID); err == nil {
		t.Error("a relocated config ciphertext opened successfully")
	}
}

// Enabling encryption on an existing panel must not break stored configs.
func TestPlaintextConfigStillReadableAfterEnablingEncryption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	plain, _ := secret.NewBox("")
	s1, err := Open(path, plain)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := s1.CreateNode("n", "h")
	if _, err := s1.InsertConfig(n.ID, `{"legacy":true}`); err != nil {
		t.Fatal(err)
	}
	if err := s1.SetConfigSkeleton(n.ID, `{"legacy":"skeleton"}`); err != nil {
		t.Fatal(err)
	}
	s1.Close()

	box, _ := secret.NewBox(storeTestKey)
	s2, err := Open(path, box)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.LatestConfig(n.ID)
	if err != nil {
		t.Fatalf("previously-plaintext config became unreadable: %v", err)
	}
	if got.Config != `{"legacy":true}` {
		t.Errorf("got %q", got.Config)
	}
	node, err := s2.GetNode(n.ID)
	if err != nil {
		t.Fatalf("previously-plaintext skeleton became unreadable: %v", err)
	}
	if node.ConfigSkeleton != `{"legacy":"skeleton"}` {
		t.Errorf("got %q", node.ConfigSkeleton)
	}
}

// The management UI lists the whole pool; a scope-only default would silently
// hide every profile- and node-scoped variable.
func TestAllVariablesSpansEveryScope(t *testing.T) {
	s := testStore(t, storeTestKey)
	p, _ := s.CreateProfile("p")
	n, _ := s.CreateNode("n", "h")
	mk := func(name, scope, profileID, nodeID string) {
		t.Helper()
		if _, err := s.PutVariable(Variable{
			Name: name, Scope: scope,
			ProfileID:  nullStr(profileID),
			NodeID:     nullStr(nodeID),
			Components: []Component{{Value: "v"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("g", ScopeGlobal, "", "")
	mk("p1", ScopeProfile, p.ID, "")
	mk("n1", ScopeNode, "", n.ID)

	all, err := s.AllVariables()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected all three scopes, got %d: %+v", len(all), all)
	}
	if all[0].Scope != ScopeGlobal {
		t.Errorf("broadest scope should sort first, got %q", all[0].Scope)
	}
}

func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
