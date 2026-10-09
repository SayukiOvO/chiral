package threexui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// TestLive3XUIHTTPProbeContract calls the real official testOutbound handler in
// a fresh, disposable network-none instance. Explicit opt-in is required:
//
//	CHIRAL_3XUI_TEST_WRITE=isolated-empty-instance
//	CHIRAL_3XUI_TEST_URL=<same-network-namespace loopback provider URL>
//	CHIRAL_3XUI_TEST_TOKEN_FILE=<private mode-0600 token file>
//
// No fixture client binary is needed. Only official 3x-ui owns and starts Xray;
// this test uses HTTP for every provider read/write and starts only an HTTP
// origin on loopback. The provider rejects private test URLs before probing.
// A public literal target avoids DNS, while an owned VLESS inbound routes it
// through an owned freedom redirect to that loopback origin. Thus the test
// exercises proxy authentication and real HTTP with no external connectivity.
// It does not establish Internet reachability or exact response-body integrity.
func TestLive3XUIHTTPProbeContract(t *testing.T) {
	optIn := os.Getenv("CHIRAL_3XUI_TEST_WRITE")
	if optIn == "" {
		t.Skip("set CHIRAL_3XUI_TEST_WRITE=isolated-empty-instance to enable the official HTTP probe contract")
	}
	if optIn != "isolated-empty-instance" {
		t.Fatal("CHIRAL_3XUI_TEST_WRITE must equal isolated-empty-instance")
	}
	liveHTTPProbeRequireLoopbackNamespace(t)
	baseURL, tokenFile := os.Getenv("CHIRAL_3XUI_TEST_URL"), os.Getenv("CHIRAL_3XUI_TEST_TOKEN_FILE")
	if baseURL == "" || tokenFile == "" {
		t.Fatal("HTTP probe opt-in also requires the isolated URL and token file")
	}
	client, err := New(Config{BaseURL: baseURL, Token: readLive3XUIToken(t, tokenFile), Timeout: 40 * time.Second})
	if err != nil {
		t.Fatal("cannot construct the isolated HTTP probe client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	// Emptiness and identity checks precede ReadXrayTemplate: that upstream
	// POST read may heal legacy storage. No reused instance is mutated.
	liveHTTPProbeRequireEmpty(t, ctx, client)
	before, err := client.ReadAssembledConfig(ctx)
	if err != nil {
		t.Fatal("cannot read the initial assembled configuration")
	}
	liveWriteRequireAPIOnly(t, before)
	identity, err := liveUpdateWaitState(ctx, client, "running")
	if err != nil {
		t.Fatal("HTTP probe preflight requires one complete running provider identity")
	}
	capabilities, err := client.DiscoverCapabilities(ctx)
	if err != nil || !capabilities.Supports(CapabilityOutboundProbe) || !capabilities.Supports(CapabilityXrayUpdate) {
		t.Fatal("official provider lacks the required probe or template operation")
	}
	snapshot, err := client.ReadXrayTemplate(ctx)
	if err != nil || snapshot.OutboundTestURL == "" {
		t.Fatal("cannot obtain a complete restorable template and outbound test URL")
	}
	liveWriteRequireAPIOnly(t, snapshot.Config)

	prefix := "chiral-http-probe-" + liveWriteRandomID(t)
	path := "/" + prefix
	const targetHost = "8.8.8.8"
	targetURL := "http://" + targetHost + path
	origin := liveHTTPProbeStartOrigin(t, targetHost, path)
	defer origin.close(t)
	port := liveProjectionFreePort(t)
	credential, wrongCredential := liveWriteUUID(t), liveWriteUUID(t)
	if credential == wrongCredential {
		t.Fatal("fixture generated identical positive and negative credentials")
	}
	candidate := liveHTTPProbeConfig(t, before, prefix, credential, port, origin.address)
	// Register exact-snapshot recovery before the first provider write. Cleanup
	// rejects any foreign identity, managed objects or unrecognised template.
	defer liveHTTPProbeRestore(t, client, identity, snapshot, before, candidate, targetURL)
	if err := client.UpdateXrayTemplate(ctx, XrayTemplate{Config: candidate, OutboundTestURL: targetURL}); err != nil {
		t.Fatal("cannot apply the owned isolated HTTP probe fixture")
	}
	liveProjectionRequireStoredAndAssembled(t, ctx, client, candidate, targetURL, "HTTP probe fixture")
	liveHTTPProbeRequireIdentity(t, ctx, client, identity)
	outbound := liveHTTPProbeOutbound(t, prefix+"-client", credential, port)
	wrongOutbound := liveHTTPProbeOutbound(t, prefix+"-client", wrongCredential, port)

	positive := func(t *testing.T, status int) {
		t.Helper()
		origin.status.Store(int64(status))
		hits := origin.hits.Load()
		result, err := client.ProbeOutbound(ctx, outbound, nil, targetURL)
		if err != nil || result.HTTPStatus != status {
			t.Fatal("official real outbound probe did not return the expected successful HTTP status")
		}
		if origin.hits.Load() != hits+1 || origin.invalid.Load() != 0 {
			t.Fatal("successful probe did not make exactly one expected proxy-path origin request")
		}
		liveHTTPProbeRequireIdentity(t, ctx, client, identity)
		t.Logf("official_http_probe=true mode_real=true origin_verified=true http_status=%d", status)
	}
	for _, status := range []int{http.StatusOK, http.StatusNoContent} {
		if !t.Run(fmt.Sprintf("real_http_%d", status), func(t *testing.T) { positive(t, status) }) {
			return
		}
	}
	if !t.Run("wrong_uuid_cannot_reach_origin", func(t *testing.T) {
		origin.status.Store(http.StatusOK)
		hits := origin.hits.Load()
		u, endpointErr := client.endpointURL(outboundProbeEndpoint)
		if endpointErr != nil {
			t.Fatal("cannot construct the fixed official negative probe endpoint")
		}
		form := url.Values{"mode": {"real"}, "outbound": {string(wrongOutbound)}}
		var wire struct {
			Success    *bool  `json:"success"`
			Tag        string `json:"tag"`
			Mode       string `json:"mode"`
			HTTPStatus *int   `json:"httpStatus"`
		}
		// A ContractError alone could be a preflight/schema failure without
		// testing this credential. Require independent, real official evidence
		// of an executed real probe whose nested result explicitly failed.
		if err := client.sensitiveEnvelope(ctx, http.MethodPost, u, []byte(form.Encode()), "application/x-www-form-urlencoded", &wire); err != nil ||
			wire.Success == nil || *wire.Success || wire.Mode != "real" || wire.Tag != prefix+"-client" ||
			(wire.HTTPStatus != nil && *wire.HTTPStatus != 0) {
			t.Fatal("official provider did not confirm the wrong-credential probe failure")
		}
		result, err := client.ProbeOutbound(ctx, wrongOutbound, nil, targetURL)
		var contractError *ContractError
		if !errors.As(err, &contractError) || result != (OutboundProbeResult{}) {
			t.Fatal("invalid credential did not produce a definite rejected probe result")
		}
		if origin.hits.Load() != hits || origin.invalid.Load() != 0 {
			t.Fatal("invalid credential reached the protected fixture origin")
		}
		// Cached process status alone is insufficient: a fresh authenticated
		// positive request must still traverse the same provider and inbound.
		positive(t, http.StatusOK)
		t.Log("official_inner_failure_on_wrong_credential=true wrong_credential_rejected=true negative_origin_hits=0 provider_control_probe_verified=true")
	}) {
		return
	}
	if !t.Run("origin_500_rejected_despite_official_success", func(t *testing.T) {
		origin.status.Store(http.StatusInternalServerError)
		hits := origin.hits.Load()
		u, err := client.endpointURL(outboundProbeEndpoint)
		if err != nil {
			t.Fatal("cannot construct the fixed official probe endpoint")
		}
		form := url.Values{"mode": {"real"}, "outbound": {string(outbound)}}
		var wire struct {
			Success    *bool  `json:"success"`
			Tag        string `json:"tag"`
			Mode       string `json:"mode"`
			HTTPStatus *int   `json:"httpStatus"`
		}
		// This unmodified HTTP response establishes the official semantic
		// difference; it is not a mock, fault response, or extra client process.
		if err := client.sensitiveEnvelope(ctx, http.MethodPost, u, []byte(form.Encode()), "application/x-www-form-urlencoded", &wire); err != nil ||
			wire.Success == nil || !*wire.Success || wire.Mode != "real" || wire.Tag != prefix+"-client" ||
			wire.HTTPStatus == nil || *wire.HTTPStatus != http.StatusInternalServerError {
			t.Fatal("official provider did not expose its successful HTTP-500 reachability result")
		}
		if origin.hits.Load() != hits+1 || origin.invalid.Load() != 0 {
			t.Fatal("official HTTP-500 observation did not traverse the fixture origin")
		}
		result, err := client.ProbeOutbound(ctx, outbound, nil, targetURL)
		var contractError *ContractError
		if !errors.As(err, &contractError) || result != (OutboundProbeResult{}) || origin.hits.Load() != hits+2 {
			t.Fatal("adapter accepted an HTTP-500 probe or failed to reach the controlled origin")
		}
		positive(t, http.StatusNoContent)
		t.Log("official_inner_success_on_500=true adapter_rejected_500=true provider_control_probe_verified=true")
	}) {
		return
	}
	if !t.Run("update_recovery_with_official_http_probe", func(t *testing.T) {
		liveHTTPProbeUpdateRecovery(t, ctx, client, identity, origin, outbound, targetURL, baseURL, tokenFile)
	}) {
		return
	}
	liveHTTPProbeRequireEmpty(t, ctx, client)
	liveProjectionRequireStoredAndAssembled(t, ctx, client, candidate, targetURL, "post-probe fixture")
	t.Log("upstream_unmodified=true http_only_control=true no_local_xray_process=true loopback_namespace_verified=true internet_reachability_verified=false")
}

// Installation results are deliberately injected; only service lifecycle and
// every data-path probe use the real official HTTP API. No download occurs.
func liveHTTPProbeUpdateRecovery(t *testing.T, ctx context.Context, client *Client, identity Status, origin *liveHTTPProbeOrigin, outbound json.RawMessage, targetURL, baseURL, tokenFile string) {
	t.Helper()
	origin.status.Store(http.StatusNoContent)
	probe := func(ctx context.Context) error {
		hits := origin.hits.Load()
		result, err := client.ProbeOutbound(ctx, outbound, nil, targetURL)
		if err != nil {
			return err
		}
		if result.HTTPStatus != http.StatusNoContent || origin.hits.Load() != hits+1 || origin.invalid.Load() != 0 {
			return errors.New("official recovery probe did not verify the isolated origin")
		}
		return nil
	}
	for _, mode := range []string{"reported_failure", "lost_recovery_response", "lost_install_response"} {
		if !t.Run(mode, func(t *testing.T) {
			fault := &liveUpdateFaultTransport{next: directTransport(), real: client, mode: mode, probe: probe}
			injected, err := New(Config{BaseURL: baseURL, Token: readLive3XUIToken(t, tokenFile), Timeout: 25 * time.Second,
				HTTPClient: &http.Client{Transport: fault}})
			if err != nil {
				t.Fatal("cannot construct the isolated update fault client")
			}
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal("cannot protect the isolated update journal directory")
			}
			journalPath := filepath.Join(dir, "update.json")
			updates, err := OpenXrayUpdates(journalPath, injected)
			if err != nil {
				t.Fatal("cannot create the isolated update journal")
			}
			defer updates.Close()
			request := XrayUpdateRequest{ID: liveWriteRandomID(t), Version: "v99.0.1", ExpectedPanelGUID: identity.PanelGUID,
				ExpectedPanelVersion: identity.PanelVersion, ExpectedRunningVersion: identity.Xray.Version}
			check := func(result XrayUpdateResult, runErr error) {
				t.Helper()
				liveUpdateRequireResult(t, result, runErr, mode, identity.Xray.Version)
				wantRestarts := int32(1)
				if mode == "lost_install_response" {
					wantRestarts = 0
				}
				if fault.installs.Load() != 1 || fault.restarts.Load() != wantRestarts ||
					fault.restartResponseLost.Load() != (mode == "lost_recovery_response") {
					t.Fatal("official HTTP recovery repeated a mutation or lost its fault boundary")
				}
			}
			check(updates.Run(ctx, request, probe))
			check(updates.Run(ctx, request, probe))
			if err := updates.Close(); err != nil {
				t.Fatal("cannot close the isolated update journal")
			}
			reopened, err := OpenXrayUpdates(journalPath, injected)
			if err != nil {
				t.Fatal("cannot reopen the isolated update journal")
			}
			defer reopened.Close()
			check(reopened.Reconcile(ctx, request, probe))
			if err := probe(ctx); err != nil {
				t.Fatal("final official HTTP recovery probe did not verify the origin")
			}
			t.Logf("fault_injection=true real_install_dispatched=false official_http_probe=true install_dispatches=%d recovery_restarts=%d durable_replay_safe=true",
				fault.installs.Load(), fault.restarts.Load())
		}) {
			return
		}
	}
}

type liveHTTPProbeOrigin struct {
	address     string
	server      *http.Server
	done        chan error
	status      atomic.Int64
	hits        atomic.Int64
	invalid     atomic.Int64
	written     atomic.Int64
	writeErrors atomic.Int64
}

func liveHTTPProbeStartOrigin(t *testing.T, expectedHost, expectedPath string) *liveHTTPProbeOrigin {
	return liveHTTPProbeStartOriginWithBody(t, expectedHost, expectedPath, []byte("chiral-isolated-http-probe"))
}

func liveHTTPProbeStartOriginWithBody(t *testing.T, expectedHost, expectedPath string, body []byte) *liveHTTPProbeOrigin {
	t.Helper()
	body = append([]byte(nil), body...)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("cannot create the isolated loopback HTTP origin")
	}
	origin := &liveHTTPProbeOrigin{address: listener.Addr().String(), done: make(chan error, 1)}
	origin.status.Store(http.StatusOK)
	origin.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
		remoteIP := net.ParseIP(remoteHost)
		if err != nil || remoteIP == nil || !remoteIP.IsLoopback() || r.Method != http.MethodGet ||
			r.Host != expectedHost || r.URL.Path != expectedPath || r.URL.RawQuery != "" {
			origin.invalid.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		origin.hits.Add(1)
		status := int(origin.status.Load())
		w.Header().Set("Content-Type", "text/plain")
		if status != http.StatusNoContent {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		w.WriteHeader(status)
		if status != http.StatusNoContent {
			n, err := w.Write(body)
			origin.written.Add(int64(n))
			if err != nil || n != len(body) {
				origin.writeErrors.Add(1)
			}
		}
	})}
	go func() { origin.done <- origin.server.Serve(listener) }()
	return origin
}

func (o *liveHTTPProbeOrigin) close(t *testing.T) {
	t.Helper()
	if err := o.server.Close(); err != nil {
		t.Error("cannot close the isolated HTTP origin")
	}
	select {
	case err := <-o.done:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Error("isolated HTTP origin exited unexpectedly")
		}
	case <-time.After(3 * time.Second):
		t.Error("isolated HTTP origin did not terminate within its cleanup budget")
	}
}

func liveHTTPProbeRequireLoopbackNamespace(t *testing.T) {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) == 0 {
		t.Fatal("cannot verify the isolated network namespace")
	}
	loopbackUp := false
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback == 0 {
			t.Fatal("HTTP probe contract requires a network-none namespace with no active non-loopback interface")
		}
		loopbackUp = true
	}
	if !loopbackUp {
		t.Fatal("isolated network namespace has no active loopback interface")
	}
}

func liveHTTPProbeRequireEmpty(t *testing.T, ctx context.Context, client *Client) {
	t.Helper()
	inbounds, err := client.ListInbounds(ctx)
	if err != nil || len(inbounds) != 0 {
		t.Fatal("HTTP probe contract requires a successfully read empty managed inbound list")
	}
	clients, err := client.ListClients(ctx)
	if err != nil || len(clients) != 0 {
		t.Fatal("HTTP probe contract requires a successfully read empty managed client list")
	}
}

func liveHTTPProbeRequireIdentity(t *testing.T, ctx context.Context, client *Client, expected Status) {
	t.Helper()
	actual, err := liveUpdateWaitState(ctx, client, "running")
	if err != nil || actual.PanelGUID != expected.PanelGUID || actual.PanelVersion != expected.PanelVersion || actual.Xray.Version != expected.Xray.Version {
		t.Fatal("HTTP probe provider identity or running version changed")
	}
}

func liveHTTPProbeConfig(t *testing.T, baseline json.RawMessage, prefix, credential string, inboundPort int, originAddress string) json.RawMessage {
	t.Helper()
	config := liveWriteObject(t, baseline)
	var inbounds, outbounds []json.RawMessage
	if json.Unmarshal(config["inbounds"], &inbounds) != nil || json.Unmarshal(config["outbounds"], &outbounds) != nil {
		t.Fatal("initial configuration lacks inbound or outbound arrays")
	}
	inboundTag, outboundTag := prefix+"-in", prefix+"-redirect"
	for _, row := range append(append([]json.RawMessage{}, inbounds...), outbounds...) {
		var identity struct{ Tag string }
		if json.Unmarshal(row, &identity) != nil || identity.Tag == inboundTag || identity.Tag == outboundTag {
			t.Fatal("fixture tag conflicts with the initial configuration")
		}
	}
	inbound := liveWriteJSON(t, map[string]any{
		"tag": inboundTag, "listen": "127.0.0.1", "port": inboundPort, "protocol": "vless",
		"settings":       map[string]any{"decryption": "none", "clients": []any{map[string]any{"id": credential, "email": prefix + "@invalid", "level": 0}}},
		"streamSettings": map[string]any{"network": "tcp", "security": "none"},
		"sniffing":       map[string]any{"enabled": false},
	})
	originHost, originPort, err := net.SplitHostPort(originAddress)
	if err != nil || originHost != "127.0.0.1" {
		t.Fatal("invalid isolated origin address")
	}
	portNumber, err := strconv.Atoi(originPort)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		t.Fatal("invalid isolated origin port")
	}
	outbound := liveWriteJSON(t, map[string]any{
		"tag": outboundTag, "protocol": "freedom", "settings": map[string]any{
			"redirect": originAddress,
			// Xray applies finalRules after redirect. Override private-address
			// blocking only for this owned outbound's exact loopback origin.
			"finalRules": []any{
				map[string]any{"action": "allow", "network": "tcp", "ip": []string{"127.0.0.1/32"}, "port": originPort},
				map[string]any{"action": "block"},
			},
		},
	})
	config["inbounds"] = liveWriteJSON(t, append(inbounds, inbound))
	config["outbounds"] = liveWriteJSON(t, append(outbounds, outbound))
	routing := liveWriteObject(t, config["routing"])
	var rules []json.RawMessage
	if json.Unmarshal(routing["rules"], &rules) != nil {
		t.Fatal("initial configuration lacks routing rules")
	}
	fixtureRule := liveWriteJSON(t, map[string]any{
		"type": "field", "inboundTag": []string{inboundTag}, "network": "tcp", "ip": []string{"8.8.8.8/32"}, "port": "80", "outboundTag": outboundTag,
	})
	rules, err = liveWriteInsertFixtureRule(rules, fixtureRule)
	if err != nil {
		t.Fatal("initial configuration lacks the exact preserved API routing rule")
	}
	routing["rules"] = liveWriteJSON(t, rules)
	config["routing"] = liveWriteJSON(t, routing)
	return liveWriteJSON(t, config)
}

func liveHTTPProbeOutbound(t *testing.T, tag, credential string, port int) json.RawMessage {
	t.Helper()
	return liveWriteJSON(t, map[string]any{
		"tag": tag, "protocol": "vless", "settings": map[string]any{
			"vnext": []any{map[string]any{"address": "127.0.0.1", "port": port, "users": []any{map[string]any{"id": credential, "encryption": "none"}}}},
		},
		"streamSettings": map[string]any{"network": "tcp", "security": "none"},
	})
}

func liveHTTPProbeRestore(t *testing.T, client *Client, identity Status, snapshot XrayTemplate, baseline, candidate json.RawMessage, targetURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	actual, err := client.Status(ctx)
	if err != nil || actual.PanelGUID != identity.PanelGUID || actual.PanelVersion != identity.PanelVersion {
		t.Error("cleanup refused to mutate an unconfirmed provider identity")
		return
	}
	inbounds, inboundErr := client.ListInbounds(ctx)
	clients, clientErr := client.ListClients(ctx)
	if inboundErr != nil || clientErr != nil || len(inbounds) != 0 || len(clients) != 0 {
		t.Error("cleanup refused to overwrite unrecognised managed objects")
		return
	}
	current, err := client.ReadXrayTemplate(ctx)
	if err != nil || (current.OutboundTestURL != targetURL && current.OutboundTestURL != snapshot.OutboundTestURL) {
		t.Error("cleanup refused to overwrite an unconfirmed template target")
		return
	}
	own, ownErr := CompareConfig(candidate, current.Config)
	original, originalErr := CompareConfig(snapshot.Config, current.Config)
	if ownErr != nil || originalErr != nil || (!own.Equivalent && !original.Equivalent) {
		t.Error("cleanup refused to overwrite a template outside exact fixture ownership")
		return
	}
	if err := client.UpdateXrayTemplate(ctx, snapshot); err != nil {
		t.Error("cleanup could not restore the complete original template and test URL")
		return
	}
	restored, err := client.ReadXrayTemplate(ctx)
	if err != nil || restored.OutboundTestURL != snapshot.OutboundTestURL {
		t.Error("cleanup could not verify the original template target")
		return
	}
	stored, err := CompareConfig(snapshot.Config, restored.Config)
	if err != nil || !stored.Equivalent {
		t.Error("cleanup did not restore the complete original stored template")
		return
	}
	after, err := client.ReadAssembledConfig(ctx)
	if err != nil {
		t.Error("cleanup could not read the restored assembled configuration")
		return
	}
	assembled, err := CompareConfig(baseline, after)
	if err != nil || !assembled.Equivalent {
		t.Error("cleanup did not restore the complete original assembled configuration")
		return
	}
	// No managed rows were created, and no broad deletion is needed.
	liveHTTPProbeRequireEmpty(t, ctx, client)
	liveHTTPProbeRequireIdentity(t, ctx, client, identity)
	t.Log("cleanup_verified=true empty_objects=true template_restored=true outbound_test_url_restored=true assembled_config_unchanged=true")
}
