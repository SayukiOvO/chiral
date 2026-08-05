package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/secret"
)

const rotateOldKey = "rotate-old-key-0123456789abcdef"
const rotateNewKey = "rotate-new-key-fedcba9876543210"

// Rotation has to move every sealed column, and reading the panel afterwards
// under the new key must produce exactly what it produced before. Anything
// missed becomes a row that can never be decrypted again.
func TestRotateSecretKeyMovesEverySealedValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rotate.db")
	oldBox, err := secret.NewBox(rotateOldKey)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, oldBox)
	if err != nil {
		t.Fatal(err)
	}

	// Seed one row in every sealed table.
	n, err := s.CreateNode("tokyo-1", "join-hash")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetConfigSkeleton(n.ID, `{"skeleton":true}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertConfig(n.ID, `{"rendered":"secret-inside"}`, ""); err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProfile("tokyo-reality")
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.PutVariable(Variable{
		Name: "reality", Scope: ScopeGlobal,
		Components: []Component{
			{Name: "private", Value: "PRIVATE-KEY-VALUE", Secret: true},
			{Name: "public", Value: "public-key-value"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUserWithToken(User{Name: "alice", Enabled: true}, "sub-token-raw", "sub-token-hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID,
		Email: "alice@p.n", Secret: "CREDENTIAL-UUID",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAlertTarget("telegram", "ops", "123:ABC:-100"); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAdmin("mai", "pw-hash", "superadmin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutMFACredential(MFACredential{
		AdminID: a.ID, Kind: MFATOTP, Name: "app", Secret: "TOTPSECRET", Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDevice(u.ID, "203.0.113.7", n.ID, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}

	newBox, err := secret.NewBox(rotateNewKey)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := s.RotateSecretKey(newBox)
	if err != nil {
		t.Fatalf("rotation failed: %v", err)
	}
	// Eight sealed columns seeded; the public variable component must NOT move.
	if moved < 8 {
		t.Fatalf("only %d values re-sealed; something was missed", moved)
	}

	// Reopen under the new key and read everything back.
	s.Close()
	rotated, err := Open(path, newBox)
	if err != nil {
		t.Fatal(err)
	}
	defer rotated.Close()

	gotNode, err := rotated.GetNode(n.ID)
	if err != nil || gotNode.ConfigSkeleton != `{"skeleton":true}` {
		t.Errorf("skeleton = %q, %v", gotNode.ConfigSkeleton, err)
	}
	cfg, err := rotated.ConfigAt(n.ID, 1)
	if err != nil || cfg.Config != `{"rendered":"secret-inside"}` {
		t.Errorf("config = %q, %v", cfg.Config, err)
	}
	vars, err := rotated.AllVariables()
	if err != nil {
		t.Fatal(err)
	}
	for _, gv := range vars {
		if gv.ID != v.ID {
			continue
		}
		for _, c := range gv.Components {
			switch c.Name {
			case "private":
				if c.Value != "PRIVATE-KEY-VALUE" {
					t.Errorf("secret component = %q", c.Value)
				}
			case "public":
				// The public one was never sealed and must still be readable
				// as itself, not as ciphertext.
				if c.Value != "public-key-value" {
					t.Errorf("public component = %q; rotation encrypted a public value", c.Value)
				}
			}
		}
	}
	cred, err := rotated.FindCredential(u.ID, p.ID, n.ID, "")
	if err != nil || cred.Secret != "CREDENTIAL-UUID" {
		t.Errorf("credential = %q, %v", cred.Secret, err)
	}
	tok, err := rotated.SubToken(u.ID)
	if err != nil || tok != "sub-token-raw" {
		t.Errorf("sub token = %q, %v", tok, err)
	}
	devices, err := rotated.UserDevices(u.ID)
	if err != nil || len(devices) != 1 || devices[0].IP != "203.0.113.7" {
		t.Errorf("devices = %v, %v", devices, err)
	}
	targets, err := rotated.ListAlertTargets()
	if err != nil || len(targets) != 1 {
		t.Fatalf("alert targets = %v, %v", targets, err)
	}
	// ListAlertTargets decrypts on the way out, so reading it back at all is
	// the assertion.
	if targets[0].Config != "123:ABC:-100" {
		t.Errorf("alert config = %q", targets[0].Config)
	}
	creds, err := rotated.ConfirmedMFACredentials(a.ID)
	if err != nil || len(creds) != 1 || creds[0].Secret != "TOTPSECRET" {
		t.Errorf("mfa secret = %v, %v", creds, err)
	}
}

// Rotating to no key at all would silently write every secret in the clear.
func TestRotateRefusesAnEmptyKey(t *testing.T) {
	s := testStore(t, rotateOldKey)
	plain, _ := secret.NewBox("")
	if _, err := s.RotateSecretKey(plain); err == nil {
		t.Fatal("rotating to an empty key was allowed")
	}
}

// Running it twice must not corrupt anything: the second pass finds everything
// already under the new key and leaves it alone.
func TestRotateIsRepeatable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rotate.db")
	oldBox, _ := secret.NewBox(rotateOldKey)
	s, err := Open(path, oldBox)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := s.CreateNode("tokyo-1", "h")
	if _, err := s.InsertConfig(n.ID, `{"a":1}`, ""); err != nil {
		t.Fatal(err)
	}
	newBox, _ := secret.NewBox(rotateNewKey)
	if _, err := s.RotateSecretKey(newBox); err != nil {
		t.Fatal(err)
	}
	s.Close()
	again, err := Open(path, newBox)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	moved, err := again.RotateSecretKey(newBox)
	if err != nil {
		t.Fatalf("second rotation failed: %v", err)
	}
	if moved != 0 {
		t.Errorf("second rotation moved %d values; it should find nothing to do", moved)
	}
	cfg, err := again.ConfigAt(n.ID, 1)
	if err != nil || cfg.Config != `{"a":1}` {
		t.Errorf("config after a repeat rotation = %q, %v", cfg.Config, err)
	}
}

// Every sealed column must be listed in sealedColumns(). This catches the
// failure mode that matters: adding a new encrypted column and forgetting the
// rotation, so a key change quietly orphans it.
func TestEverySealedColumnIsRotated(t *testing.T) {
	known := map[string]bool{}
	for _, c := range sealedColumns() {
		known[c.table+"."+c.column] = true
	}
	// Every call to box.Seal in this package writes to one of these.
	for _, want := range []string{
		"variable_components.value",
		"node_configs.config",
		"nodes.config_skeleton",
		"credentials.secret",
		"alert_targets.config",
		"mfa_credentials.secret",
		"user_devices.ip_enc",
		"users.sub_token_enc",
		"node_configs.probe_outbound",
	} {
		if !known[want] {
			t.Errorf("%s is sealed but not in sealedColumns(); a key rotation would orphan it", want)
		}
	}
	if len(known) != 9 {
		t.Errorf("sealedColumns() has %d entries; if you added one, extend this test too "+
			"(%v)", len(known), strings.Join(keysOf(known), ", "))
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
