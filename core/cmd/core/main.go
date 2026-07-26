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
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	"github.com/SayukiOvO/chiral/core/internal/alert"
	"github.com/SayukiOvO/chiral/core/internal/api"
	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/mail"
	"github.com/SayukiOvO/chiral/core/internal/node"
	"github.com/SayukiOvO/chiral/core/internal/passkey"
	"github.com/SayukiOvO/chiral/core/internal/profile"
	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/subscription"
	"github.com/SayukiOvO/chiral/core/internal/template"
	"github.com/SayukiOvO/chiral/core/internal/user"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

const version = "0.1.0-dev"

// enforceInterval is how often quota, expiry and renewal are reconciled onto
// the nodes. It bounds how far a user can run past their quota.
const enforceInterval = 60 * time.Second

func main() {
	var (
		dbPath     = flag.String("db", envOr("CHIRAL_DB_PATH", "data/chiral.db"), "path to the SQLite database")
		grpcListen = flag.String("grpc-listen", envOr("CHIRAL_GRPC_LISTEN", ":8443"), "listen address for agent gRPC")
		httpListen = flag.String("http-listen", envOr("CHIRAL_HTTP_LISTEN", ":8080"), "listen address for the REST API")
		grpcPublic = flag.String("grpc-public-addr", envOr("CHIRAL_GRPC_PUBLIC_ADDR", "localhost:8443"), "address agents dial, used in generated compose snippets")
		publicURL  = flag.String("public-url", envOr("CHIRAL_PUBLIC_URL", ""), "the panel's own base URL, used to build subscription links")
		tlsCert    = flag.String("tls-cert", os.Getenv("CHIRAL_TLS_CERT"), "TLS certificate for the gRPC endpoint (plaintext if empty; dev only)")
		tlsKey     = flag.String("tls-key", os.Getenv("CHIRAL_TLS_KEY"), "TLS key for the gRPC endpoint")
		hbTimeout  = flag.Duration("heartbeat-timeout", 30*time.Second, "a node with no frames for this long counts as offline")
	)
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Env only, never a flag: command lines leak via `ps` and shell history.
	adminToken := os.Getenv("CHIRAL_ADMIN_TOKEN")

	if err := run(logger, *dbPath, *grpcListen, *httpListen, *grpcPublic, *publicURL, *tlsCert, *tlsKey, adminToken, *hbTimeout); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
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

	// The panel keeps its own Xray binary to validate a rendered config
	// before pushing it, and for key generators Go cannot implement.
	xray := template.Xray{Bin: os.Getenv("CHIRAL_XRAY_BIN")}
	if !xray.Available() {
		logger.Warn("no Xray binary (CHIRAL_XRAY_BIN); configs are pushed without panel-side validation and ML-DSA-65 is unavailable")
	}
	users := user.NewService(st, logger)
	profiles := profile.NewService(st, xray, mgr, mgr, users, logger)
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

	tlsEnabled := tlsCert != "" || tlsKey != ""
	// Keepalive so both sides detect dead connections in ~40s instead of the
	// OS TCP default (minutes). MinTime guards against ping abuse.
	opts := []grpc.ServerOption{
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 10 * time.Second, PermitWithoutStream: true}),
	}
	if tlsEnabled {
		creds, err := credentials.NewServerTLSFromFile(tlsCert, tlsKey)
		if err != nil {
			return err
		}
		opts = append(opts, grpc.Creds(creds))
	} else {
		logger.Warn("gRPC endpoint is PLAINTEXT; set CHIRAL_TLS_CERT/CHIRAL_TLS_KEY in production")
	}
	grpcSrv := grpc.NewServer(opts...)
	chiralv1.RegisterAgentServiceServer(grpcSrv, svc)

	grpcLis, err := net.Listen("tcp", grpcListen)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Addr:    httpListen,
		Handler: api.NewServer(st, mgr, profiles, subs, alerts, passkeys, mailer, adminToken, grpcPublic, publicURL, tlsEnabled, xray.Available(), logger).Handler(),
	}

	errCh := make(chan error, 2)
	go func() {
		logger.Info("gRPC listening", "addr", grpcListen, "tls", tlsCert != "")
		errCh <- grpcSrv.Serve(grpcLis)
	}()
	go func() {
		logger.Info("HTTP listening", "addr", httpListen, "version", version)
		if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
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
				if _, err := st.PruneAudit(time.Now()); err != nil {
					logger.Error("pruning audit log failed", "err", err)
				}
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
