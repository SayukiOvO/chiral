// chiral-core is the Panel backend: REST API for the frontend, the gRPC
// endpoint agents dial back to, and the SQLite source of truth.
package main

import (
	"context"
	"crypto/rand"
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

	"github.com/SayukiOvO/chiral/core/internal/api"
	"github.com/SayukiOvO/chiral/core/internal/node"
	"github.com/SayukiOvO/chiral/core/internal/store"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

const version = "0.1.0-dev"

func main() {
	var (
		dbPath     = flag.String("db", envOr("CHIRAL_DB_PATH", "data/chiral.db"), "path to the SQLite database")
		grpcListen = flag.String("grpc-listen", envOr("CHIRAL_GRPC_LISTEN", ":8443"), "listen address for agent gRPC")
		httpListen = flag.String("http-listen", envOr("CHIRAL_HTTP_LISTEN", ":8080"), "listen address for the REST API")
		grpcPublic = flag.String("grpc-public-addr", envOr("CHIRAL_GRPC_PUBLIC_ADDR", "localhost:8443"), "address agents dial, used in generated compose snippets")
		tlsCert    = flag.String("tls-cert", os.Getenv("CHIRAL_TLS_CERT"), "TLS certificate for the gRPC endpoint (plaintext if empty; dev only)")
		tlsKey     = flag.String("tls-key", os.Getenv("CHIRAL_TLS_KEY"), "TLS key for the gRPC endpoint")
		hbTimeout  = flag.Duration("heartbeat-timeout", 30*time.Second, "a node with no frames for this long counts as offline")
	)
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Env only, never a flag: command lines leak via `ps` and shell history.
	adminToken := os.Getenv("CHIRAL_ADMIN_TOKEN")

	if err := run(logger, *dbPath, *grpcListen, *httpListen, *grpcPublic, *tlsCert, *tlsKey, adminToken, *hbTimeout); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, dbPath, grpcListen, httpListen, grpcPublic, tlsCert, tlsKey, adminToken string, hbTimeout time.Duration) error {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return err
	}
	st, err := store.Open(dbPath)
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

	mgr := node.NewManager(hbTimeout, logger)
	svc := node.NewService(st, mgr, logger)

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
		Handler: api.NewServer(st, mgr, adminToken, grpcPublic, tlsEnabled, logger).Handler(),
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
