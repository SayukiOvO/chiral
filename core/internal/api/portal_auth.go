package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/portal"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

// Signing in, claiming an operator-issued account, and the two things a signed
// in subscriber can change about themselves.

// portalLogin exchanges an email and password for a session.
//
// Deliberately does NOT check whether the user is allowed to proxy. Someone
// whose subscription expired or whose quota is spent must be able to sign in
// and read why — turning them away at the door with "incorrect email or
// password" would be both untrue and the fastest route to a support ticket.
// store.UserByPortalSession draws the same line: it checks user_accounts
// .disabled, not users.enabled.
func (s *Server) portalLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON with an email and a password")
		return
	}

	a, err := s.st.UserAccountByEmail(req.Email)
	if err != nil || a.Disabled {
		// Same message and the same work either way: without the dummy hash,
		// an unknown address would answer two orders of magnitude faster than
		// a real one and the panel would be an email-enumeration oracle.
		auth.SpendVerification(req.Password)
		if err != nil && !store.IsNotFound(err) {
			s.logger.Error("portal login lookup failed", "err", err)
		}
		writeErr(w, http.StatusUnauthorized, "incorrect email or password")
		return
	}
	if !auth.VerifyPassword(req.Password, a.PasswordHash) {
		writeErr(w, http.StatusUnauthorized, "incorrect email or password")
		return
	}
	s.issuePortalSession(w, a.UserID)
}

func (s *Server) issuePortalSession(w http.ResponseWriter, userID string) {
	token, hash := auth.NewSecret()
	if err := s.st.CreatePortalSession(hash, userID, store.PortalSessionTTL); err != nil {
		s.internalErr(w, "create portal session", err)
		return
	}
	if err := s.st.TouchUserAccountLogin(userID); err != nil {
		s.logger.Error("recording portal login failed", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_at": time.Now().Add(store.PortalSessionTTL).Unix(),
	})
}

func (s *Server) portalLogout(w http.ResponseWriter, r *http.Request, _ portal.Identity) {
	if err := s.st.DeletePortalSession(auth.HashSecret(bearer(r))); err != nil {
		s.internalErr(w, "delete portal session", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// portalChangePassword changes the caller's own password, dropping every
// session including this one.
func (s *Server) portalChangePassword(w http.ResponseWriter, r *http.Request, id portal.Identity) {
	var req struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	if len(req.New) < auth.MinPasswordLen {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	a, err := s.st.UserAccount(id.UserID)
	if err != nil {
		s.internalErr(w, "load account", err)
		return
	}
	if !auth.VerifyPassword(req.Current, a.PasswordHash) {
		writeErr(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	hash, err := auth.HashPassword(req.New)
	if err != nil {
		s.internalErr(w, "hash password", err)
		return
	}
	// Drops every session, this one included. That is the point: someone
	// changing their password believes it is compromised.
	if err := s.st.SetUserAccountPassword(id.UserID, hash); err != nil {
		s.internalErr(w, "set password", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- claiming an operator-issued account ---

// portalClaimLookup exchanges a claim token for the name it belongs to, so the
// claim page can say who it is for before asking for a password.
//
// The token travels in the body, not the path. /sub/{token} already makes the
// mistake of putting a credential in a URL, where it lands in reverse-proxy
// access logs, browser history and Referer headers; repeating that for a token
// that can TAKE OVER an account would be a step backwards.
func (s *Server) portalClaimLookup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON with a token")
		return
	}
	ch, err := s.st.GetPortalChallenge(auth.HashSecret(req.Token), store.PortalPurposeClaim)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "that link is invalid or has expired")
		return
	}
	u, err := s.st.GetUser(ch.UserID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "that link is invalid or has expired")
		return
	}
	// The proxy user's name, which the operator chose — not an email address,
	// which would make a leaked link an address disclosure as well.
	writeJSON(w, http.StatusOK, map[string]any{"user_name": u.Name})
}

// portalClaim spends a claim token to set a password.
func (s *Server) portalClaim(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	if len(req.Password) < auth.MinPasswordLen {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	hashOfToken := auth.HashSecret(req.Token)
	ch, err := s.st.GetPortalChallenge(hashOfToken, store.PortalPurposeClaim)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "that link is invalid or has expired")
		return
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.internalErr(w, "hash password", err)
		return
	}

	existing, err := s.st.UserAccount(ch.UserID)
	switch {
	case err == nil:
		// The account already exists, so this link is acting as a password
		// reset. It must NOT be allowed to move the address: a leaked link
		// would otherwise let someone point the account at their own inbox and
		// then own it permanently through password recovery.
		if strings.TrimSpace(req.Email) != "" &&
			store.NormalizeEmail(req.Email) != existing.Email {
			writeErr(w, http.StatusBadRequest,
				"this link can set a new password but cannot change the email address")
			return
		}
		if err := s.st.SetUserAccountPassword(ch.UserID, passwordHash); err != nil {
			s.internalErr(w, "set password", err)
			return
		}
	case store.IsNotFound(err):
		addr := store.NormalizeEmail(req.Email)
		if addr == "" {
			writeErr(w, http.StatusBadRequest, "an email address is required to claim this account")
			return
		}
		if _, err := s.st.CreateUserAccount(ch.UserID, addr, passwordHash); err != nil {
			if isConflict(err) {
				writeErr(w, http.StatusConflict, "that email address is already in use")
				return
			}
			s.internalErr(w, "create account", err)
			return
		}
	default:
		s.internalErr(w, "load account", err)
		return
	}

	// One-time by construction, and every prior session is dropped: whoever
	// held the old password should not still be signed in after a reset.
	s.st.DeletePortalChallenge(hashOfToken)
	if err := s.st.DeletePortalSessionsFor(ch.UserID); err != nil {
		s.logger.Error("clearing portal sessions failed", "err", err)
	}
	s.issuePortalSession(w, ch.UserID)
}

// --- the operator side of claiming ---

// issuePortalLink mints a one-time link for a user, shaped exactly like
// resetJoinToken: returned once, stored only as a hash.
func (s *Server) issuePortalLink(w http.ResponseWriter, r *http.Request) {
	u, err := s.st.GetUser(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load user", err, "no such user")
		return
	}
	token, hash := auth.NewSecret()
	if err := s.st.CreatePortalChallenge(hash, u.ID, store.PortalPurposeClaim, "",
		store.PortalClaimTTL); err != nil {
		s.internalErr(w, "create claim challenge", err)
		return
	}
	s.audit(r, "user.portal_link", "user", u.ID, u.Name, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"claim_url":  strings.TrimRight(s.publicURL, "/") + "/#/claim/" + token,
		"expires_at": time.Now().Add(store.PortalClaimTTL).Unix(),
	})
}
