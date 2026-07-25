package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/SayukiOvO/chiral/core/internal/alert"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

type alertTargetView struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Config is masked: a bot token is a credential, and an admin UI has no
	// reason to display one it already holds.
	ConfigHint string `json:"config_hint"`
	Enabled    bool   `json:"enabled"`
	CreatedAt  int64  `json:"created_at"`
	LastError  string `json:"last_error"`
	LastSentAt int64  `json:"last_sent_at"`
}

// configHint shows enough to tell two targets apart without revealing the
// secret: the tail of a webhook URL, or the chat id of a telegram target.
func configHint(kind, config string) string {
	switch kind {
	case store.AlertTelegram:
		if idx := strings.LastIndex(config, ":"); idx > 0 {
			return "chat " + config[idx+1:]
		}
		return "•••"
	default:
		if len(config) <= 40 {
			return config
		}
		return config[:40] + "…"
	}
}

func toAlertView(t store.AlertTarget) alertTargetView {
	return alertTargetView{
		ID: t.ID, Kind: t.Kind, Name: t.Name,
		ConfigHint: configHint(t.Kind, t.Config),
		Enabled:    t.Enabled, CreatedAt: t.CreatedAt,
		LastError: t.LastError, LastSentAt: t.LastSentAt,
	}
}

func (s *Server) listAlertTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := s.st.ListAlertTargets()
	if err != nil {
		s.internalErr(w, "list alert targets", err)
		return
	}
	views := make([]alertTargetView, 0, len(targets))
	for _, t := range targets {
		views = append(views, toAlertView(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": views})
}

func (s *Server) createAlertTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind   string `json:"kind"`
		Name   string `json:"name"`
		Config string `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	req.Name, req.Config = strings.TrimSpace(req.Name), strings.TrimSpace(req.Config)
	if req.Name == "" || req.Config == "" {
		writeErr(w, http.StatusBadRequest, "name and config are required")
		return
	}
	switch req.Kind {
	case store.AlertTelegram:
		if strings.LastIndex(req.Config, ":") <= 0 {
			writeErr(w, http.StatusBadRequest, `telegram config must be "<bot-token>:<chat-id>"`)
			return
		}
	case store.AlertWebhook:
		if !strings.HasPrefix(req.Config, "http://") && !strings.HasPrefix(req.Config, "https://") {
			writeErr(w, http.StatusBadRequest, "webhook config must be an http(s) URL")
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "kind must be telegram or webhook")
		return
	}

	t, err := s.st.CreateAlertTarget(req.Kind, req.Name, req.Config)
	if err != nil {
		s.internalErr(w, "create alert target", err)
		return
	}
	s.audit(r, "alert.create", "alert_target", t.ID, t.Name, t.Kind)
	writeJSON(w, http.StatusCreated, toAlertView(t))
}

func (s *Server) updateAlertTarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
		writeErr(w, http.StatusBadRequest, `body must be JSON with "enabled"`)
		return
	}
	if err := s.st.SetAlertTargetEnabled(id, *req.Enabled); err != nil {
		s.notFoundOr(w, "update alert target", err, "no such target")
		return
	}
	t, err := s.st.GetAlertTarget(id)
	if err != nil {
		s.internalErr(w, "reload alert target", err)
		return
	}
	s.audit(r, "alert.update", "alert_target", id, t.Name, "enabled="+boolText(*req.Enabled))
	writeJSON(w, http.StatusOK, toAlertView(t))
}

func (s *Server) deleteAlertTarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := s.st.GetAlertTarget(id)
	if err != nil {
		s.notFoundOr(w, "load alert target", err, "no such target")
		return
	}
	if err := s.st.DeleteAlertTarget(id); err != nil {
		s.internalErr(w, "delete alert target", err)
		return
	}
	s.audit(r, "alert.delete", "alert_target", id, t.Name, "")
	w.WriteHeader(http.StatusNoContent)
}

// testAlertTarget sends a message now. Finding out a chat id is wrong during
// an incident is the worst possible time, so this exists.
func (s *Server) testAlertTarget(w http.ResponseWriter, r *http.Request) {
	if s.alerts == nil {
		writeErr(w, http.StatusServiceUnavailable, "alerting is not configured")
		return
	}
	id := r.PathValue("id")
	t, err := s.st.GetAlertTarget(id)
	if err != nil {
		s.notFoundOr(w, "load alert target", err, "no such target")
		return
	}
	err = s.alerts.Deliver(r.Context(), t, alert.Event{
		Title: "Chiral test",
		Body:  "If you can read this, alerting works.",
		Node:  "-",
	})
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if rerr := s.st.RecordAlertResult(id, msg); rerr != nil {
		s.logger.Error("recording alert result failed", "target", t.Name, "err", rerr)
	}
	s.audit(r, "alert.test", "alert_target", id, t.Name, msg)
	if err != nil {
		// The target is the caller's configuration, so its rejection is a 400
		// about their input rather than a 500 about ours.
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
