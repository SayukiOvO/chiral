package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/agent/internal/threexui"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

func TestRuntimeConfigFromEnv(t *testing.T) {
	t.Setenv("CHIRAL_RUNTIME_PROVIDER", runtimeProvider3XUIShadow)
	t.Setenv("CHIRAL_3XUI_URL", "http://127.0.0.1:2053")
	t.Setenv("CHIRAL_3XUI_TOKEN_FILE", "/run/secrets/3x-ui")
	t.Setenv("CHIRAL_3XUI_ALLOW_PUBLIC", "1")
	cfg, err := runtimeConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != runtimeProvider3XUIShadow || !cfg.AllowPublic || cfg.ThreeXUIURL == "" || cfg.TokenFile == "" {
		t.Fatalf("runtime config = %#v", cfg)
	}
}

func TestRuntimeConfigRejectsUnknownValues(t *testing.T) {
	t.Setenv("CHIRAL_RUNTIME_PROVIDER", "3x-ui-active")
	if _, err := runtimeConfigFromEnv(); err == nil {
		t.Fatal("future ACTIVE mode was accepted before parity gates exist")
	}
	t.Setenv("CHIRAL_RUNTIME_PROVIDER", runtimeProviderDirect)
	t.Setenv("CHIRAL_3XUI_ALLOW_PUBLIC", "true")
	if _, err := runtimeConfigFromEnv(); err != nil {
		t.Fatalf("direct provider should ignore 3x-ui-only settings: %v", err)
	}
	t.Setenv("CHIRAL_RUNTIME_PROVIDER", runtimeProvider3XUIShadow)
	t.Setenv("CHIRAL_3XUI_ALLOW_PUBLIC", "true")
	if _, err := runtimeConfigFromEnv(); err == nil {
		t.Fatal("ambiguous allow-public value was accepted")
	}
}

func TestReadSecretFileAcceptsOneTerminatingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("opaque-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readSecretFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "opaque-token" {
		t.Fatalf("token = %q", got)
	}
}

func TestReadSecretFileRejectsBroadPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("opaque-token"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecretFile(path); err == nil {
		t.Fatal("world-readable secret was accepted")
	}
}

func TestRuntimeStatusFromObservation(t *testing.T) {
	at := time.Unix(1777777777, 0)
	tests := []struct {
		name        string
		observation threexui.Observation
		health      chiralv1.RuntimeHealth
		errorPart   string
	}{
		{
			name: "ready",
			observation: threexui.Observation{
				PanelVersion:          "v3.7.0",
				XrayState:             "running",
				XrayVersion:           "25.8.3",
				SupportedCapabilities: threexui.RequiredCapabilities(),
				ContractDigest:        strings.Repeat("a", 64),
				ContractObservedAt:    at,
			},
			health: chiralv1.RuntimeHealth_RUNTIME_HEALTH_READY,
		},
		{
			name: "incompatible",
			observation: threexui.Observation{
				MissingCapabilities: []threexui.Capability{threexui.CapabilityXrayUpdate},
			},
			health:    chiralv1.RuntimeHealth_RUNTIME_HEALTH_INCOMPATIBLE,
			errorPart: "xray_update",
		},
		{
			name: "credential incompatible",
			observation: threexui.Observation{
				Error: &threexui.HTTPStatusError{StatusCode: 403, Status: "403 Forbidden"},
			},
			health:    chiralv1.RuntimeHealth_RUNTIME_HEALTH_INCOMPATIBLE,
			errorPart: "credential is incompatible",
		},
		{
			name: "unreachable",
			observation: threexui.Observation{
				PanelVersion: "v3.7.0",
				Error:        errors.New("upstream included an unsafe detail"),
			},
			health:    chiralv1.RuntimeHealth_RUNTIME_HEALTH_UNREACHABLE,
			errorPart: "see Agent log",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runtimeStatusFromObservation(tt.observation, at)
			if got.GetProvider() != "3x-ui" || got.GetMode() != chiralv1.RuntimeMode_RUNTIME_MODE_SHADOW || got.GetHealth() != tt.health || got.GetObservedAtUnix() != at.Unix() {
				t.Fatalf("status = %+v", got)
			}
			if !strings.Contains(got.GetError(), tt.errorPart) {
				t.Fatalf("error %q does not contain %q", got.GetError(), tt.errorPart)
			}
			if strings.Contains(got.GetError(), "unsafe detail") {
				t.Fatalf("upstream detail crossed the node boundary: %q", got.GetError())
			}
			if tt.name == "ready" && (got.GetXrayState() != chiralv1.XrayState_XRAY_STATE_RUNNING || got.GetXrayVersion() != "25.8.3") {
				t.Fatalf("shadow Xray observation was dropped: %+v", got)
			}
		})
	}
}

func TestRuntimeStatusRejectsIncompleteReadyEvidence(t *testing.T) {
	got := runtimeStatusFromObservation(threexui.Observation{
		PanelVersion:          "v3.7.0",
		XrayState:             "new-upstream-state",
		SupportedCapabilities: threexui.RequiredCapabilities(),
	}, time.Unix(1777777777, 0))
	if got.GetHealth() != chiralv1.RuntimeHealth_RUNTIME_HEALTH_INCOMPATIBLE {
		t.Fatalf("incomplete evidence was READY: %+v", got)
	}
}

type blockingShadowObserver struct {
	started chan struct{}
	release chan struct{}
	result  threexui.Observation
}

func (o *blockingShadowObserver) Observe(ctx context.Context) threexui.Observation {
	select {
	case <-o.started:
	default:
		close(o.started)
	}
	select {
	case <-o.release:
		return o.result
	case <-ctx.Done():
		return threexui.Observation{Error: ctx.Err()}
	}
}

func TestShadowMonitorKeepsHeartbeatSnapshotNonBlocking(t *testing.T) {
	observer := &blockingShadowObserver{
		started: make(chan struct{}),
		release: make(chan struct{}),
		result: threexui.Observation{
			PanelVersion:          "v3.7.0",
			XrayState:             "running",
			SupportedCapabilities: threexui.RequiredCapabilities(),
			ContractDigest:        strings.Repeat("a", 64),
			ContractObservedAt:    time.Now(),
		},
	}
	monitor := newShadowRuntimeMonitorWithObserver(
		observer,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		time.Hour,
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go monitor.Run(ctx)
	select {
	case <-observer.started:
	case <-time.After(time.Second):
		t.Fatal("background observation did not start")
	}

	started := time.Now()
	pending := monitor.Snapshot()
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("snapshot blocked behind HTTP observation for %s", elapsed)
	}
	if pending.GetHealth() != chiralv1.RuntimeHealth_RUNTIME_HEALTH_UNSPECIFIED || pending.GetError() != "observation pending" {
		t.Fatalf("pending snapshot = %+v", pending)
	}
	close(observer.release)
	deadline := time.Now().Add(time.Second)
	for monitor.Snapshot().GetHealth() != chiralv1.RuntimeHealth_RUNTIME_HEALTH_READY {
		if time.Now().After(deadline) {
			t.Fatal("completed observation was not published")
		}
		time.Sleep(time.Millisecond)
	}

	first := monitor.Snapshot()
	first.Capabilities = append(first.Capabilities, "mutated")
	if len(monitor.Snapshot().GetCapabilities()) != len(threexui.RequiredCapabilities()) {
		t.Fatal("snapshot caller mutated the cached protobuf")
	}
}

func TestShadowSnapshotReportsRelativeContractAge(t *testing.T) {
	monitor := newShadowRuntimeMonitorWithObserver(
		&blockingShadowObserver{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		time.Hour,
	)
	monitor.now = func() time.Time { return time.Unix(200, 0) }
	monitor.current = &chiralv1.RuntimeStatus{ObservedAtUnix: 125}
	if got := monitor.Snapshot().GetObservedAgeSeconds(); got != 75 {
		t.Fatalf("observation age = %d, want 75", got)
	}
}
