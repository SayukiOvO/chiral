package auth

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("correct horse battery staple", hash) {
		t.Error("the right password was rejected")
	}
	if VerifyPassword("wrong", hash) {
		t.Error("a wrong password was accepted")
	}
}

// The stored form must not contain the password, and must carry its own
// parameters so they can be raised later without invalidating what exists.
func TestHashFormat(t *testing.T) {
	hash, err := HashPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, "hunter2") {
		t.Fatal("the password appears in its own hash")
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2" {
		t.Fatalf("unexpected encoding: %q", hash)
	}
	if parts[1] != "210000" {
		t.Errorf("iterations not recorded in the hash: %q", parts[1])
	}
}

// Same password, different salt: two identical passwords must not produce
// identical rows, or the database reveals who shares one.
func TestHashesAreSalted(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Error("two hashes of the same password are identical; the salt is not working")
	}
	if !VerifyPassword("same", a) || !VerifyPassword("same", b) {
		t.Error("salted hashes do not verify")
	}
}

// A corrupted or foreign hash must fail closed, not error in a way that could
// be told apart from a wrong password.
func TestMalformedHashesFailClosed(t *testing.T) {
	for _, h := range []string{
		"", "garbage", "pbkdf2$", "pbkdf2$abc$x$y", "pbkdf2$1000$!!!$y",
		"pbkdf2$1000$aGVsbG8$!!!", "bcrypt$10$salt$hash", "$$$",
		"pbkdf2$0$aGVsbG8$aGVsbG8", "pbkdf2$-5$aGVsbG8$aGVsbG8",
	} {
		if VerifyPassword("anything", h) {
			t.Errorf("malformed hash was accepted: %q", h)
		}
	}
}

// An empty password must not verify against a hash of a real one.
func TestEmptyPasswordDoesNotMatch(t *testing.T) {
	hash, _ := HashPassword("real")
	if VerifyPassword("", hash) {
		t.Error("empty password accepted")
	}
}

// Iterations travel with the hash, so raising the cost later must not
// invalidate passwords hashed under the old one.
func TestOlderIterationCountStillVerifies(t *testing.T) {
	salt := []byte("sixteen-byte-slt")
	const cheap = 1000
	key, err := pbkdf2.Key(sha256.New, "legacy", salt, cheap, keyLen)
	if err != nil {
		t.Fatal(err)
	}
	old := fmt.Sprintf("pbkdf2$%d$%s$%s", cheap,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))

	if !VerifyPassword("legacy", old) {
		t.Error("a hash written at a lower cost no longer verifies")
	}
	if VerifyPassword("wrong", old) {
		t.Error("a wrong password verified against the legacy hash")
	}
}

// --- roles ---

func TestRoleHierarchy(t *testing.T) {
	super := Identity{Role: RoleSuperadmin}
	op := Identity{Role: RoleOperator}
	viewer := Identity{Role: RoleViewer}

	if !super.CanAdmin() || !super.CanWrite() {
		t.Error("superadmin should be able to do everything")
	}
	if op.CanAdmin() {
		t.Error("an operator must not manage admins")
	}
	if !op.CanWrite() {
		t.Error("an operator should be able to write")
	}
	if viewer.CanWrite() || viewer.CanAdmin() {
		t.Error("a viewer must not write")
	}
	if !viewer.Can(RoleViewer) {
		t.Error("a viewer should pass a viewer check")
	}
}

// An unknown or empty role must grant nothing — a row with a corrupted role
// should fail closed rather than open.
func TestUnknownRoleGrantsNothing(t *testing.T) {
	for _, r := range []Role{"", "root", "admin", "SUPERADMIN"} {
		id := Identity{Role: r}
		if id.Can(RoleViewer) || id.CanWrite() || id.CanAdmin() {
			t.Errorf("role %q was granted something", r)
		}
	}
}

func TestValidRole(t *testing.T) {
	for _, r := range Roles() {
		if !ValidRole(r) {
			t.Errorf("%q should be valid", r)
		}
	}
	for _, r := range []Role{"", "root", "Superadmin"} {
		if ValidRole(r) {
			t.Errorf("%q should be rejected", r)
		}
	}
}

// The environment token is a superadmin, but must be identifiable as such in
// the audit trail rather than looking like a named person.
func TestEnvTokenIdentityIsLabelled(t *testing.T) {
	id := EnvTokenIdentity()
	if !id.CanAdmin() {
		t.Error("the env token should have full rights")
	}
	if !id.ViaToken {
		t.Error("the env token must be marked as such")
	}
	if id.Name == "" {
		t.Error("the env token needs a name for the audit trail")
	}
	if id.ID != "" {
		t.Error("the env token has no account behind it, so it should carry no id")
	}
}
