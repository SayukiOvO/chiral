package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These run the real kernel. The claim under test — "a client can get online
// through this node" — is exactly the kind that a mock would assert into
// existence: a fake proxy always proxies. The chain here is genuine end to end,
// probe client -> SOCKS -> the node's own VLESS inbound -> its outbound -> an
// HTTP server, with only the last hop replaced by a local one so the test does
// not depend on the internet.

const probeTestUUID = "8673288c-253e-4a63-bfc0-2828a6203a97"

// nodeUnderTest starts a kernel configured the way a real node is: one VLESS
// inbound carrying a subscriber, and a freedom outbound. Returns its manager
// and the inbound port.
func nodeUnderTest(t *testing.T) (*Manager, int) {
	t.Helper()
	bin := testBin(t)
	dir := t.TempDir()
	port := freePort(t)

	cfg := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag": "vless-in", "listen": "127.0.0.1", "port": port, "protocol": "vless",
			"settings": map[string]any{
				"clients":    []any{map[string]any{"id": probeTestUUID, "email": "probe@node"}},
				"decryption": "none",
			},
		}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(bin, dir, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1})), nil)
	if err := m.Start(); err != nil {
		t.Fatalf("starting the node kernel: %v", err)
	}
	t.Cleanup(m.Stop)
	return m, port
}

// vlessOutbound is what Core's xray-json template renders: the client side of
// the inbound above.
func vlessOutbound(port int, uuid string) []byte {
	return []byte(fmt.Sprintf(`{
	  "protocol": "vless",
	  "settings": {"vnext": [{"address": "127.0.0.1", "port": %d,
	    "users": [{"id": "%s", "encryption": "none"}]}]},
	  "streamSettings": {"network": "tcp"}
	}`, port, uuid))
}

// The positive case, and the one the whole design turns on: ACTIVE has to mean
// this.
func TestDataPathProvesTrafficReachesTheInternet(t *testing.T) {
	m, port := nodeUnderTest(t)

	// The far end. Local so the test is hermetic; from the kernel's point of
	// view it is an ordinary HTTP origin reached through its freedom outbound.
	var served int
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()

	m.SetProbe(vlessOutbound(port, probeTestUUID), origin.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := m.DataPath(ctx, m.Binary()); err != nil {
		t.Fatalf("a working node failed its own data-path probe: %v", err)
	}
	if served == 0 {
		t.Fatal("the probe reported success but the origin was never reached")
	}
}

// The case the old API probe could not see. The kernel is up, its config
// parsed, it is listening — and the credential does not match, so no subscriber
// gets a single byte through. This is the shape of an upgrade that breaks a
// protocol, and reporting it as ACTIVE is the failure the canary exists to
// prevent.
func TestDataPathFailsWhenTheNodeIsUpButCarriesNothing(t *testing.T) {
	m, port := nodeUnderTest(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()

	// A live kernel, a real inbound, and a client the inbound will reject.
	m.SetProbe(vlessOutbound(port, "11111111-2222-3333-4444-555555555555"), origin.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := m.DataPath(ctx, m.Binary())
	if err == nil {
		t.Fatal("the probe passed a node that carries no traffic")
	}
	if errors.Is(err, ErrNoProbe) {
		t.Fatalf("a rejected credential was reported as 'nothing to test with': %v", err)
	}
	t.Logf("correctly refused: %v", err)
}

// Nothing listening at all — the inbound is gone, which is what a kernel that
// failed to bind looks like from a client's side.
func TestDataPathFailsWhenNothingAcceptsOnTheInbound(t *testing.T) {
	m, _ := nodeUnderTest(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer origin.Close()

	dead := freePort(t) // nobody is listening here
	m.SetProbe(vlessOutbound(dead, probeTestUUID), origin.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := m.DataPath(ctx, m.Binary()); err == nil {
		t.Fatal("the probe passed while dialling a port nobody listens on")
	}
}

// "Nothing to test with" must stay distinguishable from "tested and broken".
// Collapsing them would roll a node back for the sin of having no users.
func TestNoProbeIsItsOwnOutcomeAndNotAFailure(t *testing.T) {
	m, _ := nodeUnderTest(t)
	m.SetProbe(nil, "")

	err := m.DataPath(context.Background(), m.Binary())
	if !errors.Is(err, ErrNoProbe) {
		t.Fatalf("DataPath() = %v, want ErrNoProbe", err)
	}
}

// A template that renders something other than one outbound object must be
// reported as a template problem, not as a dead data path — the fixes are
// completely different.
func TestAMalformedOutboundIsReportedAsSuch(t *testing.T) {
	m, _ := nodeUnderTest(t)
	m.SetProbe([]byte(`{"protocol": "vless",`), "http://127.0.0.1:1/")

	err := m.DataPath(context.Background(), m.Binary())
	if err == nil || errors.Is(err, ErrNoProbe) {
		t.Fatalf("DataPath() = %v, want a parse complaint", err)
	}
	t.Logf("reported: %v", err)
}

// The probe kernel is a second process holding a port; leaking one would poison
// every later probe on the node. Run several and watch them all succeed.
func TestRepeatedProbesDoNotLeakTheirKernel(t *testing.T) {
	m, port := nodeUnderTest(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	m.SetProbe(vlessOutbound(port, probeTestUUID), origin.URL)

	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		err := m.DataPath(ctx, m.Binary())
		cancel()
		if err != nil {
			t.Fatalf("probe %d failed: %v", i+1, err)
		}
	}
}

// Routing by tag, not by position. A template that renders its own fallback
// alongside the real outbound would otherwise let the probe leave through the
// node's plain uplink and report a success for a proxy that never carried it.
func TestTheProbeConfigRoutesByTagRatherThanByOrder(t *testing.T) {
	cfg, err := probeConfig([]byte(`{"protocol":"vless","tag":"named-by-template"}`), 1080)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Routing struct {
			Rules []struct {
				InboundTag  []string `json:"inboundTag"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Routing.Rules) != 1 {
		t.Fatalf("got %d routing rules, want 1", len(parsed.Routing.Rules))
	}
	if got := parsed.Routing.Rules[0].OutboundTag; got != "named-by-template" {
		t.Errorf("routed to %q; the template's own tag was ignored", got)
	}
}

// The demonstration that this change had to happen.
//
// One kernel, one moment: its own API answers perfectly, and no subscriber can
// move a byte through it. The old canary asked the first question and reported
// ACTIVE, which is an invitation to roll the release across the fleet. Nothing
// here is hypothetical — both probes run against the same live process.
func TestTheAPIAnswersOnANodeThatCarriesNoTraffic(t *testing.T) {
	bin := testBin(t)
	dir := t.TempDir()
	apiPort, vlessPort := freePort(t), freePort(t)

	cfg := map[string]any{
		"log":   map[string]any{"loglevel": "warning"},
		"api":   map[string]any{"tag": "api", "services": []string{"StatsService"}},
		"stats": map[string]any{},
		"inbounds": []any{
			map[string]any{"tag": "chiral-api", "listen": "127.0.0.1", "port": apiPort,
				"protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1"}},
			map[string]any{"tag": "vless-in", "listen": "127.0.0.1", "port": vlessPort, "protocol": "vless",
				"settings": map[string]any{
					"clients":    []any{map[string]any{"id": probeTestUUID, "email": "probe@node"}},
					"decryption": "none",
				}},
		},
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}},
		"routing": map[string]any{"rules": []any{
			map[string]any{"type": "field", "inboundTag": []string{"chiral-api"}, "outboundTag": "api"},
		}},
	}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(bin, dir, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1})), nil)
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Wait for the control plane, the way the old check would have.
	var apiErr error
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, apiErr = m.runAPI(ctx, m.APIAddress(), "statsquery"); apiErr == nil {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if apiErr != nil {
		t.Skipf("the API never came up, so this comparison cannot be made: %v", apiErr)
	}

	// The old verdict: healthy. Now ask whether anybody can get online.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	m.SetProbe(vlessOutbound(vlessPort, "11111111-2222-3333-4444-555555555555"), origin.URL)

	if err := m.DataPath(ctx, m.Binary()); err == nil {
		t.Fatal("the data probe also passed; this test no longer demonstrates anything")
	} else {
		t.Logf("API said healthy; data path said: %v", err)
	}
}

// A dropped control plane is not evidence against a release. Saying it is
// produces a rollback with a reason an operator would then go and act on —
// hunting a transport bug that never existed.
func TestALostPanelConnectionIsNotBlamedOnTheRelease(t *testing.T) {
	m, port := nodeUnderTest(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	m.SetProbe(vlessOutbound(port, probeTestUUID), origin.URL)

	// Cancelled before anything is touched: the node must be left exactly as it
	// was, and the release must not be mentioned.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome, msg, err := m.Activate(ctx, m.Binary()+"-other")
	if err != nil {
		t.Fatal(err)
	}
	if outcome != OutcomeInconclusive {
		t.Fatalf("outcome = %v, want inconclusive; %s", outcome, msg)
	}
	if !strings.Contains(msg, "panel connection") {
		t.Errorf("message = %q; it should name the lost connection, not the kernel", msg)
	}
}
