package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/SayukiOvO/chiral/core/internal/store"
)

// Restricted destinations: networks some nodes reach that most subscribers
// must not. Reads are admin; every mutation is write-level and re-applies the
// nodes it changes, because the whole feature IS lines in node configs.
func (s *Server) routeRestricted(mux *http.ServeMux) {
	mux.Handle("GET /api/restricted", s.requireAdmin(s.listRestricted))
	mux.Handle("POST /api/restricted", s.requireWrite(s.createRestricted))
	mux.Handle("PUT /api/restricted/{id}", s.requireWrite(s.updateRestricted))
	mux.Handle("DELETE /api/restricted/{id}", s.requireWrite(s.deleteRestricted))
	mux.Handle("PUT /api/restricted/{id}/nodes", s.requireWrite(s.setRestrictedNodes))
	mux.Handle("PUT /api/restricted/{id}/users", s.requireWrite(s.setRestrictedUsers))
}

type restrictedView struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	CIDRs   []string `json:"cidrs"`
	Domains []string `json:"domains"`
	// NodeIDs are where the network exists; enforcement also lands on relay
	// entries automatically, which the console explains rather than lists.
	NodeIDs []string `json:"node_ids"`
	// AllowedUserIDs may go there; everyone else is blackholed.
	AllowedUserIDs []string `json:"allowed_user_ids"`
}

func restrictedViewOf(d store.RestrictedDestination) restrictedView {
	return restrictedView{
		ID: d.ID, Name: d.Name, CIDRs: d.CIDRs, Domains: d.Domains,
		NodeIDs: d.NodeIDs, AllowedUserIDs: d.AllowedUserIDs,
	}
}

func (s *Server) listRestricted(w http.ResponseWriter, r *http.Request) {
	dests, err := s.st.ListRestrictedDestinations()
	if err != nil {
		s.internalErr(w, "list restricted destinations", err)
		return
	}
	out := make([]restrictedView, 0, len(dests))
	for _, d := range dests {
		out = append(out, restrictedViewOf(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"destinations": out})
}

// parseRestrictedBody validates the operator's CIDR and domain text into the
// stored form, refusing shapes the routing rule would silently mismatch.
func parseRestrictedBody(name, cidrs, domains string) (string, []string, []string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil, nil, fmt.Errorf(`"name" is required`)
	}
	cs, err := store.ParseCIDRLines(cidrs)
	if err != nil {
		return "", nil, nil, err
	}
	ds, err := store.ParseDomainLines(domains)
	if err != nil {
		return "", nil, nil, err
	}
	if len(cs) == 0 && len(ds) == 0 {
		return "", nil, nil, fmt.Errorf("至少要有一个 IP 段或域名后缀")
	}
	return name, cs, ds, nil
}

func (s *Server) createRestricted(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		CIDRs   string `json:"cidrs"`
		Domains string `json:"domains"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	name, cs, ds, err := parseRestrictedBody(req.Name, req.CIDRs, req.Domains)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	d, err := s.st.CreateRestrictedDestination(name, cs, ds)
	if err != nil {
		s.internalErr(w, "create restricted destination", err)
		return
	}
	// Nothing to apply yet: a fresh destination is enforced on no node and
	// allows nobody, which are the next two decisions, made separately.
	s.audit(r, "restricted.create", "restricted", d.ID, d.Name, "")
	writeJSON(w, http.StatusCreated, restrictedViewOf(d))
}

func (s *Server) updateRestricted(w http.ResponseWriter, r *http.Request) {
	d, err := s.st.GetRestrictedDestination(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load restricted destination", err, "no such destination")
		return
	}
	var req struct {
		Name    string `json:"name"`
		CIDRs   string `json:"cidrs"`
		Domains string `json:"domains"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	name, cs, ds, err := parseRestrictedBody(req.Name, req.CIDRs, req.Domains)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.st.UpdateRestrictedDestination(d.ID, name, cs, ds); err != nil {
		s.notFoundOr(w, "update restricted destination", err, "no such destination")
		return
	}
	s.audit(r, "restricted.update", "restricted", d.ID, name, "")
	s.applyRestricted(r, d.ID)
	d, _ = s.st.GetRestrictedDestination(d.ID)
	writeJSON(w, http.StatusOK, restrictedViewOf(d))
}

func (s *Server) deleteRestricted(w http.ResponseWriter, r *http.Request) {
	d, err := s.st.GetRestrictedDestination(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load restricted destination", err, "no such destination")
		return
	}
	// The affected nodes, captured before the rows cascade away.
	nodes, _ := s.profiles.RestrictedNodeIDs(d.ID)
	if err := s.st.DeleteRestrictedDestination(d.ID); err != nil {
		s.notFoundOr(w, "delete restricted destination", err, "no such destination")
		return
	}
	s.audit(r, "restricted.delete", "restricted", d.ID, d.Name, "")
	s.applyNodes(r, nodes)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setRestrictedNodes(w http.ResponseWriter, r *http.Request) {
	d, err := s.st.GetRestrictedDestination(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load restricted destination", err, "no such destination")
		return
	}
	var req struct {
		NodeIDs []string `json:"node_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	// Nodes leaving the scope need their rules removed as much as nodes
	// entering need them added — affected is the union of before and after.
	before, _ := s.profiles.RestrictedNodeIDs(d.ID)
	if err := s.st.SetRestrictedDestinationNodes(d.ID, req.NodeIDs); err != nil {
		s.internalErr(w, "set restricted nodes", err)
		return
	}
	after, _ := s.profiles.RestrictedNodeIDs(d.ID)
	s.audit(r, "restricted.nodes", "restricted", d.ID, d.Name,
		fmt.Sprintf("%d nodes", len(req.NodeIDs)))
	s.applyNodes(r, union(before, after))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setRestrictedUsers(w http.ResponseWriter, r *http.Request) {
	d, err := s.st.GetRestrictedDestination(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load restricted destination", err, "no such destination")
		return
	}
	var req struct {
		AllowedUserIDs []string `json:"allowed_user_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	if err := s.st.SetRestrictedDestinationAllows(d.ID, req.AllowedUserIDs); err != nil {
		s.internalErr(w, "set restricted allows", err)
		return
	}
	s.audit(r, "restricted.users", "restricted", d.ID, d.Name,
		fmt.Sprintf("%d allowed", len(req.AllowedUserIDs)))
	s.applyRestricted(r, d.ID)
	w.WriteHeader(http.StatusNoContent)
}

// applyRestricted re-assembles every node a destination's rules live on.
func (s *Server) applyRestricted(r *http.Request, destID string) {
	nodes, err := s.profiles.RestrictedNodeIDs(destID)
	if err != nil {
		s.logger.Warn("restricted destination changed but affected nodes unknown", "dest", destID, "err", err)
		return
	}
	s.applyNodes(r, nodes)
}

// applyNodes is best-effort, like every other config propagation here: the
// rows are written, and heartbeat reconciliation delivers to any node that is
// offline right now.
func (s *Server) applyNodes(r *http.Request, nodeIDs []string) {
	for _, id := range nodeIDs {
		if _, err := s.profiles.Apply(r.Context(), id); err != nil {
			s.logger.Warn("restricted destination changed but node not re-applied", "node", id, "err", err)
		}
	}
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range append(a, b...) {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
