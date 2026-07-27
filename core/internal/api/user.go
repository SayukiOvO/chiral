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
	Allowed bool `json:"allowed"`
	// DeviceLimit is the expected number of concurrent source addresses, 0 for
	// none. Nothing enforces it — see store.User.DeviceLimit.
	DeviceLimit int `json:"device_limit"`
	// OnlineDevices is the current count, absent when recording is off so the
	// UI can distinguish "nobody connected" from "not measuring".
	OnlineDevices *int             `json:"online_devices,omitempty"`
	ProfileIDs    []string         `json:"profile_ids"`
	Credentials   []credentialView `json:"credentials,omitempty"`
	CreatedAt     int64            `json:"created_at"`
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
		Allowed:     user.Allowed(u, time.Now().Unix()),
		DeviceLimit: u.DeviceLimit,
		ProfileIDs:  profileIDs,
		CreatedAt:   u.CreatedAt,
	}
	if s.online != nil {
		n := s.online.Status(u.ID, time.Now()).Count
		v.OnlineDevices = &n
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
	DeviceLimit int    `json:"device_limit"`
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
	u, err := s.st.CreateUserWithToken(store.User{
		Name:        strings.TrimSpace(req.Name),
		QuotaBytes:  req.QuotaBytes,
		ExpiresAt:   req.ExpiresAt,
		RenewPeriod: req.RenewPeriod,
		Enabled:     enabled,
		DeviceLimit: req.DeviceLimit,
	}, token, hash)
	if err != nil {
		if isConflict(err) {
			writeErr(w, http.StatusConflict, "a user with that name already exists")
			return
		}
		s.internalErr(w, "create user", err)
		return
	}
	// Settle the sync state straight away. A brand-new user has no
	// credentials yet, so there is nothing to push — without this they would
	// sit showing "syncing" until the next sweep for no reason.
	s.syncUserNow(r.Context(), u.ID)
	u, err = s.st.GetUser(u.ID)
	if err != nil {
		s.internalErr(w, "load user", err)
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
	u.DeviceLimit = req.DeviceLimit
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
	// Two steps, both needed. The online op makes the change bite now; the
	// re-assembly makes it survive a restart, since a reconnecting agent
	// replays the stored config.
	s.syncUserNow(r.Context(), id)
	s.reassembleForUser(r.Context(), id)
	s.getUser(w, r)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, err := s.st.GetUser(id)
	if err != nil {
		s.notFoundOr(w, "load user", err, "no such user")
		return
	}
	// Where they are installed has to be read BEFORE the delete: the
	// credentials are the record of that, and they cascade away with the user.
	nodeIDs := s.nodesForUser(id)

	// Take them off the running kernels first, while the rows the operation
	// needs still exist.
	u.Enabled = false
	if err := s.st.UpdateUser(u); err == nil {
		s.syncUserNow(r.Context(), id)
	}
	if err := s.st.DeleteUser(id); err != nil {
		s.internalErr(w, "delete user", err)
		return
	}
	// Then rewrite those nodes' stored configs without them. Skipping this
	// would leave a deleted user in the last stored config, and a reconnect
	// would hand them access back with no row left to revoke.
	s.reassemble(r.Context(), nodeIDs)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resetSubToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, err := s.st.GetUser(id)
	if err != nil {
		s.notFoundOr(w, "load user", err, "no such user")
		return
	}
	token, hash := auth.NewSecret()
	if err := s.st.ResetSubToken(id, token, hash); err != nil {
		s.notFoundOr(w, "reset subscription token", err, "no such user")
		return
	}
	// Worth auditing: this invalidates every client the person has configured,
	// and none of the proxy-user operations recorded anything until now.
	s.audit(r, "user.sub_token_reset", "user", u.ID, u.Name, "")
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
	// Granting access has to reach the nodes to mean anything: assembly is
	// what mints the credential for each bound node and writes it into the
	// inbound. Without this the entitlement would sit in the database doing
	// nothing until an operator happened to apply the node by hand.
	nodeIDs, err := s.st.ProfileNodeIDs(profileID)
	if err != nil {
		s.internalErr(w, "list profile nodes", err)
		return
	}
	s.reassemble(r.Context(), nodeIDs)
	// The credentials exist now, so the user can be installed online too.
	s.syncUserNow(r.Context(), userID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) unbindUserProfile(w http.ResponseWriter, r *http.Request) {
	userID, profileID := r.PathValue("id"), r.PathValue("profileID")

	// Same ordering as deletion, for the same reason: the credentials record
	// where the user is installed and what an online removal needs to name, so
	// read the nodes and take them off the kernels before dropping the rows.
	nodeIDs := s.nodesForUserProfile(userID, profileID)
	s.removeUserFromNodes(r.Context(), userID, profileID)

	if err := s.st.UnbindUserProfile(userID, profileID); err != nil {
		s.internalErr(w, "unbind user from profile", err)
		return
	}
	if err := s.st.DeleteCredentialsForBinding(userID, profileID); err != nil {
		s.internalErr(w, "drop credentials", err)
		return
	}
	// Rewrite the stored configs so a reconnect does not restore the access
	// that was just revoked.
	s.reassemble(r.Context(), nodeIDs)
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

// reassemble rewrites the stored config of each node so it matches current
// membership. Failures are logged, not returned: the membership change itself
// succeeded, and a node that could not be reached converges on its next apply.
func (s *Server) reassemble(ctx context.Context, nodeIDs []string) {
	if s.profiles == nil || len(nodeIDs) == 0 {
		return
	}
	for id, err := range s.profiles.ApplyUserNodes(ctx, nodeIDs) {
		s.logger.Error("node did not converge after a membership change",
			"node", id, "err", err)
	}
}

// removeUserFromNodes takes a user off the running kernels for one profile,
// before the rows that describe the operation are dropped.
func (s *Server) removeUserFromNodes(ctx context.Context, userID, profileID string) {
	if s.profiles == nil {
		return
	}
	if err := s.profiles.RemoveUserFromProfile(ctx, userID, profileID); err != nil {
		s.logger.Error("removing user from profile's nodes failed",
			"user", userID, "profile", profileID, "err", err)
	}
}

func (s *Server) nodesForUser(userID string) []string {
	if s.profiles == nil {
		return nil
	}
	ids, err := s.profiles.NodesForUser(userID)
	if err != nil {
		s.logger.Error("listing a user's nodes failed", "user", userID, "err", err)
		return nil
	}
	return ids
}

func (s *Server) nodesForUserProfile(userID, profileID string) []string {
	if s.profiles == nil {
		return nil
	}
	ids, err := s.profiles.NodesForUserProfile(userID, profileID)
	if err != nil {
		s.logger.Error("listing a user's nodes for a profile failed",
			"user", userID, "profile", profileID, "err", err)
		return nil
	}
	return ids
}

// reassembleForUser rewrites the stored configs of the nodes a user is
// installed on.
func (s *Server) reassembleForUser(ctx context.Context, userID string) {
	s.reassemble(ctx, s.nodesForUser(userID))
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
