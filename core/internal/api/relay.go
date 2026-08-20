package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
)

// Relay lines: one node of this fleet reaching the internet through another.
//
// Write-level throughout except the listing, on the same reasoning as external
// nodes — a line holds a working credential, and creating or deleting one
// rewrites two machines' configs.
func (s *Server) routeRelays(mux *http.ServeMux) {
	mux.Handle("GET /api/relays", s.requireAdmin(s.listRelays))
	mux.Handle("POST /api/relays", s.requireWrite(s.createRelay))
	mux.Handle("PUT /api/relays/{id}", s.requireWrite(s.updateRelay))
	mux.Handle("DELETE /api/relays/{id}", s.requireWrite(s.deleteRelay))
	mux.Handle("GET /api/relays/{id}/users", s.requireAdmin(s.relayUsers))
	mux.Handle("PUT /api/relays/{id}/users", s.requireWrite(s.setRelayUsers))
}

type relayView struct {
	ID          string `json:"id"`
	EntryNodeID string `json:"entry_node_id"`
	ExitNodeID  string `json:"exit_node_id"`
	ProfileID   string `json:"profile_id"`
	Label       string `json:"label"`
	Enabled     bool   `json:"enabled"`
	// EntryName, ExitName and ProfileName save the console a second request
	// just to render a row that reads as a sentence.
	EntryName   string  `json:"entry_name"`
	ExitName    string  `json:"exit_name"`
	ProfileName string  `json:"profile_name"`
	TrafficRate float64 `json:"traffic_rate"`
	// Problem is why this line cannot currently be assembled, empty when it
	// can. Both causes are things an operator does elsewhere and would not
	// connect to this page — unbinding the profile from the exit, deleting the
	// xray-json template — and the symptom without this is a line that renders
	// into every subscription and never connects.
	Problem string `json:"problem,omitempty"`
}

// The secret is deliberately not in the view. It is the one credential in this
// panel that nobody ever has to type: both ends are ours, and both get it from
// an assembled config. Showing it would add an exposure with no use.

func (s *Server) relayView(r store.NodeRelay) relayView {
	v := relayView{
		ID: r.ID, EntryNodeID: r.EntryNodeID, ExitNodeID: r.ExitNodeID,
		ProfileID: r.ProfileID, Label: r.Label, Enabled: r.Enabled,
		TrafficRate: r.TrafficRate,
	}
	if n, err := s.st.GetNode(r.EntryNodeID); err == nil {
		v.EntryName = nodeLabel(n)
	}
	if n, err := s.st.GetNode(r.ExitNodeID); err == nil {
		v.ExitName = nodeLabel(n)
	}
	if p, err := s.st.GetProfile(r.ProfileID); err == nil {
		v.ProfileName = p.Name
	}
	v.Problem = s.relayProblem(r)
	return v
}

// relayProblem states, in the operator's language, why a line will not work.
func (s *Server) relayProblem(r store.NodeRelay) string {
	bound := false
	if nodeIDs, err := s.st.ProfileNodeIDs(r.ProfileID); err == nil {
		for _, id := range nodeIDs {
			if id == r.ExitNodeID {
				bound = true
			}
		}
	}
	if !bound {
		return "出口节点已不再绑定该接入配置，该线路无法建立连接"
	}
	kinds, err := s.st.ClientTemplateKinds(r.ProfileID)
	if err != nil {
		return ""
	}
	for _, k := range kinds {
		if k == "xray-json" {
			return ""
		}
	}
	// The entry dials the exit with the same artefact a customer's Xray client
	// would use, which is the only way to avoid a second, drifting copy of
	// "how to dial this inbound". Without it there is nothing to dial with.
	return "该接入配置没有 xray-json 客户端模板，入口节点无法据此建立连接"
}

func nodeLabel(n store.Node) string {
	if n.DisplayName != "" {
		return n.DisplayName
	}
	return n.Name
}

func (s *Server) listRelays(w http.ResponseWriter, r *http.Request) {
	relays, err := s.st.ListNodeRelays()
	if err != nil {
		s.internalErr(w, "list relays", err)
		return
	}
	out := make([]relayView, 0, len(relays))
	for _, rl := range relays {
		out = append(out, s.relayView(rl))
	}
	writeJSON(w, http.StatusOK, map[string]any{"relays": out})
}

func (s *Server) createRelay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EntryNodeID string  `json:"entry_node_id"`
		ExitNodeID  string  `json:"exit_node_id"`
		ProfileID   string  `json:"profile_id"`
		Label       string  `json:"label"`
		TrafficRate float64 `json:"traffic_rate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	req.Label = strings.TrimSpace(req.Label)
	if req.EntryNodeID == "" || req.ExitNodeID == "" || req.ProfileID == "" {
		writeErr(w, http.StatusBadRequest, `"entry_node_id", "exit_node_id" and "profile_id" are required`)
		return
	}
	// A node cannot relay through itself. Xray would accept the config and the
	// node would dial its own inbound, which either loops or fails depending
	// on the transport — neither of which is what anyone meant.
	if req.EntryNodeID == req.ExitNodeID {
		writeErr(w, http.StatusBadRequest, "入口节点与出口节点不能相同")
		return
	}
	if req.Label == "" {
		writeErr(w, http.StatusBadRequest, `"label" is required`)
		return
	}
	// The exit has to actually serve the profile the entry is going to dial.
	// Without this the line assembles into an outbound aimed at an inbound
	// that does not exist, and the only symptom is a proxy that never
	// connects.
	nodeIDs, err := s.st.ProfileNodeIDs(req.ProfileID)
	if err != nil {
		s.internalErr(w, "load profile nodes", err)
		return
	}
	bound := false
	for _, id := range nodeIDs {
		if id == req.ExitNodeID {
			bound = true
		}
	}
	if !bound {
		writeErr(w, http.StatusBadRequest, "出口节点未绑定该接入配置")
		return
	}
	secret, err := template.Generate(template.GenUUID)
	if err != nil {
		s.internalErr(w, "generate relay credential", err)
		return
	}
	rl, err := s.st.CreateNodeRelay(store.NodeRelay{
		EntryNodeID: req.EntryNodeID, ExitNodeID: req.ExitNodeID,
		ProfileID: req.ProfileID, Label: req.Label, Enabled: true,
		Secret: secret.Components[""], TrafficRate: req.TrafficRate,
	})
	if err != nil {
		if isConflict(err) {
			writeErr(w, http.StatusConflict, "这两个节点之间已存在一条使用该接入配置的线路")
			return
		}
		s.internalErr(w, "create relay", err)
		return
	}
	s.audit(r, "relay.create", "relay", rl.ID, rl.Label,
		fmt.Sprintf("%s -> %s", rl.EntryNodeID, rl.ExitNodeID))
	// Both ends change: the exit gains a client, the entry gains an outbound.
	s.applyRelayEnds(r, rl)
	writeJSON(w, http.StatusCreated, s.relayView(rl))
}

func (s *Server) updateRelay(w http.ResponseWriter, r *http.Request) {
	rl, err := s.st.GetNodeRelay(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load relay", err, "no such relay")
		return
	}
	var req struct {
		Label       *string  `json:"label"`
		Enabled     *bool    `json:"enabled"`
		TrafficRate *float64 `json:"traffic_rate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	label, enabled, rate := rl.Label, rl.Enabled, rl.TrafficRate
	if req.Label != nil {
		if label = strings.TrimSpace(*req.Label); label == "" {
			writeErr(w, http.StatusBadRequest, `"label" cannot be empty`)
			return
		}
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if req.TrafficRate != nil {
		if *req.TrafficRate <= 0 {
			writeErr(w, http.StatusBadRequest, "倍率必须大于 0")
			return
		}
		rate = *req.TrafficRate
	}
	if err := s.st.UpdateNodeRelay(rl.ID, label, enabled, rate); err != nil {
		s.notFoundOr(w, "update relay", err, "no such relay")
		return
	}
	rl.Label, rl.Enabled, rl.TrafficRate = label, enabled, rate
	s.audit(r, "relay.update", "relay", rl.ID, rl.Label, "")
	// A disabled line has to leave both configs, so this re-applies either way.
	s.applyRelayEnds(r, rl)
	writeJSON(w, http.StatusOK, s.relayView(rl))
}

func (s *Server) deleteRelay(w http.ResponseWriter, r *http.Request) {
	rl, err := s.st.GetNodeRelay(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load relay", err, "no such relay")
		return
	}
	if err := s.st.DeleteNodeRelay(rl.ID); err != nil {
		s.notFoundOr(w, "delete relay", err, "no such relay")
		return
	}
	s.audit(r, "relay.delete", "relay", rl.ID, rl.Label, "")
	s.applyRelayEnds(r, rl)
	w.WriteHeader(http.StatusNoContent)
}

// applyRelayEnds re-assembles the two nodes a line touches.
//
// Best-effort, and deliberately not fatal to the request that caused it: the
// row is already written, and heartbeat reconciliation pushes the current
// config as soon as a node that was offline comes back. Failing the API call
// here would tell the operator the line was not created when it was.
func (s *Server) applyRelayEnds(r *http.Request, rl store.NodeRelay) {
	for _, id := range []string{rl.ExitNodeID, rl.EntryNodeID} {
		if _, err := s.profiles.Apply(r.Context(), id); err != nil {
			s.logger.Warn("relay changed but node not re-applied",
				"relay", rl.ID, "node", id, "err", err)
		}
	}
}

// relayUsers answers "who may take this line", the same relation the user page
// writes from the other end.
func (s *Server) relayUsers(w http.ResponseWriter, r *http.Request) {
	rl, err := s.st.GetNodeRelay(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load relay", err, "no such relay")
		return
	}
	denied, err := s.st.NodeRelayDenies(rl.ID)
	if err != nil {
		s.internalErr(w, "load relay denies", err)
		return
	}
	users, err := s.st.ListUsers()
	if err != nil {
		s.internalErr(w, "list users", err)
		return
	}
	// Entitled here means "holds a profile that reaches the entry node", since
	// that is where they would connect. Someone who does not is shown but
	// cannot be helped by allowing them.
	entitled := map[string]bool{}
	for _, u := range users {
		pids, err := s.st.UserProfileIDs(u.ID)
		if err != nil {
			continue
		}
		for _, pid := range pids {
			nids, err := s.st.ProfileNodeIDs(pid)
			if err != nil {
				continue
			}
			for _, nid := range nids {
				if nid == rl.EntryNodeID {
					entitled[u.ID] = true
				}
			}
		}
	}
	out := []map[string]any{}
	for _, u := range users {
		_, no := denied[u.ID]
		out = append(out, map[string]any{
			"id": u.ID, "name": u.Name, "allowed": !no, "entitled": entitled[u.ID],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (s *Server) setRelayUsers(w http.ResponseWriter, r *http.Request) {
	rl, err := s.st.GetNodeRelay(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load relay", err, "no such relay")
		return
	}
	var req struct {
		DeniedUsers []string `json:"denied_users"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	if err := s.st.SetNodeRelayAccess(rl.ID, req.DeniedUsers); err != nil {
		s.internalErr(w, "set relay access", err)
		return
	}
	s.audit(r, "relay.access", "relay", rl.ID, rl.Label, "")
	// Unlike denying a node, this changes the entry's config: the routing rule
	// that sends a subscriber down this line is built from the emails of the
	// people allowed on it, so someone newly allowed has no rule until the
	// entry is re-applied.
	if _, err := s.profiles.Apply(r.Context(), rl.EntryNodeID); err != nil {
		s.logger.Warn("relay access changed but entry not re-applied",
			"relay", rl.ID, "node", rl.EntryNodeID, "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// relayEntryNodes is every node that starts a line.
//
// All of them, not just the ones named in a request: permissions are written
// as a whole-set replacement, so a line absent from the denied list is one
// somebody may have just been allowed onto.
func (s *Server) relayEntryNodes() []string {
	relays, err := s.st.ListNodeRelays()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, rl := range relays {
		if !rl.Enabled || seen[rl.EntryNodeID] {
			continue
		}
		seen[rl.EntryNodeID] = true
		out = append(out, rl.EntryNodeID)
	}
	return out
}
