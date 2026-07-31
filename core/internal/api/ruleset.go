package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/SayukiOvO/chiral/core/internal/ruleset"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

// Rulesets are the routing configurations clash-family subscriptions are
// rendered against: an ACL4SSR preset, or an .ini the operator points at.
//
// Reads are admin, writes are write-level, and refreshing is a write because
// it replaces what every subscriber on that ruleset receives.
func (s *Server) routeRulesets(mux *http.ServeMux) {
	mux.Handle("GET /api/rulesets", s.requireAdmin(s.listRulesets))
	mux.Handle("GET /api/rulesets/presets", s.requireAdmin(s.listPresets))
	mux.Handle("POST /api/rulesets", s.requireWrite(s.createRuleset))
	mux.Handle("PUT /api/rulesets/{id}", s.requireWrite(s.updateRuleset))
	mux.Handle("DELETE /api/rulesets/{id}", s.requireWrite(s.deleteRuleset))
	mux.Handle("POST /api/rulesets/{id}/refresh", s.requireWrite(s.refreshRuleset))
	mux.Handle("PUT /api/users/{id}/ruleset", s.requireWrite(s.setUserRuleset))
}

type rulesetView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Preset    string `json:"preset"`
	URL       string `json:"url"`
	FetchedAt int64  `json:"fetched_at"`
	LastError string `json:"last_error"`
	// Groups and Rules describe what was actually fetched, so the console can
	// show that a ruleset is loaded rather than merely configured.
	Groups int `json:"groups"`
	Rules  int `json:"rules"`
	Lists  int `json:"lists"`
}

func toRulesetView(r store.Ruleset) rulesetView {
	v := rulesetView{
		ID: r.ID, Name: r.Name, Preset: r.Preset, URL: r.URL,
		FetchedAt: r.FetchedAt.Int64, LastError: r.LastError,
	}
	if r.INI != "" {
		if cfg, err := ruleset.ParseINI(r.INI); err == nil {
			v.Groups, v.Rules, v.Lists = len(cfg.Groups), len(cfg.Rules), len(cfg.ListURLs())
		}
	}
	return v
}

func (s *Server) listRulesets(w http.ResponseWriter, r *http.Request) {
	sets, err := s.st.ListRulesets()
	if err != nil {
		s.internalErr(w, "list rulesets", err)
		return
	}
	views := make([]rulesetView, 0, len(sets))
	for _, rs := range sets {
		views = append(views, toRulesetView(rs))
	}
	writeJSON(w, http.StatusOK, map[string]any{"rulesets": views})
}

func (s *Server) listPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"presets": ruleset.Presets()})
}

func (s *Server) createRuleset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Preset string `json:"preset"`
		URL    string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	req.Name, req.Preset, req.URL = strings.TrimSpace(req.Name), strings.TrimSpace(req.Preset), strings.TrimSpace(req.URL)

	// A preset supplies its own URL. Accepting one from the caller as well
	// would let a ruleset claim to be a preset while fetching something else,
	// and the console shows the preset name.
	if req.Preset != "" {
		p, err := ruleset.FindPreset(req.Preset)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		req.URL = ruleset.PresetURL(p.Key)
		if req.Name == "" {
			req.Name = p.Name
		}
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, `"name" is required`)
		return
	}
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		writeErr(w, http.StatusBadRequest, `"url" must be an http(s) URL to a subconverter .ini`)
		return
	}

	rs, err := s.st.CreateRuleset(req.Name, req.Preset, req.URL)
	if err != nil {
		if isConflict(err) {
			writeErr(w, http.StatusConflict, "a ruleset with that name already exists")
			return
		}
		s.internalErr(w, "create ruleset", err)
		return
	}
	// Fetched immediately: a ruleset that exists but has never been fetched
	// renders nothing, and an operator who just created one has no reason to
	// expect a second step.
	if s.rulesets != nil {
		if err := s.rulesets.Refresh(r.Context(), rs.ID); err != nil {
			s.logger.Warn("initial ruleset fetch failed", "ruleset", rs.Name, "err", err)
		}
		rs, _ = s.st.GetRuleset(rs.ID)
	}
	s.audit(r, "ruleset.create", "ruleset", rs.ID, rs.Name, req.Preset)
	writeJSON(w, http.StatusCreated, toRulesetView(rs))
}

func (s *Server) updateRuleset(w http.ResponseWriter, r *http.Request) {
	rs, err := s.st.GetRuleset(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load ruleset", err, "no such ruleset")
		return
	}
	var req struct {
		Name *string `json:"name"`
		URL  *string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		rs.Name = strings.TrimSpace(*req.Name)
	}
	if req.URL != nil {
		if rs.Preset != "" {
			writeErr(w, http.StatusBadRequest, "a built-in preset's source cannot be changed; create a custom ruleset instead")
			return
		}
		url := strings.TrimSpace(*req.URL)
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			writeErr(w, http.StatusBadRequest, `"url" must be an http(s) URL`)
			return
		}
		rs.URL = url
	}
	if err := s.st.UpdateRuleset(rs.ID, rs.Name, rs.URL); err != nil {
		if isConflict(err) {
			writeErr(w, http.StatusConflict, "a ruleset with that name already exists")
			return
		}
		s.internalErr(w, "update ruleset", err)
		return
	}
	s.audit(r, "ruleset.update", "ruleset", rs.ID, rs.Name, "")
	rs, _ = s.st.GetRuleset(rs.ID)
	writeJSON(w, http.StatusOK, toRulesetView(rs))
}

func (s *Server) deleteRuleset(w http.ResponseWriter, r *http.Request) {
	rs, err := s.st.GetRuleset(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load ruleset", err, "no such ruleset")
		return
	}
	if err := s.st.DeleteRuleset(rs.ID); err != nil {
		s.notFoundOr(w, "delete ruleset", err, "no such ruleset")
		return
	}
	// Subscribers on it fall back to no rules rather than to a dangling
	// reference; the column is ON DELETE SET NULL.
	s.audit(r, "ruleset.delete", "ruleset", rs.ID, rs.Name, "")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) refreshRuleset(w http.ResponseWriter, r *http.Request) {
	if s.rulesets == nil {
		writeErr(w, http.StatusServiceUnavailable, "rulesets are not configured on this panel")
		return
	}
	rs, err := s.st.GetRuleset(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load ruleset", err, "no such ruleset")
		return
	}
	if err := s.rulesets.Refresh(r.Context(), rs.ID); err != nil {
		// Reported with the stored error rather than as a 500: the previous
		// copy is still serving, and the operator needs the reason.
		rs, _ = s.st.GetRuleset(rs.ID)
		writeJSON(w, http.StatusOK, toRulesetView(rs))
		return
	}
	s.audit(r, "ruleset.refresh", "ruleset", rs.ID, rs.Name, "")
	rs, _ = s.st.GetRuleset(rs.ID)
	writeJSON(w, http.StatusOK, toRulesetView(rs))
}

func (s *Server) setUserRuleset(w http.ResponseWriter, r *http.Request) {
	u, err := s.st.GetUser(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load user", err, "no such user")
		return
	}
	var req struct {
		RulesetID string `json:"ruleset_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	req.RulesetID = strings.TrimSpace(req.RulesetID)
	if req.RulesetID != "" {
		if _, err := s.st.GetRuleset(req.RulesetID); err != nil {
			s.notFoundOr(w, "load ruleset", err, "no such ruleset")
			return
		}
	}
	if err := s.st.SetUserRuleset(u.ID, req.RulesetID); err != nil {
		s.internalErr(w, "set user ruleset", err)
		return
	}
	// No node push: rules live in the subscription, not in any node's config.
	// The subscriber picks them up on their client's next refresh.
	s.audit(r, "user.ruleset", "user", u.ID, u.Name, req.RulesetID)
	w.WriteHeader(http.StatusNoContent)
}
