package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
)

// Per-node egress rules: which traffic leaves a node by which route. Every
// mutation re-applies the nodes it touches, because the feature IS lines in a
// node config — and when the landing is another of our nodes, that node needs
// re-applying too, to accept the dial credential.
func (s *Server) routeEgress(mux *http.ServeMux) {
	mux.Handle("GET /api/nodes/{id}/egress", s.requireAdmin(s.listEgress))
	mux.Handle("POST /api/nodes/{id}/egress", s.requireWrite(s.createEgress))
	mux.Handle("PUT /api/nodes/{id}/egress/order", s.requireWrite(s.reorderEgress))
	mux.Handle("PUT /api/egress/{ruleId}", s.requireWrite(s.updateEgress))
	mux.Handle("DELETE /api/egress/{ruleId}", s.requireWrite(s.deleteEgress))
}

type egressView struct {
	ID      string   `json:"id"`
	NodeID  string   `json:"node_id"`
	Label   string   `json:"label"`
	Domains []string `json:"domains"`
	IPs     []string `json:"ips"`

	TargetKind      string `json:"target_kind"`
	TargetProxyID   string `json:"target_proxy_id,omitempty"`
	TargetNodeID    string `json:"target_node_id,omitempty"`
	TargetProfileID string `json:"target_profile_id,omitempty"`
	// TargetName is what to show: the provider's or the node's label, so the
	// console renders a row without a second request.
	TargetName string `json:"target_name"`
	Enabled    bool   `json:"enabled"`
	// Problem is why this rule cannot be assembled, empty when it can.
	Problem string `json:"problem,omitempty"`
}

func orEmptyStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (s *Server) egressView(r store.EgressRule) egressView {
	v := egressView{
		ID: r.ID, NodeID: r.NodeID, Label: r.Label,
		Domains: orEmptyStrings(r.Domains), IPs: orEmptyStrings(r.IPs),
		TargetKind: r.TargetKind, TargetProxyID: r.TargetProxyID,
		TargetNodeID: r.TargetNodeID, TargetProfileID: r.TargetProfileID,
		Enabled: r.Enabled,
	}
	switch r.TargetKind {
	case store.EgressDirect:
		v.TargetName = "直连"
	case store.EgressExternal:
		if p, err := s.st.GetExternalProxy(r.TargetProxyID); err == nil {
			v.TargetName = p.Label()
		} else {
			v.Problem = "这个外部节点已经不存在了"
		}
	case store.EgressNode:
		if n, err := s.st.GetNode(r.TargetNodeID); err == nil {
			v.TargetName = nodeLabel(n)
		}
		v.Problem = s.egressProblem(r)
	}
	return v
}

// egressProblem states, in the operator's language, why a fleet landing will
// not work — both causes being things done on another page entirely.
func (s *Server) egressProblem(r store.EgressRule) string {
	bound := false
	if ids, err := s.st.ProfileNodeIDs(r.TargetProfileID); err == nil {
		for _, id := range ids {
			if id == r.TargetNodeID {
				bound = true
			}
		}
	}
	if !bound {
		return "目标节点已经不再绑定这个接入配置，拨不通"
	}
	kinds, err := s.st.ClientTemplateKinds(r.TargetProfileID)
	if err != nil {
		return ""
	}
	for _, k := range kinds {
		if k == "xray-json" {
			return ""
		}
	}
	return "这个接入配置没有 xray-json 客户端模板，节点无从拨号"
}

func (s *Server) listEgress(w http.ResponseWriter, r *http.Request) {
	rules, err := s.st.EgressRulesOn(r.PathValue("id"))
	if err != nil {
		s.internalErr(w, "list egress rules", err)
		return
	}
	out := make([]egressView, 0, len(rules))
	for _, rule := range rules {
		out = append(out, s.egressView(rule))
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": out})
}

func (s *Server) createEgress(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("id")
	if _, err := s.st.GetNode(nodeID); err != nil {
		s.notFoundOr(w, "load node", err, "no such node")
		return
	}
	var req struct {
		Label           string `json:"label"`
		Domains         string `json:"domains"`
		IPs             string `json:"ips"`
		TargetKind      string `json:"target_kind"`
		TargetProxyID   string `json:"target_proxy_id"`
		TargetNodeID    string `json:"target_node_id"`
		TargetProfileID string `json:"target_profile_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	req.Label = strings.TrimSpace(req.Label)
	if req.Label == "" {
		writeErr(w, http.StatusBadRequest, `"label" is required`)
		return
	}
	domains, err := store.ParseEgressDomains(req.Domains)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ips, err := store.ParseEgressIPs(req.IPs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// A rule with nothing to match would render as an Xray rule with no
	// destination condition, which matches EVERYTHING and silently takes the
	// whole node with it.
	if len(domains) == 0 && len(ips) == 0 {
		writeErr(w, http.StatusBadRequest, "至少要有一条匹配（geosite / geoip / 域名后缀 / CIDR）")
		return
	}

	rule := store.EgressRule{
		NodeID: nodeID, Label: req.Label, Domains: domains, IPs: ips,
		TargetKind: req.TargetKind, Enabled: true,
	}
	switch req.TargetKind {
	case store.EgressDirect:
	case store.EgressExternal:
		if _, err := s.st.GetExternalProxy(req.TargetProxyID); err != nil {
			writeErr(w, http.StatusBadRequest, "没有这个外部节点")
			return
		}
		rule.TargetProxyID = req.TargetProxyID
	case store.EgressNode:
		if req.TargetNodeID == nodeID {
			writeErr(w, http.StatusBadRequest, "落点不能是这个节点自己——那就是直连")
			return
		}
		if _, err := s.st.GetNode(req.TargetNodeID); err != nil {
			writeErr(w, http.StatusBadRequest, "没有这个节点")
			return
		}
		ids, err := s.st.ProfileNodeIDs(req.TargetProfileID)
		if err != nil {
			s.internalErr(w, "load profile nodes", err)
			return
		}
		bound := false
		for _, id := range ids {
			if id == req.TargetNodeID {
				bound = true
			}
		}
		if !bound {
			writeErr(w, http.StatusBadRequest, "目标节点没有绑定这个接入配置")
			return
		}
		secret, err := template.Generate(template.GenUUID)
		if err != nil {
			s.internalErr(w, "generate egress credential", err)
			return
		}
		rule.TargetNodeID, rule.TargetProfileID = req.TargetNodeID, req.TargetProfileID
		rule.Secret = secret.Components[""]
	default:
		writeErr(w, http.StatusBadRequest, `"target_kind" must be direct, external or node`)
		return
	}

	created, err := s.st.CreateEgressRule(rule)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "egress.create", "node", nodeID, created.Label,
		fmt.Sprintf("-> %s", created.TargetKind))
	s.applyEgressEnds(r, created)
	writeJSON(w, http.StatusCreated, s.egressView(created))
}

func (s *Server) updateEgress(w http.ResponseWriter, r *http.Request) {
	rule, err := s.st.GetEgressRule(r.PathValue("ruleId"))
	if err != nil {
		s.notFoundOr(w, "load egress rule", err, "no such rule")
		return
	}
	var req struct {
		Label   *string `json:"label"`
		Domains *string `json:"domains"`
		IPs     *string `json:"ips"`
		Enabled *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	label, domains, ips, enabled := rule.Label, rule.Domains, rule.IPs, rule.Enabled
	if req.Label != nil {
		if label = strings.TrimSpace(*req.Label); label == "" {
			writeErr(w, http.StatusBadRequest, `"label" cannot be empty`)
			return
		}
	}
	if req.Domains != nil {
		if domains, err = store.ParseEgressDomains(*req.Domains); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.IPs != nil {
		if ips, err = store.ParseEgressIPs(*req.IPs); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if len(domains) == 0 && len(ips) == 0 {
		writeErr(w, http.StatusBadRequest, "至少要有一条匹配（geosite / geoip / 域名后缀 / CIDR）")
		return
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if err := s.st.UpdateEgressRule(rule.ID, label, domains, ips, enabled); err != nil {
		s.notFoundOr(w, "update egress rule", err, "no such rule")
		return
	}
	rule.Label, rule.Domains, rule.IPs, rule.Enabled = label, domains, ips, enabled
	s.audit(r, "egress.update", "node", rule.NodeID, rule.Label, "")
	s.applyEgressEnds(r, rule)
	writeJSON(w, http.StatusOK, s.egressView(rule))
}

func (s *Server) deleteEgress(w http.ResponseWriter, r *http.Request) {
	rule, err := s.st.GetEgressRule(r.PathValue("ruleId"))
	if err != nil {
		s.notFoundOr(w, "load egress rule", err, "no such rule")
		return
	}
	if err := s.st.DeleteEgressRule(rule.ID); err != nil {
		s.notFoundOr(w, "delete egress rule", err, "no such rule")
		return
	}
	s.audit(r, "egress.delete", "node", rule.NodeID, rule.Label, "")
	s.applyEgressEnds(r, rule)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) reorderEgress(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("id")
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	if err := s.st.ReorderEgressRules(nodeID, req.IDs); err != nil {
		s.internalErr(w, "reorder egress rules", err)
		return
	}
	// Order IS priority — routing is first-match — so a reorder changes what
	// the node does and has to be pushed like any other change.
	s.audit(r, "egress.order", "node", nodeID, "", fmt.Sprintf("%d rules", len(req.IDs)))
	s.applyNodes(r, []string{nodeID})
	w.WriteHeader(http.StatusNoContent)
}

// applyEgressEnds re-applies the node carrying the rule and, for a fleet
// landing, the node being dialled — the latter has to accept the credential.
func (s *Server) applyEgressEnds(r *http.Request, rule store.EgressRule) {
	nodes := []string{rule.NodeID}
	if rule.TargetKind == store.EgressNode && rule.TargetNodeID != "" {
		nodes = append(nodes, rule.TargetNodeID)
	}
	s.applyNodes(r, nodes)
}
