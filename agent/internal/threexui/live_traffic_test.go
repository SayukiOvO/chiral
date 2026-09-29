package threexui

import (
	"bytes"
	"context"
	"crypto/sha256"
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

// liveWriteTrafficContract is a harness-only data-plane client, not an Agent
// backend. Provider management and cumulative billing are exclusively HTTP.
// The explicit client binary must belong to the disposable official instance;
// this helper neither discovers nor reads its managed config or Xray API.
func liveWriteTrafficContract(t *testing.T, ctx context.Context, provider *Client, account map[string]any, inbound Inbound) {
	t.Helper()
	bin := os.Getenv("CHIRAL_3XUI_TEST_XRAY_BIN")
	if bin == "" {
		t.Skip("set CHIRAL_3XUI_TEST_XRAY_BIN to enable the isolated real-data-plane fixture")
	}
	info, err := os.Stat(bin)
	if !filepath.IsAbs(bin) || err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatal("CHIRAL_3XUI_TEST_XRAY_BIN must be an executable regular file at an absolute path")
	}
	email, emailOK := account["email"].(string)
	uuid, uuidOK := account["id"].(string)
	if !emailOK || !uuidOK || email == "" || uuid == "" || account["enable"] != true || inbound.Listen != "127.0.0.1" || inbound.Port <= 0 {
		t.Fatal("data-plane fixture requires its enabled owned loopback client")
	}
	baselineSnapshot, err := provider.TrafficSnapshot(ctx)
	if err != nil {
		t.Fatal("cannot read the official HTTP traffic baseline")
	}
	baseline := liveTrafficOwnedCounter(t, baselineSnapshot, email, inbound.ID)

	// Distinct deterministic bodies make direction swaps observable. The
	// origin is local to the isolated network namespace, with no redirects.
	upload := bytes.Repeat([]byte("chiral-upload-1|"), 2048)    // 32 KiB
	download := bytes.Repeat([]byte("chiral-download|"), 32768) // 512 KiB
	var delivered atomic.Int32
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/ready" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
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
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(download)))
		_, _ = w.Write(download)
	}))
	// Force IPv4 loopback rather than relying on httptest's host-dependent
	// fallback family. Neither the origin nor SOCKS listener is public.
	_ = origin.Listener.Close()
	origin.Listener, err = net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("cannot allocate the loopback fixture origin")
	}
	origin.Start()
	defer origin.Close()
	socksListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("cannot reserve a loopback SOCKS fixture port")
	}
	socksPort := socksListener.Addr().(*net.TCPAddr).Port
	if err := socksListener.Close(); err != nil {
		t.Fatal("cannot release the reserved SOCKS fixture port")
	}
	config := liveWriteJSON(t, map[string]any{
		"log": map[string]any{"loglevel": "none"},
		"inbounds": []any{map[string]any{"tag": "fixture-socks", "listen": "127.0.0.1", "port": socksPort, "protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false}}},
		"outbounds": []any{map[string]any{"tag": "fixture-vless", "protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": inbound.Port,
				"users": []any{map[string]any{"id": uuid, "encryption": "none"}}}}},
			"streamSettings": map[string]any{"network": "tcp", "security": "none"}}},
		"routing": map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"fixture-socks"}, "outboundTag": "fixture-vless"}}},
	})
	configPath := filepath.Join(t.TempDir(), "fixture-client.json")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal("cannot write the private temporary fixture client configuration")
	}
	cmd := exec.CommandContext(ctx, bin, "run", "-c", configPath, "-format", "json")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal("cannot start the temporary fixture client process")
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		select {
		case <-done:
			t.Error("temporary fixture client exited before its deliberate termination")
			return
		default:
		}
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Error("cannot terminate the temporary fixture client")
		}
		select {
		case <-done:
			// Kill is deliberate, so a nonzero exit is expected. Wait must
			// still return an observed process exit, not just an absent error.
			var exitErr *exec.ExitError
			if cmd.ProcessState == nil || (waitErr != nil && !errors.As(waitErr, &exitErr)) {
				t.Error("temporary fixture client exit was not confirmed")
				return
			}
			t.Log("fixture_process_reaped=true")
		case <-time.After(5 * time.Second):
			t.Error("temporary fixture client was not reaped within five seconds")
		}
	}
	defer stop()

	proxyURL := &url.URL{Scheme: "socks5", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(socksPort))}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true, DisableCompression: true}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	readyCtx, readyCancel := context.WithTimeout(ctx, 20*time.Second)
	defer readyCancel()
	for {
		select {
		case <-done:
			t.Fatal("temporary fixture client exited before completing a proxied request")
		default:
		}
		requestCtx, requestCancel := context.WithTimeout(readyCtx, time.Second)
		req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, origin.URL+"/ready", nil)
		if err != nil {
			requestCancel()
			t.Fatal("cannot create the loopback readiness request")
		}
		response, err := httpClient.Do(req)
		ready := err == nil && response.StatusCode == http.StatusNoContent
		if response != nil {
			_ = response.Body.Close()
		}
		requestCancel()
		if ready {
			break
		}
		select {
		case <-readyCtx.Done():
			t.Fatal("temporary SOCKS-to-VLESS path did not reach the local origin within twenty seconds")
		case <-time.After(100 * time.Millisecond):
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin.URL+"/payload", bytes.NewReader(upload))
	if err != nil {
		t.Fatal("cannot create the deterministic payload request")
	}
	response, err := httpClient.Do(req)
	if err != nil {
		t.Fatal("deterministic payload did not traverse the SOCKS-to-VLESS path")
	}
	got, readErr := io.ReadAll(io.LimitReader(response.Body, int64(len(download))+1))
	closeErr := response.Body.Close()
	if response.StatusCode != http.StatusOK || readErr != nil || closeErr != nil || !bytes.Equal(got, download) || delivered.Load() != 1 {
		t.Fatal("deterministic request or response bytes did not match end to end")
	}
	digest := sha256.Sum256(download)
	t.Logf("payload_verified=true response_sha256=%x upload_bytes=%d download_bytes=%d", digest, len(upload), len(download))
	stop() // Close the tunnel before waiting for provider-owned counter flushes.
	if t.Failed() {
		return
	}

	pollCtx, pollCancel := context.WithTimeout(ctx, 40*time.Second)
	defer pollCancel()
	lastSnapshot, lastCounter := baselineSnapshot, baseline
	reportNumbers := func() {
		for _, line := range liveTrafficNumericDiagnostics(baselineSnapshot, lastSnapshot, baseline, lastCounter) {
			t.Log(line)
		}
	}
	for {
		snapshot, err := provider.TrafficSnapshot(pollCtx)
		if err != nil {
			reportNumbers()
			if pollCtx.Err() != nil {
				t.Fatal("official user counters did not account for both payload directions within forty seconds")
			}
			t.Fatal("official HTTP traffic sampling failed; no partial billing result was accepted")
		}
		current := liveTrafficOwnedCounter(t, snapshot, email, inbound.ID)
		lastSnapshot, lastCounter = snapshot, current
		if current.ID != baseline.ID || current.Up < baseline.Up || current.Down < baseline.Down {
			reportNumbers()
			t.Fatal("official user counter identity changed or a cumulative counter decreased")
		}
		up, down := current.Up-baseline.Up, current.Down-baseline.Down
		if up >= uint64(len(upload)) && down >= uint64(len(download)) {
			if up > uint64(len(upload))*3 || down > uint64(len(download))*2 || down <= up*4 {
				reportNumbers()
				t.Fatal("official user counter direction or magnitude does not match the isolated payload")
			}
			t.Logf("billing_verified=true user_up_delta=%d user_down_delta=%d accounting_scope=client_only", up, down)
			return
		}
		select {
		case <-pollCtx.Done():
			reportNumbers()
			t.Fatal("official user counters did not account for both payload directions within forty seconds")
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// liveTrafficNumericDiagnostics never formats provider-controlled strings or
// object dumps. Scope names are fixed, and values are exclusively counters or
// row positions. Other scopes are diagnostic only, never added to user usage.
func liveTrafficNumericDiagnostics(baseline, last TrafficSnapshot, initial, current ClientTraffic) []string {
	lines := []string{fmt.Sprintf("billing_baseline_up=%d billing_baseline_down=%d billing_last_up=%d billing_last_down=%d billing_delta_up=%d billing_delta_down=%d",
		initial.Up, initial.Down, current.Up, current.Down, int64(current.Up)-int64(initial.Up), int64(current.Down)-int64(initial.Down))}
	for _, scope := range []struct {
		name string
		rows []TaggedTraffic
	}{
		{"baseline_inbound", baseline.Inbounds}, {"last_inbound", last.Inbounds},
		{"baseline_outbound", baseline.Outbounds}, {"last_outbound", last.Outbounds},
	} {
		shown := min(len(scope.rows), 8)
		lines = append(lines, fmt.Sprintf("billing_%s_count=%d shown=%d", scope.name, len(scope.rows), shown))
		for i, row := range scope.rows[:shown] {
			lines = append(lines, fmt.Sprintf("billing_%s_row=%d up=%d down=%d", scope.name, i, row.Up, row.Down))
		}
	}
	return lines
}

func liveTrafficOwnedCounter(t *testing.T, snapshot TrafficSnapshot, email string, inboundID int64) ClientTraffic {
	t.Helper()
	// Inbound and outbound counters are intentionally ignored here. They are
	// separate views of these bytes, not additional subscriber usage.
	if len(snapshot.Clients) != 1 || snapshot.Clients[0].Email != email || snapshot.Clients[0].InboundID != inboundID {
		t.Fatal("official traffic snapshot lacks the unique local owned client counter")
	}
	return snapshot.Clients[0]
}

func TestLiveTrafficFixturePayloadSizes(t *testing.T) {
	// Keep direction/magnitude assertions tied to the fixture corpus, not to
	// comments that might become stale when the byte sequences change.
	if got := len("chiral-upload-1|") * 2048; got != 32<<10 {
		t.Fatalf("unexpected upload fixture size: %d", got)
	}
	if got := len("chiral-download|") * 32768; got != 512<<10 {
		t.Fatalf("unexpected download fixture size: %d", got)
	}
}

func TestLiveTrafficNumericDiagnosticsKeepOnlyCounters(t *testing.T) {
	initial := ClientTraffic{ID: 1, InboundID: 2, Email: "SECRET_EMAIL", Up: 3, Down: 4}
	current := ClientTraffic{ID: 1, InboundID: 2, Email: "SECRET_EMAIL", Up: 13, Down: 104}
	baseline := TrafficSnapshot{Clients: []ClientTraffic{initial}, Inbounds: []TaggedTraffic{{Tag: "SECRET_INBOUND", Up: 7, Down: 8}}, Outbounds: []TaggedTraffic{{Tag: "SECRET_OUTBOUND", Up: 9, Down: 10}}}
	last := TrafficSnapshot{Clients: []ClientTraffic{current}, Inbounds: []TaggedTraffic{{Tag: "SECRET_INBOUND", Up: 17, Down: 108}}, Outbounds: []TaggedTraffic{{Tag: "SECRET_OUTBOUND", Up: 19, Down: 110}}}
	lines := liveTrafficNumericDiagnostics(baseline, last, initial, current)
	if lines[0] != "billing_baseline_up=3 billing_baseline_down=4 billing_last_up=13 billing_last_down=104 billing_delta_up=10 billing_delta_down=100" {
		t.Fatal("numeric diagnostic lost baseline or delta values")
	}
	output := strings.Join(lines, "\n")
	if strings.Contains(output, "SECRET") || strings.Contains(output, "Email") || strings.Contains(output, "Tag") {
		t.Fatal("numeric diagnostic exposed a provider identity")
	}
	for _, wanted := range []string{"billing_last_inbound_row=0 up=17 down=108", "billing_last_outbound_row=0 up=19 down=110"} {
		if !strings.Contains(output, wanted) {
			t.Fatal("numeric diagnostic lost scope counters")
		}
	}
	last.Inbounds = make([]TaggedTraffic, 100)
	output = strings.Join(liveTrafficNumericDiagnostics(baseline, last, initial, current), "\n")
	if !strings.Contains(output, "billing_last_inbound_count=100 shown=8") || strings.Contains(output, "billing_last_inbound_row=8") {
		t.Fatal("numeric diagnostic did not bound its row output")
	}
}
