package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/SayukiOvO/chiral/agent/internal/threexui"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

const (
	runtimeProviderDirect     = "direct-xray"
	runtimeProvider3XUIShadow = "3x-ui-shadow"
	maxTokenFileBytes         = 8 << 10
	shadowRequestTimeout      = 8 * time.Second
	shadowObservationInterval = 10 * time.Second
)

type runtimeConfig struct {
	Provider    string
	ThreeXUIURL string
	TokenFile   string
	AllowPublic bool
}

func runtimeConfigFromEnv() (runtimeConfig, error) {
	cfg := runtimeConfig{
		Provider:    envOr("CHIRAL_RUNTIME_PROVIDER", runtimeProviderDirect),
		ThreeXUIURL: os.Getenv("CHIRAL_3XUI_URL"),
		TokenFile:   os.Getenv("CHIRAL_3XUI_TOKEN_FILE"),
	}
	if cfg.Provider != runtimeProviderDirect && cfg.Provider != runtimeProvider3XUIShadow {
		return runtimeConfig{}, fmt.Errorf("CHIRAL_RUNTIME_PROVIDER must be %q or %q", runtimeProviderDirect, runtimeProvider3XUIShadow)
	}
	if cfg.Provider == runtimeProviderDirect {
		return cfg, nil
	}
	switch value := os.Getenv("CHIRAL_3XUI_ALLOW_PUBLIC"); value {
	case "", "0":
	case "1":
		cfg.AllowPublic = true
	default:
		return runtimeConfig{}, fmt.Errorf("CHIRAL_3XUI_ALLOW_PUBLIC must be 0 or 1")
	}
	return cfg, nil
}

type shadowObserver interface {
	Observe(context.Context) threexui.Observation
}

// shadowRuntimeMonitor is the I/O boundary between a node-local 3x-ui and
// latency-sensitive gRPC heartbeats. Run owns all HTTP calls; Snapshot only
// clones the latest immutable value under a short read lock.
type shadowRuntimeMonitor struct {
	observer shadowObserver
	logger   *slog.Logger
	interval time.Duration

	mu      sync.RWMutex
	current *chiralv1.RuntimeStatus
	now     func() time.Time
}

func newShadowRuntimeMonitor(cfg runtimeConfig, logger *slog.Logger) (*shadowRuntimeMonitor, error) {
	if cfg.Provider != runtimeProvider3XUIShadow {
		return nil, fmt.Errorf("shadow monitor requires the %q provider", runtimeProvider3XUIShadow)
	}
	token, err := readSecretFile(cfg.TokenFile)
	if err != nil {
		return nil, fmt.Errorf("reading CHIRAL_3XUI_TOKEN_FILE: %w", err)
	}
	api, err := threexui.New(threexui.Config{
		BaseURL:     cfg.ThreeXUIURL,
		Token:       token,
		AllowPublic: cfg.AllowPublic,
		Timeout:     3 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	observer, err := threexui.NewObserver(api, threexui.ObserverConfig{})
	if err != nil {
		return nil, err
	}
	return newShadowRuntimeMonitorWithObserver(observer, logger, shadowObservationInterval), nil
}

func newShadowRuntimeMonitorWithObserver(observer shadowObserver, logger *slog.Logger, interval time.Duration) *shadowRuntimeMonitor {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = shadowObservationInterval
	}
	return &shadowRuntimeMonitor{
		observer: observer,
		logger:   logger,
		interval: interval,
		now:      time.Now,
		current: &chiralv1.RuntimeStatus{
			Provider: "3x-ui",
			Mode:     chiralv1.RuntimeMode_RUNTIME_MODE_SHADOW,
			Error:    "observation pending",
		},
	}
}

func (m *shadowRuntimeMonitor) Run(ctx context.Context) {
	m.refresh(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.refresh(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (m *shadowRuntimeMonitor) refresh(ctx context.Context) {
	requestCtx, cancel := context.WithTimeout(ctx, shadowRequestTimeout)
	observation := m.observer.Observe(requestCtx)
	cancel()
	status := runtimeStatusFromObservation(observation, m.now())
	if observation.Error != nil {
		// The detailed error is useful locally and has already passed through
		// the 3x-ui client's token redaction. Core receives only a fixed summary.
		m.logger.Warn("3x-ui shadow observation failed", "err", observation.Error)
	} else if len(observation.MissingCapabilities) != 0 {
		m.logger.Warn("3x-ui shadow contract is incompatible", "missing", observation.MissingCapabilities)
	}
	m.mu.Lock()
	m.current = status
	m.mu.Unlock()
}

func (m *shadowRuntimeMonitor) Snapshot() *chiralv1.RuntimeStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	status := copyRuntimeStatus(m.current)
	if status != nil && status.GetObservedAtUnix() > 0 {
		age := m.now().Unix() - status.GetObservedAtUnix()
		switch {
		case age <= 0:
			status.ObservedAgeSeconds = 0
		case age > math.MaxUint32:
			status.ObservedAgeSeconds = math.MaxUint32
		default:
			status.ObservedAgeSeconds = uint32(age)
		}
	}
	return status
}

func runtimeStatusFromObservation(observation threexui.Observation, at time.Time) *chiralv1.RuntimeStatus {
	observedAt := at
	if observation.Error == nil && !observation.ContractObservedAt.IsZero() {
		observedAt = observation.ContractObservedAt
	}
	status := &chiralv1.RuntimeStatus{
		Provider:       "3x-ui",
		Mode:           chiralv1.RuntimeMode_RUNTIME_MODE_SHADOW,
		Version:        boundedPrintable(observation.PanelVersion, 64),
		ContractDigest: observation.ContractDigest,
		ObservedAtUnix: observedAt.Unix(),
		XrayState:      shadowXrayState(observation.XrayState),
		XrayVersion:    boundedPrintable(observation.XrayVersion, 64),
	}
	for _, capability := range observation.SupportedCapabilities {
		status.Capabilities = append(status.Capabilities, string(capability))
	}
	switch {
	case observation.Error != nil:
		if isProviderCompatibilityError(observation.Error) {
			status.Health = chiralv1.RuntimeHealth_RUNTIME_HEALTH_INCOMPATIBLE
			status.Error = "provider contract or credential is incompatible; see Agent log"
		} else {
			status.Health = chiralv1.RuntimeHealth_RUNTIME_HEALTH_UNREACHABLE
			status.Error = "provider observation failed; see Agent log"
		}
	case len(observation.MissingCapabilities) != 0:
		status.Health = chiralv1.RuntimeHealth_RUNTIME_HEALTH_INCOMPATIBLE
		missing := make([]string, 0, len(observation.MissingCapabilities))
		for _, capability := range observation.MissingCapabilities {
			missing = append(missing, string(capability))
		}
		status.Error = "missing required capabilities: " + strings.Join(missing, ", ")
	case observation.ContractObservedAt.IsZero() || status.GetContractDigest() == "" || status.GetXrayState() == chiralv1.XrayState_XRAY_STATE_UNSPECIFIED:
		status.Health = chiralv1.RuntimeHealth_RUNTIME_HEALTH_INCOMPATIBLE
		status.Error = "provider returned an incomplete shadow contract"
	default:
		status.Health = chiralv1.RuntimeHealth_RUNTIME_HEALTH_READY
	}
	return status
}

func isProviderCompatibilityError(err error) bool {
	var statusErr *threexui.HTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode >= 400 && statusErr.StatusCode < 500 && statusErr.StatusCode != 408 && statusErr.StatusCode != 429
	}
	var apiErr *threexui.APIError
	if errors.As(err, &apiErr) {
		return true
	}
	var contractErr *threexui.ContractError
	if errors.As(err, &contractErr) {
		return true
	}
	var sizeErr *threexui.ResponseTooLargeError
	return errors.As(err, &sizeErr)
}

func shadowXrayState(value string) chiralv1.XrayState {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "running":
		return chiralv1.XrayState_XRAY_STATE_RUNNING
	case "stop", "stopped":
		return chiralv1.XrayState_XRAY_STATE_STOPPED
	case "error":
		return chiralv1.XrayState_XRAY_STATE_ERROR
	default:
		return chiralv1.XrayState_XRAY_STATE_UNSPECIFIED
	}
}

func copyRuntimeStatus(in *chiralv1.RuntimeStatus) *chiralv1.RuntimeStatus {
	if in == nil {
		return nil
	}
	return &chiralv1.RuntimeStatus{
		Provider:           in.GetProvider(),
		Mode:               in.GetMode(),
		Version:            in.GetVersion(),
		Capabilities:       append([]string(nil), in.GetCapabilities()...),
		Error:              in.GetError(),
		ContractDigest:     in.GetContractDigest(),
		ObservedAtUnix:     in.GetObservedAtUnix(),
		Health:             in.GetHealth(),
		XrayState:          in.GetXrayState(),
		XrayVersion:        in.GetXrayVersion(),
		ObservedAgeSeconds: in.GetObservedAgeSeconds(),
	}
}

func boundedPrintable(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		if r < 0x21 || r > 0x7e || b.Len()+1 > maxBytes {
			continue
		}
		b.WriteByte(byte(r))
	}
	return b.String()
}

func readSecretFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("secret must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("secret file permissions must not grant group or other access")
	}
	if info.Size() > maxTokenFileBytes {
		return "", fmt.Errorf("secret file exceeds %d bytes", maxTokenFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxTokenFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxTokenFileBytes {
		return "", fmt.Errorf("secret file exceeds %d bytes", maxTokenFileBytes)
	}
	// Secret managers commonly terminate files with exactly one newline. Do
	// not TrimSpace: leading/trailing spaces are invalid token bytes and should
	// fail validation rather than silently changing the credential.
	token := string(data)
	if strings.HasSuffix(token, "\n") {
		token = strings.TrimSuffix(token, "\n")
		token = strings.TrimSuffix(token, "\r")
	}
	if token == "" {
		return "", fmt.Errorf("secret file is empty")
	}
	return token, nil
}
