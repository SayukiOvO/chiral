// chiral-agent runs on every node host: it registers with Core using a
// one-time join token, keeps a persistent gRPC stream, and manages the local
// Xray-core process.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SayukiOvO/chiral/agent/internal/agentlock"
	"github.com/SayukiOvO/chiral/agent/internal/client"
	"github.com/SayukiOvO/chiral/agent/internal/collector"
	"github.com/SayukiOvO/chiral/agent/internal/xray"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// version is stamped at link time by the release build; see the panel's copy.
var version = "0.1.0-dev"

func main() {
	var (
		panelAddr  = flag.String("panel", envOr("PANEL_URL", ""), "host:port of Core's gRPC endpoint")
		stateDir   = flag.String("state-dir", envOr("CHIRAL_STATE_DIR", "/var/lib/chiral-agent"), "directory for identity and config.json")
		xrayBin    = flag.String("xray-bin", envOr("CHIRAL_XRAY_BIN", "xray"), "path to the Xray-core binary")
		insecureTr = flag.Bool("insecure", os.Getenv("CHIRAL_INSECURE") == "1", "use plaintext gRPC (dev only)")
		hbInterval = flag.Duration("heartbeat-interval", 10*time.Second, "heartbeat period")
		showVer    = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Env only, never a flag: command lines leak via `ps` and shell history.
	joinToken := os.Getenv("JOIN_TOKEN")

	if *panelAddr == "" {
		logger.Error("PANEL_URL (or -panel) is required")
		os.Exit(2)
	}

	// One agent per state dir. Held for the whole run; see agentlock for why
	// two supervisors over one directory is unrecoverable rather than merely
	// noisy.
	unlock, err := agentlock.Acquire(*stateDir)
	if err != nil {
		logger.Error("could not lock the state directory", "dir", *stateDir, "err", err)
		os.Exit(2)
	}
	defer unlock()

	col := collector.New()
	// The event hook is wired after the client exists; xray only holds the
	// indirection.
	var cl *client.Client
	xr := xray.New(*xrayBin, *stateDir, logger, func(kind chiralv1.EventKind, msg string) {
		if cl != nil {
			cl.QueueEvent(kind, msg)
		}
	})
	// A kernel installed by a previous run wins over the one baked into the
	// image, but only if it is really there and really is what it claims. The
	// image's binary is the floor: a container restart must not silently
	// downgrade a node Core believes it has upgraded.
	if active := xray.NewInstaller(*stateDir, xr).ActiveBinary(); active != "" && active != *xrayBin {
		logger.Info("using the kernel installed by a previous run", "bin", active)
		xr.UseBinary(active)
	}

	cl = client.New(client.Config{
		PanelAddr:         *panelAddr,
		JoinToken:         joinToken,
		StateDir:          *stateDir,
		Insecure:          *insecureTr,
		AgentVersion:      version,
		HeartbeatInterval: *hbInterval,
	}, xr, col, logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Info("chiral-agent starting", "version", version, "panel", *panelAddr)

	// Serve with the last known-good config immediately; don't leave proxies
	// down while (re)connecting to Core. The config version is unknown until
	// Core reconciles, which is harmless: identical content skips a restart.
	if xr.HasPersistedConfig() {
		if err := xr.Start(); err != nil {
			logger.Error("could not start xray-core from persisted config", "err", err)
		}
	}

	err = cl.Run(ctx)
	xr.Stop()
	if err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
