package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/SayukiOvO/chiral/core/internal/store"
)

// Subscriber groups: the operator says once what a class of subscriber gets.
//
// Every mutation here can change who holds a credential on a node, so it ends
// the same way a per-subscriber change does — by re-assembling the nodes
// involved. A grant that exists only in the database is a subscription naming
// a node that will refuse the connection.
func (s *Server) routeGroups(mux *http.ServeMux) {
	mux.Handle("GET /api/groups", s.requireAdmin(s.listGroups))
	mux.Handle("POST /api/groups", s.requireWrite(s.createGroup))
	mux.Handle("PUT /api/groups/{id}", s.requireWrite(s.updateGroup))
	mux.Handle("DELETE /api/groups/{id}", s.requireWrite(s.deleteGroup))
	mux.Handle("POST /api/groups/{id}/profiles/{profileId}", s.requireWrite(s.bindGroupProfile))
	mux.Handle("DELETE /api/groups/{id}/profiles/{profileId}", s.requireWrite(s.unbindGroupProfile))
	mux.Handle("GET /api/groups/{id}/nodes", s.requireAdmin(s.groupNodeAccess))
	mux.Handle("PUT /api/groups/{id}/nodes", s.requireWrite(s.setGroupNodeAccess))
	mux.Handle("PUT /api/groups/{id}/ruleset", s.requireWrite(s.setGroupRuleset))
	mux.Handle("PUT /api/users/{id}/group", s.requireWrite(s.setUserGroup))
	mux.Handle("PUT /api/users/{id}/profiles/{profileID}", s.requireWrite(s.setUserProfileAccess))
}

type groupView struct {
	store.SubscriberGroup
	ProfileIDs []string `json:"profile_ids"`
}

func (s *Server) groupView(g store.SubscriberGroup) groupView {
	v := groupView{SubscriberGroup: g, ProfileIDs: []string{}}
	if ids, err := s.st.GroupProfileIDs(g.ID); err == nil && ids != nil {
		v.ProfileIDs = ids
	}
	return v
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.st.ListSubscriberGroups()
	if err != nil {
		s.internalErr(w, "list groups", err)
		return
	}
	out := make([]groupView, 0, len(groups))
	for _, g := range groups {
		out = append(out, s.groupView(g))
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Note string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, `"name" is required`)
		return
	}
	g, err := s.st.CreateSubscriberGroup(req.Name, strings.TrimSpace(req.Note))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "该名称已被占用")
		return
	}
	s.audit(r, "group.create", "group", g.ID, g.Name, "")
	writeJSON(w, http.StatusCreated, s.groupView(g))
}

func (s *Server) updateGroup(w http.ResponseWriter, r *http.Request) {
	g, err := s.st.GetSubscriberGroup(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load group", err, "no such group")
		return
	}
	var req struct {
		Name string `json:"name"`
		Note string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, `"name" is required`)
		return
	}
	if err := s.st.UpdateSubscriberGroup(g.ID, name, strings.TrimSpace(req.Note)); err != nil {
		writeErr(w, http.StatusBadRequest, "该名称已被占用")
		return
	}
	s.audit(r, "group.update", "group", g.ID, name, "")
	updated, _ := s.st.GetSubscriberGroup(g.ID)
	writeJSON(w, http.StatusOK, s.groupView(updated))
}

// deleteGroup dissolves a group. Its members keep exactly what it decided —
// the store writes it into their own rows first — so this changes no
// subscription, and the console says as much.
func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	g, err := s.st.GetSubscriberGroup(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load group", err, "no such group")
		return
	}
	if err := s.st.DeleteSubscriberGroup(g.ID); err != nil {
		s.notFoundOr(w, "delete group", err, "no such group")
		return
	}
	s.audit(r, "group.delete", "group", g.ID, g.Name, fmt.Sprintf("%d members", g.Members))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) bindGroupProfile(w http.ResponseWriter, r *http.Request) {
	s.changeGroupProfile(w, r, true)
}

func (s *Server) unbindGroupProfile(w http.ResponseWriter, r *http.Request) {
	s.changeGroupProfile(w, r, false)
}

// changeGroupProfile grants or withdraws an access configuration for a whole
// group, then re-assembles the nodes that configuration reaches: the members'
// credentials live in those configs, and a grant nobody minted is a node that
// refuses the very subscription that names it.
func (s *Server) changeGroupProfile(w http.ResponseWriter, r *http.Request, grant bool) {
	groupID, profileID := r.PathValue("id"), r.PathValue("profileId")
	g, err := s.st.GetSubscriberGroup(groupID)
	if err != nil {
		s.notFoundOr(w, "load group", err, "no such group")
		return
	}
	if _, err := s.st.GetProfile(profileID); err != nil {
		s.notFoundOr(w, "load profile", err, "no such profile")
		return
	}
	members, err := s.st.GroupMemberIDs(groupID)
	if err != nil {
		s.internalErr(w, "list members", err)
		return
	}
	nodeIDs, err := s.st.ProfileNodeIDs(profileID)
	if err != nil {
		s.internalErr(w, "list profile nodes", err)
		return
	}
	if grant {
		err = s.st.BindGroupProfile(groupID, profileID)
	} else {
		err = s.st.UnbindGroupProfile(groupID, profileID)
		// The credentials that grant reached are per (subscriber, profile),
		// and they have to go before the configs are rewritten, or a reconnect
		// would restore what was just withdrawn.
		for _, uid := range members {
			if e := s.st.DeleteCredentialsForBinding(uid, profileID); e != nil && err == nil {
				err = e
			}
		}
	}
	if err != nil {
		s.internalErr(w, "change group profile", err)
		return
	}
	action := "group.profile.revoke"
	if grant {
		action = "group.profile.grant"
	}
	s.audit(r, action, "group", groupID, g.Name, profileID)
	s.reassemble(r.Context(), nodeIDs)
	for _, uid := range members {
		s.syncUserNow(r.Context(), uid)
	}
	w.WriteHeader(http.StatusNoContent)
}

// groupNodeAccess answers, for one group, the same question the per-subscriber
// view answers: every node, line and external node that exists, and whether
// this group holds it.
func (s *Server) groupNodeAccess(w http.ResponseWriter, r *http.Request) {
	g, err := s.st.GetSubscriberGroup(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load group", err, "no such group")
		return
	}
	deniedNodes, err := s.st.GroupNodeDenies(g.ID)
	if err != nil {
		s.internalErr(w, "load group denies", err)
		return
	}
	deniedRelays, err := s.st.GroupRelayDenies(g.ID)
	if err != nil {
		s.internalErr(w, "load group denies", err)
		return
	}
	deniedProxies, err := s.st.GroupExternalDenies(g.ID)
	if err != nil {
		s.internalErr(w, "load group denies", err)
		return
	}
	// Which fleet nodes the group's own grants reach. A member's personal
	// grants are theirs, not the group's, so they are not counted here.
	entitled := map[string]struct{}{}
	if pids, err := s.st.GroupProfileIDs(g.ID); err == nil {
		for _, pid := range pids {
			if nids, err := s.st.ProfileNodeIDs(pid); err == nil {
				for _, nid := range nids {
					entitled[nid] = struct{}{}
				}
			}
		}
	}

	nodeName := map[string]string{}
	fleet := []nodeAccessEntry{}
	if nodes, err := s.st.ListNodes(); err == nil {
		for _, n := range nodes {
			_, denied := deniedNodes[n.ID]
			_, reaches := entitled[n.ID]
			nodeName[n.ID] = nodeLabel(n)
			fleet = append(fleet, nodeAccessEntry{
				ID: n.ID, Name: nodeLabel(n), Source: "fleet",
				Allowed: !denied, Entitled: reaches,
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
				ext = append(ext, nodeAccessEntry{
					ID: p.ID, Name: p.Label(), Source: sub.Name,
					Allowed: !denied, Entitled: sub.Enabled && p.Enabled,
				})
			}
		}
	}
	relays := []nodeAccessEntry{}
	if all, err := s.st.ListNodeRelays(); err == nil {
		for _, rl := range all {
			_, denied := deniedRelays[rl.ID]
			_, reaches := entitled[rl.EntryNodeID]
			_, entryDenied := deniedNodes[rl.EntryNodeID]
			relays = append(relays, nodeAccessEntry{
				ID: rl.ID, Name: rl.Label,
				Source:   nodeName[rl.EntryNodeID] + " → " + nodeName[rl.ExitNodeID],
				Allowed:  !denied,
				Entitled: rl.Enabled && reaches && !entryDenied,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"fleet": fleet, "external": ext, "relay": relays})
}

func (s *Server) setGroupNodeAccess(w http.ResponseWriter, r *http.Request) {
	g, err := s.st.GetSubscriberGroup(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load group", err, "no such group")
		return
	}
	var req struct {
		DeniedNodes   []string `json:"denied_nodes"`
		DeniedProxies []string `json:"denied_proxies"`
		DeniedRelays  []string `json:"denied_relays"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	if err := s.st.SetGroupNodeAccess(g.ID, req.DeniedNodes, req.DeniedProxies, req.DeniedRelays); err != nil {
		s.internalErr(w, "set group node access", err)
		return
	}
	s.audit(r, "group.node_access", "group", g.ID, g.Name,
		fmt.Sprintf("denied %d fleet, %d external, %d relay",
			len(req.DeniedNodes), len(req.DeniedProxies), len(req.DeniedRelays)))
	// As for a subscriber: a denial changes what a subscription renders, not
	// what a node holds — except a relay, whose routing rule is built from the
	// emails allowed on it.
	for _, id := range s.relayEntryNodes() {
		if _, err := s.profiles.Apply(r.Context(), id); err != nil {
			s.logger.Warn("group access changed but relay entry not re-applied", "node", id, "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setGroupRuleset(w http.ResponseWriter, r *http.Request) {
	g, err := s.st.GetSubscriberGroup(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load group", err, "no such group")
		return
	}
	var req struct {
		RulesetID string `json:"ruleset_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	if req.RulesetID != "" {
		if _, err := s.st.GetRuleset(req.RulesetID); err != nil {
			writeErr(w, http.StatusBadRequest, "指定的规则集不存在")
			return
		}
	}
	if err := s.st.SetGroupRuleset(g.ID, req.RulesetID); err != nil {
		s.internalErr(w, "set group ruleset", err)
		return
	}
	s.audit(r, "group.ruleset", "group", g.ID, g.Name, req.RulesetID)
	w.WriteHeader(http.StatusNoContent)
}

// setUserGroup moves one subscriber in or out of a group.
//
// Joining DISCARDS their personal grants and denials — see the store for why
// keeping them would leave the group deciding nothing — so this is the request
// the console warns about before sending. Leaving keeps what the group decided.
func (s *Server) setUserGroup(w http.ResponseWriter, r *http.Request) {
	u, err := s.st.GetUser(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load user", err, "no such user")
		return
	}
	var req struct {
		GroupID string `json:"group_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	name := ""
	if req.GroupID != "" {
		g, err := s.st.GetSubscriberGroup(req.GroupID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "指定的用户组不存在")
			return
		}
		name = g.Name
	}
	// The nodes to re-assemble are those this subscriber's credentials live
	// on, before AND after: a move can both withdraw and grant, and each side
	// is a different set of nodes.
	nodeIDs := s.userCredentialNodes(u.ID)
	if err := s.st.SetUserGroup(u.ID, req.GroupID); err != nil {
		s.notFoundOr(w, "set user group", err, "no such user")
		return
	}
	nodeIDs = append(nodeIDs, s.userCredentialNodes(u.ID)...)
	// Credentials for a binding the subscriber no longer holds must go, or a
	// reconnect would restore access the move withdrew.
	if err := s.st.DeleteCredentialsNotEntitled(u.ID); err != nil {
		s.logger.Error("dropping credentials after a group change failed", "user", u.ID, "err", err)
	}
	s.audit(r, "user.group", "user", u.ID, u.Name, name)
	s.reassemble(r.Context(), nodeIDs)
	s.syncUserNow(r.Context(), u.ID)
	w.WriteHeader(http.StatusNoContent)
}

// userCredentialNodes lists the nodes this subscriber's access configurations
// currently reach.
func (s *Server) userCredentialNodes(userID string) []string {
	var out []string
	pids, err := s.st.UserProfileIDs(userID)
	if err != nil {
		return nil
	}
	for _, pid := range pids {
		if nids, err := s.st.ProfileNodeIDs(pid); err == nil {
			out = append(out, nids...)
		}
	}
	return out
}

// setUserProfileAccess is the three-state control a member of a group needs:
// granted to them personally, withheld from them despite their group, or
// neither — in which case the group decides.
//
// The two-endpoint form it replaces could not express the middle state, so a
// member of a group that grants a configuration had a chip that looked on,
// deleted a row that did not exist when clicked, and stayed on. A control that
// changes nothing is worse than one that is absent.
func (s *Server) setUserProfileAccess(w http.ResponseWriter, r *http.Request) {
	userID, profileID := r.PathValue("id"), r.PathValue("profileID")
	if _, err := s.st.GetUser(userID); err != nil {
		s.notFoundOr(w, "load user", err, "no such user")
		return
	}
	if _, err := s.st.GetProfile(profileID); err != nil {
		s.notFoundOr(w, "load profile", err, "no such profile")
		return
	}
	var req struct {
		State string `json:"state"` // grant | deny | inherit
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	grant, clear := false, false
	switch req.State {
	case "grant":
		grant = true
	case "deny":
	case "inherit":
		clear = true
	default:
		writeErr(w, http.StatusBadRequest, `"state" 必须是 grant、deny 或 inherit`)
		return
	}

	// Whether this ends in a grant decides the order: a withdrawal has to take
	// the user off the running kernels while the credentials still say where
	// they are installed.
	nodeIDs, err := s.st.ProfileNodeIDs(profileID)
	if err != nil {
		s.internalErr(w, "list profile nodes", err)
		return
	}
	held := s.holdsProfile(userID, profileID)
	if err := s.st.SetUserProfileAccess(userID, profileID, grant, clear); err != nil {
		s.internalErr(w, "set profile access", err)
		return
	}
	nowHeld := s.holdsProfile(userID, profileID)
	if held && !nowHeld {
		s.removeUserFromNodes(r.Context(), userID, profileID)
		if err := s.st.DeleteCredentialsForBinding(userID, profileID); err != nil {
			s.internalErr(w, "drop credentials", err)
			return
		}
	}
	s.audit(r, "user.profile."+req.State, "user", userID, "", profileID)
	s.reassemble(r.Context(), nodeIDs)
	if nowHeld {
		s.syncUserNow(r.Context(), userID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// holdsProfile reports whether the subscriber currently holds one access
// configuration, by whatever route.
func (s *Server) holdsProfile(userID, profileID string) bool {
	ids, err := s.st.UserProfileIDs(userID)
	if err != nil {
		return false
	}
	for _, id := range ids {
		if id == profileID {
			return true
		}
	}
	return false
}
