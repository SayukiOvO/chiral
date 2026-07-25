package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

type userView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	QuotaBytes  int64  `json:"quota_bytes"`
	UsedBytes   int64  `json:"used_bytes"`
	ExpiresAt   int64  `json:"expires_at"`
	RenewPeriod int64  `json:"renew_period"`
	Enabled     bool   `json:"enabled"`
	// Active is what the nodes were last told, as opposed to what should be
	// true; surfacing both makes a stuck sync visible instead of mysterious.
	Active bool `json:"active"`
	// Allowed is the computed verdict: enabled, in date, and under quota.
	Allowed     bool             `json:"allowed"`
	ProfileIDs  []string         `json:"profile_ids"`
	Credentials []credentialView `json:"credentials,omitempty"`
	CreatedAt   int64            `json:"created_at"`
}

type credentialView struct {
	ProfileID string `json:"profile_id"`
	NodeID    string `json:"node_id"`
	Email     string `json:"email"`
	UpBytes   int64  `json:"up_bytes"`
	DownBytes int64  `json:"down_bytes"`
	// The secret itself is never returned: it reaches the user through their
	// subscription, and an admin UI has no reason to display it.
}

func (s *Server) userView(u store.User, withCredentials bool) (userView, error) {
	profileIDs, err := s.st.UserProfileIDs(u.ID)
	if err != nil {
		return userView{}, err
	}
	v := userView{
		ID: u.ID, Name: u.Name,
		QuotaBytes: u.QuotaBytes, UsedBytes: u.UsedBytes,
		ExpiresAt: u.ExpiresAt, RenewPeriod: u.RenewPeriod,
		Enabled: u.Enabled, Active: u.Active,
		Allowed:    user.Allowed(u, time.Now().Unix()),
		ProfileIDs: profileIDs,
		CreatedAt:  u.CreatedAt,
	}
	if withCredentials {
		creds, err := s.st.UserCredentials(u.ID)
		if err != nil {
			return userView{}, err
		}
		for _, c := range creds {
			v.Credentials = append(v.Credentials, credentialView{
				ProfileID: c.ProfileID, NodeID: c.NodeID, Email: c.Email,
				UpBytes: c.UpBytes, DownBytes: c.DownBytes,
			})
		}
	}
	return v, nil
}

type userRequest struct {
	Name        string `json:"name"`
	QuotaBytes  int64  `json:"quota_bytes"`
	ExpiresAt   int64  `json:"expires_at"`
	RenewPeriod int64  `json:"renew_period"`
	Enabled     *bool  `json:"enabled"`
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var req userRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, `body must be JSON with a non-empty "name"`)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	token, hash := auth.NewSecret()
	u, err := s.st.CreateUser(store.User{
		Name:        strings.TrimSpace(req.Name),
		QuotaBytes:  req.QuotaBytes,
		ExpiresAt:   req.ExpiresAt,
		RenewPeriod: req.RenewPeriod,
		Enabled:     enabled,
	}, hash)
	if err != nil {
		if isConflict(err) {
			writeErr(w, http.StatusConflict, "a user with that name already exists")
			return
		}
		s.internalErr(w, "create user", err)
		return
	}
	v, err := s.userView(u, false)
	if err != nil {
		s.internalErr(w, "load user", err)
		return
	}
	// The token is shown exactly once; only its hash is stored.
	writeJSON(w, http.StatusCreated, map[string]any{
		"user":             v,
		"subscription_url": s.subscriptionURL(token),
	})
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.st.ListUsers()
	if err != nil {
		s.internalErr(w, "list users", err)
		return
	}
	views := make([]userView, 0, len(users))
	for _, u := range users {
		v, err := s.userView(u, false)
		if err != nil {
			s.internalErr(w, "build user view", err)
			return
		}
		views = append(views, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": views})
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.st.GetUser(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load user", err, "no such user")
		return
	}
	v, err := s.userView(u, true)
	if err != nil {
		s.internalErr(w, "build user view", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, err := s.st.GetUser(id)
	if err != nil {
		s.notFoundOr(w, "load user", err, "no such user")
		return
	}
	var req userRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	if name := strings.TrimSpace(req.Name); name != "" {
		u.Name = name
	}
	u.QuotaBytes, u.ExpiresAt, u.RenewPeriod = req.QuotaBytes, req.ExpiresAt, req.RenewPeriod
	if req.Enabled != nil {
		u.Enabled = *req.Enabled
	}
	if err := s.st.UpdateUser(u); err != nil {
		if isConflict(err) {
			writeErr(w, http.StatusConflict, "a user with that name already exists")
			return
		}
		s.internalErr(w, "update user", err)
		return
	}
	// Apply the change to the live nodes immediately rather than waiting for
	// the next sweep — an operator who clicks "disable" expects it to bite.
	s.syncUserNow(r.Context(), id)
	s.getUser(w, r)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, err := s.st.GetUser(id)
	if err != nil {
		s.notFoundOr(w, "load user", err, "no such user")
		return
	}
	// Take the credentials off the nodes before dropping the rows, or the
	// clients array keeps serving a user who no longer exists until the next
	// config push.
	u.Enabled = false
	if err := s.st.UpdateUser(u); err == nil {
		s.syncUserNow(r.Context(), id)
	}
	if err := s.st.DeleteUser(id); err != nil {
		s.internalErr(w, "delete user", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resetSubToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	token, hash := auth.NewSecret()
	if err := s.st.ResetSubToken(id, hash); err != nil {
		s.notFoundOr(w, "reset subscription token", err, "no such user")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscription_url": s.subscriptionURL(token)})
}

func (s *Server) bindUserProfile(w http.ResponseWriter, r *http.Request) {
	userID, profileID := r.PathValue("id"), r.PathValue("profileID")
	if _, err := s.st.GetUser(userID); err != nil {
		s.notFoundOr(w, "load user", err, "no such user")
		return
	}
	if _, err := s.st.GetProfile(profileID); err != nil {
		s.notFoundOr(w, "load profile", err, "no such profile")
		return
	}
	if err := s.st.BindUserProfile(userID, profileID); err != nil {
		s.internalErr(w, "bind user to profile", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) unbindUserProfile(w http.ResponseWriter, r *http.Request) {
	userID, profileID := r.PathValue("id"), r.PathValue("profileID")
	if err := s.st.UnbindUserProfile(userID, profileID); err != nil {
		s.internalErr(w, "unbind user from profile", err)
		return
	}
	// Revoking the entitlement drops the credentials with it; leaving them
	// would keep the user working on nodes they are no longer entitled to
	// until the next config push.
	if err := s.st.DeleteCredentialsForBinding(userID, profileID); err != nil {
		s.internalErr(w, "drop credentials", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// syncUserNow pushes a user's state to the live nodes, logging rather than
// failing the request: the periodic sweep is the backstop.
func (s *Server) syncUserNow(ctx context.Context, userID string) {
	if s.profiles == nil {
		return
	}
	if _, err := s.profiles.SyncUser(ctx, userID); err != nil {
		s.logger.Error("syncing user after change failed", "user", userID, "err", err)
	}
}

func (s *Server) subscriptionURL(token string) string {
	base := strings.TrimSuffix(s.publicURL, "/")
	if base == "" {
		return "/sub/" + token
	}
	return base + "/sub/" + token
}

func isConflict(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
