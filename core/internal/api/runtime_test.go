package api

import (
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/SayukiOvO/chiral/core/internal/node"
	"github.com/SayukiOvO/chiral/core/internal/store"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

func TestOfflineNodeRuntimeIsUnknown(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &Server{mgr: node.NewManager(30*time.Second, logger)}
	v := s.view(store.Node{ID: "offline"})
	if v.RuntimeProvider != "unknown" || v.RuntimeMode != "UNKNOWN" || v.RuntimeHealth != "UNKNOWN" {
		t.Fatalf("offline node invented an active runtime: %#v", v)
	}
}

func TestPresentMalformedRuntimeDoesNotUseLegacyFallback(t *testing.T) {
	v := nodeView{RuntimeProvider: "unknown", RuntimeMode: "UNKNOWN", RuntimeHealth: "UNKNOWN"}
	applyHeartbeatRuntimeStatus(&v, &chiralv1.Heartbeat{Runtime: &chiralv1.RuntimeStatus{}}, time.Now())
	if v.RuntimeProvider != "unknown" || v.RuntimeMode != "UNKNOWN" || v.RuntimeHealth != "UNKNOWN" {
		t.Fatalf("malformed status was mistaken for a legacy Agent: %#v", v)
	}
}

func TestAbsentRuntimeUsesLegacyFallback(t *testing.T) {
	at := time.Unix(1777777777, 0)
	v := nodeView{}
	applyHeartbeatRuntimeStatus(&v, &chiralv1.Heartbeat{}, at)
	if v.RuntimeProvider != "direct-xray" || v.RuntimeMode != "ACTIVE" || v.RuntimeHealth != "READY" || v.RuntimeObservedAt != at.Unix() {
		t.Fatalf("legacy Agent compatibility was lost: %#v", v)
	}
}

func TestApplyRuntimeStatus(t *testing.T) {
	receivedAt := time.Unix(1777777792, 0)
	v := nodeView{RuntimeProvider: "direct-xray", RuntimeMode: "ACTIVE"}
	applyRuntimeStatus(&v, &chiralv1.RuntimeStatus{
		Provider:           "3x-ui",
		Mode:               chiralv1.RuntimeMode_RUNTIME_MODE_SHADOW,
		Health:             chiralv1.RuntimeHealth_RUNTIME_HEALTH_READY,
		Version:            "v3.7.0",
		Capabilities:       append([]string(nil), required3XUIShadowCapabilities...),
		ContractDigest:     strings.Repeat("a", 64),
		Error:              "",
		ObservedAtUnix:     1777777777,
		ObservedAgeSeconds: 15,
		XrayState:          chiralv1.XrayState_XRAY_STATE_RUNNING,
		XrayVersion:        "25.8.3",
	}, receivedAt)

	if v.RuntimeProvider != "3x-ui" || v.RuntimeMode != "SHADOW" || v.RuntimeVersion != "v3.7.0" {
		t.Fatalf("runtime view = %#v", v)
	}
	if want := required3XUIShadowCapabilities; !reflect.DeepEqual(v.RuntimeCapabilities, want) {
		t.Fatalf("capabilities = %v, want %v", v.RuntimeCapabilities, want)
	}
	if v.RuntimeContract != strings.Repeat("a", 64) {
		t.Fatalf("contract digest = %q", v.RuntimeContract)
	}
	if v.RuntimeHealth != "READY" || v.RuntimeObservedAt != 1777777777 {
		t.Fatalf("runtime health/freshness = %q/%d", v.RuntimeHealth, v.RuntimeObservedAt)
	}
	if v.RuntimeXrayState != "RUNNING" || v.RuntimeXrayVersion != "25.8.3" {
		t.Fatalf("shadow Xray view = %q/%q", v.RuntimeXrayState, v.RuntimeXrayVersion)
	}
}

func TestApplyRuntimeStatusRejectsUnsupported3XUIActiveMode(t *testing.T) {
	v := nodeView{}
	applyRuntimeStatus(&v, &chiralv1.RuntimeStatus{
		Provider:       "3x-ui",
		Mode:           chiralv1.RuntimeMode_RUNTIME_MODE_ACTIVE,
		Health:         chiralv1.RuntimeHealth_RUNTIME_HEALTH_READY,
		ObservedAtUnix: time.Now().Unix(),
	}, time.Now())
	if v.RuntimeMode != "UNKNOWN" || v.RuntimeHealth != "UNKNOWN" || !strings.Contains(v.RuntimeError, "not supported") {
		t.Fatalf("unsupported 3x-ui ACTIVE mode was trusted: %#v", v)
	}
}

func TestApplyRuntimeStatusRequiresFreshReadyEvidence(t *testing.T) {
	v := nodeView{}
	applyRuntimeStatus(&v, &chiralv1.RuntimeStatus{
		Provider: "direct-xray",
		Mode:     chiralv1.RuntimeMode_RUNTIME_MODE_ACTIVE,
		Health:   chiralv1.RuntimeHealth_RUNTIME_HEALTH_READY,
	}, time.Now())
	if v.RuntimeHealth != "UNKNOWN" {
		t.Fatalf("READY without an observation time was trusted: %#v", v)
	}
}

func TestApplyRuntimeStatusMapsFutureEnumsToUnknown(t *testing.T) {
	v := nodeView{}
	applyRuntimeStatus(&v, &chiralv1.RuntimeStatus{
		Provider: "future-provider",
		Mode:     chiralv1.RuntimeMode(99),
		Health:   chiralv1.RuntimeHealth(99),
	}, time.Now())
	if v.RuntimeMode != "UNKNOWN" || v.RuntimeHealth != "UNKNOWN" {
		t.Fatalf("future enums looked active/healthy: %#v", v)
	}
}

func TestBoundedRuntimeText(t *testing.T) {
	got := boundedRuntimeText("  unavailable\n"+strings.Repeat("界", 300), 256)
	if len(got) > 256 || strings.ContainsAny(got, "\r\n\x00") || !utf8.ValidString(got) {
		t.Fatalf("unsafe runtime summary %q (len %d)", got, len(got))
	}
}

func TestRuntimeStatusBoundsUntrustedFieldsAndNormalisesClock(t *testing.T) {
	capabilities := make([]string, 40)
	for i := range capabilities {
		capabilities[i] = "capability"
	}
	v := nodeView{}
	receivedAt := time.Unix(1777777777, 0)
	applyRuntimeStatus(&v, &chiralv1.RuntimeStatus{
		Provider:       strings.Repeat("p", 100),
		Version:        strings.Repeat("v", 100),
		Capabilities:   capabilities,
		ContractDigest: strings.Repeat("G", 64),
		Error:          strings.Repeat("e", 300),
		ObservedAtUnix: time.Now().Add(time.Hour).Unix(),
	}, receivedAt)
	if len(v.RuntimeProvider) > 64 || len(v.RuntimeVersion) > 64 || len(v.RuntimeCapabilities) > 32 || len(v.RuntimeError) > 256 {
		t.Fatalf("unbounded runtime view: %#v", v)
	}
	if v.RuntimeContract != "" || v.RuntimeObservedAt != receivedAt.Unix() {
		t.Fatalf("invalid digest or Agent wall-clock crossed API boundary: %#v", v)
	}
}

func TestRuntimeStatusRequiresTimeForFailureHealth(t *testing.T) {
	v := nodeView{}
	applyRuntimeStatus(&v, &chiralv1.RuntimeStatus{
		Provider: "3x-ui",
		Mode:     chiralv1.RuntimeMode_RUNTIME_MODE_SHADOW,
		Health:   chiralv1.RuntimeHealth_RUNTIME_HEALTH_UNREACHABLE,
	}, time.Now())
	if v.RuntimeHealth != "UNKNOWN" {
		t.Fatalf("failure without an observation time was trusted forever: %#v", v)
	}
}

func TestApplyRuntimeStatusIgnoresMissingObservation(t *testing.T) {
	v := nodeView{}
	applyRuntimeStatus(&v, nil, time.Now())
	if v.RuntimeProvider != "" || v.RuntimeMode != "" {
		t.Fatalf("missing observation invented a runtime: %#v", v)
	}
}
