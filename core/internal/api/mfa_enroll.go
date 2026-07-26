package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/passkey"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

// Enrolment always ends with a proof. A factor is created unconfirmed and only
// counts once possession has been demonstrated — otherwise a mistyped secret
// or an abandoned ceremony would lock the account out at the next login.
//
// Every route here acts on the caller's OWN account. A superadmin managing
// someone else's factors would be a way to take over their account, so the
// only cross-account operation is deleting the whole admin.

type mfaView struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Confirmed  bool   `json:"confirmed"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
}

func (s *Server) listMFA(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r.Context())
	creds, err := s.st.MFACredentials(id.ID)
	if err != nil {
		s.internalErr(w, "load factors", err)
		return
	}
	views := make([]mfaView, 0, len(creds))
	for _, c := range creds {
		views = append(views, mfaView{
			ID: c.ID, Kind: c.Kind, Name: c.Name, Confirmed: c.Confirmed,
			CreatedAt: c.CreatedAt, LastUsedAt: c.LastUsedAt,
		})
	}
	remaining, err := s.st.CountUnusedRecoveryCodes(id.ID)
	if err != nil {
		s.logger.Error("counting recovery codes failed", "err", err)
	}
	email, verified, err := s.st.AdminEmail(id.ID)
	if err != nil {
		s.internalErr(w, "load email", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"factors":        views,
		"recovery_left":  remaining,
		"email":          email,
		"email_verified": verified,
		"passkey_ready":  s.passkeys != nil,
		"email_ready":    s.mailer != nil && s.mailer.Enabled(),
		"passkey_rp_id":  passkeyRPID(s),
	})
}

func passkeyRPID(s *Server) string {
	if s.passkeys == nil {
		return ""
	}
	return s.passkeys.RPID()
}

func (s *Server) deleteMFA(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r.Context())
	if err := s.st.DeleteMFACredential(id.ID, r.PathValue("id")); err != nil {
		s.notFoundOr(w, "delete factor", err, "no such factor on this account")
		return
	}
	s.audit(r, "mfa.delete", "admin", id.ID, id.Name, r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

// --- TOTP ---

// beginTOTP mints an unconfirmed factor and returns the secret to scan. It is
// not usable until confirmTOTP proves the app is generating matching codes.
func (s *Server) beginTOTP(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r.Context())
	var req struct {
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Authenticator app"
	}

	secret, err := auth.NewTOTPSecret()
	if err != nil {
		s.internalErr(w, "generate secret", err)
		return
	}
	cred, err := s.st.PutMFACredential(store.MFACredential{
		AdminID: id.ID, Kind: store.MFATOTP, Name: name, Secret: secret,
	})
	if err != nil {
		s.internalErr(w, "store factor", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":     cred.ID,
		"secret": secret,
		"uri":    auth.TOTPURI("Chiral", id.Name, secret),
	})
}

func (s *Server) confirmTOTP(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r.Context())
	var req struct {
		ID   string `json:"id"`
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON with id and code")
		return
	}
	cred, err := s.st.GetMFACredential(req.ID)
	if err != nil || cred.AdminID != id.ID || cred.Kind != store.MFATOTP {
		writeErr(w, http.StatusNotFound, "no such enrolment on this account")
		return
	}
	if !auth.VerifyTOTP(cred.Secret, req.Code, time.Now()) {
		writeErr(w, http.StatusBadRequest, "that code does not match; check the phone's clock")
		return
	}
	cred.Confirmed = true
	if _, err := s.st.PutMFACredential(cred); err != nil {
		s.internalErr(w, "confirm factor", err)
		return
	}
	s.audit(r, "mfa.enroll", "admin", id.ID, id.Name, "totp")
	s.afterFirstFactor(w, r, id.ID, cred.Name)
}

// --- passkeys ---

func (s *Server) beginPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	if s.passkeys == nil {
		writeErr(w, http.StatusServiceUnavailable,
			"passkeys need the panel served over https (or localhost); check CHIRAL_PUBLIC_URL")
		return
	}
	id := identityFrom(r.Context())
	a, err := s.st.GetAdmin(id.ID)
	if err != nil {
		s.internalErr(w, "load admin", err)
		return
	}
	u, err := s.passkeyUser(a)
	if err != nil {
		s.internalErr(w, "load passkeys", err)
		return
	}
	options, session, err := s.passkeys.BeginRegistration(u)
	if err != nil {
		s.internalErr(w, "begin passkey registration", err)
		return
	}
	token, hash := auth.NewSecret()
	if err := s.st.CreateChallenge(hash, id.ID, store.PurposeWebAuthnRegister,
		string(session), store.WebAuthnChallengeTTL); err != nil {
		s.internalErr(w, "store challenge", err)
		return
	}
	// The options go to the browser as-is; the token comes back with the
	// response so the challenge can be found again.
	writeJSON(w, http.StatusOK, map[string]any{
		"challenge": token,
		"options":   json.RawMessage(options),
	})
}

func (s *Server) finishPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	if s.passkeys == nil {
		writeErr(w, http.StatusServiceUnavailable, "passkeys are not available on this panel")
		return
	}
	id := identityFrom(r.Context())
	var req struct {
		Challenge string          `json:"challenge"`
		Name      string          `json:"name"`
		Response  json.RawMessage `json:"response"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON with challenge, name and response")
		return
	}
	hash := auth.HashSecret(req.Challenge)
	ch, err := s.st.GetChallenge(hash, store.PurposeWebAuthnRegister)
	if err != nil || ch.AdminID != id.ID {
		writeErr(w, http.StatusUnauthorized, "challenge is invalid or has expired")
		return
	}
	a, err := s.st.GetAdmin(id.ID)
	if err != nil {
		s.internalErr(w, "load admin", err)
		return
	}
	u, err := s.passkeyUser(a)
	if err != nil {
		s.internalErr(w, "load passkeys", err)
		return
	}
	cred, err := s.passkeys.FinishRegistration(u, []byte(ch.Data), req.Response)
	if err != nil {
		s.st.DeleteChallenge(hash)
		writeErr(w, http.StatusBadRequest, "that passkey was not accepted: "+err.Error())
		return
	}
	s.st.DeleteChallenge(hash)

	encoded, err := passkey.EncodeCredential(cred)
	if err != nil {
		s.internalErr(w, "encode credential", err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Passkey"
	}
	// Confirmed on creation: the ceremony IS the proof of possession, unlike
	// TOTP where the secret is copied by hand.
	if _, err := s.st.PutMFACredential(store.MFACredential{
		AdminID: id.ID, Kind: store.MFAPasskey, Name: name,
		Secret: encoded, CredentialID: passkey.CredentialID(cred), Confirmed: true,
	}); err != nil {
		s.internalErr(w, "store passkey", err)
		return
	}
	s.audit(r, "mfa.enroll", "admin", id.ID, id.Name, "passkey:"+name)
	s.afterFirstFactor(w, r, id.ID, name)
}

// --- email ---

func (s *Server) sendEmailVerification(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r.Context())
	if s.mailer == nil || !s.mailer.Enabled() {
		writeErr(w, http.StatusServiceUnavailable,
			"email is not configured; set CHIRAL_SMTP_HOST and CHIRAL_SMTP_FROM")
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON with an email")
		return
	}
	addr := strings.TrimSpace(req.Email)
	if _, err := mail.ParseAddress(addr); err != nil {
		writeErr(w, http.StatusBadRequest, "that is not a valid email address")
		return
	}
	// Recorded unverified: an address only becomes usable as a factor once a
	// code sent to it comes back.
	if err := s.st.SetAdminEmail(id.ID, addr, false); err != nil {
		s.internalErr(w, "store email", err)
		return
	}
	code, err := auth.NewNumericCode()
	if err != nil {
		s.internalErr(w, "generate code", err)
		return
	}
	if err := s.st.CreateChallenge(auth.HashSecret(id.ID+":email-verify"), id.ID,
		store.PurposeEmailVerify, auth.HashSecret(code), store.EmailCodeTTL); err != nil {
		s.internalErr(w, "store code", err)
		return
	}
	body := fmt.Sprintf("Your Chiral verification code is %s.\n\n"+
		"It expires in %d minutes.", code, int(store.EmailCodeTTL.Minutes()))
	if err := s.mailer.Send(addr, "Verify your Chiral email", body); err != nil {
		s.logger.Error("sending verification failed", "admin", id.Name, "err", err)
		writeErr(w, http.StatusBadGateway, "could not send the email: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent_to": maskEmail(addr)})
}

func (s *Server) confirmEmail(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r.Context())
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON with a code")
		return
	}
	hash := auth.HashSecret(id.ID + ":email-verify")
	ch, err := s.st.GetChallenge(hash, store.PurposeEmailVerify)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "no code is pending, or it has expired")
		return
	}
	if subtle.ConstantTimeCompare([]byte(ch.Data), []byte(auth.HashSecret(strings.TrimSpace(req.Code)))) != 1 {
		exhausted, err := s.st.BumpChallengeAttempts(hash)
		if err != nil {
			s.logger.Error("counting attempts failed", "err", err)
		}
		if exhausted {
			s.st.DeleteChallenge(hash)
			writeErr(w, http.StatusUnauthorized, "too many attempts; send a new code")
			return
		}
		writeErr(w, http.StatusUnauthorized, "that code is not valid")
		return
	}
	s.st.DeleteChallenge(hash)

	email, _, err := s.st.AdminEmail(id.ID)
	if err != nil {
		s.internalErr(w, "load email", err)
		return
	}
	if err := s.st.SetAdminEmail(id.ID, email, true); err != nil {
		s.internalErr(w, "mark verified", err)
		return
	}
	// A verified address becomes a usable factor.
	if _, err := s.st.PutMFACredential(store.MFACredential{
		AdminID: id.ID, Kind: store.MFAEmail, Name: email, Confirmed: true,
	}); err != nil {
		s.internalErr(w, "enrol email factor", err)
		return
	}
	s.audit(r, "mfa.enroll", "admin", id.ID, id.Name, "email")
	s.afterFirstFactor(w, r, id.ID, email)
}

// --- recovery codes ---

func (s *Server) regenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r.Context())
	codes, hashes, err := auth.NewRecoveryCodes()
	if err != nil {
		s.internalErr(w, "generate recovery codes", err)
		return
	}
	if err := s.st.ReplaceRecoveryCodes(id.ID, hashes); err != nil {
		s.internalErr(w, "store recovery codes", err)
		return
	}
	s.audit(r, "mfa.recovery_codes", "admin", id.ID, id.Name, "regenerated")
	// Shown once. Only hashes are kept, so there is no second chance.
	writeJSON(w, http.StatusOK, map[string]any{"codes": codes})
}

// afterFirstFactor issues recovery codes alongside the first confirmed factor.
// Enrolling MFA without a way back in is how people lock themselves out, and
// asking them to remember a separate step does not work.
func (s *Server) afterFirstFactor(w http.ResponseWriter, r *http.Request, adminID, name string) {
	remaining, err := s.st.CountUnusedRecoveryCodes(adminID)
	if err != nil {
		s.logger.Error("counting recovery codes failed", "err", err)
	}
	resp := map[string]any{"confirmed": true, "name": name}
	if remaining == 0 {
		codes, hashes, err := auth.NewRecoveryCodes()
		if err == nil && s.st.ReplaceRecoveryCodes(adminID, hashes) == nil {
			resp["recovery_codes"] = codes
		} else if err != nil {
			s.logger.Error("generating recovery codes failed", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
