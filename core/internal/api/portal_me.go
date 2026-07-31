package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/SayukiOvO/chiral/core/internal/portal"
)

// portalMe is the whole home page in one request.
//
// One round trip rather than four, and the per-node traffic is inlined rather
// than fetched per row, because the portal is a public read surface in front
// of a SQLite handle pinned to a single connection (store.Open sets
// MaxOpenConns(1)). N subscribers polling a 1+N page is a cheap way to
// contend with config assembly and traffic ingest for the write lock. For the
// same reason the page does not auto-refresh; pulling to refresh is the user's
// choice.
func (s *Server) portalMe(w http.ResponseWriter, r *http.Request, id portal.Identity) {
	v := s.portalView(id)

	account, err := v.Account()
	if err != nil {
		s.internalErr(w, "load account", err)
		return
	}
	nodes, err := v.Nodes()
	if err != nil {
		s.internalErr(w, "load nodes", err)
		return
	}
	sub, err := v.Subscription(s.portal.PublicURL)
	if err != nil {
		s.internalErr(w, "load subscription", err)
		return
	}

	body := map[string]any{
		"account":      account,
		"nodes":        nodes,
		"subscription": sub,
		// What the panel has switched on, so the page can leave out a section
		// rather than render an empty one that looks broken.
		"features": map[string]any{
			"devices": s.online != nil,
		},
	}
	choices, current, err := v.Rules()
	if err != nil {
		s.internalErr(w, "load rule choices", err)
		return
	}
	if len(choices) > 0 {
		body["rules"] = map[string]any{"choices": choices, "current": current}
	}

	if s.online != nil {
		devices, err := v.Devices()
		if err != nil {
			s.internalErr(w, "load devices", err)
			return
		}
		body["devices"] = devices
	}

	// Nothing here should sit in a shared cache: it is one person's account
	// state behind a bearer token.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, body)
}

// portalChooseRules lets a subscriber pick their own routing rules.
//
// Through the View, like everything else on this side: the user id comes from
// the authenticated identity and never from the request, so this handler
// cannot be aimed at somebody else's account even by accident.
func (s *Server) portalChooseRules(w http.ResponseWriter, r *http.Request, id portal.Identity) {
	var req struct {
		RulesetID string `json:"ruleset_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	if err := s.portalView(id).ChooseRules(strings.TrimSpace(req.RulesetID)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
