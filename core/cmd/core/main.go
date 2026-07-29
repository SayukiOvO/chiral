// chiral-core is the Panel backend: REST API for the frontend, the gRPC
// endpoint agents dial back to, and the SQLite source of truth.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	"github.com/SayukiOvO/chiral/core/internal/alert"
	"github.com/SayukiOvO/chiral/core/internal/api"
	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/kernel"
	"github.com/SayukiOvO/chiral/core/internal/mail"
	"github.com/SayukiOvO/chiral/core/internal/node"
	"github.com/SayukiOvO/chiral/core/internal/online"
	"github.com/SayukiOvO/chiral/core/internal/passkey"
	"github.com/SayukiOvO/chiral/core/internal/profile"
	"github.com/SayukiOvO/chiral/core/internal/release"
	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/subscription"
	"github.com/SayukiOvO/chiral/core/internal/template"
	"github.com/SayukiOvO/chiral/core/internal/upgrade"
	"github.com/SayukiOvO/chiral/core/internal/user"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// version is stamped at link time by the release build:
//
//	-ldflags "-X main.version=0.3.1"
//
// A var, not a const, for exactly that reason. The default is what a local
// build reports, and it should not look like a release.
var version = "0.1.0-dev"

// onlineInterval is how often agents enumerate connected addresses.
//
// This interval IS the sampling error. Measured on Xray 26.3.27, an address
// vanishes from the kernel's set within about two seconds of disconnecting —
// there is no lingering window — so a session that opens and closes between
// two rounds is never seen. Thirty seconds catches casual password sharing and
// will not catch someone deliberately keeping sessions short. Shortening it
// buys accuracy at one `xray api` subprocess per online user per round.
const onlineInterval = 30 * time.Second

// onlineEnabled reports whether the operator asked for source-address
// recording. Off by default: it is the only feature that records where users
// connect from, and that should be a decision, not a default.
func onlineEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHIRAL_ONLINE_RECORD"))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// enforceInterval is how often quota, expiry and renewal are reconciled onto
// the nodes. It bounds how far a user can run past their quota.
const enforceInterval = 60 * time.Second

func main() {
	var (
		dbPath = flag.String("db", envOr("CHIRAL_DB_PATH", "data/chiral.db"), "path to the SQLite database")
		// Deliberately unusual ports. A panel shares its host with whatever
		// else the operator runs, and 443, 80, 8080 and 8443 are all spoken
		// for on a typical box — by a web server, by Xray itself, or by
		// another panel. Defaults that collide turn the first start into a
		// bind error on somebody's production service. Both are below 32768
		// so they cannot clash with an outgoing connection's source port.
		grpcListen = flag.String("grpc-listen", envOr("CHIRAL_GRPC_LISTEN", ":26443"), "listen address for agent gRPC")
		httpListen = flag.String("http-listen", envOr("CHIRAL_HTTP_LISTEN", "127.0.0.1:26080"), "listen address for the REST API")
		grpcPublic = flag.String("grpc-public-addr", os.Getenv("CHIRAL_GRPC_PUBLIC_ADDR"), "address agents dial; defaults to the public URL's host on the gRPC port")
		publicURL  = flag.String("public-url", envOr("CHIRAL_PUBLIC_URL", ""), "the panel's own base URL, used to build subscription links")
		tlsCert    = flag.String("tls-cert", os.Getenv("CHIRAL_TLS_CERT"), "TLS certificate; serves HTTPS and gRPC-over-TLS when set")
		tlsKey     = flag.String("tls-key", os.Getenv("CHIRAL_TLS_KEY"), "TLS key")
		hbTimeout  = flag.Duration("heartbeat-timeout", 30*time.Second, "a node with no frames for this long counts as offline")
		rotate     = flag.Bool("rotate-secret-key", false,
			"re-seal every encrypted value from CHIRAL_SECRET_KEY to CHIRAL_SECRET_KEY_NEW, then exit")
		showVer = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Env only, never a flag: command lines leak via `ps` and shell history.
	adminToken := os.Getenv("CHIRAL_ADMIN_TOKEN")

	if *rotate {
		if err := rotateSecretKey(logger, *dbPath); err != nil {
			logger.Error("key rotation failed; the database is unchanged", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := run(logger, *dbPath, *grpcListen, *httpListen, *grpcPublic, *publicURL, *tlsCert, *tlsKey, adminToken, *hbTimeout); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// rotateSecretKey re-seals the database from the current key to a new one.
//
// A separate mode rather than something the running panel does, because it
// must have the database to itself: it rewrites every ciphertext in one
// transaction, and a Core serving traffic alongside would be reading rows it
// is about to invalidate.
//
// Both keys come from the environment for the same reason the admin token
// does — a key on a command line is in `ps` and in shell history.
func rotateSecretKey(logger *slog.Logger, dbPath string) error {
	oldKey, newKey := os.Getenv("CHIRAL_SECRET_KEY"), os.Getenv("CHIRAL_SECRET_KEY_NEW")
	if newKey == "" {
		return errors.New("set CHIRAL_SECRET_KEY_NEW to the key you are rotating TO " +
			"(CHIRAL_SECRET_KEY stays the current one)")
	}
	if newKey == oldKey {
		return errors.New("CHIRAL_SECRET_KEY_NEW is the same as the current key; nothing to do")
	}
	oldBox, err := secret.NewBox(oldKey)
	if err != nil {
		return fmt.Errorf("current key: %w", err)
	}
	newBox, err := secret.NewBox(newKey)
	if err != nil {
		return fmt.Errorf("new key: %w", err)
	}

	st, err := store.Open(dbPath, oldBox)
	if err != nil {
		return err
	}
	defer st.Close()

	logger.Info("re-sealing; take a copy of the database first if you have not", "db", dbPath)
	moved, err := st.RotateSecretKey(newBox)
	if err != nil {
		return err
	}
	logger.Info("rotation complete — set CHIRAL_SECRET_KEY to the new key and start normally",
		"values_resealed", moved)
	return nil
}

func run(logger *slog.Logger, dbPath, grpcListen, httpListen, grpcPublic, publicURL, tlsCert, tlsKey, adminToken string, hbTimeout time.Duration) error {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return err
	}
	// Secret variable components (REALITY private keys, post-quantum seeds)
	// are encrypted at rest with this key. Without it the database alone is
	// enough to impersonate every node, so say so plainly.
	box, err := secret.NewBox(os.Getenv("CHIRAL_SECRET_KEY"))
	if err != nil {
		return err
	}
	if !box.Enabled() {
		logger.Warn("CHIRAL_SECRET_KEY not set; private key material is stored UNENCRYPTED. Generate one with `openssl rand -base64 32`")
	}

	// The portal stores each subscription token in a form it can read back, so
	// it can show someone their own link. That is a bearer credential for a
	// public URL: without encryption, a copy of chiral.db is a working link for
	// every subscriber. A refusal rather than another warning — a warning
	// scrolls past, and this one cannot be undone after the fact.
	portalCfg := api.PortalFromEnv(publicURL)
	if portalCfg.Enabled() && !box.Enabled() {
		return errors.New("CHIRAL_PORTAL_MODE is set but CHIRAL_SECRET_KEY is empty: " +
			"the portal stores recoverable subscription tokens and will not do so in the clear. " +
			"Generate a key with `openssl rand -base64 32`")
	}
	// The portal's other prerequisite, refused for the same reason.
	//
	// Without a public URL the subscription link comes out as a bare path —
	// "/sub/<token>" — which is not something a customer can paste into any
	// client. Showing someone their own link is the portal's primary action, so
	// this is not a degraded feature but a broken one, and it fails silently on
	// the customer's side where no operator ever sees it. A warning would scroll
	// past exactly like the one this rule was written for.
	if portalCfg.Enabled() && strings.TrimSpace(publicURL) == "" {
		return errors.New("CHIRAL_PORTAL_MODE is set but CHIRAL_PUBLIC_URL is empty: " +
			"every subscriber would be shown a relative subscription link that no client can use. " +
			"Set it to the panel's externally reachable base URL, e.g. https://panel.example.com")
	}

	st, err := store.Open(dbPath, box)
	if err != nil {
		return err
	}
	defer st.Close()

	if adminToken == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		adminToken = hex.EncodeToString(b)
		logger.Warn("CHIRAL_ADMIN_TOKEN not set; generated a temporary admin token for this run", "token", adminToken)
	}

	// Bootstrap the first account. Doing it here rather than in a setup wizard
	// keeps a fresh deployment usable from its compose file alone, and the
	// generated password is printed once so it cannot sit in an env file.
	if err := bootstrapAdmin(logger, st); err != nil {
		return err
	}

	mgr := node.NewManager(hbTimeout, logger)
	svc := node.NewService(st, mgr, st, logger)

	// Source-address recording, off unless asked for. With it off no agent
	// polls, nothing is written, and user_devices stays empty — which is the
	// whole point of the switch: this is the one feature that records where
	// people connect from.
	var onlineReg *online.Registry
	if onlineEnabled() {
		// The TTL must outlast the polling interval comfortably, or a user
		// flickers offline between rounds.
		onlineReg = online.New(3 * onlineInterval)
		svc.EnableOnlineTracking(onlineReg, st, onlineInterval)
		logger.Info("recording online source addresses",
			"interval", onlineInterval, "retention", store.DeviceRetention)
	}

	// The panel keeps Xray binaries to validate a rendered config before
	// pushing it, and for key generators Go cannot implement. Plural, because
	// `xray -test` only answers for the build that runs it and nodes no longer
	// all run the same one — see core/internal/kernel.
	// One-time repair for keys written before x25519 scalars were clamped.
	// Safe to run every start: it changes only bits the published public key
	// never depended on, so subscriptions already in customers' clients keep
	// working — and until it runs, every REALITY inbound rejects every client.
	if n, err := st.RepairX25519Clamping(); err != nil {
		logger.Error("repairing x25519 private keys failed", "err", err)
	} else if n > 0 {
		logger.Warn("repaired x25519 private keys that REALITY could not use; "+
			"re-apply the affected nodes to push the corrected config", "count", n)
	}

	baked := template.Xray{Bin: os.Getenv("CHIRAL_XRAY_BIN")}
	if !baked.Available() {
		logger.Warn("no Xray binary (CHIRAL_XRAY_BIN); configs are pushed without panel-side validation and ML-DSA-65 is unavailable")
	}
	kernels := kernel.New(envOr("CHIRAL_KERNEL_DIR", "/var/lib/chiral/kernels"), baked)
	logger.Info("xray kernels available for validation", "versions", kernels.Versions())
	// Runtime kernel upgrades. The registry doubles as the install root, so a
	// version fetched for a node is immediately a version the panel can
	// validate configs against.
	upgrades := upgrade.NewService(st, kernels,
		release.Client{Token: os.Getenv("CHIRAL_GITHUB_TOKEN")}, mgr, logger)
	svc.EnableUpgrades(upgrades)
	defer upgrades.Close()

	// Where agents fetch from to prove traffic flows. Fleet-wide and settable
	// because an endpoint unreachable from one region would otherwise make
	// every canary there inconclusive with no way to correct it but a redeploy.
	mgr.SetProbeURL(os.Getenv("CHIRAL_PROBE_URL"))

	users := user.NewService(st, logger)
	profiles := profile.NewService(st, kernels, mgr, mgr, users, logger)
	subs := subscription.NewService(st, profiles)

	// Passkeys need a secure context; without a usable public URL they are
	// simply not offered rather than offered and failing at the last step.
	passkeys, err := passkey.New(publicURL, "Chiral")
	if err != nil {
		logger.Warn("passkeys unavailable", "reason", err)
		passkeys = nil
	}
	mailer := mail.NewSender(mail.FromEnv())
	if !mailer.Enabled() {
		logger.Info("SMTP not configured; email verification and email codes are unavailable")
	}
	alerts := alert.NewService(st, mgr, logger)
	// A rollback is announced immediately, bypassing the liveness debounce:
	// it does not flap, and the notification's whole value is arriving while
	// the person who pressed the button is still watching.
	upgrades.EnableAlerts(alerts)

	// One certificate, both listeners. Set it and the panel serves HTTPS and
	// gRPC-over-TLS itself; leave it and both are plaintext for a reverse proxy
	// to terminate. There is no third arrangement worth a setting.
	tlsCert, tlsKey = credentialPath(tlsCert), credentialPath(tlsKey)
	terminatesTLS := tlsCert != "" && tlsKey != ""
	if (tlsCert == "") != (tlsKey == "") {
		return errors.New("CHIRAL_TLS_CERT and CHIRAL_TLS_KEY must be set together")
	}

	// Whether AGENTS use TLS is a different question, and deriving it from the
	// certificate was wrong: behind a reverse proxy Core holds no certificate
	// while the endpoint agents dial is very much TLS. It comes from the public
	// URL's scheme instead — the one thing that always describes what is on the
	// outside, whoever terminates it.
	publicTLS := !strings.HasPrefix(publicURL, "http://")

	// Agents dial the panel's own host unless explicitly pointed elsewhere.
	// Repeating the hostname in a second setting is one more thing to get out
	// of step with the first.
	if grpcPublic == "" {
		host := publicHost(publicURL)
		if host == "" {
			return errors.New("set CHIRAL_PUBLIC_URL (e.g. https://panel.example.com) so agents know where to connect")
		}
		_, port, err := net.SplitHostPort(grpcListen)
		if err != nil || port == "" {
			port = "26443"
		}
		grpcPublic = net.JoinHostPort(host, port)
	}
	// Keepalive so both sides detect dead connections in ~40s instead of the
	// OS TCP default (minutes). MinTime guards against ping abuse.
	opts := []grpc.ServerOption{
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 10 * time.Second, PermitWithoutStream: true}),
	}
	if terminatesTLS {
		creds, err := credentials.NewServerTLSFromFile(tlsCert, tlsKey)
		if err != nil {
			return err
		}
		opts = append(opts, grpc.Creds(creds))
	} else if publicTLS {
		logger.Info("listening in plaintext; a reverse proxy must terminate TLS", "public", publicURL)
	} else {
		logger.Warn("CHIRAL_PUBLIC_URL is http://, so agents are told to connect in plaintext: " +
			"node credentials will cross the network in the clear")
	}
	grpcSrv := grpc.NewServer(opts...)
	chiralv1.RegisterAgentServiceServer(grpcSrv, svc)

	grpcLis, err := net.Listen("tcp", grpcListen)
	if err != nil {
		return err
	}
	apiServer := api.NewServer(st, mgr, profiles, subs, alerts, passkeys, mailer,
		adminToken, grpcPublic, publicURL, publicTLS, baked.Available(), logger)
	// Where the join snippet tells a node to pull the agent from. Defaults to
	// the published image; set it when running a fork or a private registry,
	// or the snippet an operator pastes points at something that is not there.
	apiServer.SetAgentImage(os.Getenv("CHIRAL_AGENT_IMAGE"))
	if onlineReg != nil {
		apiServer.EnableOnlineTracking(onlineReg)
	}
	apiServer.EnableUpgrades(upgrades)
	if portalCfg.Enabled() {
		apiServer.EnablePortal(portalCfg)
		logger.Info("end-user portal enabled", "mode", portalCfg.Mode,
			"invite_code", portalCfg.InviteCode != "")
	}
	httpSrv := &http.Server{
		Addr:    httpListen,
		Handler: apiServer.Handler(),
	}

	errCh := make(chan error, 2)
	go func() {
		logger.Info("gRPC listening", "addr", grpcListen, "tls", terminatesTLS)
		errCh <- grpcSrv.Serve(grpcLis)
	}()
	go func() {
		logger.Info("HTTP listening", "addr", httpListen, "tls", terminatesTLS, "version", version)
		var err error
		if terminatesTLS {
			err = httpSrv.ListenAndServeTLS(tlsCert, tlsKey)
		} else {
			err = httpSrv.ListenAndServe()
		}
		if !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	// Keep the panel's copy of the newest kernel warm, so pressing "upgrade" is
	// instant rather than a multi-minute wait behind a download nobody can see.
	// Best effort: a panel with no egress to GitHub is supported.
	go upgrades.RunPrewarm(ctx, 6*time.Hour)
	defer stop()

	// Quota and expiry are enforced by sweep rather than on the traffic path:
	// usage arrives between passes, so a user goes over quietly and is cut off
	// at the next one instead of mid-request.
	go func() {
		t := time.NewTicker(enforceInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if n, err := profiles.EnforceQuotas(ctx); err != nil {
					logger.Error("quota sweep failed", "err", err)
				} else if n > 0 {
					logger.Info("quota sweep changed user access", "users", n)
				}
				// Bound the series tables in the same pass; the panel is the
				// only writer, so a plain delete is enough.
				if n, err := st.PruneHistory(time.Now()); err != nil {
					logger.Error("pruning history failed", "err", err)
				} else if n > 0 {
					logger.Info("pruned expired history", "rows", n)
				}
				if _, err := st.PruneSessions(time.Now()); err != nil {
					logger.Error("pruning sessions failed", "err", err)
				}
				// Expired challenges are not merely clutter: they hold their
				// primary key, and several challenges are keyed off something
				// stable, so a stale row is what a later "send me another
				// code" collides with.
				if _, err := st.PruneChallenges(time.Now()); err != nil {
					logger.Error("pruning auth challenges failed", "err", err)
				}
				if _, err := st.PruneAudit(time.Now()); err != nil {
					logger.Error("pruning audit log failed", "err", err)
				}
				// node_configs was never bounded before: every apply left a
				// full sealed config behind forever.
				if n, err := st.PruneConfigs(); err != nil {
					logger.Error("pruning config history failed", "err", err)
				} else if n > 0 {
					logger.Info("pruned old config versions", "rows", n)
				}
				if portalCfg.Enabled() {
					if _, err := st.PrunePortalSessions(time.Now()); err != nil {
						logger.Error("pruning portal sessions failed", "err", err)
					}
					if _, err := st.PrunePortalChallenges(time.Now()); err != nil {
						logger.Error("pruning portal challenges failed", "err", err)
					}
				}
				if onlineReg != nil {
					// Addresses are personal data on a shorter clock than the
					// rest of the history; see store.DeviceRetention.
					if _, err := st.PruneDevices(time.Now()); err != nil {
						logger.Error("pruning device history failed", "err", err)
					}
					// Drops the view of any node that stopped reporting
					// without disconnecting.
					onlineReg.Prune(time.Now())
				}
				apiServer.PruneLimiter(time.Now())
				// Availability changes are announced from swept state rather
				// than at the moment of disconnection, so they survive a
				// panel restart and get their debounce for free.
				if n, err := alerts.Sweep(ctx); err != nil {
					logger.Error("alert sweep failed", "err", err)
				} else if n > 0 {
					logger.Info("sent node availability alerts", "count", n)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpSrv.Shutdown(shutdownCtx)
	// GracefulStop alone would wait forever on live Channel streams: close
	// all sessions so handlers return, and hard-stop as a backstop.
	mgr.CloseAll()
	done := make(chan struct{})
	go func() {
		grpcSrv.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		logger.Warn("graceful stop timed out; forcing")
		grpcSrv.Stop()
	}
	return nil
}

// bootstrapAdmin creates the first superadmin when the panel has no accounts
// yet. CHIRAL_ADMIN_USER / CHIRAL_ADMIN_PASSWORD seed it for an automated
// deployment; otherwise a password is generated and printed once.
//
// The environment token keeps working alongside accounts as a break-glass
// path, so this can never be the reason someone is locked out.
func bootstrapAdmin(logger *slog.Logger, st *store.Store) error {
	n, err := st.CountAdmins()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	username := envOr("CHIRAL_ADMIN_USER", "admin")
	password := os.Getenv("CHIRAL_ADMIN_PASSWORD")
	generated := password == ""
	if generated {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		password = base64.RawURLEncoding.EncodeToString(b)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if _, err := st.CreateAdmin(username, hash, auth.RoleSuperadmin); err != nil {
		return err
	}
	if generated {
		logger.Warn("created the first admin account; this password is shown once",
			"username", username, "password", password)
	} else {
		logger.Info("created the first admin account from the environment", "username", username)
	}
	return nil
}

// publicHost is the bare hostname of the panel's public URL, without scheme,
// port or path.
//
// url.Parse rather than trimming prefixes: the panel's own port is not 443 by
// default any more, so a hand-rolled TrimSuffix(":443") leaves "host:26080"
// behind and JoinHostPort then produces "host:26080:26443" — an address no
// agent can dial, in a command an operator pastes onto another machine.
func publicHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "//") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// credentialPath resolves systemd's "%d" credentials specifier.
//
// LoadCredential= is how a service running as a DynamicUser reads a root-owned
// private key: systemd copies it somewhere the dynamic UID can read and points
// $CREDENTIALS_DIRECTORY at it. Unit files write that location as %d, and
// systemd expands the specifier — but only inside unit directives. An
// EnvironmentFile is passed through verbatim, so CHIRAL_TLS_CERT=%d/tls-cert
// arrives here as those literal characters and open() fails on a path that
// does not exist.
//
// Expanding it here means the documented configuration is the one that works.
func credentialPath(p string) string {
	if !strings.HasPrefix(p, "%d/") {
		return p
	}
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if dir == "" {
		// Not started with LoadCredential. Leaving the literal in place makes
		// the resulting error name the path that was actually tried.
		return p
	}
	return filepath.Join(dir, strings.TrimPrefix(p, "%d/"))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
