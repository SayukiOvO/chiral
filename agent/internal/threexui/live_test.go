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
	"testing"
	"time"
)

// TestLive3XUIReadOnlyContract exercises an explicitly configured, isolated
// official 3x-ui instance. It only sends GET requests; no login, configuration,
// client, traffic, or lifecycle mutations are performed.
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
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	status, err := client.Status(ctx)
	if err != nil {
		t.Fatalf("reading live 3x-ui status: %v", err)
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
	before, err := client.ConfigJSON(ctx)
	if err != nil {
		t.Fatalf("reading the initial live 3x-ui assembled configuration: %v", err)
	}
	before = normaliseLive3XUIConfig(t, before)

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

	after, err := client.ConfigJSON(ctx)
	if err != nil {
		t.Fatalf("reading the final live 3x-ui assembled configuration: %v", err)
	}
	if !bytes.Equal(before, normaliseLive3XUIConfig(t, after)) {
		t.Fatal("live 3x-ui assembled configuration changed during the read-only observation")
	}

	// One invalid bearer request verifies the auth boundary without attempting
	// a login or triggering a sequence of failed authentication requests.
	unauthorised, err := New(Config{BaseURL: baseURL, Token: token + "-invalid-live-contract", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("constructing the negative-authentication client: %v", err)
	}
	_, err = unauthorised.Status(ctx)
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || (statusErr.StatusCode != http.StatusUnauthorized && statusErr.StatusCode != http.StatusForbidden) {
		t.Fatal("live 3x-ui did not reject an invalid bearer token with HTTP 401 or 403")
	}

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
