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
			v.Problem = "该外部节点已不存在"
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
		return "目标节点已不再绑定该接入配置，无法建立连接"
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
	return "该接入配置没有 xray-json 客户端模板，节点无法据此建立连接"
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
		writeErr(w, http.StatusBadRequest, "至少需要一条匹配条件（geosite / geoip / 域名后缀 / CIDR）")
		return
	}

	target, errMsg := s.resolveEgressTarget(nodeID, req.TargetKind,
		req.TargetProxyID, req.TargetNodeID, req.TargetProfileID)
	if errMsg != "" {
		writeErr(w, http.StatusBadRequest, errMsg)
		return
	}
	if target.Kind == store.EgressNode {
		secret, err := template.Generate(template.GenUUID)
		if err != nil {
			s.internalErr(w, "generate egress credential", err)
			return
		}
		target.Secret = secret.Components[""]
	}
	rule := store.EgressRule{
		NodeID: nodeID, Label: req.Label, Domains: domains, IPs: ips,
		TargetKind: target.Kind, TargetProxyID: target.ProxyID,
		TargetNodeID: target.NodeID, TargetProfileID: target.ProfileID,
		Secret: target.Secret, Enabled: true,
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
		// The landing may be changed too. Omitting target_kind leaves it
		// alone, so a caller that only edits the match cannot move a rule by
		// accident.
		TargetKind      *string `json:"target_kind"`
		TargetProxyID   string  `json:"target_proxy_id"`
		TargetNodeID    string  `json:"target_node_id"`
		TargetProfileID string  `json:"target_profile_id"`
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
		writeErr(w, http.StatusBadRequest, "至少需要一条匹配条件（geosite / geoip / 域名后缀 / CIDR）")
		return
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if err := s.st.UpdateEgressRule(rule.ID, label, domains, ips, enabled); err != nil {
		s.notFoundOr(w, "update egress rule", err, "no such rule")
		return
	}
	// Remember where it used to land: a rule that stops dialling a node
	// leaves a machine credential on that node until it is re-assembled.
	previous := rule
	if req.TargetKind != nil && *req.TargetKind != "" {
		target, errMsg := s.resolveEgressTarget(rule.NodeID, *req.TargetKind,
			req.TargetProxyID, req.TargetNodeID, req.TargetProfileID)
		if errMsg != "" {
			writeErr(w, http.StatusBadRequest, errMsg)
			return
		}
		// The dial credential is minted per rule and per landing. Moving to a
		// different node means a different credential; moving away from a
		// fleet landing means none at all.
		if target.Kind == store.EgressNode &&
			(previous.TargetKind != store.EgressNode || previous.TargetNodeID != target.NodeID ||
				previous.TargetProfileID != target.ProfileID) {
			secret, err := template.Generate(template.GenUUID)
			if err != nil {
				s.internalErr(w, "generate egress credential", err)
				return
			}
			target.Secret = secret.Components[""]
		} else if target.Kind == store.EgressNode {
			target.Secret = previous.Secret
		}
		if err := s.st.SetEgressTarget(rule.ID, target.Kind, target.ProxyID,
			target.NodeID, target.ProfileID, target.Secret); err != nil {
			s.internalErr(w, "set egress target", err)
			return
		}
	}
	updated, err := s.st.GetEgressRule(rule.ID)
	if err != nil {
		s.internalErr(w, "reload egress rule", err)
		return
	}
	s.audit(r, "egress.update", "node", updated.NodeID, updated.Label, "")
	s.applyEgressEnds(r, updated)
	// The node it used to dial has to drop the credential it no longer needs.
	if previous.TargetKind == store.EgressNode && previous.TargetNodeID != "" &&
		previous.TargetNodeID != updated.TargetNodeID {
		s.applyNodes(r, []string{previous.TargetNodeID})
	}
	writeJSON(w, http.StatusOK, s.egressView(updated))
}

// egressTarget is a validated landing for an egress rule.
type egressTarget struct {
	Kind      string
	ProxyID   string
	NodeID    string
	ProfileID string
	Secret    string
}

// resolveEgressTarget validates a landing, returning an operator-facing
// message rather than an error so create and update phrase refusals alike.
func (s *Server) resolveEgressTarget(onNode, kind, proxyID, nodeID, profileID string) (egressTarget, string) {
	switch kind {
	case store.EgressDirect:
		return egressTarget{Kind: kind}, ""
	case store.EgressExternal:
		if _, err := s.st.GetExternalProxy(proxyID); err != nil {
			return egressTarget{}, "指定的外部节点不存在"
		}
		return egressTarget{Kind: kind, ProxyID: proxyID}, ""
	case store.EgressNode:
		if nodeID == onNode {
			return egressTarget{}, "出口不能是本节点，该情形等同于直接出站"
		}
		if _, err := s.st.GetNode(nodeID); err != nil {
			return egressTarget{}, "指定的节点不存在"
		}
		ids, err := s.st.ProfileNodeIDs(profileID)
		if err != nil {
			return egressTarget{}, "无法读取该接入配置绑定的节点"
		}
		bound := false
		for _, id := range ids {
			if id == nodeID {
				bound = true
			}
		}
		if !bound {
			return egressTarget{}, "目标节点未绑定该接入配置"
		}
		if s.egressReaches(nodeID, onNode, map[string]bool{}) {
			return egressTarget{}, "该配置将形成环路：目标节点的出站规则最终指回本节点"
		}
		return egressTarget{Kind: kind, NodeID: nodeID, ProfileID: profileID}, ""
	}
	return egressTarget{}, `"target_kind" 必须是 direct、external 或 node`
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

// egressReaches reports whether traffic entering `from` can arrive at `to`
// by following enabled fleet landings.
func (s *Server) egressReaches(from, to string, seen map[string]bool) bool {
	if from == to {
		return true
	}
	if seen[from] {
		return false
	}
	seen[from] = true
	rules, err := s.st.EgressRulesOn(from)
	if err != nil {
		// Unknown means unproven, and an unproven loop is not a reason to
		// refuse: the render would still be valid, and xray on both ends
		// survives a rule that never fires.
		return false
	}
	for _, r := range rules {
		if r.Enabled && r.TargetKind == store.EgressNode && s.egressReaches(r.TargetNodeID, to, seen) {
			return true
		}
	}
	return false
}
