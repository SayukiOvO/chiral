package api

import (
	"encoding/json"
	"net/http"
	"net/mail"
	"os"
	"strings"

	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/portal"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/subscription"
)

// The end user's API, kept apart from the operator's by construction rather
// than by care. Four things separate them, in decreasing order of how much
// they would survive a careless edit:
//
//  1. Token resolution. A portal token is looked up in portal_sessions only,
//     and AdminBySession joins admins on admin_id, so a portal token cannot
//     resolve to an admin identity. That guarantee is a table name.
//
//  2. Handler signature. portalHandler takes a portal.Identity argument;
//     admin handlers take (w, r) and read auth.Identity from the context. So
//     `s.requireAdmin(s.portalMe)` and `s.requireUser(s.listNodes)` are both
//     compile errors.
//
//  3. Scoped data. Everything a portal handler shows about the fleet comes
//     from a *portal.View built from its own identity, and package portal
//     cannot reach the store at all — it is handed a narrow portal.Data
//     interface, so a query for someone else's data is a compile error THERE.
//
//     Be precise about the limit of this: handlers in THIS package are methods
//     on *Server and so still hold s.st. portalLogin and portalChangePassword
//     use it deliberately, for their own account rows. Nothing stops a future
//     handler here from calling s.st.ListNodes() — (2) stops the wrong guard,
//     not a correctly guarded handler fetching too much. Route fleet data
//     through View and that stays impossible; reach for s.st and you are on
//     your own.
//
//  4. A route-prefix assertion in both middlewares, so a misregistration is a
//     loudly broken route rather than a silent hole.

// Portal modes, from CHIRAL_PORTAL_MODE.
const (
	// PortalOff: the routes are not registered at all, so upgrading a panel
	// that has not asked for a portal changes nothing about its surface.
	PortalOff = "off"
	// PortalClosed: existing users can sign in; registration is refused.
	PortalClosed = "closed"
	// PortalOpen: anyone may register, optionally gated by a shared code.
	PortalOpen = "open"
)

// PortalConfig is how the portal was configured at startup.
type PortalConfig struct {
	Mode string
	// InviteCode, when set, must accompany a registration. A single shared
	// code rather than per-invite rows: Mai asked for a portal, not a billing
	// system, and one env var covers "not completely open to the internet".
	InviteCode string
	// PublicURL is the panel's own base URL, used to build subscription links.
	PublicURL string
}

// PortalFromEnv reads the portal's configuration.
func PortalFromEnv(publicURL string) PortalConfig {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("CHIRAL_PORTAL_MODE")))
	switch mode {
	case PortalClosed, PortalOpen:
	default:
		mode = PortalOff
	}
	return PortalConfig{
		Mode:       mode,
		InviteCode: os.Getenv("CHIRAL_PORTAL_INVITE_CODE"),
		PublicURL:  publicURL,
	}
}

func (c PortalConfig) Enabled() bool { return c.Mode != PortalOff }

// portalHandler is the portal's handler shape. The extra argument is what
// makes mixing the two guards a compile error.
type portalHandler func(http.ResponseWriter, *http.Request, portal.Identity)

// requireUser admits a valid portal session and nothing else.
func (s *Server) requireUser(h portalHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/portal/") {
			// A misregistration should fail obviously on its first request
			// rather than quietly granting a portal session access to an
			// operator route.
			s.logger.Error("portal guard registered on a non-portal route", "path", r.URL.Path)
			writeErr(w, http.StatusInternalServerError, "route misconfigured")
			return
		}
		token := bearer(r)
		if token == "" {
			writeErr(w, http.StatusUnauthorized, "missing or invalid credentials")
			return
		}
		u, err := s.st.UserByPortalSession(auth.HashSecret(token))
		if err != nil {
			if !store.IsNotFound(err) {
				s.logger.Error("portal session lookup failed", "err", err)
			}
			writeErr(w, http.StatusUnauthorized, "missing or invalid credentials")
			return
		}
		// Nothing goes into the request context. Any admin handler reached by
		// mistake therefore sees a zero auth.Identity, whose empty role ranks
		// below viewer and can do nothing — fail closed.
		h(w, r, portal.Identity{UserID: u.ID, Name: u.Name})
	})
}

// view builds the scoped accessor for a portal request.
//
// s.st satisfies portal.Data, but the View only ever exposes the handful of
// user-scoped methods on that interface — the handler never holds the store
// itself, so it cannot reach past its own user.
func (s *Server) portalView(id portal.Identity) *portal.View {
	return portal.NewView(id, s.st, clientKinds{})
}

// clientKinds tells the portal which subscription formats exist. A tiny type
// rather than passing subscription.Service, so the portal keeps depending on
// the fact and not on the service.
type clientKinds struct{}

func (clientKinds) Kinds() []string { return subscription.Kinds() }

// routePortal registers the end-user tree. Called once from Handler, and not
// at all when the portal is off.
func (s *Server) routePortal(mux *http.ServeMux) {
	if !s.portal.Enabled() {
		return
	}

	// Unauthenticated, and every one of them throttled: these answer before
	// any credential exists, exactly like the admin login routes. config is
	// only a few static booleans, but it is on the list so the rule stays
	// "every route in this block has a budget" rather than "every route except
	// the ones someone judged cheap".
	mux.HandleFunc("GET /api/portal/config", s.throttle("portal-config", limitConfig, s.portalConfig))
	mux.HandleFunc("POST /api/portal/register", s.throttle("portal-register", limitRegister, s.portalRegister))
	mux.HandleFunc("POST /api/portal/login", s.throttle("portal-login", limitLogin, s.portalLogin))
	mux.HandleFunc("POST /api/portal/claim/lookup", s.throttle("portal-claim", limitClaim, s.portalClaimLookup))
	mux.HandleFunc("POST /api/portal/claim", s.throttle("portal-claim", limitClaim, s.portalClaim))

	// Authenticated.
	mux.Handle("GET /api/portal/me", s.requireUser(s.portalMe))
	mux.Handle("POST /api/portal/logout", s.requireUser(s.portalLogout))
	mux.Handle("POST /api/portal/password", s.requireUser(s.portalChangePassword))
}

// portalConfig tells the sign-in page what it may offer. Unauthenticated by
// necessity — it is what the page reads before anyone has signed in — and
// deliberately says nothing beyond which forms to draw.
func (s *Server) portalConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":              s.portal.Mode,
		"registration_open": s.portal.Mode == PortalOpen,
		"invite_required":   s.portal.Mode == PortalOpen && s.portal.InviteCode != "",
		"email_ready":       s.mailer != nil && s.mailer.Enabled(),
	})
}

// portalRegister creates a subscriber account.
//
// The response is identical whether or not the address is already taken. The
// obvious implementation — 409 on the unique index — would be a public
// customer-email oracle, which is precisely what the panel is careful to avoid
// on the admin login. Someone who already has an account learns so by email,
// not by watching status codes.
func (s *Server) portalRegister(w http.ResponseWriter, r *http.Request) {
	if s.portal.Mode != PortalOpen {
		writeErr(w, http.StatusForbidden, "registration is closed on this panel")
		return
	}
	var req struct {
		Email      string `json:"email"`
		Password   string `json:"password"`
		InviteCode string `json:"invite_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON with an email and a password")
		return
	}
	if s.portal.InviteCode != "" && !subtleEqual(strings.TrimSpace(req.InviteCode), s.portal.InviteCode) {
		writeErr(w, http.StatusForbidden, "that registration code is not valid")
		return
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(req.Email))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "that is not a valid email address")
		return
	}
	if len(req.Password) < auth.MinPasswordLen {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	// Constant response from here on, whatever happens.
	accepted := func() {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"registered": true,
			"message":    "if that address is not already registered, the account is ready to sign in",
		})
	}
	if _, err := s.st.UserAccountByEmail(addr.Address); err == nil {
		// Already registered. Spend comparable work so the timing does not
		// answer the question the status code refuses to.
		auth.SpendVerification(req.Password)
		accepted()
		return
	} else if !store.IsNotFound(err) {
		s.internalErr(w, "look up account", err)
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.internalErr(w, "hash password", err)
		return
	}
	if err := s.createSubscriber(addr.Address, hash); err != nil {
		s.logger.Error("portal registration failed", "err", err)
		s.internalErr(w, "create account", err)
		return
	}
	accepted()
}

// createSubscriber makes the proxy user and its login identity.
//
// The new user is granted no profiles. That needs no admission logic of its
// own: Render over zero profiles produces an empty subscription and
// EnsureCredentials mints nothing, so the account is inert by construction
// until an operator grants something. The portal shows "not provisioned yet"
// rather than an empty subscription file.
func (s *Server) createSubscriber(email, passwordHash string) error {
	token, hash := auth.NewSecret()
	u, err := s.st.CreateUserWithToken(store.User{
		Name:    newSubscriberName(),
		Enabled: true,
	}, token, hash)
	if err != nil {
		return err
	}
	if _, err := s.st.CreateUserAccount(u.ID, email, passwordHash); err != nil {
		return err
	}
	return nil
}

// newSubscriberName generates the users.name for a self-registered account.
//
// Never derived from the email address, for two reasons. users.name is NOT
// NULL UNIQUE, so mai@example.com and mai@example.org would collide and the
// second signup would fail; and user.StatsEmail feeds users.name into
// credentials.email, which templates expose as {{user.email}} and render into
// the subscriber's own config file — so deriving it would write the person's
// login address into Xray's stats keys and their subscription.
func newSubscriberName() string { return "u" + store.NewID()[:10] }
