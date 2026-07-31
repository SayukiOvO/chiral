// Package api serves the REST API for the web frontend. M1 scope: node CRUD,
// online status, and config push. WebSocket push and richer auth/RBAC come
// later.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/alert"
	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/mail"
	"github.com/SayukiOvO/chiral/core/internal/node"
	"github.com/SayukiOvO/chiral/core/internal/passkey"
	"github.com/SayukiOvO/chiral/core/internal/profile"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/subscription"
	"github.com/SayukiOvO/chiral/core/internal/upgrade"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

type Server struct {
	st  *store.Store
	mgr *node.Manager
	// adminToken guards every /api route (Authorization: Bearer <token>).
	// Proper admin accounts and RBAC replace this later.
	adminToken string
	// grpcPublicAddr is the address agents dial, embedded in generated
	// compose snippets, e.g. "panel.example.com:26443".
	grpcPublicAddr string
	// grpcTLS mirrors whether the gRPC endpoint serves TLS; plaintext panels
	// need CHIRAL_INSECURE in the generated agent compose.
	grpcTLS bool
	// agentImage is what the generated join snippet tells a node to run.
	agentImage string
	// profiles owns template rendering, config assembly and delivery.
	profiles *profile.Service
	// xrayAvailable reports whether the panel has a binary for `xray -test`
	// and for generators Go cannot implement.
	xrayAvailable bool
	// subs renders subscriptions for end users.
	subs *subscription.Service
	// alerts delivers node availability notifications.
	alerts *alert.Service
	// upgrades drives runtime Xray-core upgrades; nil when no kernel directory
	// is configured, in which case the endpoints say so rather than 404.
	upgrades *upgrade.Service
	// publicURL is the panel's own base URL, used to build subscription links
	// an operator can hand out.
	publicURL string
	// passkeys is nil when the panel's URL cannot host WebAuthn (plain http
	// off localhost), in which case passkeys are simply not offered rather
	// than offered and failing at the last step.
	passkeys *passkey.Service
	// mailer is nil-safe: with no SMTP configured, email is not offered as a
	// factor.
	mailer *mail.Sender
	// limiter budgets the routes that answer before any credential has been
	// checked. See ratelimit.go.
	limiter *limiter
	// online is nil unless source-address recording is switched on, in which
	// case the panel reports counts as "not recording" rather than as zero.
	online OnlineSource
	// portal configures the end-user tree. With Mode off, none of its routes
	// are registered at all.
	portal PortalConfig
	// routing serves the rule lists a clash subscription refers to. Nil when
	// no ruleset service is wired, in which case those routes 404.
	routing RuleLists
	// rulesets fetches and refreshes routing configurations. Nil leaves the
	// management endpoints answering 503 with a reason rather than 404.
	rulesets Rulesets
	// externals fetches other people's subscriptions.
	externals Externals
	logger    *slog.Logger
}

// RuleLists resolves a subscriber's provider name to its contents.
// Implemented by the ruleset service.
type RuleLists interface {
	ListFor(u store.User, name string) (string, bool, error)
}

// Rulesets fetches routing configurations. Implemented by the ruleset service.
type Rulesets interface {
	Refresh(ctx context.Context, id string) error
}

// Externals fetches subscriptions belonging to other people. Implemented by
// the external service.
type Externals interface {
	Refresh(ctx context.Context, id string) error
}

// EnableExternals wires management of other people's subscriptions. Called at
// startup.
func (s *Server) EnableExternals(e Externals) { s.externals = e }

// EnableRuleLists wires rule-provider serving and ruleset management. Called
// at startup.
func (s *Server) EnableRuleLists(r RuleLists, m Rulesets) { s.routing, s.rulesets = r, m }

// EnablePortal switches on the end-user tree. Called at startup.
func (s *Server) EnablePortal(cfg PortalConfig) { s.portal = cfg }

// EnableOnlineTracking wires in the address registry. Called at startup only
// when the operator asked for recording.
func (s *Server) EnableOnlineTracking(src OnlineSource) { s.online = src }

// EnableUpgrades wires runtime kernel upgrades. Left unwired, the endpoints
// return 503 with a reason rather than 404 — "not configured" and "not a thing
// this panel does" are different answers, and only one tells an operator what
// to change.
func (s *Server) EnableUpgrades(u *upgrade.Service) { s.upgrades = u }

func NewServer(st *store.Store, mgr *node.Manager, profiles *profile.Service, subs *subscription.Service,
	alerts *alert.Service, passkeys *passkey.Service, mailer *mail.Sender,
	adminToken, grpcPublicAddr, publicURL string, grpcTLS, xrayAvailable bool, logger *slog.Logger) *Server {
	return &Server{
		st: st, mgr: mgr, profiles: profiles, subs: subs, alerts: alerts,
		passkeys: passkeys, mailer: mailer, adminToken: adminToken,
		grpcPublicAddr: grpcPublicAddr, publicURL: publicURL,
		grpcTLS: grpcTLS, xrayAvailable: xrayAvailable,
		// 8192 buckets is far more than a real panel sees and still bounded;
		// the sweep prunes expired ones.
		limiter: newLimiter(8192),
		logger:  logger,
	}
}

// PruneLimiter drops expired rate-limit buckets. Called from the sweep.
func (s *Server) PruneLimiter(now time.Time) { s.limiter.prune(now) }

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
	mux.Handle("POST /api/nodes", s.requireWrite(s.createNode))
	mux.Handle("GET /api/nodes", s.requireAdmin(s.listNodes))
	mux.Handle("PUT /api/nodes/{id}", s.requireWrite(s.updateNode))
	mux.Handle("DELETE /api/nodes/{id}", s.requireWrite(s.deleteNode))
	mux.Handle("POST /api/nodes/{id}/join-token", s.requireWrite(s.resetJoinToken))
	mux.Handle("POST /api/nodes/{id}/restart-xray", s.requireWrite(s.restartXray))

	// Runtime Xray-core upgrades. Reads are admin, the install is write, and
	// every install is audited whether or not it reached the node.
	mux.Handle("GET /api/xray/available", s.requireAdmin(s.availableXray))
	mux.Handle("GET /api/xray/installs", s.requireAdmin(s.xrayInstalls))
	mux.Handle("POST /api/nodes/{id}/xray/install", s.requireWrite(s.installXray))
	mux.Handle("GET /api/xray/upgrade", s.requireAdmin(s.xrayUpgrade))
	mux.Handle("POST /api/xray/upgrade", s.requireWrite(s.startXrayCanary))
	mux.Handle("POST /api/xray/upgrade/promote", s.requireWrite(s.promoteXray))
	mux.Handle("POST /api/xray/upgrade/retry", s.requireWrite(s.retryXray))
	mux.Handle("DELETE /api/xray/upgrade", s.requireWrite(s.abandonXray))
	s.routeTemplates(mux)
	s.routeRulesets(mux)
	s.routeExternals(mux)

	mux.Handle("POST /api/users", s.requireWrite(s.createUser))
	mux.Handle("GET /api/users", s.requireAdmin(s.listUsers))
	mux.Handle("GET /api/users/{id}", s.requireAdmin(s.getUser))
	mux.Handle("PUT /api/users/{id}", s.requireWrite(s.updateUser))
	mux.Handle("DELETE /api/users/{id}", s.requireWrite(s.deleteUser))
	mux.Handle("POST /api/users/{id}/sub-token", s.requireWrite(s.resetSubToken))
	mux.Handle("POST /api/users/{id}/profiles/{profileID}", s.requireWrite(s.bindUserProfile))
	mux.Handle("DELETE /api/users/{id}/profiles/{profileID}", s.requireWrite(s.unbindUserProfile))
	// Adopt a credential a server already issued, so migrating one does not
	// invalidate what subscribers already have configured.
	mux.Handle("PUT /api/users/{id}/credentials/{profileID}/{nodeId}", s.requireWrite(s.adoptCredential))
	// Hands the operator a one-time link a subscriber uses to set their own
	// password, so nobody has to send a password by hand.
	mux.Handle("POST /api/users/{id}/portal-link", s.requireWrite(s.issuePortalLink))
	// Portal access, separate from users.enabled: one governs signing in, the
	// other governs whether their proxy credentials work.
	mux.Handle("PUT /api/users/{id}/portal-access", s.requireWrite(s.setPortalAccess))

	mux.Handle("GET /api/nodes/{id}/samples", s.requireAdmin(s.nodeSamples))
	mux.Handle("GET /api/traffic", s.requireAdmin(s.trafficSeries))

	// How many addresses, versus which addresses. The first is an operations
	// question; the second is "where does this person live", so it takes the
	// highest role and writes an audit entry. See online.go.
	mux.Handle("GET /api/users/{id}/online", s.requireAdmin(s.userOnline))
	mux.Handle("GET /api/users/{id}/devices", s.requireSuperadmin(s.userDevices))

	// Authentication. Login is the only unauthenticated /api route.
	// Every route below answers before any credential has been checked, so
	// each one carries a per-IP budget. Adding an unauthenticated route
	// without a throttle is the mistake this grouping exists to make visible.
	mux.HandleFunc("POST /api/login", s.throttle("login", limitLogin, s.login))
	mux.HandleFunc("POST /api/login/mfa", s.throttle("mfa", limitMFA, s.verifyMFA))
	mux.HandleFunc("POST /api/login/email", s.throttle("email-code", limitEmailCode, s.sendEmailCode))
	mux.HandleFunc("POST /api/login/passkey/begin", s.throttle("mfa", limitMFA, s.beginPasskeyLogin))
	mux.HandleFunc("POST /api/login/passkey/finish", s.throttle("mfa", limitMFA, s.finishPasskeyLogin))
	mux.Handle("POST /api/logout", s.requireAdmin(s.logout))
	mux.Handle("GET /api/whoami", s.requireAdmin(s.whoami))

	mux.Handle("GET /api/admins", s.requireSuperadmin(s.listAdmins))
	mux.Handle("POST /api/admins", s.requireSuperadmin(s.createAdmin))
	mux.Handle("PUT /api/admins/{id}", s.requireSuperadmin(s.updateAdmin))
	mux.Handle("DELETE /api/admins/{id}", s.requireSuperadmin(s.deleteAdmin))
	// Changing your own password only needs to be logged in; the handler
	// checks that it is your own account or that you are a superadmin.
	mux.Handle("POST /api/admins/{id}/password", s.requireAdmin(s.changePassword))

	// Enrolling and removing your own second factors.
	mux.Handle("GET /api/mfa", s.requireAdmin(s.listMFA))
	mux.Handle("POST /api/mfa/totp/begin", s.requireAdmin(s.beginTOTP))
	mux.Handle("POST /api/mfa/totp/confirm", s.requireAdmin(s.confirmTOTP))
	mux.Handle("POST /api/mfa/passkey/begin", s.requireAdmin(s.beginPasskeyRegistration))
	mux.Handle("POST /api/mfa/passkey/finish", s.requireAdmin(s.finishPasskeyRegistration))
	mux.Handle("POST /api/mfa/email/send", s.requireAdmin(s.sendEmailVerification))
	mux.Handle("POST /api/mfa/email/confirm", s.requireAdmin(s.confirmEmail))
	mux.Handle("POST /api/mfa/recovery", s.requireAdmin(s.regenerateRecoveryCodes))
	mux.Handle("DELETE /api/mfa/{id}", s.requireAdmin(s.deleteMFA))

	mux.Handle("GET /api/audit", s.requireAdmin(s.auditLog))

	mux.Handle("GET /api/alerts", s.requireAdmin(s.listAlertTargets))
	mux.Handle("POST /api/alerts", s.requireWrite(s.createAlertTarget))
	mux.Handle("PUT /api/alerts/{id}", s.requireWrite(s.updateAlertTarget))
	mux.Handle("DELETE /api/alerts/{id}", s.requireWrite(s.deleteAlertTarget))
	mux.Handle("POST /api/alerts/{id}/test", s.requireWrite(s.testAlertTarget))

	// The one route end users reach, authenticated by the token in the path.
	mux.HandleFunc("GET /sub/{token}", s.throttle("sub", limitSubscription, s.serveSubscription))
	// The rule lists a clash-family subscription refers to, served from the
	// panel so the client does not have to reach GitHub to route anything.
	mux.HandleFunc("GET /sub/{token}/rules/{name}", s.throttle("sub", limitSubscription, s.serveRuleList))

	// The end user's tree, registered as a unit and only when the portal is
	// switched on. Every route in it is guarded by requireUser, whose handler
	// signature does not unify with the admin guards above.
	s.routePortal(mux)

	// Last, because it registers the catch-all. Go 1.22's mux is
	// most-specific-wins rather than first-registered, so the order is for the
	// reader's benefit rather than the router's.
	assets, source, complaint := WebAssets()
	if complaint != "" {
		s.logger.Warn(complaint)
	}
	s.routeStatic(mux, assets, source)
	return mux
}

type nodeView struct {
	ID string `json:"id"`
	// Name is the operator's name for the box; DisplayName is what
	// subscribers see in the portal. Blank means unset — the portal numbers
	// the line instead, and never falls back to Name, which usually encodes
	// the provider and datacentre.
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Hostname    string `json:"hostname"`
	PublicIP    string `json:"public_ip"`
	// Address is the operator's override, empty when unset; Dialable is what
	// is actually handed to clients. Both, so the console can show that a
	// detected address is in use rather than leaving it to be inferred.
	Address      string `json:"address"`
	Dialable     string `json:"dialable"`
	AgentVersion string `json:"agent_version"`
	// XrayVersion is what the live kernel process was started from;
	// XrayInstalledVersion is what the next start would use. They differ only
	// while an upgrade is mid-flight or has failed to take.
	XrayVersion          string `json:"xray_version"`
	XrayInstalledVersion string `json:"xray_installed_version"`
	// Platform is what decides whether this node can be upgraded at all: an
	// unknown one has no release asset to hand it, and the operator choosing a
	// canary should be able to see that before pressing the button.
	Platform     string `json:"platform"`
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
		ID:                   n.ID,
		Name:                 n.Name,
		DisplayName:          n.DisplayName,
		Hostname:             n.Hostname,
		PublicIP:             n.PublicIP,
		Address:              n.Address,
		Dialable:             n.Dialable(),
		AgentVersion:         n.AgentVersion,
		XrayVersion:          n.XrayVersion,
		XrayInstalledVersion: n.XrayInstalledVersion,
		Platform:             n.Platform,
		CreatedAt:            n.CreatedAt,
		RegisteredAt:         n.RegisteredAt.Int64,
		LastSeenAt:           n.LastSeenAt.Int64,
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
		"install":    s.installCommand(joinToken),
		"compose":    s.composeSnippet(joinToken),
	})
}

func (s *Server) updateNode(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.GetNode(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load node", err, "no such node")
		return
	}
	// Pointers so an OMITTED field and an EXPLICITLY EMPTY one stay different.
	//
	// Blank display_name is a meaningful value — it clears the customer-facing
	// name and puts the line back to being numbered — which is exactly why it
	// cannot also be what "I did not mention this field" decodes to. With plain
	// strings, a request that only renamed the box silently wiped the name
	// subscribers see, while the same omission of `name` was preserved. Two
	// fields, two opposite behaviours, one struct.
	var req struct {
		Name        *string `json:"name"`
		DisplayName *string `json:"display_name"`
		// Address overrides what clients are told to dial. Empty means fall
		// back to the address the agent's connection came from, which is only
		// right when nothing translates addresses in between.
		Address *string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	if req.Name != nil {
		if name := strings.TrimSpace(*req.Name); name != "" {
			n.Name = name
		}
	}
	if req.DisplayName != nil {
		n.DisplayName = strings.TrimSpace(*req.DisplayName)
	}
	if req.Address != nil {
		n.Address = strings.TrimSpace(*req.Address)
	}

	if err := s.st.UpdateNode(n.ID, n.Name, n.DisplayName, n.Address); err != nil {
		if isConflict(err) {
			writeErr(w, http.StatusConflict, "a node with that name already exists")
			return
		}
		s.notFoundOr(w, "update node", err, "no such node")
		return
	}
	s.audit(r, "node.update", "node", n.ID, n.Name, n.DisplayName)
	n, err = s.st.GetNode(n.ID)
	if err != nil {
		s.internalErr(w, "load node", err)
		return
	}
	writeJSON(w, http.StatusOK, s.view(n))
}

// DefaultAgentImage is where this project publishes the agent, and what the
// join snippet names unless CHIRAL_AGENT_IMAGE says otherwise.
//
// Overridable because a fork, a private registry or a pinned tag are all
// ordinary — and because the snippet this produces is pasted straight into a
// shell on another machine, so a wrong image here fails minutes later and one
// host away from the mistake.
const DefaultAgentImage = "moonwx/chiral-agent:latest"

// SetAgentImage overrides the image the join snippet names. Empty keeps
// DefaultAgentImage.
func (s *Server) SetAgentImage(image string) { s.agentImage = image }

// installCommand is the one line an operator runs on a new node.
//
// Offered before the compose snippet because it is the shorter true answer:
// the installer fetches the release, installs Xray beside it, writes the unit
// and starts it. The compose file remains for anyone already running
// containers, which is a preference rather than a requirement now that the
// panel ships as a static binary.
func (s *Server) installCommand(joinToken string) string {
	return fmt.Sprintf(
		"curl -fsSL https://raw.githubusercontent.com/SayukiOvO/chiral/main/deploy/install.sh "+
			"| sudo sh -s -- --agent --panel-url %s --token %s",
		s.grpcPublicAddr, joinToken)
}

// composeSnippet renders the docker-compose the operator pastes onto the node
// machine. Kept in sync with deploy/agent/docker-compose.yml.tmpl.
func (s *Server) composeSnippet(joinToken string) string {
	image := s.agentImage
	if image == "" {
		image = DefaultAgentImage
	}
	insecure := ""
	if !s.grpcTLS {
		insecure = "\n      CHIRAL_INSECURE: \"1\" # panel gRPC has no TLS; do not use over untrusted networks"
	}
	return fmt.Sprintf(`services:
  chiral-agent:
    image: %s
    restart: unless-stopped
    network_mode: host
    environment:
      PANEL_URL: %q
      JOIN_TOKEN: %q
      # Identity, the applied config, and every Xray version this node has been
      # upgraded to — must match the volume below, or a container restart drops
      # the node back to the kernel baked into the image.
      CHIRAL_STATE_DIR: /var/lib/chiral-agent%s
    volumes:
      - chiral-agent-data:/var/lib/chiral-agent
volumes:
  chiral-agent-data:
`, image, s.grpcPublicAddr, joinToken, insecure)
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
		"install":    s.installCommand(joinToken),
		"compose":    s.composeSnippet(joinToken),
	})
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
