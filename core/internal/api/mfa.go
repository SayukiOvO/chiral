package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/passkey"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

// Login is two steps once a second factor is enrolled: the password earns a
// short-lived challenge, and the challenge is spent on a factor to get a
// session.
//
// The challenge is a credential in its own right — anyone holding it has
// already passed the password — so it is issued, hashed and expired exactly
// like a session token, and it is bound to one purpose so a registration
// ceremony can never be replayed as a login.

// mfaMethod describes a factor to the login screen, without leaking anything
// useful to someone who only has the password.
type mfaMethod struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// startMFA issues the login challenge and describes what will satisfy it.
func (s *Server) startMFA(w http.ResponseWriter, a store.Admin, creds []store.MFACredential) {
	token, hash := auth.NewSecret()
	if err := s.st.CreateChallenge(hash, a.ID, store.PurposeLogin, "", store.LoginChallengeTTL); err != nil {
		s.internalErr(w, "create login challenge", err)
		return
	}
	methods := make([]mfaMethod, 0, len(creds))
	for _, c := range creds {
		methods = append(methods, mfaMethod{Kind: c.Kind, ID: c.ID, Name: c.Name})
	}
	recovery, err := s.st.CountUnusedRecoveryCodes(a.ID)
	if err != nil {
		s.logger.Error("counting recovery codes failed", "admin", a.Username, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mfa_required":  true,
		"challenge":     token,
		"expires_at":    time.Now().Add(store.LoginChallengeTTL).Unix(),
		"methods":       methods,
		"has_recovery":  recovery > 0,
		"passkey_ready": s.passkeys != nil,
	})
}

// issueSession completes a login, whether or not a second factor was involved.
func (s *Server) issueSession(w http.ResponseWriter, a store.Admin, how string) {
	token, hash := auth.NewSecret()
	if err := s.st.CreateSession(hash, a.ID, store.SessionTTL); err != nil {
		s.internalErr(w, "create session", err)
		return
	}
	if err := s.st.TouchAdminLogin(a.ID); err != nil {
		s.logger.Error("recording login time failed", "admin", a.Username, "err", err)
	}
	identity := auth.Identity{ID: a.ID, Name: a.Username, Role: a.Role}
	if err := s.st.Audit(identity, "login", "admin", a.ID, a.Username, how); err != nil {
		s.logger.Error("writing audit entry failed", "action", "login", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_at": time.Now().Add(store.SessionTTL).Unix(),
		"admin":      adminView(a),
	})
}

// spendChallenge resolves a login challenge to its admin. The challenge is
// consumed by the caller on success; on failure the attempt is counted, and an
// exhausted challenge is destroyed rather than left to be guessed at.
func (s *Server) spendChallenge(w http.ResponseWriter, token, purpose string) (store.Admin, string, bool) {
	if strings.TrimSpace(token) == "" {
		writeErr(w, http.StatusUnauthorized, "missing challenge")
		return store.Admin{}, "", false
	}
	hash := auth.HashSecret(token)
	ch, err := s.st.GetChallenge(hash, purpose)
	if err != nil {
		// Expired and never-existed are the same answer on purpose.
		writeErr(w, http.StatusUnauthorized, "challenge is invalid or has expired")
		return store.Admin{}, "", false
	}
	a, err := s.st.GetAdmin(ch.AdminID)
	if err != nil || a.Disabled {
		s.st.DeleteChallenge(hash)
		writeErr(w, http.StatusUnauthorized, "challenge is invalid or has expired")
		return store.Admin{}, "", false
	}
	return a, hash, true
}

// failChallenge counts a wrong answer and burns the challenge once the budget
// is gone, so a six-digit code cannot be walked through within its lifetime.
func (s *Server) failChallenge(w http.ResponseWriter, hash, msg string) {
	exhausted, err := s.st.BumpChallengeAttempts(hash)
	if err != nil {
		s.logger.Error("counting challenge attempts failed", "err", err)
	}
	if exhausted {
		s.st.DeleteChallenge(hash)
		writeErr(w, http.StatusUnauthorized, "too many attempts; start again")
		return
	}
	writeErr(w, http.StatusUnauthorized, msg)
}

// --- second step of login ---

// verifyMFA spends a login challenge with a TOTP code, an emailed code, or a
// recovery code. Passkeys go through the WebAuthn pair below, because they
// need a challenge of their own.
func (s *Server) verifyMFA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Challenge string `json:"challenge"`
		Method    string `json:"method"` // totp | email | recovery
		Code      string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON with challenge, method and code")
		return
	}
	a, hash, ok := s.spendChallenge(w, req.Challenge, store.PurposeLogin)
	if !ok {
		return
	}

	switch req.Method {
	case store.MFATOTP:
		creds, err := s.st.ConfirmedMFACredentials(a.ID)
		if err != nil {
			s.internalErr(w, "load factors", err)
			return
		}
		for _, c := range creds {
			if c.Kind != store.MFATOTP {
				continue
			}
			if auth.VerifyTOTP(c.Secret, req.Code, time.Now()) {
				s.st.TouchMFACredential(c.ID)
				s.st.DeleteChallenge(hash)
				s.issueSession(w, a, "totp")
				return
			}
		}
		s.failChallenge(w, hash, "that code is not valid")

	case "recovery":
		used, err := s.st.UseRecoveryCode(a.ID, auth.HashSecret(auth.NormalizeRecoveryCode(req.Code)))
		if err != nil {
			s.internalErr(w, "check recovery code", err)
			return
		}
		if !used {
			s.failChallenge(w, hash, "that recovery code is not valid or has been used")
			return
		}
		s.st.DeleteChallenge(hash)
		// Worth recording separately: a recovery code being spent usually
		// means someone lost a device, and the operator should see that.
		s.issueSession(w, a, "recovery-code")

	case store.MFAEmail:
		s.verifyEmailCode(w, a, req.Challenge, hash, req.Code)

	default:
		writeErr(w, http.StatusBadRequest, "method must be totp, email or recovery")
	}
}

// sendEmailCode mails a one-time code to a verified address, as the second
// step of a login already past the password.
func (s *Server) sendEmailCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Challenge string `json:"challenge"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON with a challenge")
		return
	}
	a, _, ok := s.spendChallenge(w, req.Challenge, store.PurposeLogin)
	if !ok {
		return
	}
	if !a.EmailVerified || a.Email == "" {
		writeErr(w, http.StatusBadRequest, "this account has no verified email address")
		return
	}
	if s.mailer == nil || !s.mailer.Enabled() {
		writeErr(w, http.StatusServiceUnavailable, "email is not configured on this panel")
		return
	}

	code, err := auth.NewNumericCode()
	if err != nil {
		s.internalErr(w, "generate code", err)
		return
	}
	// The code is stored against the login challenge itself, so it cannot be
	// replayed against a different login.
	if err := s.st.CreateChallenge(auth.HashSecret(req.Challenge+":email"), a.ID,
		store.PurposeEmailCode, auth.HashSecret(code), store.EmailCodeTTL); err != nil {
		s.internalErr(w, "store email code", err)
		return
	}
	body := fmt.Sprintf("Your Chiral sign-in code is %s.\n\n"+
		"It expires in %d minutes. If you did not try to sign in, someone has your password — change it.",
		code, int(store.EmailCodeTTL.Minutes()))
	if err := s.mailer.Send(a.Email, "Chiral sign-in code", body); err != nil {
		s.logger.Error("sending login code failed", "admin", a.Username, "err", err)
		writeErr(w, http.StatusBadGateway, "could not send the email; check the panel's SMTP settings")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent_to": maskEmail(a.Email)})
}

// verifyEmailCode checks a code mailed for THIS login. The emailed code is
// stored under a key derived from the login challenge, so a code issued for
// one login cannot be spent on another.
func (s *Server) verifyEmailCode(w http.ResponseWriter, a store.Admin, challenge, loginHash, code string) {
	emailHash := auth.HashSecret(challenge + ":email")
	ch, err := s.st.GetChallenge(emailHash, store.PurposeEmailCode)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "no code has been sent, or it has expired")
		return
	}
	if subtle.ConstantTimeCompare([]byte(ch.Data), []byte(auth.HashSecret(strings.TrimSpace(code)))) != 1 {
		// Count against the emailed code, not the login: six digits would
		// otherwise be walkable while the login challenge stayed fresh.
		exhausted, err := s.st.BumpChallengeAttempts(emailHash)
		if err != nil {
			s.logger.Error("counting email code attempts failed", "err", err)
		}
		if exhausted {
			s.st.DeleteChallenge(emailHash)
			writeErr(w, http.StatusUnauthorized, "too many attempts; request a new code")
			return
		}
		writeErr(w, http.StatusUnauthorized, "that code is not valid")
		return
	}
	s.st.DeleteChallenge(emailHash)
	s.st.DeleteChallenge(loginHash)
	s.issueSession(w, a, "email-code")
}

// maskEmail shows enough to recognise the address without disclosing it to
// someone who only has the password.
func maskEmail(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at <= 0 {
		return "***"
	}
	local, domain := addr[:at], addr[at+1:]
	if len(local) <= 2 {
		return local[:1] + "***@" + domain
	}
	return local[:2] + "***@" + domain
}

// --- passkey ceremonies ---

func (s *Server) passkeyUser(a store.Admin) (passkey.User, error) {
	creds, err := s.st.ConfirmedMFACredentials(a.ID)
	if err != nil {
		return passkey.User{}, err
	}
	u := passkey.User{ID: a.ID, Name: a.Username, DisplayName: a.Username}
	for _, c := range creds {
		if c.Kind != store.MFAPasskey {
			continue
		}
		cred, err := passkey.DecodeCredential(c.Secret)
		if err != nil {
			s.logger.Error("stored passkey is unreadable", "admin", a.Username, "id", c.ID, "err", err)
			continue
		}
		u.Creds = append(u.Creds, cred)
	}
	return u, nil
}

// beginPasskeyLogin turns a login challenge into a WebAuthn assertion.
func (s *Server) beginPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	if s.passkeys == nil {
		writeErr(w, http.StatusServiceUnavailable, "passkeys are not available on this panel")
		return
	}
	var req struct {
		Challenge string `json:"challenge"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON with a challenge")
		return
	}
	a, _, ok := s.spendChallenge(w, req.Challenge, store.PurposeLogin)
	if !ok {
		return
	}
	u, err := s.passkeyUser(a)
	if err != nil {
		s.internalErr(w, "load passkeys", err)
		return
	}
	if len(u.Creds) == 0 {
		writeErr(w, http.StatusBadRequest, "this account has no passkeys")
		return
	}
	options, session, err := s.passkeys.BeginLogin(u)
	if err != nil {
		s.internalErr(w, "begin passkey login", err)
		return
	}
	// Keyed off the login challenge, so an assertion cannot be moved to
	// another login.
	if err := s.st.CreateChallenge(auth.HashSecret(req.Challenge+":webauthn"), a.ID,
		store.PurposeWebAuthnLogin, string(session), store.WebAuthnChallengeTTL); err != nil {
		s.internalErr(w, "store webauthn challenge", err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(options)
}

// finishPasskeyLogin verifies the assertion and issues the session.
func (s *Server) finishPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	if s.passkeys == nil {
		writeErr(w, http.StatusServiceUnavailable, "passkeys are not available on this panel")
		return
	}
	var req struct {
		Challenge string          `json:"challenge"`
		Response  json.RawMessage `json:"response"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON with challenge and response")
		return
	}
	a, loginHash, ok := s.spendChallenge(w, req.Challenge, store.PurposeLogin)
	if !ok {
		return
	}
	waHash := auth.HashSecret(req.Challenge + ":webauthn")
	ch, err := s.st.GetChallenge(waHash, store.PurposeWebAuthnLogin)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "no passkey challenge is pending; start again")
		return
	}
	u, err := s.passkeyUser(a)
	if err != nil {
		s.internalErr(w, "load passkeys", err)
		return
	}
	cred, err := s.passkeys.FinishLogin(u, []byte(ch.Data), req.Response)
	if err != nil {
		s.st.DeleteChallenge(waHash)
		s.failChallenge(w, loginHash, "that passkey was not accepted")
		return
	}

	// The signature counter going backwards is the signal a credential has
	// been cloned. Record it and keep the new counter.
	if stored, err := s.st.FindPasskey(passkey.CredentialID(cred)); err == nil {
		if encoded, err := passkey.EncodeCredential(cred); err == nil {
			stored.Secret = encoded
			if _, err := s.st.PutMFACredential(stored); err != nil {
				s.logger.Error("updating passkey counter failed", "err", err)
			}
		}
		s.st.TouchMFACredential(stored.ID)
		if cred.Authenticator.CloneWarning {
			s.logger.Warn("passkey signature counter went backwards; the credential may be cloned",
				"admin", a.Username, "credential", stored.Name)
			s.st.Audit(auth.Identity{ID: a.ID, Name: a.Username, Role: a.Role},
				"passkey.clone_warning", "admin", a.ID, a.Username, stored.Name)
		}
	}

	s.st.DeleteChallenge(waHash)
	s.st.DeleteChallenge(loginHash)
	s.issueSession(w, a, "passkey")
}
