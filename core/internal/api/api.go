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
	"unicode/utf8"

	"github.com/SayukiOvO/chiral/core/internal/alert"
	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/external"
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
	// Blocked reports which external proxies a subscriber cannot be given
	// because of a chain that does not resolve for them, and what each one was
	// chained through. The console needs the same answer the renderer reaches.
	Blocked(denied map[string]struct{}, chainName func(nodeID string) string) (map[string]external.ChainRef, error)
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
	s.routeRelays(mux)
	s.routeRestricted(mux)
	s.routeEgress(mux)
	s.routeGroups(mux)
	s.routeSettings(mux)

	mux.Handle("POST /api/users", s.requireWrite(s.createUser))
	mux.Handle("GET /api/users", s.requireAdmin(s.listUsers))
	mux.Handle("GET /api/users/{id}", s.requireAdmin(s.getUser))
	mux.Handle("PUT /api/users/{id}", s.requireWrite(s.updateUser))
	mux.Handle("DELETE /api/users/{id}", s.requireWrite(s.deleteUser))
	mux.Handle("GET /api/users/{id}/sub-token", s.requireWrite(s.subToken))
	mux.Handle("POST /api/users/{id}/sub-token", s.requireWrite(s.resetSubToken))
	mux.Handle("POST /api/users/{id}/profiles/{profileID}", s.requireWrite(s.bindUserProfile))
	mux.Handle("DELETE /api/users/{id}/profiles/{profileID}", s.requireWrite(s.unbindUserProfile))
	// Adopt a credential a server already issued, so migrating one does not
	// invalidate what subscribers already have configured.
	mux.Handle("PUT /api/users/{id}/credentials/{profileID}/{nodeId}", s.requireWrite(s.adoptCredential))
	// Which nodes this subscriber's subscription carries, fleet and external.
	mux.Handle("GET /api/users/{id}/nodes", s.requireAdmin(s.userNodeAccess))
	mux.Handle("PUT /api/users/{id}/nodes", s.requireWrite(s.setUserNodeAccess))
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
	// RuntimeProvider names the node-local implementation being used or observed. RuntimeMode is
	// ACTIVE for the implementation receiving writes, or SHADOW while an
	// adapter is only proving its live API contract during migration.
	RuntimeProvider     string   `json:"runtime_provider"`
	RuntimeMode         string   `json:"runtime_mode"`
	RuntimeHealth       string   `json:"runtime_health"`
	RuntimeVersion      string   `json:"runtime_version,omitempty"`
	RuntimeCapabilities []string `json:"runtime_capabilities,omitempty"`
	RuntimeContract     string   `json:"runtime_contract_digest,omitempty"`
	RuntimeError        string   `json:"runtime_error,omitempty"`
	RuntimeObservedAt   int64    `json:"runtime_observed_at,omitempty"`
	RuntimeXrayState    string   `json:"runtime_xray_state,omitempty"`
	RuntimeXrayVersion  string   `json:"runtime_xray_version,omitempty"`
	// Platform is what decides whether this node can be upgraded at all: an
	// unknown one has no release asset to hand it, and the operator choosing a
	// canary should be able to see that before pressing the button.
	Platform     string `json:"platform"`
	CreatedAt    int64  `json:"created_at"`
	RegisteredAt int64  `json:"registered_at,omitempty"`
	LastSeenAt   int64  `json:"last_seen_at,omitempty"`

	Online bool `json:"online"`
	// TrafficRate is what a byte here costs the subscriber's quota.
	TrafficRate float64    `json:"traffic_rate"`
	XrayState   string     `json:"xray_state,omitempty"`
	Metrics     *metricsIn `json:"metrics,omitempty"`
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
		RuntimeProvider:      "unknown",
		RuntimeMode:          "UNKNOWN",
		RuntimeHealth:        "UNKNOWN",
		Platform:             n.Platform,
		CreatedAt:            n.CreatedAt,
		RegisteredAt:         n.RegisteredAt.Int64,
		LastSeenAt:           n.LastSeenAt.Int64,
		TrafficRate:          n.TrafficRate,
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
		applyHeartbeatRuntimeStatus(&v, hb, st.HeartbeatAt)
	}
	return v
}

func applyHeartbeatRuntimeStatus(v *nodeView, hb *chiralv1.Heartbeat, heartbeatAt time.Time) {
	if v == nil || hb == nil {
		return
	}
	if runtime := hb.GetRuntime(); runtime != nil {
		// A present-but-malformed status is not an old Agent. Only absence is
		// eligible for the legacy compatibility identity below.
		applyRuntimeStatus(v, runtime, heartbeatAt)
		return
	}
	// An agent old enough not to send RuntimeStatus can only be the legacy
	// direct implementation. Apply this compatibility default only after an
	// actual heartbeat; an offline/new node has no current runtime evidence.
	v.RuntimeProvider = "direct-xray"
	v.RuntimeMode = "ACTIVE"
	if heartbeatAt.IsZero() {
		v.RuntimeHealth = "UNKNOWN"
		return
	}
	v.RuntimeHealth = "READY"
	v.RuntimeObservedAt = heartbeatAt.Unix()
}

func applyRuntimeStatus(v *nodeView, runtime *chiralv1.RuntimeStatus, receivedAt time.Time) {
	if v == nil || runtime == nil || runtime.GetProvider() == "" {
		return
	}
	v.RuntimeProvider = boundedRuntimeText(runtime.GetProvider(), 64)
	if v.RuntimeProvider == "" {
		v.RuntimeProvider = "unknown"
	}
	switch runtime.GetMode() {
	case chiralv1.RuntimeMode_RUNTIME_MODE_ACTIVE:
		v.RuntimeMode = "ACTIVE"
	case chiralv1.RuntimeMode_RUNTIME_MODE_SHADOW:
		v.RuntimeMode = "SHADOW"
	default:
		v.RuntimeMode = "UNKNOWN"
	}
	switch runtime.GetHealth() {
	case chiralv1.RuntimeHealth_RUNTIME_HEALTH_READY:
		v.RuntimeHealth = "READY"
	case chiralv1.RuntimeHealth_RUNTIME_HEALTH_INCOMPATIBLE:
		v.RuntimeHealth = "INCOMPATIBLE"
	case chiralv1.RuntimeHealth_RUNTIME_HEALTH_UNREACHABLE:
		v.RuntimeHealth = "UNREACHABLE"
	default:
		v.RuntimeHealth = "UNKNOWN"
	}
	v.RuntimeVersion = boundedRuntimeText(runtime.GetVersion(), 64)
	for _, capability := range runtime.GetCapabilities() {
		if len(v.RuntimeCapabilities) == 32 {
			break
		}
		if capability = boundedRuntimeText(capability, 64); capability != "" {
			v.RuntimeCapabilities = append(v.RuntimeCapabilities, capability)
		}
	}
	if isLowerHexDigest(runtime.GetContractDigest()) {
		v.RuntimeContract = runtime.GetContractDigest()
	}
	v.RuntimeError = boundedRuntimeText(runtime.GetError(), 256)
	if runtime.GetObservedAtUnix() > 0 && !receivedAt.IsZero() {
		// Preserve age, not the Agent's wall-clock timestamp. A node with a bad
		// clock must not make a fresh contract immediately stale (or keep an old
		// one green for minutes) in an administrator's browser.
		observedAt := receivedAt.Unix() - int64(runtime.GetObservedAgeSeconds())
		if observedAt > 0 {
			v.RuntimeObservedAt = observedAt
		}
	}
	switch runtime.GetXrayState() {
	case chiralv1.XrayState_XRAY_STATE_RUNNING:
		v.RuntimeXrayState = "RUNNING"
	case chiralv1.XrayState_XRAY_STATE_STOPPED:
		v.RuntimeXrayState = "STOPPED"
	case chiralv1.XrayState_XRAY_STATE_ERROR:
		v.RuntimeXrayState = "ERROR"
	default:
		if v.RuntimeProvider == "3x-ui" {
			v.RuntimeXrayState = "UNKNOWN"
		}
	}
	v.RuntimeXrayVersion = boundedRuntimeText(runtime.GetXrayVersion(), 64)

	// This release deliberately has no 3x-ui ACTIVE implementation. An Agent
	// heartbeat is observation, not authority to enable writes; accepting a
	// self-reported future mode here would make the UI advertise a cutover that
	// Core cannot enforce or roll back.
	switch v.RuntimeProvider {
	case "direct-xray":
		if v.RuntimeMode != "ACTIVE" {
			v.RuntimeMode = "UNKNOWN"
			v.RuntimeHealth = "UNKNOWN"
		}
	case "3x-ui":
		if v.RuntimeMode != "SHADOW" {
			v.RuntimeMode = "UNKNOWN"
			v.RuntimeHealth = "UNKNOWN"
			v.RuntimeError = "3x-ui ACTIVE is not supported by this Core release"
		} else if v.RuntimeHealth == "READY" && (v.RuntimeObservedAt == 0 || v.RuntimeContract == "" || v.RuntimeXrayState == "UNKNOWN" || !has3XUIShadowCapabilities(v.RuntimeCapabilities)) {
			v.RuntimeHealth = "UNKNOWN"
			v.RuntimeError = "3x-ui shadow evidence is incomplete"
		}
	default:
		v.RuntimeMode = "UNKNOWN"
		v.RuntimeHealth = "UNKNOWN"
	}
	if v.RuntimeHealth != "UNKNOWN" && v.RuntimeObservedAt == 0 {
		v.RuntimeHealth = "UNKNOWN"
	}
}

var required3XUIShadowCapabilities = []string{
	"status", "inbounds_list", "inbounds_add", "inbounds_update", "inbounds_delete",
	"clients_list", "clients_add", "clients_update", "clients_delete", "client_traffic",
	"clients_online", "client_ips", "xray_update", "xray_restart", "get_config", "install_xray",
}

func has3XUIShadowCapabilities(capabilities []string) bool {
	seen := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		seen[capability] = struct{}{}
	}
	for _, required := range required3XUIShadowCapabilities {
		if _, ok := seen[required]; !ok {
			return false
		}
	}
	return true
}

func boundedRuntimeText(value string, maxBytes int) string {
	var bounded strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if bounded.Len()+utf8.RuneLen(r) > maxBytes {
			break
		}
		bounded.WriteRune(r)
	}
	return bounded.String()
}

func isLowerHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, b := range []byte(value) {
		if (b < '0' || b > '9') && (b < 'a' || b > 'f') {
			return false
		}
	}
	return true
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
		// TrafficRate is what a byte here costs the subscriber's quota.
		TrafficRate *float64 `json:"traffic_rate"`
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

	if req.TrafficRate != nil {
		if *req.TrafficRate <= 0 {
			writeErr(w, http.StatusBadRequest, "倍率必须大于 0")
			return
		}
		if err := s.st.SetNodeTrafficRate(n.ID, *req.TrafficRate); err != nil {
			s.notFoundOr(w, "set traffic rate", err, "no such node")
			return
		}
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
	// The nodes on the other end of anything this one participates in,
	// collected BEFORE the row cascades away. A deleted node takes its relay
	// lines and egress rules with it, but the machine credentials those put
	// on the OTHER nodes are ordinary client entries that only disappear when
	// those nodes are re-assembled — otherwise a credential outlives the rule
	// that justified it, indefinitely.
	others := s.peerNodesOf(id)
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
	s.applyNodes(r, others)
	w.WriteHeader(http.StatusNoContent)
}

// peerNodesOf lists the other nodes whose configs mention this one: the exits
// and entries of its relay lines, and both ends of its egress rules.
func (s *Server) peerNodesOf(id string) []string {
	seen := map[string]bool{id: true}
	var out []string
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	if relays, err := s.st.ListNodeRelays(); err == nil {
		for _, rl := range relays {
			if rl.EntryNodeID == id {
				add(rl.ExitNodeID)
			}
			if rl.ExitNodeID == id {
				add(rl.EntryNodeID)
			}
		}
	}
	if rules, err := s.st.EgressRulesOn(id); err == nil {
		for _, r := range rules {
			add(r.TargetNodeID)
		}
	}
	if rules, err := s.st.EgressRulesLandingOn(id); err == nil {
		for _, r := range rules {
			add(r.NodeID)
		}
	}
	return out
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
