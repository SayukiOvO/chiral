package threexui

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

func TestCandidateBootstrapRejectsUnsupportedFieldsAndImplicitUsers(t *testing.T) {
	base := `{"tag":"lab-vless","listen":"127.0.0.1","port":25001,"protocol":"vless","settings":{"decryption":"none","clients":[]},"streamSettings":{"network":"tcp","security":"none"},"sniffing":{"enabled":false}}`
	inbound, err := candidateBootstrapInbound([]byte(base))
	if err != nil || !inbound.Enable || inbound.Tag != "lab-vless" {
		t.Fatal("valid empty loopback lab inbound was refused")
	}
	for _, change := range []func(map[string]any){
		func(v map[string]any) { v["listen"] = "0.0.0.0" },
		func(v map[string]any) { v["port"] = 443 },
		func(v map[string]any) { v["tag"] = "api" },
		func(v map[string]any) { v["allocate"] = map[string]any{"strategy": "random"} },
		func(v map[string]any) {
			v["settings"].(map[string]any)["clients"] = []any{map[string]any{"id": "credential"}}
		},
		func(v map[string]any) { v["settings"].(map[string]any)["fallbacks"] = []any{} },
		func(v map[string]any) { v["streamSettings"].(map[string]any)["security"] = "reality" },
		func(v map[string]any) { v["sniffing"].(map[string]any)["enabled"] = true },
	} {
		var value map[string]any
		_ = json.Unmarshal([]byte(base), &value)
		change(value)
		raw, _ := json.Marshal(value)
		if _, err := candidateBootstrapInbound(raw); !errors.Is(err, runtimeprovider.ErrOperationNotExecuted) {
			t.Fatal("unsupported bootstrap field was silently dropped or did not produce pre-write refusal")
		}
	}
}

func TestRuntimeCandidateRefusalsDoNotCallUnconfiguredProvider(t *testing.T) {
	rt := &Runtime{}
	for _, call := range []func() error{
		func() error { return rt.Apply(0, []byte(`{}`)) },
		func() error { return rt.Apply(1, []byte(`{"inbounds":[],"inbounds":[]}`)) },
		func() error { return rt.Restart() },
		func() error { return rt.AddUser(context.Background(), "in", "user@credential", []byte(`{}`)) },
		func() error { return rt.ProvisionInbound(context.Background(), []byte(`{}`)) },
	} {
		if err := call(); !errors.Is(err, runtimeprovider.ErrOperationNotExecuted) {
			t.Fatal("candidate refusal attempted provider I/O or was not distinguished from an unknown write")
		}
	}
}

func TestRuntimeAccountCompatibilityDoesNotStripUnknownClientMembers(t *testing.T) {
	for _, field := range []string{"level", "reverse", "extra", "alterId"} {
		account := map[string]any{"id": "11111111-1111-4111-8111-111111111111", "email": "user@credential", field: 0}
		if !errors.Is(runtimeAccountCompatible("vless", account), runtimeprovider.ErrOperationNotExecuted) {
			t.Fatal("unrepresentable account field was not refused")
		}
	}
	if err := runtimeAccountCompatible("vless", map[string]any{"id": "11111111-1111-4111-8111-111111111111", "email": "user@credential"}); err != nil {
		t.Fatal("supported account fields were refused")
	}
}

func TestRuntimeClientConfigPreservesUnknownFieldsAndOtherAccounts(t *testing.T) {
	before := []byte(`{"env":{"key":"value"},"inbounds":[{"tag":"target","protocol":"vless","allocate":{"strategy":"always"},"settings":{"decryption":"none","unknown":{"value":9007199254740993},"clients":[{"id":"old","email":"user@credential","reverse":{"tag":"r"}},{"id":"control","email":"control@credential","level":7}]}}],"outbounds":[]}`)
	want, _, existed, err := runtimeClientConfig(before, "target", "user@credential", map[string]any{"id": "new", "email": "user@credential"})
	if err != nil || !existed {
		t.Fatal("cannot construct exact account replacement")
	}
	expected := []byte(`{"env":{"key":"value"},"inbounds":[{"tag":"target","protocol":"vless","allocate":{"strategy":"always"},"settings":{"decryption":"none","unknown":{"value":9007199254740993},"clients":[{"id":"new","email":"user@credential"},{"id":"control","email":"control@credential","level":7}]}}],"outbounds":[]}`)
	comparison, err := CompareConfig(expected, want)
	if err != nil || !comparison.Equivalent {
		t.Fatal("account edit changed an unrelated field, account, order or large integer")
	}
}

func TestRuntimeCandidateSnapshotCannotAdvertiseActiveAndIsIndependent(t *testing.T) {
	rt := &Runtime{request: OwnershipObservationRequest{ExpectedPanelVersion: "3.9.0"}}
	rt.cacheStatus(Status{Xray: XrayStatus{State: "running", Version: "26.9.9"}}, nil)
	view := rt.Snapshot()
	if view.GetMode() != chiralv1.RuntimeMode_RUNTIME_MODE_CANDIDATE || view.GetHealth() != chiralv1.RuntimeHealth_RUNTIME_HEALTH_INCOMPATIBLE || rt.InstalledVersion() != "" {
		t.Fatal("isolated provider advertised production readiness or unverified on-disk version")
	}
	view.Mode = chiralv1.RuntimeMode_RUNTIME_MODE_ACTIVE
	if rt.Snapshot().GetMode() != chiralv1.RuntimeMode_RUNTIME_MODE_CANDIDATE {
		t.Fatal("caller mutated the cached provider status")
	}
}
