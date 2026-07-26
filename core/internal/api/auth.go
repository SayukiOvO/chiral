package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

// Authentication has two paths on purpose.
//
// A session comes from logging in with an account and carries that person's
// name into the audit trail. The environment token is the break-glass path:
// an operator who has lost every password still has their compose file, and
// it acts as a superadmin labelled "env-token" so the trail never silently
// attributes an action to nobody.

type ctxKey int

const identityKey ctxKey = 0

// identityFrom returns who is making this request. Handlers past the auth
// middleware can rely on it being present.
func identityFrom(ctx context.Context) auth.Identity {
	id, _ := ctx.Value(identityKey).(auth.Identity)
	return id
}

// bearer pulls the credential out of the Authorization header.
func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

// authenticate resolves a credential to an identity, or reports that it is not
// usable. Session tokens are looked up first; the environment token is the
// fallback so that rotating it cannot lock out logged-in admins.
func (s *Server) authenticate(token string) (auth.Identity, bool) {
	if token == "" {
		return auth.Identity{}, false
	}
	if a, err := s.st.AdminBySession(auth.HashSecret(token)); err == nil {
		return auth.Identity{ID: a.ID, Name: a.Username, Role: a.Role}, true
	} else if !store.IsNotFound(err) {
		s.logger.Error("session lookup failed", "err", err)
		return auth.Identity{}, false
	}
	// Constant-time compare: this token is a secret, and an early-exit
	// comparison would leak its prefix to anyone who can time requests.
	if s.adminToken != "" && subtleEqual(token, s.adminToken) {
		return auth.EnvTokenIdentity(), true
	}
	return auth.Identity{}, false
}

// require builds a handler that admits only identities holding min.
func (s *Server) require(min auth.Role, h func(http.ResponseWriter, *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.authenticate(bearer(r))
		if !ok {
			writeErr(w, http.StatusUnauthorized, "missing or invalid credentials")
			return
		}
		if !id.Can(min) {
			// 403, not 404: the caller is authenticated, and hiding the
			// existence of an endpoint from a logged-in colleague buys
			// nothing but confusion.
			writeErr(w, http.StatusForbidden, "your role does not allow this")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), identityKey, id)))
	})
}

// requireAdmin keeps the old name for read endpoints: any authenticated role
// may read.
func (s *Server) requireAdmin(h func(http.ResponseWriter, *http.Request)) http.Handler {
	return s.require(auth.RoleViewer, h)
}

// requireWrite guards anything that changes state.
func (s *Server) requireWrite(h func(http.ResponseWriter, *http.Request)) http.Handler {
	return s.require(auth.RoleOperator, h)
}

// requireSuperadmin guards managing admins.
func (s *Server) requireSuperadmin(h func(http.ResponseWriter, *http.Request)) http.Handler {
	return s.require(auth.RoleSuperadmin, h)
}

// audit records an action, logging rather than failing the request: the action
// already happened, and refusing to report it would be worse than a gap.
func (s *Server) audit(r *http.Request, action, targetType, targetID, targetName, detail string) {
	if err := s.st.Audit(identityFrom(r.Context()), action, targetType, targetID, targetName, detail); err != nil {
		s.logger.Error("writing audit entry failed", "action", action, "err", err)
	}
}

// --- endpoints ---

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON with username and password")
		return
	}
	a, err := s.st.FindAdminByUsername(strings.TrimSpace(req.Username))
	if err != nil || a.Disabled || !auth.VerifyPassword(req.Password, a.PasswordHash) {
		// One message for every failure: whether the account exists is not
		// something an unauthenticated caller should be able to probe.
		if err != nil && !store.IsNotFound(err) {
			s.logger.Error("login lookup failed", "err", err)
		}
		writeErr(w, http.StatusUnauthorized, "incorrect username or password")
		return
	}

	// A correct password only finishes the job when no second factor is
	// enrolled. Otherwise it earns a short-lived challenge, and the session is
	// issued only once that challenge is spent.
	factors, err := s.st.ConfirmedMFACredentials(a.ID)
	if err != nil {
		s.internalErr(w, "load factors", err)
		return
	}
	if len(factors) > 0 {
		s.startMFA(w, a, factors)
		return
	}
	s.issueSession(w, a, "password")
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if token := bearer(r); token != "" {
		if err := s.st.DeleteSession(auth.HashSecret(token)); err != nil {
			s.logger.Error("deleting session failed", "err", err)
		}
	}
	s.audit(r, "logout", "admin", identityFrom(r.Context()).ID, identityFrom(r.Context()).Name, "")
	w.WriteHeader(http.StatusNoContent)
}

// whoami lets the frontend show who it is logged in as and adapt to the role
// rather than offering buttons that will be refused.
func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"id":        id.ID,
		"name":      id.Name,
		"role":      id.Role,
		"via_token": id.ViaToken,
		"can_write": id.CanWrite(),
		"can_admin": id.CanAdmin(),
	})
}

type adminJSON struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Role      auth.Role `json:"role"`
	Disabled  bool      `json:"disabled"`
	CreatedAt int64     `json:"created_at"`
	LastLogin int64     `json:"last_login"`
}

func adminView(a store.Admin) adminJSON {
	return adminJSON{
		ID: a.ID, Username: a.Username, Role: a.Role,
		Disabled: a.Disabled, CreatedAt: a.CreatedAt, LastLogin: a.LastLogin,
	}
}

func (s *Server) listAdmins(w http.ResponseWriter, r *http.Request) {
	admins, err := s.st.ListAdmins()
	if err != nil {
		s.internalErr(w, "list admins", err)
		return
	}
	views := make([]adminJSON, 0, len(admins))
	for _, a := range admins {
		views = append(views, adminView(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"admins": views})
}

func (s *Server) createAdmin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string    `json:"username"`
		Password string    `json:"password"`
		Role     auth.Role `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" {
		writeErr(w, http.StatusBadRequest, "username is required")
		return
	}
	if len(req.Password) < auth.MinPasswordLen {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if !auth.ValidRole(req.Role) {
		writeErr(w, http.StatusBadRequest, "role must be superadmin, operator or viewer")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.internalErr(w, "hash password", err)
		return
	}
	a, err := s.st.CreateAdmin(username, hash, req.Role)
	if err != nil {
		if isConflict(err) {
			writeErr(w, http.StatusConflict, "that username is taken")
			return
		}
		s.internalErr(w, "create admin", err)
		return
	}
	s.audit(r, "admin.create", "admin", a.ID, a.Username, string(a.Role))
	writeJSON(w, http.StatusCreated, adminView(a))
}

func (s *Server) updateAdmin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	target, err := s.st.GetAdmin(id)
	if err != nil {
		s.notFoundOr(w, "load admin", err, "no such admin")
		return
	}
	var req struct {
		Role     auth.Role `json:"role"`
		Disabled *bool     `json:"disabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	role := target.Role
	if req.Role != "" {
		if !auth.ValidRole(req.Role) {
			writeErr(w, http.StatusBadRequest, "role must be superadmin, operator or viewer")
			return
		}
		role = req.Role
	}
	disabled := target.Disabled
	if req.Disabled != nil {
		disabled = *req.Disabled
	}

	// Refuse to remove the last way in. Demoting or disabling the final
	// enabled superadmin would leave the panel manageable only by whoever
	// holds the environment token.
	losingSuper := target.Role == auth.RoleSuperadmin && !target.Disabled &&
		(role != auth.RoleSuperadmin || disabled)
	if losingSuper {
		n, err := s.st.CountEnabledSuperadmins()
		if err != nil {
			s.internalErr(w, "count superadmins", err)
			return
		}
		if n <= 1 {
			writeErr(w, http.StatusConflict, "this is the last enabled superadmin")
			return
		}
	}

	if err := s.st.UpdateAdminRole(id, role, disabled); err != nil {
		s.internalErr(w, "update admin", err)
		return
	}
	s.audit(r, "admin.update", "admin", id, target.Username,
		"role="+string(role)+" disabled="+boolText(disabled))
	updated, err := s.st.GetAdmin(id)
	if err != nil {
		s.internalErr(w, "reload admin", err)
		return
	}
	writeJSON(w, http.StatusOK, adminView(updated))
}

func (s *Server) deleteAdmin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	target, err := s.st.GetAdmin(id)
	if err != nil {
		s.notFoundOr(w, "load admin", err, "no such admin")
		return
	}
	if identityFrom(r.Context()).ID == id {
		writeErr(w, http.StatusConflict, "you cannot delete the account you are signed in as")
		return
	}
	if target.Role == auth.RoleSuperadmin && !target.Disabled {
		n, err := s.st.CountEnabledSuperadmins()
		if err != nil {
			s.internalErr(w, "count superadmins", err)
			return
		}
		if n <= 1 {
			writeErr(w, http.StatusConflict, "this is the last enabled superadmin")
			return
		}
	}
	if err := s.st.DeleteAdmin(id); err != nil {
		s.internalErr(w, "delete admin", err)
		return
	}
	s.audit(r, "admin.delete", "admin", id, target.Username, "")
	w.WriteHeader(http.StatusNoContent)
}

// changePassword lets someone change their own password, or a superadmin
// reset anyone's. Changing your own requires the current one, so a walk-up to
// an unlocked browser cannot lock the owner out.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	me := identityFrom(r.Context())
	if id != me.ID && !me.CanAdmin() {
		writeErr(w, http.StatusForbidden, "you may only change your own password")
		return
	}
	target, err := s.st.GetAdmin(id)
	if err != nil {
		s.notFoundOr(w, "load admin", err, "no such admin")
		return
	}
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
	if id == me.ID && !auth.VerifyPassword(req.Current, target.PasswordHash) {
		writeErr(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	hash, err := auth.HashPassword(req.New)
	if err != nil {
		s.internalErr(w, "hash password", err)
		return
	}
	// This also drops the target's sessions, including the caller's own when
	// they changed their own password — that is the point.
	if err := s.st.SetAdminPassword(id, hash); err != nil {
		s.internalErr(w, "set password", err)
		return
	}
	s.audit(r, "admin.password", "admin", id, target.Username, "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) auditLog(w http.ResponseWriter, r *http.Request) {
	before := int64(0)
	if raw := r.URL.Query().Get("before"); raw != "" {
		if v, err := parseInt64(raw); err == nil {
			before = v
		}
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if v, err := parseInt64(raw); err == nil && v > 0 {
			limit = int(v)
		}
	}
	entries, err := s.st.AuditPage(before, limit,
		r.URL.Query().Get("actor_id"), r.URL.Query().Get("action"))
	if err != nil {
		s.internalErr(w, "load audit log", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// subtleEqual compares two secrets without leaking their prefix through
// timing.
func subtleEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func parseInt64(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
