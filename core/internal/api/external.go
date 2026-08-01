package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/SayukiOvO/chiral/core/internal/store"
)

// External subscriptions are nodes this panel does not run. Every route is
// write-level except the listing: the body of one is a working credential for
// somebody else's service.
func (s *Server) routeExternals(mux *http.ServeMux) {
	mux.Handle("GET /api/externals", s.requireAdmin(s.listExternals))
	mux.Handle("POST /api/externals", s.requireWrite(s.createExternal))
	mux.Handle("PUT /api/externals/{id}", s.requireWrite(s.updateExternal))
	mux.Handle("DELETE /api/externals/{id}", s.requireWrite(s.deleteExternal))
	mux.Handle("POST /api/externals/{id}/refresh", s.requireWrite(s.refreshExternal))
	mux.Handle("PUT /api/externals/{id}/proxies/{proxyId}", s.requireWrite(s.setExternalProxy))
	mux.Handle("GET /api/externals/{id}/proxies/{proxyId}/users", s.requireAdmin(s.externalProxyUsers))
	mux.Handle("PUT /api/externals/{id}/proxies/{proxyId}/users", s.requireWrite(s.setExternalProxyUsers))
}

type externalProxyView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Server string `json:"server"`
	Port   int    `json:"port"`
	// ChainNodeID is the fleet node this one is dialled through and
	// ChainProxyID another external node; at most one is set, and both empty
	// means a direct dial.
	ChainNodeID  string `json:"chain_node_id"`
	ChainProxyID string `json:"chain_proxy_id"`
	Enabled      bool   `json:"enabled"`
}

type externalView struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	URL       string              `json:"url"`
	Enabled   bool                `json:"enabled"`
	FetchedAt int64               `json:"fetched_at"`
	LastError string              `json:"last_error"`
	Proxies   []externalProxyView `json:"proxies"`
}

func (s *Server) externalView(e store.ExternalSub) externalView {
	v := externalView{
		ID: e.ID, Name: e.Name, URL: e.URL, Enabled: e.Enabled,
		FetchedAt: e.FetchedAt.Int64, LastError: e.LastError,
		Proxies: []externalProxyView{},
	}
	proxies, err := s.st.ExternalProxies(e.ID)
	if err != nil {
		return v
	}
	for _, p := range proxies {
		v.Proxies = append(v.Proxies, externalProxyView{
			ID: p.ID, Name: p.Name, Type: p.Type, Server: p.Server, Port: p.Port,
			ChainNodeID: p.ChainNodeID, ChainProxyID: p.ChainProxyID, Enabled: p.Enabled,
		})
	}
	return v
}

func (s *Server) listExternals(w http.ResponseWriter, r *http.Request) {
	subs, err := s.st.ListExternalSubs()
	if err != nil {
		s.internalErr(w, "list external subscriptions", err)
		return
	}
	views := make([]externalView, 0, len(subs))
	for _, e := range subs {
		views = append(views, s.externalView(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"externals": views})
}

func (s *Server) createExternal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		URL  string `json:"url"`
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	req.Name, req.URL, req.Body = strings.TrimSpace(req.Name), strings.TrimSpace(req.URL), strings.TrimSpace(req.Body)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, `"name" is required`)
		return
	}
	if req.URL == "" && req.Body == "" {
		writeErr(w, http.StatusBadRequest, `give either a "url" to fetch or a "body" to parse`)
		return
	}
	if req.URL != "" && !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		writeErr(w, http.StatusBadRequest, `"url" must be an http(s) URL`)
		return
	}

	e, err := s.st.CreateExternalSub(req.Name, req.URL, req.Body)
	if err != nil {
		if isConflict(err) {
			writeErr(w, http.StatusConflict, "a source with that name already exists")
			return
		}
		s.internalErr(w, "create external subscription", err)
		return
	}
	// Parsed straight away, so an operator learns here that the link gives
	// nothing usable rather than from a subscriber later.
	if s.externals != nil {
		if err := s.externals.Refresh(r.Context(), e.ID); err != nil {
			s.logger.Warn("initial external fetch failed", "name", e.Name, "err", err)
		}
		e, _ = s.st.GetExternalSub(e.ID)
	}
	s.audit(r, "external.create", "external", e.ID, e.Name, "")
	writeJSON(w, http.StatusCreated, s.externalView(e))
}

func (s *Server) updateExternal(w http.ResponseWriter, r *http.Request) {
	e, err := s.st.GetExternalSub(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load external subscription", err, "no such source")
		return
	}
	var req struct {
		Name    *string `json:"name"`
		URL     *string `json:"url"`
		Enabled *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		e.Name = strings.TrimSpace(*req.Name)
	}
	if req.URL != nil {
		e.URL = strings.TrimSpace(*req.URL)
	}
	if req.Enabled != nil {
		e.Enabled = *req.Enabled
	}
	if err := s.st.UpdateExternalSub(e.ID, e.Name, e.URL, e.Enabled); err != nil {
		if isConflict(err) {
			writeErr(w, http.StatusConflict, "a source with that name already exists")
			return
		}
		s.internalErr(w, "update external subscription", err)
		return
	}
	s.audit(r, "external.update", "external", e.ID, e.Name, "")
	e, _ = s.st.GetExternalSub(e.ID)
	writeJSON(w, http.StatusOK, s.externalView(e))
}

func (s *Server) deleteExternal(w http.ResponseWriter, r *http.Request) {
	e, err := s.st.GetExternalSub(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load external subscription", err, "no such source")
		return
	}
	if err := s.st.DeleteExternalSub(e.ID); err != nil {
		s.notFoundOr(w, "delete external subscription", err, "no such source")
		return
	}
	s.audit(r, "external.delete", "external", e.ID, e.Name, "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) refreshExternal(w http.ResponseWriter, r *http.Request) {
	if s.externals == nil {
		writeErr(w, http.StatusServiceUnavailable, "external subscriptions are not configured on this panel")
		return
	}
	e, err := s.st.GetExternalSub(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load external subscription", err, "no such source")
		return
	}
	// A failure is reported through the stored error, not as a 500: the
	// previous proxies keep serving and the operator needs the reason.
	_ = s.externals.Refresh(r.Context(), e.ID)
	s.audit(r, "external.refresh", "external", e.ID, e.Name, "")
	e, _ = s.st.GetExternalSub(e.ID)
	writeJSON(w, http.StatusOK, s.externalView(e))
}

func (s *Server) setExternalProxy(w http.ResponseWriter, r *http.Request) {
	if _, err := s.st.GetExternalSub(r.PathValue("id")); err != nil {
		s.notFoundOr(w, "load external subscription", err, "no such source")
		return
	}
	var req struct {
		ChainNodeID  *string `json:"chain_node_id"`
		ChainProxyID *string `json:"chain_proxy_id"`
		Enabled      *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	proxies, err := s.st.ExternalProxies(r.PathValue("id"))
	if err != nil {
		s.internalErr(w, "load proxies", err)
		return
	}
	var cur store.ExternalProxy
	for _, p := range proxies {
		if p.ID == r.PathValue("proxyId") {
			cur = p
		}
	}
	if cur.ID == "" {
		writeErr(w, http.StatusNotFound, "no such proxy")
		return
	}
	// The two targets are one setting with two kinds of value, so naming either
	// one clears the other — otherwise switching a proxy from a fleet relay to
	// an external one would leave both set, and the store would refuse it.
	chainNode, chainProxy, enabled := cur.ChainNodeID, cur.ChainProxyID, cur.Enabled
	if req.ChainNodeID != nil {
		chainNode = strings.TrimSpace(*req.ChainNodeID)
		if chainNode != "" {
			chainProxy = ""
			if _, err := s.st.GetNode(chainNode); err != nil {
				s.notFoundOr(w, "load chain node", err, "no such node")
				return
			}
		}
	}
	if req.ChainProxyID != nil {
		chainProxy = strings.TrimSpace(*req.ChainProxyID)
		if chainProxy != "" {
			chainNode = ""
			if _, err := s.st.GetExternalProxy(chainProxy); err != nil {
				s.notFoundOr(w, "load chain proxy", err, "no such external node")
				return
			}
		}
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if err := s.st.SetExternalProxy(cur.ID, chainNode, chainProxy, enabled); err != nil {
		if errors.Is(err, store.ErrChainCycle) {
			// 409 rather than 400: the request is well-formed, it is the
			// resulting graph that cannot exist.
			writeErr(w, http.StatusConflict, "that would make the chain loop back on itself")
			return
		}
		s.internalErr(w, "update proxy", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type proxyUserEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Allowed is false when this subscriber has been denied this node.
	Allowed bool `json:"allowed"`
}

// externalProxyUsers answers "who gets this node" — the same relation the user
// page reads the other way round. An operator who has just added a source is
// thinking about the node, not about each subscriber in turn, and making them
// open every user to find out was the wrong shape.
func (s *Server) externalProxyUsers(w http.ResponseWriter, r *http.Request) {
	p, err := s.st.GetExternalProxy(r.PathValue("proxyId"))
	if err != nil {
		s.notFoundOr(w, "load proxy", err, "no such external node")
		return
	}
	denied, err := s.st.ExternalProxyDenies(p.ID)
	if err != nil {
		s.internalErr(w, "load denies", err)
		return
	}
	users, err := s.st.ListUsers()
	if err != nil {
		s.internalErr(w, "list users", err)
		return
	}
	out := make([]proxyUserEntry, 0, len(users))
	for _, u := range users {
		_, no := denied[u.ID]
		out = append(out, proxyUserEntry{ID: u.ID, Name: u.Name, Allowed: !no})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (s *Server) setExternalProxyUsers(w http.ResponseWriter, r *http.Request) {
	p, err := s.st.GetExternalProxy(r.PathValue("proxyId"))
	if err != nil {
		s.notFoundOr(w, "load proxy", err, "no such external node")
		return
	}
	var req struct {
		DeniedUsers []string `json:"denied_users"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	if err := s.st.SetExternalProxyAccess(p.ID, req.DeniedUsers); err != nil {
		s.internalErr(w, "set proxy access", err)
		return
	}
	s.audit(r, "external.access", "external", p.ID, p.Name, "")
	w.WriteHeader(http.StatusNoContent)
}
