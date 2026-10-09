package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/SayukiOvO/chiral/agent/internal/client"
	"github.com/SayukiOvO/chiral/agent/internal/collector"
	"github.com/SayukiOvO/chiral/agent/internal/threexui"
)

func runCandidate(ctx context.Context, cfg runtimeConfig, clCfg client.Config, initialize bool, provisionFile string, logger *slog.Logger) error {
	// Never reinterpret an existing direct-Xray state directory as an HTTP
	// provider. An isolated candidate gets its own Agent identity and volume.
	if _, err := os.Lstat(filepath.Join(clCfg.StateDir, "config.json")); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("3x-ui candidate requires a separate state directory without direct-Xray config.json")
	}
	token, err := readSecretFile(cfg.TokenFile)
	if err != nil {
		return err
	}
	api, err := threexui.New(threexui.Config{BaseURL: cfg.ThreeXUIURL, Token: token, Timeout: 15 * time.Second})
	if err != nil {
		return err
	}
	preflight, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dir := filepath.Join(clCfg.StateDir, "3x-ui")
	var ownership *threexui.OwnershipStore
	if initialize {
		ownership, err = threexui.InitializeRuntimeStore(preflight, api, dir)
	} else {
		status, statusErr := api.Status(preflight)
		if statusErr != nil {
			return fmt.Errorf("candidate provider identity could not be verified")
		}
		binding, bindingErr := threexui.RuntimeStoreBinding(api, status.PanelGUID)
		if bindingErr != nil {
			return bindingErr
		}
		ownership, err = threexui.OpenOwnershipStore(dir, binding)
	}
	if err != nil {
		return err
	}
	defer ownership.Close()
	rt, err := threexui.NewRuntime(preflight, api, ownership, threexui.RuntimeConfig{
		InitializeReporting: initialize, CandidateUserWrites: cfg.CandidateUserWrites,
	})
	if err != nil {
		return err
	}
	defer rt.Close()
	if provisionFile != "" {
		inbound, err := readCandidateProvisionFile(provisionFile)
		if err != nil {
			return err
		}
		if err := rt.ProvisionInbound(preflight, inbound); err != nil {
			return err
		}
		logger.Info("isolated candidate bootstrap inbound confirmed")
	}
	clCfg.RuntimeStatusSnapshot = rt.Snapshot
	clCfg.UserOperationReceipts = true
	cl := client.NewWithRuntime(clCfg, rt, collector.New(), logger)
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	monitorDone := make(chan struct{})
	go func() { defer close(monitorDone); rt.Run(monitorCtx) }()
	defer func() { stopMonitor(); <-monitorDone }()
	logger.Info("isolated 3x-ui candidate starting", "user_writes", cfg.CandidateUserWrites)
	return cl.Run(ctx)
}

func readCandidateProvisionFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 64<<10 {
		return nil, fmt.Errorf("candidate bootstrap requires a regular private mode-0600 JSON file of at most 64 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("candidate bootstrap file could not be read")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("candidate bootstrap file identity changed")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return nil, fmt.Errorf("candidate bootstrap file could not be read within the size limit")
	}
	return raw, nil
}
