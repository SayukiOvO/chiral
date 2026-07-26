package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

// The login flow is where a mistake is an authentication bypass rather than a
// bug, so these exercise the HTTP surface end to end rather than the pieces.

func mfaFixture(t *testing.T) (*Server, *store.Store, store.Admin) {
	t.Helper()
	box, _ := secret.NewBox("mfa-test-key-0123456789abcdef")
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	hash, err := auth.HashPassword("correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateAdmin("mai", hash, auth.RoleSuperadmin)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		st:     st,
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
	return srv, st, a
}

func post(t *testing.T, h http.HandlerFunc, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/", bytes.NewReader(raw))
	w := httptest.NewRecorder()
	h(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

// Without a second factor, a correct password is still a complete login.
func TestLoginWithoutMFAIssuesASession(t *testing.T) {
	srv, _, _ := mfaFixture(t)
	w, body := post(t, srv.login, map[string]string{"username": "mai", "password": "correct-horse"})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if body["token"] == nil || body["token"] == "" {
		t.Error("no session token")
	}
	if body["mfa_required"] != nil {
		t.Error("MFA was demanded from an account with no factors")
	}
}

// enrolTOTP adds a confirmed authenticator to an account.
func enrolTOTP(t *testing.T, st *store.Store, adminID string) string {
	t.Helper()
	sec, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutMFACredential(store.MFACredential{
		AdminID: adminID, Kind: store.MFATOTP, Name: "phone",
		Secret: sec, Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
	return sec
}

// The password alone must stop being enough the moment a factor is enrolled.
func TestPasswordAloneIsNotASessionOnceMFAExists(t *testing.T) {
	srv, st, a := mfaFixture(t)
	enrolTOTP(t, st, a.ID)

	w, body := post(t, srv.login, map[string]string{"username": "mai", "password": "correct-horse"})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if body["mfa_required"] != true {
		t.Fatal("a second factor was not demanded")
	}
	if body["token"] != nil {
		t.Fatal("a session token was issued before the second factor")
	}
	if body["challenge"] == nil || body["challenge"] == "" {
		t.Fatal("no challenge to spend")
	}
}

// An unconfirmed enrolment must not count: it would lock the account out.
func TestUnconfirmedFactorDoesNotDemandMFA(t *testing.T) {
	srv, st, a := mfaFixture(t)
	sec, _ := auth.NewTOTPSecret()
	st.PutMFACredential(store.MFACredential{
		AdminID: a.ID, Kind: store.MFATOTP, Name: "half-done", Secret: sec, Confirmed: false,
	})
	_, body := post(t, srv.login, map[string]string{"username": "mai", "password": "correct-horse"})
	if body["mfa_required"] == true {
		t.Error("an unfinished enrolment locked the account behind MFA")
	}
	if body["token"] == nil {
		t.Error("no session issued")
	}
}

func TestTOTPCompletesLogin(t *testing.T) {
	srv, st, a := mfaFixture(t)
	sec := enrolTOTP(t, st, a.ID)
	_, start := post(t, srv.login, map[string]string{"username": "mai", "password": "correct-horse"})
	challenge := start["challenge"].(string)

	code, _ := auth.TOTPCode(sec, time.Now())
	w, body := post(t, srv.verifyMFA, map[string]string{
		"challenge": challenge, "method": "totp", "code": code,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if body["token"] == nil || body["token"] == "" {
		t.Fatal("no session issued after a valid code")
	}
}

func TestWrongTOTPIsRefused(t *testing.T) {
	srv, st, a := mfaFixture(t)
	enrolTOTP(t, st, a.ID)
	_, start := post(t, srv.login, map[string]string{"username": "mai", "password": "correct-horse"})

	w, body := post(t, srv.verifyMFA, map[string]string{
		"challenge": start["challenge"].(string), "method": "totp", "code": "000000",
	})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", w.Code)
	}
	if body["token"] != nil {
		t.Fatal("a session was issued for a wrong code")
	}
}

// A challenge must be spendable exactly once, or it becomes a reusable
// password-equivalent.
func TestChallengeCannotBeReplayed(t *testing.T) {
	srv, st, a := mfaFixture(t)
	sec := enrolTOTP(t, st, a.ID)
	_, start := post(t, srv.login, map[string]string{"username": "mai", "password": "correct-horse"})
	challenge := start["challenge"].(string)
	code, _ := auth.TOTPCode(sec, time.Now())

	if w, _ := post(t, srv.verifyMFA, map[string]string{
		"challenge": challenge, "method": "totp", "code": code,
	}); w.Code != http.StatusOK {
		t.Fatalf("first use failed: %d", w.Code)
	}
	w, body := post(t, srv.verifyMFA, map[string]string{
		"challenge": challenge, "method": "totp", "code": code,
	})
	if w.Code == http.StatusOK || body["token"] != nil {
		t.Fatal("the challenge was accepted a second time")
	}
}

// Guessing must be bounded: six digits inside a five-minute window is
// otherwise walkable.
func TestChallengeBurnsAfterTooManyAttempts(t *testing.T) {
	srv, st, a := mfaFixture(t)
	sec := enrolTOTP(t, st, a.ID)
	_, start := post(t, srv.login, map[string]string{"username": "mai", "password": "correct-horse"})
	challenge := start["challenge"].(string)

	for i := 0; i < store.MaxChallengeAttempts; i++ {
		post(t, srv.verifyMFA, map[string]string{
			"challenge": challenge, "method": "totp", "code": "000000",
		})
	}
	// Even the CORRECT code must not work now.
	code, _ := auth.TOTPCode(sec, time.Now())
	w, body := post(t, srv.verifyMFA, map[string]string{
		"challenge": challenge, "method": "totp", "code": code,
	})
	if w.Code == http.StatusOK || body["token"] != nil {
		t.Fatal("a burned challenge still completed a login")
	}
}

func TestExpiredChallengeIsRefused(t *testing.T) {
	srv, st, a := mfaFixture(t)
	sec := enrolTOTP(t, st, a.ID)
	token, hash := auth.NewSecret()
	// Issued already expired.
	if err := st.CreateChallenge(hash, a.ID, store.PurposeLogin, "", -time.Minute); err != nil {
		t.Fatal(err)
	}
	code, _ := auth.TOTPCode(sec, time.Now())
	w, _ := post(t, srv.verifyMFA, map[string]string{
		"challenge": token, "method": "totp", "code": code,
	})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", w.Code)
	}
}

// A challenge issued for one purpose must never satisfy another, or a
// registration ceremony could be replayed as a login.
func TestChallengePurposesDoNotCross(t *testing.T) {
	srv, st, a := mfaFixture(t)
	sec := enrolTOTP(t, st, a.ID)
	token, hash := auth.NewSecret()
	if err := st.CreateChallenge(hash, a.ID, store.PurposeWebAuthnRegister, "", time.Minute); err != nil {
		t.Fatal(err)
	}
	code, _ := auth.TOTPCode(sec, time.Now())
	w, _ := post(t, srv.verifyMFA, map[string]string{
		"challenge": token, "method": "totp", "code": code,
	})
	if w.Code == http.StatusOK {
		t.Fatal("a registration challenge completed a login")
	}
}

// Another account's factor must not satisfy this login.
func TestFactorsDoNotCrossAccounts(t *testing.T) {
	srv, st, a := mfaFixture(t)
	enrolTOTP(t, st, a.ID)

	otherHash, _ := auth.HashPassword("x")
	other, err := st.CreateAdmin("someone-else", otherHash, auth.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	otherSecret := enrolTOTP(t, st, other.ID)

	_, start := post(t, srv.login, map[string]string{"username": "mai", "password": "correct-horse"})
	code, _ := auth.TOTPCode(otherSecret, time.Now())
	w, body := post(t, srv.verifyMFA, map[string]string{
		"challenge": start["challenge"].(string), "method": "totp", "code": code,
	})
	if w.Code == http.StatusOK || body["token"] != nil {
		t.Fatal("another account's authenticator completed this login")
	}
}

func TestRecoveryCodeCompletesLoginAndIsSingleUse(t *testing.T) {
	srv, st, a := mfaFixture(t)
	enrolTOTP(t, st, a.ID)
	codes, hashes, err := auth.NewRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceRecoveryCodes(a.ID, hashes); err != nil {
		t.Fatal(err)
	}

	_, start := post(t, srv.login, map[string]string{"username": "mai", "password": "correct-horse"})
	w, body := post(t, srv.verifyMFA, map[string]string{
		"challenge": start["challenge"].(string), "method": "recovery", "code": codes[0],
	})
	if w.Code != http.StatusOK || body["token"] == nil {
		t.Fatalf("a recovery code did not complete the login: %d %s", w.Code, w.Body)
	}

	// The same code must not work twice.
	_, start2 := post(t, srv.login, map[string]string{"username": "mai", "password": "correct-horse"})
	w2, _ := post(t, srv.verifyMFA, map[string]string{
		"challenge": start2["challenge"].(string), "method": "recovery", "code": codes[0],
	})
	if w2.Code == http.StatusOK {
		t.Fatal("a recovery code was accepted twice")
	}
}

// How it was typed carries no meaning.
func TestRecoveryCodeIgnoresFormatting(t *testing.T) {
	srv, st, a := mfaFixture(t)
	enrolTOTP(t, st, a.ID)
	codes, hashes, _ := auth.NewRecoveryCodes()
	st.ReplaceRecoveryCodes(a.ID, hashes)

	_, start := post(t, srv.login, map[string]string{"username": "mai", "password": "correct-horse"})
	messy := "  " + string([]rune(codes[0])[:5]) + " - " + string([]rune(codes[0])[6:]) + "  "
	w, _ := post(t, srv.verifyMFA, map[string]string{
		"challenge": start["challenge"].(string), "method": "recovery", "code": messy,
	})
	if w.Code != http.StatusOK {
		t.Errorf("a differently-formatted recovery code was refused: %d %s", w.Code, w.Body)
	}
}

// A disabled account must not be able to finish a login that started before
// it was disabled.
func TestDisablingAnAccountInvalidatesItsChallenge(t *testing.T) {
	srv, st, a := mfaFixture(t)
	sec := enrolTOTP(t, st, a.ID)
	_, start := post(t, srv.login, map[string]string{"username": "mai", "password": "correct-horse"})

	if err := st.UpdateAdminRole(a.ID, a.Role, true); err != nil {
		t.Fatal(err)
	}
	code, _ := auth.TOTPCode(sec, time.Now())
	w, _ := post(t, srv.verifyMFA, map[string]string{
		"challenge": start["challenge"].(string), "method": "totp", "code": code,
	})
	if w.Code == http.StatusOK {
		t.Fatal("a disabled account completed a login")
	}
}

// Without SMTP, email must be refused rather than silently appearing to work.
func TestEmailFactorNeedsSMTP(t *testing.T) {
	srv, st, a := mfaFixture(t)
	enrolTOTP(t, st, a.ID)
	st.SetAdminEmail(a.ID, "mai@example.com", true)
	_, start := post(t, srv.login, map[string]string{"username": "mai", "password": "correct-horse"})

	w, _ := post(t, srv.sendEmailCode, map[string]string{"challenge": start["challenge"].(string)})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503 when SMTP is unconfigured", w.Code)
	}
}

// Passkeys are refused rather than half-offered when the panel's URL cannot
// host them.
func TestPasskeyLoginNeedsAConfiguredService(t *testing.T) {
	srv, st, a := mfaFixture(t)
	enrolTOTP(t, st, a.ID)
	_, start := post(t, srv.login, map[string]string{"username": "mai", "password": "correct-horse"})
	w, _ := post(t, srv.beginPasskeyLogin, map[string]string{"challenge": start["challenge"].(string)})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", w.Code)
	}
}

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"mai@example.com": "ma***@example.com",
		"a@b.com":         "a***@b.com",
		"notanemail":      "***",
	}
	for in, want := range cases {
		if got := maskEmail(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
