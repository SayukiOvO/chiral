package threexui

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLive3XUIReadOnlyContract exercises an explicitly configured, isolated
// official 3x-ui instance. A transport allowlist permits only status, OpenAPI,
// and persisted outbound counters. In particular, getConfigJson is forbidden:
// its apparent GET can run user lifecycle maintenance upstream. This test does
// not claim database immutability (e.g. token-use timestamps) or config fidelity.
func TestLive3XUIReadOnlyContract(t *testing.T) {
	baseURL := os.Getenv("CHIRAL_3XUI_TEST_URL")
	tokenFile := os.Getenv("CHIRAL_3XUI_TEST_TOKEN_FILE")
	if baseURL == "" && tokenFile == "" {
		t.Skip("set CHIRAL_3XUI_TEST_URL and CHIRAL_3XUI_TEST_TOKEN_FILE to test an isolated 3x-ui instance")
	}
	if baseURL == "" || tokenFile == "" {
		t.Fatal("CHIRAL_3XUI_TEST_URL and CHIRAL_3XUI_TEST_TOKEN_FILE must both be set")
	}
	token := readLive3XUIToken(t, tokenFile)
	client, err := New(Config{BaseURL: baseURL, Token: token, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("constructing the live 3x-ui client: %v", err)
	}
	allowed := make(map[string]bool)
	for _, endpoint := range []string{statusEndpoint, openAPIEndpoint, outboundTrafficEndpoint} {
		u, err := client.endpointURL(endpoint)
		if err != nil {
			t.Fatal("cannot construct the fixed SHADOW observation allowlist")
		}
		allowed[u.Path] = true
	}
	var mu sync.Mutex
	calls := make(map[string]int)
	forbidden := 0
	next := directTransport()
	if closer, ok := next.(interface{ CloseIdleConnections() }); ok {
		defer closer.CloseIdleConnections()
	}
	guard := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		if r.Method != http.MethodGet || r.URL.Scheme != client.base.Scheme || r.URL.Host != client.base.Host ||
			r.URL.RawQuery != "" || !allowed[r.URL.Path] {
			forbidden++
			mu.Unlock()
			return nil, errors.New("live SHADOW contract blocked a request outside its observational allowlist")
		}
		calls[r.URL.Path]++
		mu.Unlock()
		return next.RoundTrip(r)
	})
	client.http.Transport = guard
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// The HTTP listener can become ready before the first cached status object
	// exists. Bounded read-only polling must obtain a valid snapshot, never
	// treat an empty envelope as successful observation.
	preflightCtx, preflightCancel := context.WithTimeout(ctx, 10*time.Second)
	defer preflightCancel()
	var status Status
	statusAttempts := 0
	for {
		statusAttempts++
		status, err = client.Status(preflightCtx)
		if err == nil {
			break
		}
		select {
		case <-preflightCtx.Done():
			t.Fatal("official status did not provide a valid snapshot within the startup budget")
		case <-time.After(250 * time.Millisecond):
		}
	}
	if strings.TrimSpace(status.PanelGUID) == "" {
		t.Fatal("live 3x-ui status has no panel identity")
	}
	switch strings.ToLower(strings.TrimSpace(status.Xray.State)) {
	case "running", "stop", "stopped", "error":
	default:
		t.Fatal("live 3x-ui status has an unsupported Xray state")
	}

	capabilities, err := client.DiscoverCapabilities(ctx)
	if err != nil {
		t.Fatalf("reading the live 3x-ui OpenAPI contract: %v", err)
	}
	if missing := capabilities.Missing(); len(missing) != 0 {
		t.Fatalf("live 3x-ui contract is missing capabilities: %v", missing)
	}
	digest, err := hex.DecodeString(capabilities.DocumentSHA256)
	if err != nil || len(digest) != 32 {
		t.Fatal("live 3x-ui contract has no valid SHA-256 digest")
	}
	observer, err := NewObserver(client, ObserverConfig{})
	if err != nil {
		t.Fatalf("constructing the live 3x-ui observer: %v", err)
	}
	started := time.Now()
	observation := observer.Observe(ctx)
	if observation.Error != nil {
		t.Fatalf("observing the live 3x-ui instance: %v", observation.Error)
	}
	if observation.PanelVersion != status.PanelVersion || observation.XrayState != status.Xray.State || observation.XrayVersion != status.Xray.Version {
		t.Fatal("live 3x-ui identity or Xray status changed during contract verification")
	}
	if observation.ContractDigest != capabilities.DocumentSHA256 || observation.ContractObservedAt.Before(started) || observation.ContractObservedAt.After(time.Now()) {
		t.Fatal("live 3x-ui observer did not return fresh, matching contract evidence")
	}
	if len(observation.MissingCapabilities) != 0 || len(observation.SupportedCapabilities) != len(RequiredCapabilities()) {
		t.Fatal("live 3x-ui observer did not verify every required capability")
	}

	cached := observer.Observe(ctx)
	if cached.Error != nil || cached.ContractDigest != observation.ContractDigest || !cached.ContractObservedAt.Equal(observation.ContractObservedAt) {
		t.Fatal("cached SHADOW observation lost its original verified contract")
	}

	// One invalid bearer request verifies the auth boundary without attempting
	// a login or triggering a sequence of failed authentication requests.
	unauthorised, err := New(Config{BaseURL: baseURL, Token: token + "-invalid-live-contract", Timeout: 5 * time.Second,
		HTTPClient: &http.Client{Transport: guard}})
	if err != nil {
		t.Fatalf("constructing the negative-authentication client: %v", err)
	}
	_, err = unauthorised.Status(ctx)
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || (statusErr.StatusCode != http.StatusUnauthorized && statusErr.StatusCode != http.StatusForbidden) {
		t.Fatal("live 3x-ui did not reject an invalid bearer token with HTTP 401 or 403")
	}
	mu.Lock()
	defer mu.Unlock()
	if forbidden != 0 || len(calls) != 3 {
		t.Fatal("live SHADOW observation attempted a request outside its exact allowlist")
	}
	for endpoint, expected := range map[string]int{statusEndpoint: statusAttempts + 3, openAPIEndpoint: 2, outboundTrafficEndpoint: 1} {
		u, err := client.endpointURL(endpoint)
		if err != nil || calls[u.Path] != expected {
			t.Fatal("live SHADOW observation did not execute the expected read-only contract requests")
		}
	}
	t.Log("shadow_request_allowlist_verified=true assembled_config_requests=0 template_requests=0 explicit_mutation_requests=0 config_equivalence_verified=false")

	t.Logf("panel_version=%q xray_state=%q xray_version=%q contract_sha256=%s capability_count=%d",
		client.safeDiagnostic(status.PanelVersion), client.safeDiagnostic(status.Xray.State),
		client.safeDiagnostic(status.Xray.Version), capabilities.DocumentSHA256, len(observation.SupportedCapabilities))
}

func readLive3XUIToken(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal("cannot open CHIRAL_3XUI_TEST_TOKEN_FILE")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("CHIRAL_3XUI_TEST_TOKEN_FILE must be a readable regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatal("CHIRAL_3XUI_TEST_TOKEN_FILE must not grant group or other access")
	}
	const maxTokenBytes = 8 << 10
	data, err := io.ReadAll(io.LimitReader(f, maxTokenBytes+1))
	if err != nil || len(data) > maxTokenBytes {
		t.Fatal("CHIRAL_3XUI_TEST_TOKEN_FILE must be readable and at most 8192 bytes")
	}
	token := string(data)
	if strings.HasSuffix(token, "\n") {
		token = strings.TrimSuffix(strings.TrimSuffix(token, "\n"), "\r")
	}
	if err := validateToken(token); err != nil {
		t.Fatalf("invalid CHIRAL_3XUI_TEST_TOKEN_FILE: %v", err)
	}
	return token
}

// Used by explicitly authorized write/recovery fixtures, never by SHADOW.
func normaliseLive3XUIConfig(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	var config map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&config); err != nil || len(config) == 0 {
		t.Fatal("live 3x-ui assembled configuration must be a nonempty JSON object")
	}
	normalised, err := json.Marshal(config)
	if err != nil {
		t.Fatal("cannot normalise the live 3x-ui assembled configuration")
	}
	return normalised
}
