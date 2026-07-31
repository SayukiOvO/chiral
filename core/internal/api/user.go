package api

import (
	"context"
	"encoding/json"
	"fmt"
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
	// RulesetID is the routing configuration their clash-family subscription
	// is rendered against; empty means none.
	RulesetID string `json:"ruleset_id"`
	// Reason names WHICH of those failed, empty when allowed. The precedence
	// between them lives in one place (user.Reason) precisely so nothing has
	// to reimplement it; serving only the boolean forced the console to do
	// exactly that, and the portal already receives this.
	Reason string `json:"reason,omitempty"`
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
		RulesetID:   u.RulesetID,
		Reason:      user.Reason(u, time.Now().Unix()),
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

// validate rejects the values that are not merely unusual but incoherent.
//
// A negative quota is the one that matters: user.Allowed compares
// used_bytes >= quota_bytes with a "0 means unlimited" special case, so -1 is
// neither unlimited nor a limit — it is a user who is over quota the moment
// they are created, permanently, with no way to tell from the API why. The
// others are the same kind of nonsense with milder consequences.
func (r userRequest) validate() error {
	if r.QuotaBytes < 0 {
		return fmt.Errorf("quota_bytes must be 0 (unlimited) or positive, got %d", r.QuotaBytes)
	}
	if r.RenewPeriod < 0 {
		return fmt.Errorf("renew_period must be 0 (no auto-renewal) or positive, got %d", r.RenewPeriod)
	}
	if r.ExpiresAt < 0 {
		return fmt.Errorf("expires_at must be 0 (no expiry) or a unix timestamp, got %d", r.ExpiresAt)
	}
	if r.DeviceLimit < 0 {
		return fmt.Errorf("device_limit must be 0 (no limit) or positive, got %d", r.DeviceLimit)
	}
	return nil
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var req userRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, `body must be JSON with a non-empty "name"`)
		return
	}
	if err := req.validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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
	if err := req.validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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

// adoptCredential points an existing user × profile × node credential at a
// secret the operator already has in the field.
//
// The reason this endpoint exists: without it, migrating a server that already
// has users means either re-issuing every client configuration, or leaving the
// old credential hand-written in the profile's inbound template. The second is
// what it looks like when someone takes the shortcut — a live UUID sitting in
// a template every admin can read, entitled to nothing, counted against
// nobody, and unaffected by disabling the user it belongs to.
func (s *Server) adoptCredential(w http.ResponseWriter, r *http.Request) {
	userID, profileID, nodeID := r.PathValue("id"), r.PathValue("profileID"), r.PathValue("nodeId")
	var req struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	req.Secret = strings.TrimSpace(req.Secret)
	if req.Secret == "" {
		writeErr(w, http.StatusBadRequest, `"secret" is required`)
		return
	}
	u, err := s.st.GetUser(userID)
	if err != nil {
		s.notFoundOr(w, "load user", err, "no such user")
		return
	}
	if _, err := s.st.GetProfile(profileID); err != nil {
		s.notFoundOr(w, "load profile", err, "no such profile")
		return
	}
	if _, err := s.st.GetNode(nodeID); err != nil {
		s.notFoundOr(w, "load node", err, "no such node")
		return
	}
	if _, err := s.st.SetCredentialSecret(userID, profileID, nodeID,
		user.StatsEmail(u.Name, u.ID, profileID, nodeID), req.Secret); err != nil {
		s.internalErr(w, "adopt credential", err)
		return
	}
	// The node is carrying the old secret in its clients array until assembly
	// runs again.
	if _, err := s.profiles.Apply(r.Context(), nodeID); err != nil {
		s.logger.Warn("credential adopted but the node was not updated", "node", nodeID, "err", err)
	}
	s.audit(r, "user.credential_adopt", "user", userID, u.Name, "profile="+profileID+" node="+nodeID)
	w.WriteHeader(http.StatusNoContent)
}

// nodeAccessView is what the console needs to draw the per-user node list:
// every node and external proxy that exists, and whether this subscriber may
// use it.
type nodeAccessEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Source is "fleet" or the external subscription's name, so the console can
	// group them and say where a node came from.
	Source  string `json:"source"`
	Allowed bool   `json:"allowed"`
	// Entitled reports whether any profile this user holds reaches this node.
	// A fleet node they have no profile for is not something denying can
	// change, and showing it as merely "off" would be a lie.
	Entitled bool `json:"entitled"`
	// ChainedVia names the fleet node an external node dials through, when
	// that node is one this subscriber has been denied. Such a proxy cannot be
	// carried — an unresolvable dialer-proxy makes the whole document
	// unloadable, and dropping the chain would send them straight at the
	// provider — so it silently leaves the subscription along with the relay.
	// Silently is the problem: the console said it was on.
	ChainedVia string `json:"chained_via,omitempty"`
}

func (s *Server) userNodeAccess(w http.ResponseWriter, r *http.Request) {
	u, err := s.st.GetUser(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load user", err, "no such user")
		return
	}
	deniedNodes, err := s.st.UserNodeDenies(u.ID)
	if err != nil {
		s.internalErr(w, "load denies", err)
		return
	}
	deniedProxies, err := s.st.UserExternalDenies(u.ID)
	if err != nil {
		s.internalErr(w, "load denies", err)
		return
	}

	// Which fleet nodes their profiles actually reach.
	entitled := map[string]struct{}{}
	if pids, err := s.st.UserProfileIDs(u.ID); err == nil {
		for _, pid := range pids {
			if nids, err := s.st.ProfileNodeIDs(pid); err == nil {
				for _, nid := range nids {
					entitled[nid] = struct{}{}
				}
			}
		}
	}

	fleet := []nodeAccessEntry{}
	// Kept so an external node chained through a denied relay can name it.
	nodeName := map[string]string{}
	if nodes, err := s.st.ListNodes(); err == nil {
		for _, n := range nodes {
			_, denied := deniedNodes[n.ID]
			_, ok := entitled[n.ID]
			name := n.DisplayName
			if name == "" {
				name = n.Name
			}
			nodeName[n.ID] = name
			fleet = append(fleet, nodeAccessEntry{
				ID: n.ID, Name: name, Source: "fleet",
				Allowed: !denied, Entitled: ok,
			})
		}
	}

	ext := []nodeAccessEntry{}
	if subs, err := s.st.ListExternalSubs(); err == nil {
		for _, sub := range subs {
			proxies, err := s.st.ExternalProxies(sub.ID)
			if err != nil {
				continue
			}
			for _, p := range proxies {
				_, denied := deniedProxies[p.ID]
				// A relay this subscription will not carry — not entitled, or
				// denied — takes everything chained through it with it.
				var via string
				if p.ChainNodeID != "" {
					_, ok := entitled[p.ChainNodeID]
					_, no := deniedNodes[p.ChainNodeID]
					if !ok || no {
						via = nodeName[p.ChainNodeID]
						if via == "" {
							via = p.ChainNodeID
						}
					}
				}
				ext = append(ext, nodeAccessEntry{
					ID: p.ID, Name: p.Name, Source: sub.Name,
					// An external node reaches every subscriber unless denied;
					// there is no profile in between to be entitled by.
					Allowed: !denied, Entitled: sub.Enabled && p.Enabled,
					ChainedVia: via,
				})
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"fleet": fleet, "external": ext})
}

func (s *Server) setUserNodeAccess(w http.ResponseWriter, r *http.Request) {
	u, err := s.st.GetUser(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load user", err, "no such user")
		return
	}
	var req struct {
		DeniedNodes   []string `json:"denied_nodes"`
		DeniedProxies []string `json:"denied_proxies"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	if err := s.st.SetUserNodeAccess(u.ID, req.DeniedNodes, req.DeniedProxies); err != nil {
		s.internalErr(w, "set node access", err)
		return
	}
	// Nothing to push: the denial is applied when the subscription renders, so
	// the node's own config — which still carries the credential — is unchanged
	// on purpose. Denying a node hides it from a subscription; it does not
	// revoke the credential, and the console says so.
	s.audit(r, "user.node_access", "user", u.ID, u.Name,
		fmt.Sprintf("denied %d fleet, %d external", len(req.DeniedNodes), len(req.DeniedProxies)))
	w.WriteHeader(http.StatusNoContent)
}
