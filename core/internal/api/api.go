// Package api serves the REST API for the web frontend. M1 scope: node CRUD,
// online status, and config push. WebSocket push and richer auth/RBAC come
// later.
package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/node"
	"github.com/SayukiOvO/chiral/core/internal/profile"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/subscription"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

type Server struct {
	st  *store.Store
	mgr *node.Manager
	// adminToken guards every /api route (Authorization: Bearer <token>).
	// Proper admin accounts and RBAC replace this later.
	adminToken string
	// grpcPublicAddr is the address agents dial, embedded in generated
	// compose snippets, e.g. "panel.example.com:8443".
	grpcPublicAddr string
	// grpcTLS mirrors whether the gRPC endpoint serves TLS; plaintext panels
	// need CHIRAL_INSECURE in the generated agent compose.
	grpcTLS bool
	// profiles owns template rendering, config assembly and delivery.
	profiles *profile.Service
	// xrayAvailable reports whether the panel has a binary for `xray -test`
	// and for generators Go cannot implement.
	xrayAvailable bool
	// subs renders subscriptions for end users.
	subs *subscription.Service
	// publicURL is the panel's own base URL, used to build subscription links
	// an operator can hand out.
	publicURL string
	logger    *slog.Logger
}

func NewServer(st *store.Store, mgr *node.Manager, profiles *profile.Service, subs *subscription.Service,
	adminToken, grpcPublicAddr, publicURL string, grpcTLS, xrayAvailable bool, logger *slog.Logger) *Server {
	return &Server{
		st: st, mgr: mgr, profiles: profiles, subs: subs, adminToken: adminToken,
		grpcPublicAddr: grpcPublicAddr, publicURL: publicURL,
		grpcTLS: grpcTLS, xrayAvailable: xrayAvailable, logger: logger,
	}
}

// notFoundOr answers 404 for a missing row and 500 for anything else, so a
// handler does not have to spell the distinction out every time.
func (s *Server) notFoundOr(w http.ResponseWriter, what string, err error, msg string) {
	if store.IsNotFound(err) {
		writeErr(w, http.StatusNotFound, msg)
		return
	}
	s.internalErr(w, what, err)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.Handle("POST /api/nodes", s.requireAdmin(s.createNode))
	mux.Handle("GET /api/nodes", s.requireAdmin(s.listNodes))
	mux.Handle("DELETE /api/nodes/{id}", s.requireAdmin(s.deleteNode))
	mux.Handle("POST /api/nodes/{id}/join-token", s.requireAdmin(s.resetJoinToken))
	mux.Handle("PUT /api/nodes/{id}/config", s.requireAdmin(s.putConfig))
	mux.Handle("POST /api/nodes/{id}/restart-xray", s.requireAdmin(s.restartXray))
	s.routeTemplates(mux)

	mux.Handle("POST /api/users", s.requireAdmin(s.createUser))
	mux.Handle("GET /api/users", s.requireAdmin(s.listUsers))
	mux.Handle("GET /api/users/{id}", s.requireAdmin(s.getUser))
	mux.Handle("PUT /api/users/{id}", s.requireAdmin(s.updateUser))
	mux.Handle("DELETE /api/users/{id}", s.requireAdmin(s.deleteUser))
	mux.Handle("POST /api/users/{id}/sub-token", s.requireAdmin(s.resetSubToken))
	mux.Handle("POST /api/users/{id}/profiles/{profileID}", s.requireAdmin(s.bindUserProfile))
	mux.Handle("DELETE /api/users/{id}/profiles/{profileID}", s.requireAdmin(s.unbindUserProfile))

	mux.Handle("GET /api/nodes/{id}/samples", s.requireAdmin(s.nodeSamples))
	mux.Handle("GET /api/traffic", s.requireAdmin(s.trafficSeries))

	// The one route end users reach, authenticated by the token in the path.
	mux.HandleFunc("GET /sub/{token}", s.serveSubscription)
	return mux
}

func (s *Server) requireAdmin(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" || got != s.adminToken {
			writeErr(w, http.StatusUnauthorized, "missing or invalid admin token")
			return
		}
		h(w, r)
	})
}

type nodeView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
	PublicIP     string `json:"public_ip"`
	AgentVersion string `json:"agent_version"`
	XrayVersion  string `json:"xray_version"`
	CreatedAt    int64  `json:"created_at"`
	RegisteredAt int64  `json:"registered_at,omitempty"`
	LastSeenAt   int64  `json:"last_seen_at,omitempty"`

	Online    bool       `json:"online"`
	XrayState string     `json:"xray_state,omitempty"`
	Metrics   *metricsIn `json:"metrics,omitempty"`
}

type metricsIn struct {
	CPUPercent     float64 `json:"cpu_percent"`
	MemUsedBytes   uint64  `json:"mem_used_bytes"`
	MemTotalBytes  uint64  `json:"mem_total_bytes"`
	DiskUsedBytes  uint64  `json:"disk_used_bytes"`
	DiskTotalBytes uint64  `json:"disk_total_bytes"`
	NetTxBps       uint64  `json:"net_tx_bps"`
	NetRxBps       uint64  `json:"net_rx_bps"`
	ConfigVersion  int64   `json:"config_version"`
}

func (s *Server) view(n store.Node) nodeView {
	v := nodeView{
		ID:           n.ID,
		Name:         n.Name,
		Hostname:     n.Hostname,
		PublicIP:     n.PublicIP,
		AgentVersion: n.AgentVersion,
		XrayVersion:  n.XrayVersion,
		CreatedAt:    n.CreatedAt,
		RegisteredAt: n.RegisteredAt.Int64,
		LastSeenAt:   n.LastSeenAt.Int64,
	}
	st := s.mgr.State(n.ID)
	v.Online = st.Online
	if st.Online {
		v.LastSeenAt = st.LastSeen.Unix()
	}
	if hb := st.Heartbeat; hb != nil {
		v.XrayState = strings.TrimPrefix(hb.GetXrayState().String(), "XRAY_STATE_")
		v.Metrics = &metricsIn{
			CPUPercent:     hb.GetCpuPercent(),
			MemUsedBytes:   hb.GetMemUsedBytes(),
			MemTotalBytes:  hb.GetMemTotalBytes(),
			DiskUsedBytes:  hb.GetDiskUsedBytes(),
			DiskTotalBytes: hb.GetDiskTotalBytes(),
			NetTxBps:       hb.GetNetTxBps(),
			NetRxBps:       hb.GetNetRxBps(),
			ConfigVersion:  hb.GetConfigVersion(),
		}
	}
	return v
}

func (s *Server) createNode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "body must be JSON with a non-empty \"name\"")
		return
	}
	joinToken, joinTokenHash := auth.NewSecret()
	n, err := s.st.CreateNode(strings.TrimSpace(req.Name), joinTokenHash)
	if err != nil {
		s.internalErr(w, "create node", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"node":       s.view(n),
		"join_token": joinToken,
		"compose":    s.composeSnippet(joinToken),
	})
}

// composeSnippet renders the docker-compose the operator pastes onto the node
// machine. Kept in sync with deploy/agent/docker-compose.yml.tmpl.
func (s *Server) composeSnippet(joinToken string) string {
	insecure := ""
	if !s.grpcTLS {
		insecure = "\n      CHIRAL_INSECURE: \"1\" # panel gRPC has no TLS; do not use over untrusted networks"
	}
	return fmt.Sprintf(`services:
  chiral-agent:
    image: ghcr.io/sayukiovo/chiral-agent:latest
    restart: unless-stopped
    network_mode: host
    environment:
      PANEL_URL: %q
      JOIN_TOKEN: %q%s
    volumes:
      - chiral-agent-data:/var/lib/chiral-agent
volumes:
  chiral-agent-data:
`, s.grpcPublicAddr, joinToken, insecure)
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.st.ListNodes()
	if err != nil {
		s.internalErr(w, "list nodes", err)
		return
	}
	views := make([]nodeView, 0, len(nodes))
	for _, n := range nodes {
		views = append(views, s.view(n))
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": views})
}

func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.st.DeleteNode(id); err != nil {
		if store.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "no such node")
			return
		}
		s.internalErr(w, "delete node", err)
		return
	}
	// Deleting the node also revokes its credential (row gone); drop the live
	// session so the agent is cut off immediately rather than at next auth.
	s.mgr.CloseSession(id)
	w.WriteHeader(http.StatusNoContent)
}

// resetJoinToken issues a fresh join token, e.g. to recover an agent that
// lost its registration response or its state volume.
func (s *Server) resetJoinToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	joinToken, joinTokenHash := auth.NewSecret()
	if err := s.st.ResetJoinToken(id, joinTokenHash); err != nil {
		if store.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "no such node")
			return
		}
		s.internalErr(w, "reset join token", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"join_token": joinToken,
		"compose":    s.composeSnippet(joinToken),
	})
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.st.GetNode(id); err != nil {
		if store.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "no such node")
			return
		}
		s.internalErr(w, "load node", err)
		return
	}
	var cfg json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be valid JSON (the node's config.json)")
		return
	}
	// TODO(M2): validate with a panel-side `xray -test` before persisting.
	c, err := s.st.InsertConfig(id, string(cfg))
	if err != nil {
		s.internalErr(w, "persist config", err)
		return
	}
	pushErr := s.mgr.PushConfig(id, c.Version, []byte(c.Config))
	resp := map[string]any{"version": c.Version, "pushed": pushErr == nil}
	if pushErr != nil {
		// Not an error state: heartbeat reconciliation re-pushes as soon as
		// the node is reachable again.
		resp["push_error"] = pushErr.Error() + " (config saved; heartbeat reconciliation will re-push it)"
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *Server) restartXray(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.st.GetNode(id); err != nil {
		if store.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "no such node")
			return
		}
		s.internalErr(w, "load node", err)
		return
	}
	err := s.mgr.SendCommand(id, &chiralv1.Command{
		Cmd: &chiralv1.Command_RestartXray{RestartXray: &chiralv1.RestartXray{}},
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) internalErr(w http.ResponseWriter, what string, err error) {
	s.logger.Error(what+" failed", "err", err)
	writeErr(w, http.StatusInternalServerError, "internal error")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
