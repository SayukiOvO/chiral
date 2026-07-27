package api

import (
	"encoding/json"
	"net/http"

	"github.com/SayukiOvO/chiral/core/internal/store"
)

// Kernel upgrade endpoints.
//
// All of them sit behind requireWrite or requireAdmin and are audited: this is
// the one control surface that reaches out to the internet, downloads a binary,
// and runs it as the process serving every subscriber on a node. Who pressed it
// and when is not optional.

func (s *Server) availableXray(w http.ResponseWriter, r *http.Request) {
	if s.upgrades == nil {
		writeErr(w, http.StatusServiceUnavailable, "kernel upgrades are not configured")
		return
	}
	avail, err := s.upgrades.Available(r.Context())
	if err != nil {
		// Upstream being unreachable is an ordinary condition for a panel
		// behind a restricted egress, not an internal fault — and the operator
		// needs the reason verbatim to know whether to fix DNS or a token.
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, avail)
}

// xrayInstalls lists every node's latest attempt, so the console can show the
// fleet at a glance rather than polling per node.
func (s *Server) xrayInstalls(w http.ResponseWriter, r *http.Request) {
	installs, err := s.st.XrayInstalls()
	if err != nil {
		s.internalErr(w, "list kernel installs", err)
		return
	}
	if installs == nil {
		installs = []store.XrayInstall{}
	}
	writeJSON(w, http.StatusOK, installs)
}

func (s *Server) installXray(w http.ResponseWriter, r *http.Request) {
	if s.upgrades == nil {
		writeErr(w, http.StatusServiceUnavailable, "kernel upgrades are not configured")
		return
	}
	id := r.PathValue("id")
	var req struct {
		Version string `json:"version"`
		// Activate defaults to true when omitted: an operator pressing
		// "upgrade" means run it, and staging without activating is the
		// deliberate, explicit case.
		Activate *bool `json:"activate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Version == "" {
		writeErr(w, http.StatusBadRequest, "version is required")
		return
	}
	activate := req.Activate == nil || *req.Activate

	n, err := s.st.GetNode(id)
	if err != nil {
		if store.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "no such node")
			return
		}
		s.internalErr(w, "load node", err)
		return
	}

	in, err := s.upgrades.InstallOn(r.Context(), id, req.Version, activate)
	if err != nil {
		// The audit entry is written whether or not the instruction reached the
		// node. Someone asked for this; that is the fact worth keeping, and a
		// failed attempt is exactly the one an investigation would miss.
		s.audit(r, "xray.install", "node", id, n.Name, req.Version+" (failed: "+err.Error()+")")
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	s.audit(r, "xray.install", "node", id, n.Name, req.Version+boolSuffix(activate))
	writeJSON(w, http.StatusAccepted, in)
}

// --- fleet upgrades: canary, then a person ---

func (s *Server) xrayUpgrade(w http.ResponseWriter, r *http.Request) {
	u, err := s.st.ActiveUpgrade()
	if err != nil {
		if store.IsNotFound(err) {
			// Not an error: "nothing in progress" is the normal state, and a
			// 404 would make the console treat it as a fault.
			writeJSON(w, http.StatusOK, map[string]any{"active": nil})
			return
		}
		s.internalErr(w, "read the active upgrade", err)
		return
	}
	history, err := s.st.UpgradeHistory(10)
	if err != nil {
		s.internalErr(w, "read upgrade history", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": u, "history": history})
}

func (s *Server) startXrayCanary(w http.ResponseWriter, r *http.Request) {
	if s.upgrades == nil {
		writeErr(w, http.StatusServiceUnavailable, "kernel upgrades are not configured")
		return
	}
	var req struct {
		Version      string `json:"version"`
		CanaryNodeID string `json:"canary_node_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	// The node's name, not just its id: an audit entry read six months later
	// is the one place nobody can go and look the id up.
	name := ""
	if n, err := s.st.GetNode(req.CanaryNodeID); err == nil {
		name = n.Name
	}
	u, err := s.upgrades.StartCanary(r.Context(), req.Version, req.CanaryNodeID)
	if err != nil {
		s.audit(r, "xray.canary", "node", req.CanaryNodeID, name, req.Version+" (failed: "+err.Error()+")")
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	s.audit(r, "xray.canary", "node", req.CanaryNodeID, name, req.Version)
	writeJSON(w, http.StatusAccepted, u)
}

func (s *Server) promoteXray(w http.ResponseWriter, r *http.Request) {
	s.upgradeAction(w, r, "xray.promote", func() (any, error) {
		return s.upgrades.Promote(r.Context())
	})
}

func (s *Server) retryXray(w http.ResponseWriter, r *http.Request) {
	s.upgradeAction(w, r, "xray.retry", func() (any, error) {
		return s.upgrades.Retry(r.Context())
	})
}

func (s *Server) abandonXray(w http.ResponseWriter, r *http.Request) {
	s.upgradeAction(w, r, "xray.abandon", func() (any, error) {
		return s.upgrades.Abandon()
	})
}

// upgradeAction is the shared shape: refuse when unwired, audit either way,
// and report a state-machine refusal as a conflict rather than a fault — being
// in the wrong state is the operator's answer, not a bug.
func (s *Server) upgradeAction(w http.ResponseWriter, r *http.Request, action string, do func() (any, error)) {
	if s.upgrades == nil {
		writeErr(w, http.StatusServiceUnavailable, "kernel upgrades are not configured")
		return
	}
	// Read the version before acting: promote and abandon both end the upgrade,
	// and an audit entry that cannot say WHICH version was released to the
	// fleet is not worth writing.
	version := ""
	if u, err := s.st.ActiveUpgrade(); err == nil {
		version = u.Version
	}
	result, err := do()
	if err != nil {
		s.audit(r, action, "xray_upgrade", "", version, "failed: "+err.Error())
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	s.audit(r, action, "xray_upgrade", "", version, "")
	writeJSON(w, http.StatusOK, result)
}

func boolSuffix(activate bool) string {
	if activate {
		return " (activate)"
	}
	return " (staged only)"
}
