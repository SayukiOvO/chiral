package threexui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestLive3XUIUpdateRecoveryContract uses a disposable empty official instance.
// Release listing and install results are injected; no kernel is downloaded.
// Stop, recovery restart, status and proxy traffic use the unmodified backend.
func TestLive3XUIUpdateRecoveryContract(t *testing.T) {
	optIn := os.Getenv("CHIRAL_3XUI_TEST_WRITE")
	if optIn == "" {
		t.Skip("set CHIRAL_3XUI_TEST_WRITE=isolated-empty-instance to enable the isolated recovery contract")
	}
	if optIn != "isolated-empty-instance" {
		t.Fatal("CHIRAL_3XUI_TEST_WRITE must equal isolated-empty-instance")
	}
	baseURL, tokenFile := os.Getenv("CHIRAL_3XUI_TEST_URL"), os.Getenv("CHIRAL_3XUI_TEST_TOKEN_FILE")
	if baseURL == "" || tokenFile == "" {
		t.Fatal("recovery opt-in also requires the isolated URL and token file")
	}
	bin := os.Getenv("CHIRAL_3XUI_TEST_XRAY_BIN")
	info, err := os.Stat(bin)
	if !filepath.IsAbs(bin) || err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatal("recovery requires an explicit executable fixture client binary")
	}
	token := readLive3XUIToken(t, tokenFile)
	client, err := New(Config{BaseURL: baseURL, Token: token, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal("cannot construct the isolated recovery client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	inbounds, err := client.ListInbounds(ctx)
	if err != nil || len(inbounds) != 0 {
		t.Fatal("recovery preflight requires a successfully read empty inbound list")
	}
	clients, err := client.ListClients(ctx)
	if err != nil || len(clients) != 0 {
		t.Fatal("recovery preflight requires a successfully read empty client list")
	}
	before, err := client.ReadAssembledConfig(ctx)
	if err != nil {
		t.Fatal("cannot read the initial assembled configuration")
	}
	liveWriteRequireAPIOnly(t, before)
	before = normaliseLive3XUIConfig(t, before)
	snapshot, err := client.ReadXrayTemplate(ctx)
	if err != nil || snapshot.OutboundTestURL == "" {
		t.Fatal("cannot obtain a restorable initial template")
	}
	liveWriteRequireAPIOnly(t, snapshot.Config)
	prefix := "chiral-recovery-" + liveWriteRandomID(t)
	tag, email := prefix+"-in", prefix+"@invalid"
	defer liveUpdateCleanup(t, client, snapshot, before, tag, email)

	config := liveWriteObject(t, snapshot.Config)
	var outbounds []json.RawMessage
	if json.Unmarshal(config["outbounds"], &outbounds) != nil {
		t.Fatal("initial template has no outbound array")
	}
	outbound := liveWriteJSON(t, map[string]any{"tag": prefix + "-loopback", "protocol": "freedom", "settings": map[string]any{
		"finalRules": []any{map[string]any{"action": "allow", "ip": []string{"127.0.0.1/32"}}, map[string]any{"action": "block"}},
	}})
	config["outbounds"] = liveWriteJSON(t, append(outbounds, outbound))
	routing := liveWriteObject(t, config["routing"])
	var rules []json.RawMessage
	if json.Unmarshal(routing["rules"], &rules) != nil {
		t.Fatal("initial template has no routing array")
	}
	fixtureRule := liveWriteJSON(t, map[string]any{"type": "field", "inboundTag": []string{tag},
		"ip": []string{"127.0.0.1/32"}, "outboundTag": prefix + "-loopback"})
	rules, err = liveWriteInsertFixtureRule(rules, fixtureRule)
	if err != nil {
		t.Fatal(err)
	}
	routing["rules"] = liveWriteJSON(t, rules)
	config["routing"] = liveWriteJSON(t, routing)
	if err := client.UpdateXrayTemplate(ctx, XrayTemplate{Config: liveWriteJSON(t, config), OutboundTestURL: snapshot.OutboundTestURL}); err != nil {
		t.Fatal("cannot install the owned loopback fixture routing")
	}
	inbound, err := client.AddInbound(ctx, Inbound{Tag: tag, Remark: "Isolated recovery contract", Enable: true,
		Listen: "127.0.0.1", Port: 39083, Protocol: "vless", Settings: json.RawMessage(`{"clients":[],"decryption":"none"}`),
		StreamSettings: json.RawMessage(`{"network":"tcp","security":"none","tcpSettings":{"header":{"type":"none"}}}`),
		Sniffing:       json.RawMessage(`{"enabled":false,"destOverride":["http","tls"],"metadataOnly":false,"routeOnly":false}`),
		TrafficReset:   "never", TrafficResetDay: 1})
	if err != nil {
		t.Fatal("cannot create the owned loopback inbound")
	}
	account := map[string]any{"id": liveWriteUUID(t), "email": email, "enable": true, "security": "auto", "limitIp": 0,
		"totalGB": 0, "expiryTime": 0, "tgId": 0, "subId": liveWriteRandomID(t), "comment": "Isolated recovery contract",
		"reset": 0, "resetDay": 0, "resetMax": 0, "trafficReset": "never", "trafficResetDay": 1}
	if err := client.AddClient(ctx, liveWriteJSON(t, account), []int64{inbound.ID}); err != nil {
		t.Fatal("cannot create the owned loopback credential")
	}
	if err := client.RestartXray(ctx); err != nil {
		t.Fatal("cannot activate the isolated fixture before fault injection")
	}
	probe, closeProbe := liveUpdateProbe(t, ctx, bin, account, inbound)
	defer closeProbe()
	baseline, err := liveUpdateWaitState(ctx, client, "running")
	if err != nil {
		t.Log(liveUpdateReadinessDiagnostic(baseline))
		t.Fatal("cannot establish the official running provider identity")
	}
	if err := probe(ctx); err != nil {
		t.Fatal("initial real data-plane probe failed")
	}
	t.Log("fault_injection=true release_catalog_injected=true release_download_verified=false upstream_unmodified=true")

	for _, mode := range []string{"reported_failure", "lost_recovery_response", "lost_install_response"} {
		if !t.Run(mode, func(t *testing.T) {
			fault := &liveUpdateFaultTransport{next: directTransport(), real: client, mode: mode, probe: probe}
			injected, err := New(Config{BaseURL: baseURL, Token: token, Timeout: 25 * time.Second,
				HTTPClient: &http.Client{Transport: fault}})
			if err != nil {
				t.Fatal("cannot construct the fault-injection transport")
			}
			// Separate fault scenarios have independent private fixture journals;
			// neither is ever allowed to dispatch a real installation request.
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal("cannot protect the fixture journal directory")
			}
			path := filepath.Join(dir, "update.json")
			coordinator, err := OpenXrayUpdates(path, injected)
			if err != nil {
				t.Fatal("cannot create the fixture update coordinator")
			}
			defer coordinator.Close()
			request := XrayUpdateRequest{ID: liveWriteRandomID(t), Version: "v99.0.1", ExpectedPanelGUID: baseline.PanelGUID,
				ExpectedPanelVersion: baseline.PanelVersion, ExpectedRunningVersion: baseline.Xray.Version}
			result, runErr := coordinator.Run(ctx, request, probe)
			liveUpdateRequireResult(t, result, runErr, mode, baseline.Xray.Version)
			wantRestarts := int32(0)
			if mode != "lost_install_response" {
				wantRestarts = 1
			}
			checkCounts := func() {
				t.Helper()
				if fault.installs.Load() != 1 || fault.restarts.Load() != wantRestarts {
					t.Fatal("installation replay or unexpected recovery restart was observed")
				}
				if fault.restartResponseLost.Load() != (mode == "lost_recovery_response") {
					t.Fatal("recovery response-loss fixture did not confirm the official restart before discarding its response")
				}
			}
			checkCounts()
			result, runErr = coordinator.Run(ctx, request, probe)
			liveUpdateRequireResult(t, result, runErr, mode, baseline.Xray.Version)
			checkCounts()
			if err := coordinator.Close(); err != nil {
				t.Fatal("cannot close the durable fixture journal")
			}
			reopened, err := OpenXrayUpdates(path, injected)
			if err != nil {
				t.Fatal("cannot reopen the durable fixture journal")
			}
			defer reopened.Close()
			result, runErr = reopened.Reconcile(ctx, request, probe)
			liveUpdateRequireResult(t, result, runErr, mode, baseline.Xray.Version)
			checkCounts()
			if err := probe(ctx); err != nil {
				t.Fatal("final new-connection data-plane probe failed")
			}
			t.Logf("fault_injection=true install_dispatches=%d recovery_restarts=%d durable_replay_safe=true service_probe_verified=true", fault.installs.Load(), fault.restarts.Load())
			if mode == "lost_install_response" {
				t.Log("fault_injection=true install_response_lost=true real_install_dispatched=false background_install_simulated=false")
			}
		}) {
			return
		}
	}
}

func liveUpdateRequireResult(t *testing.T, result XrayUpdateResult, err error, mode, version string) {
	t.Helper()
	var failure *XrayUpdateFailure
	want := XrayUpdateUnknown
	if mode != "lost_install_response" {
		want = XrayUpdateFailed
	}
	if !errors.As(err, &failure) || result.State != want || result.ServiceState != XrayServiceReachable || result.ServiceVersion != version || result.ServiceObservedAt <= 0 {
		t.Fatalf("recovery lost the installation outcome or real service evidence: state=%q service=%q", result.State, result.ServiceState)
	}
	wantOutcome := ""
	if mode == "reported_failure" {
		wantOutcome = "succeeded"
	} else if mode == "lost_recovery_response" {
		wantOutcome = "unknown"
	}
	if result.RecoveryAttempted != (mode != "lost_install_response") || result.RecoveryOutcome != wantOutcome {
		t.Fatal("recovery outcome does not match the injected failure")
	}
}

type liveUpdateFaultTransport struct {
	next                http.RoundTripper
	real                *Client
	mode                string
	probe               XrayUpdateProbe
	installs, restarts  atomic.Int32
	restartResponseLost atomic.Bool
}

func (f *liveUpdateFaultTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != f.real.base.Scheme || r.URL.Host != f.real.base.Host {
		return nil, errors.New("fault fixture rejected an unexpected HTTP origin")
	}
	catalog, _ := f.real.endpointURL(xrayVersionsEndpoint)
	install, _ := f.real.endpointURL(xrayInstallEndpoint + "v99.0.1")
	restart, _ := f.real.endpointURL("panel/api/server/restartXrayService")
	if r.URL.Path == catalog.Path {
		if r.Method != http.MethodGet {
			return nil, errors.New("fault fixture rejected a catalog mutation")
		}
		return liveUpdateEnvelope(r, `{"success":true,"obj":["v99.0.1"]}`), nil
	}
	// Fail closed on every install URL, including unexpected tags. A future
	// coordinator change must never turn this fixture into a real download.
	if strings.HasPrefix(r.URL.Path, strings.TrimSuffix(install.Path, "v99.0.1")) {
		if r.Method != http.MethodPost || r.URL.Path != install.Path || r.URL.RawQuery != "" {
			return nil, errors.New("fault fixture rejected an unexpected installation")
		}
		f.installs.Add(1)
		if f.mode == "lost_install_response" {
			return nil, io.ErrUnexpectedEOF
		}
		if f.mode != "reported_failure" && f.mode != "lost_recovery_response" {
			return nil, errors.New("fault fixture rejected an unknown injection mode")
		}
		stop, _ := f.real.endpointURL("panel/api/server/stopXrayService")
		if err := f.real.sensitiveEnvelope(r.Context(), http.MethodPost, stop, nil, "", nil); err != nil {
			return nil, errors.New("fault injection could not stop the isolated official process")
		}
		if _, err := liveUpdateWaitState(r.Context(), f.real, "stop"); err != nil {
			return nil, errors.New("fault injection did not observe the official stopped state")
		}
		stoppedCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := f.probe(stoppedCtx); err == nil {
			return nil, errors.New("fault injection still carried proxy traffic after stopping")
		}
		return liveUpdateEnvelope(r, `{"success":false,"msg":"injected installation failure; no kernel download attempted"}`), nil
	}
	if r.URL.Path == restart.Path && r.Method == http.MethodPost {
		f.restarts.Add(1)
		if f.mode == "lost_recovery_response" {
			response, err := f.next.RoundTrip(r)
			if err != nil {
				return nil, err
			}
			body, readErr := io.ReadAll(io.LimitReader(response.Body, defaultMaxResponseBytes+1))
			closeErr := response.Body.Close()
			var reply envelope
			if response.StatusCode != http.StatusOK || readErr != nil || closeErr != nil || len(body) > defaultMaxResponseBytes || json.Unmarshal(body, &reply) != nil || reply.Success == nil || !*reply.Success {
				return nil, errors.New("fault fixture could not confirm the official recovery response")
			}
			f.restartResponseLost.Store(true)
			return nil, io.ErrUnexpectedEOF
		}
	}
	return f.next.RoundTrip(r)
}

func liveUpdateEnvelope(r *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Request: r}
}

func liveUpdateWaitState(ctx context.Context, client *Client, want string) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var last Status
	for {
		status, err := client.Status(ctx)
		if err != nil {
			if want != "running" {
				return Status{}, err
			}
			last = Status{}
		} else if want == "running" {
			last = status
			if status.Xray.State == "running" && validXrayVersion(status.Xray.Version) && validXrayVersion(status.PanelVersion) && validUpdateID(status.PanelGUID) {
				return status, nil
			}
		} else if status.Xray.State == want || (want == "stop" && status.Xray.State == "error") {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func liveUpdateReadinessDiagnostic(status Status) string {
	return fmt.Sprintf("readiness_running=%t xray_version_valid=%t panel_version_valid=%t panel_guid_valid=%t",
		status.Xray.State == "running", validXrayVersion(status.Xray.Version), validXrayVersion(status.PanelVersion), validUpdateID(status.PanelGUID))
}

func TestLiveUpdateReadinessWaitsForOneCompleteRunningSnapshot(t *testing.T) {
	responses := []string{
		`{"panelVersion":"v3.8.5","panelGuid":"fixture-guid","xray":{"state":"running","version":"Unknown"}}`,
		`{"panelVersion":"v3.8.5","panelGuid":"","xray":{"state":"running","version":"26.9.29"}}`,
		`{"panelVersion":"initializing","panelGuid":"fixture-guid","xray":{"state":"running","version":"26.9.29"}}`,
		`{"panelVersion":"","panelGuid":"fixture-guid","xray":{"state":"running","version":"26.9.29"}}`,
		`{"panelVersion":"v3.8.5","panelGuid":"fixture-guid","xray":{"state":"stop","version":"26.9.29"}}`,
		`{"panelVersion":"v3.8.5","panelGuid":"fixture-guid","xray":{"state":"running","version":"26.9.29"}}`,
	}
	calls := 0
	client, err := New(Config{BaseURL: "http://127.0.0.1:2053/fixture/", Token: testToken,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if calls >= len(responses) || r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/server/status") {
				t.Fatal("unexpected readiness request")
			}
			body := responses[calls]
			calls++
			return liveUpdateEnvelope(r, `{"success":true,"obj":`+body+`}`), nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, err := liveUpdateWaitState(ctx, client, "running")
	if err != nil || calls != len(responses) || status.PanelGUID != "fixture-guid" || status.Xray.Version != "26.9.29" {
		t.Fatal("readiness accepted incomplete cached fields or combined different status snapshots")
	}
}

func TestLiveUpdateReadinessTimeoutReportsOnlyValidityFlags(t *testing.T) {
	client, err := New(Config{BaseURL: "http://127.0.0.1:2053/fixture/", Token: testToken,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			deadline, ok := r.Context().Deadline()
			if !ok || time.Until(deadline) > 15*time.Second {
				t.Fatal("readiness request did not retain its bounded deadline")
			}
			return liveUpdateEnvelope(r, `{"success":true,"obj":{"panelVersion":"SECRET_PANEL","panelGuid":"SECRET_GUID","xray":{"state":"running","version":"SECRET_VERSION"}}}`), nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	status, err := liveUpdateWaitState(ctx, client, "running")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("incomplete readiness did not stop at its deadline")
	}
	diagnostic := liveUpdateReadinessDiagnostic(status)
	if diagnostic != "readiness_running=true xray_version_valid=false panel_version_valid=false panel_guid_valid=true" || strings.Contains(diagnostic, "SECRET") {
		t.Fatal("readiness diagnostic exposed raw identity fields or omitted validity flags")
	}
}

func TestLiveUpdateFaultTransportNeverForwardsInstallOrCatalog(t *testing.T) {
	real, err := New(Config{BaseURL: "http://127.0.0.1:2053/fixture/", Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	fault := &liveUpdateFaultTransport{real: real, mode: "lost_install_response", next: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("fixture forwarded an installation or release lookup")
		return nil, nil
	})}
	client, err := New(Config{BaseURL: real.base.String(), Token: testToken, HTTPClient: &http.Client{Transport: fault}})
	if err != nil {
		t.Fatal(err)
	}
	versions, err := client.XrayVersions(context.Background())
	if err != nil || len(versions) != 1 || versions[0] != "v99.0.1" {
		t.Fatal("fixture did not provide its explicit synthetic release")
	}
	var installErr *XrayInstallError
	if err := client.InstallXray(context.Background(), "v99.0.1"); !errors.As(err, &installErr) || installErr.Outcome != XrayInstallUnknown {
		t.Fatal("fixture response loss was not classified as unknown")
	}
	if fault.installs.Load() != 1 || fault.restarts.Load() != 0 {
		t.Fatal("unexpected fault dispatch counters")
	}
	for _, endpoint := range []string{xrayInstallEndpoint + "v99.0.2", xrayInstallEndpoint + "latest"} {
		u, _ := real.endpointURL(endpoint)
		r, _ := http.NewRequest(http.MethodPost, u.String(), nil)
		if response, err := fault.RoundTrip(r); err == nil || response != nil {
			t.Fatal("fixture accepted an unexpected installation tag")
		}
	}
	r, _ := http.NewRequest(http.MethodPost, "http://127.0.0.2:2053/fixture/panel/api/server/installXray/v99.0.1", nil)
	if response, err := fault.RoundTrip(r); err == nil || response != nil {
		t.Fatal("fixture accepted another origin")
	}
}

func TestLiveUpdateFaultTransportStopsBeforeReportedFailureAndForwardsRecovery(t *testing.T) {
	for _, mode := range []string{"reported_failure", "lost_recovery_response"} {
		t.Run(mode, func(t *testing.T) {
			var calls []string
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "Bearer "+testToken {
					t.Fatal("fixture did not preserve official API authentication")
				}
				calls = append(calls, r.Method+" "+r.URL.Path)
				switch r.URL.Path {
				case "/fixture/panel/api/server/stopXrayService", "/fixture/panel/api/server/restartXrayService":
					if r.Method != http.MethodPost {
						t.Fatal("fixture changed an official lifecycle method")
					}
					return liveUpdateEnvelope(r, `{"success":true}`), nil
				case "/fixture/panel/api/server/status":
					return liveUpdateEnvelope(r, `{"success":true,"obj":{"panelVersion":"v3.8.5","xray":{"state":"stop","version":"26.9.29"}}}`), nil
				default:
					t.Fatal("fixture forwarded an unexpected endpoint")
					return nil, nil
				}
			})
			real, err := New(Config{BaseURL: "http://127.0.0.1:2053/fixture/", Token: testToken, HTTPClient: &http.Client{Transport: transport}})
			if err != nil {
				t.Fatal(err)
			}
			probeCalls := 0
			fault := &liveUpdateFaultTransport{real: real, next: transport, mode: mode, probe: func(ctx context.Context) error {
				probeCalls++
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 2*time.Second {
					t.Fatal("stopped data-plane check was not bounded")
				}
				return context.DeadlineExceeded
			}}
			client, err := New(Config{BaseURL: real.base.String(), Token: testToken, HTTPClient: &http.Client{Transport: fault}})
			if err != nil {
				t.Fatal(err)
			}
			var installErr *XrayInstallError
			if err := client.InstallXray(context.Background(), "v99.0.1"); !errors.As(err, &installErr) || installErr.Outcome != XrayInstallFailed {
				t.Fatal("fixture did not report a definitive injected failure")
			}
			if probeCalls != 1 || len(calls) != 2 || calls[0] != "POST /fixture/panel/api/server/stopXrayService" || calls[1] != "GET /fixture/panel/api/server/status" {
				t.Fatal("fixture did not verify official stop and failed data path before reporting failure")
			}
			err = client.RestartXray(context.Background())
			if (err == nil) != (mode == "reported_failure") || fault.restartResponseLost.Load() != (mode == "lost_recovery_response") {
				t.Fatal("fixture did not preserve or discard the confirmed official recovery response as requested")
			}
			if len(calls) != 3 || calls[2] != "POST /fixture/panel/api/server/restartXrayService" || fault.restarts.Load() != 1 {
				t.Fatal("fixture recovery did not forward exactly one official restart")
			}
		})
	}
}

func liveUpdateCleanup(t *testing.T, client *Client, snapshot XrayTemplate, before json.RawMessage, tag, email string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ok := true
	report := func(message string) { ok = false; t.Error(message) }
	clients, err := client.ListClients(ctx)
	if err != nil {
		report("recovery cleanup could not list clients")
	} else {
		for _, row := range clients {
			if row.Email != email {
				report("recovery cleanup retained an unexpected client")
			} else if client.DeleteClient(ctx, email, false) != nil {
				report("recovery cleanup could not remove its owned client")
			}
		}
	}
	inbounds, err := client.ListInbounds(ctx)
	if err != nil {
		report("recovery cleanup could not list inbounds")
	} else {
		for _, row := range inbounds {
			if row.Tag != tag {
				report("recovery cleanup retained an unexpected inbound")
			} else if client.DeleteInbound(ctx, row.ID) != nil {
				report("recovery cleanup could not remove its owned inbound")
			}
		}
	}
	if client.UpdateXrayTemplate(ctx, snapshot) != nil {
		report("recovery cleanup could not restore the initial template")
	}
	if client.RestartXray(ctx) != nil {
		report("recovery cleanup could not activate the restored isolated configuration")
	}
	if _, err := liveUpdateWaitState(ctx, client, "running"); err != nil {
		report("recovery cleanup could not verify the restored process")
	}
	remainingClients, err := client.ListClients(ctx)
	if err != nil || len(remainingClients) != 0 {
		report("recovery cleanup did not verify an empty client list")
	}
	remainingInbounds, err := client.ListInbounds(ctx)
	if err != nil || len(remainingInbounds) != 0 {
		report("recovery cleanup did not verify an empty inbound list")
	}
	restored, err := client.ReadXrayTemplate(ctx)
	if err != nil || restored.OutboundTestURL != snapshot.OutboundTestURL || !liveWriteEqual(restored.Config, snapshot.Config) {
		report("recovery cleanup template differs from its original snapshot")
	}
	after, err := client.ReadAssembledConfig(ctx)
	if err != nil || !liveWriteEqual(before, after) {
		report("recovery cleanup assembled configuration differs from its initial snapshot")
	}
	if ok {
		t.Log("cleanup_verified=true empty_objects=true template_restored=true assembled_config_unchanged=true")
	}
}

// The fixture client owns only a temporary client config. Its probe returns
// errors normally so coordinator failure paths cannot exit a test goroutine.
func liveUpdateProbe(t *testing.T, ctx context.Context, bin string, account map[string]any, inbound Inbound) (XrayUpdateProbe, func()) {
	t.Helper()
	upload := bytes.Repeat([]byte("chiral-upload-1|"), 2048)
	download := bytes.Repeat([]byte("chiral-download|"), 32768)
	var delivered atomic.Int64
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/payload" {
			http.Error(w, "unexpected fixture request", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, int64(len(upload))+1))
		if err != nil || !bytes.Equal(body, upload) {
			http.Error(w, "fixture upload mismatch", http.StatusBadRequest)
			return
		}
		delivered.Add(1)
		w.Header().Set("Content-Length", strconv.Itoa(len(download)))
		_, _ = w.Write(download)
	}))
	_ = origin.Listener.Close()
	var err error
	origin.Listener, err = net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("cannot create the isolated recovery origin")
	}
	origin.Start()
	t.Cleanup(origin.Close)
	socks, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("cannot reserve the recovery SOCKS port")
	}
	port := socks.Addr().(*net.TCPAddr).Port
	if socks.Close() != nil {
		t.Fatal("cannot release the recovery SOCKS port")
	}
	config := liveWriteJSON(t, map[string]any{
		"log": map[string]any{"loglevel": "none"},
		"inbounds": []any{map[string]any{"tag": "fixture-socks", "listen": "127.0.0.1", "port": port, "protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false}}},
		"outbounds": []any{map[string]any{"tag": "fixture-vless", "protocol": "vless", "settings": map[string]any{
			"vnext": []any{map[string]any{"address": "127.0.0.1", "port": inbound.Port,
				"users": []any{map[string]any{"id": account["id"], "encryption": "none"}}}}},
			"streamSettings": map[string]any{"network": "tcp", "security": "none"}}},
	})
	path := filepath.Join(t.TempDir(), "recovery-client.json")
	if os.WriteFile(path, config, 0o600) != nil {
		t.Fatal("cannot write the private recovery client fixture")
	}
	cmd := exec.CommandContext(ctx, bin, "run", "-c", path, "-format", "json")
	cmd.Stdout, cmd.Stderr, cmd.WaitDelay = io.Discard, io.Discard, 3*time.Second
	if cmd.Start() != nil {
		t.Fatal("cannot start the recovery fixture client")
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	closed := false
	stop := func() {
		if closed {
			return
		}
		closed = true
		select {
		case <-done:
			t.Error("recovery fixture client exited before deliberate termination")
			return
		default:
		}
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Error("cannot terminate the recovery fixture client")
		}
		select {
		case <-done:
			var exitErr *exec.ExitError
			if cmd.ProcessState == nil || (waitErr != nil && !errors.As(waitErr, &exitErr)) {
				t.Error("recovery fixture client exit was not confirmed")
				return
			}
			t.Log("fixture_process_reaped=true")
		case <-time.After(5 * time.Second):
			t.Error("recovery fixture client was not reaped within five seconds")
		}
	}
	proxy := &url.URL{Scheme: "socks5", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	probe := func(parent context.Context) error {
		probeCtx, cancel := context.WithTimeout(parent, 12*time.Second)
		defer cancel()
		for {
			select {
			case <-done:
				return errors.New("recovery fixture client exited before its proxy request")
			default:
			}
			// A fresh transport and disabled keep-alives force a new SOCKS/VLESS
			// connection for every attempt, including after the recovery restart.
			transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true, DisableCompression: true}
			httpClient := &http.Client{Transport: transport, Timeout: 2 * time.Second,
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			initial := delivered.Load()
			req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, origin.URL+"/payload", bytes.NewReader(upload))
			if err != nil {
				return errors.New("cannot build the recovery payload request")
			}
			response, requestErr := httpClient.Do(req)
			if response != nil {
				body, readErr := io.ReadAll(io.LimitReader(response.Body, int64(len(download))+1))
				closeErr := response.Body.Close()
				transport.CloseIdleConnections()
				if requestErr == nil && response.StatusCode == http.StatusOK && readErr == nil && closeErr == nil && bytes.Equal(body, download) && delivered.Load() == initial+1 {
					t.Logf("service_probe_verified=true fresh_connection=true upload_bytes=%d download_bytes=%d response_sha256=%x", len(upload), len(download), sha256.Sum256(download))
					return nil
				}
			} else {
				transport.CloseIdleConnections()
			}
			select {
			case <-probeCtx.Done():
				return fmt.Errorf("recovery data-plane probe did not verify bytes: %w", probeCtx.Err())
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return probe, stop
}
