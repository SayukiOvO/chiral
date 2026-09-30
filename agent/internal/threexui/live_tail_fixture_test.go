package threexui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const liveTailChunkSize = 64 << 10

type liveTailFixture struct {
	ctx    context.Context
	cancel context.CancelFunc

	origin         *http.Server
	originListener net.Listener
	originDone     chan error
	originAddress  string
	pathPrefix     string
	invalidOrigin  atomic.Int64
	targetFresh    atomic.Int64
	controlFresh   atomic.Int64

	target  *liveTailStream
	control *liveTailStream

	cmd     *exec.Cmd
	cmdDone chan struct{}
	waitErr error

	opMu    sync.Mutex
	closeMu sync.Mutex
	started bool
	closed  bool
	revoked bool
	// Frozen at the first negative probe and never advanced. A request that
	// reaches the origin after its local timeout must fail a later check, not
	// become the next negative probe's baseline.
	revokedFreshCount int64
}

type liveTailStream struct {
	name        string
	path        string
	socksPort   int
	chunks      chan []byte
	established chan struct{}
	claimed     atomic.Bool
	sequence    uint64

	requestCancel context.CancelFunc
	response      *http.Response
	transport     *http.Transport
}

func liveTailStartFixture(t *testing.T, ctx context.Context, targetID, controlID string, inboundPort int) *liveTailFixture {
	t.Helper()
	if ctx == nil || targetID == "" || controlID == "" || targetID == controlID || inboundPort < 1 || inboundPort > 65535 {
		t.Fatal("tail fixture requires two distinct credentials and one valid inbound port")
	}
	bin := liveProjectionXrayBinary(t)
	tempDir := t.TempDir()
	fixtureCtx, cancel := context.WithCancel(ctx)
	f := &liveTailFixture{
		ctx:        fixtureCtx,
		cancel:     cancel,
		originDone: make(chan error, 1),
		pathPrefix: "/chiral-tail-" + liveWriteRandomID(t),
		cmdDone:    make(chan struct{}),
	}
	f.target = &liveTailStream{name: "target", path: f.pathPrefix + "/target/stream", chunks: make(chan []byte), established: make(chan struct{})}
	f.control = &liveTailStream{name: "control", path: f.pathPrefix + "/control/stream", chunks: make(chan []byte), established: make(chan struct{})}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatal("cannot start the private tail origin")
	}
	f.originListener = listener
	f.originAddress = listener.Addr().String()
	f.origin = &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(f.handleOrigin)}
	go func() { f.originDone <- f.origin.Serve(listener) }()
	// This cleanup is registered after TempDir's cleanup, so the process is
	// reaped before Go removes its private configuration file. Close is idempotent
	// because the main contract also defers it explicitly.
	t.Cleanup(func() { f.Close(t) })

	ports := make(map[int]bool, 3)
	ports[inboundPort] = true
	allocateSOCKS := func() int {
		for attempt := 0; attempt < 8; attempt++ {
			port := liveProjectionFreePort(t)
			if !ports[port] {
				ports[port] = true
				return port
			}
		}
		t.Fatal("cannot allocate distinct private tail SOCKS ports")
		return 0
	}
	f.target.socksPort, f.control.socksPort = allocateSOCKS(), allocateSOCKS()

	config := liveWriteJSON(t, map[string]any{
		"log": map[string]any{"loglevel": "none"},
		"inbounds": []any{
			liveTailSOCKSInbound("fixture-tail-target-socks", f.target.socksPort),
			liveTailSOCKSInbound("fixture-tail-control-socks", f.control.socksPort),
		},
		"outbounds": []any{
			map[string]any{"tag": "fixture-tail-blocked", "protocol": "blackhole"},
			liveTailVLESSOutbound("fixture-tail-target", targetID, inboundPort),
			liveTailVLESSOutbound("fixture-tail-control", controlID, inboundPort),
		},
		"routing": map[string]any{"rules": []any{
			map[string]any{"type": "field", "inboundTag": []string{"fixture-tail-target-socks"}, "outboundTag": "fixture-tail-target"},
			map[string]any{"type": "field", "inboundTag": []string{"fixture-tail-control-socks"}, "outboundTag": "fixture-tail-control"},
		}},
	})
	configPath := filepath.Join(tempDir, "tail-client.json")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal("cannot write the private tail client configuration")
	}
	f.cmd = exec.CommandContext(fixtureCtx, bin, "run", "-c", configPath, "-format", "json")
	f.cmd.Stdout, f.cmd.Stderr = io.Discard, io.Discard
	f.cmd.WaitDelay = 3 * time.Second
	if err := f.cmd.Start(); err != nil {
		t.Fatal("cannot start the private tail client process")
	}
	go func() {
		f.waitErr = f.cmd.Wait()
		close(f.cmdDone)
	}()

	readyCtx, readyCancel := context.WithTimeout(ctx, 12*time.Second)
	defer readyCancel()
	for {
		if liveProjectionSOCKSHandshake(f.target.socksPort, 300*time.Millisecond) &&
			liveProjectionSOCKSHandshake(f.control.socksPort, 300*time.Millisecond) {
			return f
		}
		select {
		case <-f.cmdDone:
			t.Fatal("private tail client exited before its SOCKS listeners became ready")
		case <-readyCtx.Done():
			t.Fatal("private tail SOCKS listeners did not become ready within twelve seconds")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func liveTailSOCKSInbound(tag string, port int) map[string]any {
	return map[string]any{
		"tag": tag, "listen": "127.0.0.1", "port": port, "protocol": "socks",
		"settings": map[string]any{"auth": "noauth", "udp": false},
	}
}

func liveTailVLESSOutbound(tag, credential string, port int) map[string]any {
	return map[string]any{
		"tag": tag, "protocol": "vless",
		"settings": map[string]any{"vnext": []any{map[string]any{
			"address": "127.0.0.1", "port": port,
			"users": []any{map[string]any{"id": credential, "encryption": "none"}},
		}}},
		"streamSettings": map[string]any{"network": "tcp", "security": "none"},
		"mux":            map[string]any{"enabled": false},
	}
}

func (f *liveTailFixture) OriginAddress() string {
	return f.originAddress
}

func (f *liveTailFixture) StartStreams(t *testing.T, ctx context.Context) {
	t.Helper()
	f.opMu.Lock()
	defer f.opMu.Unlock()
	if f.started || f.closed {
		t.Fatal("tail streams may be started exactly once before fixture closure")
	}
	f.requireProcess(t)
	f.startStream(t, ctx, f.target)
	f.startStream(t, ctx, f.control)
	f.started = true
}

func (f *liveTailFixture) startStream(t *testing.T, ctx context.Context, stream *liveTailStream) {
	t.Helper()
	proxy := &url.URL{Scheme: "socks5", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(stream.socksPort))}
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true, DisableCompression: true}
	requestCtx, requestCancel := context.WithCancel(f.ctx)
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, liveTailLogicalURL(stream.path), nil)
	if err != nil {
		requestCancel()
		t.Fatal("cannot build the established tail stream request")
	}
	type result struct {
		response *http.Response
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		response, requestErr := (&http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}).Do(request)
		resultCh <- result{response: response, err: requestErr}
	}()
	waitCtx, waitCancel := context.WithTimeout(ctx, 12*time.Second)
	defer waitCancel()
	select {
	case got := <-resultCh:
		if got.err != nil || got.response == nil || got.response.StatusCode != http.StatusOK {
			if got.response != nil {
				_ = got.response.Body.Close()
			}
			requestCancel()
			transport.CloseIdleConnections()
			t.Fatal("established tail stream did not receive the private origin response headers")
		}
		stream.requestCancel, stream.response, stream.transport = requestCancel, got.response, transport
	case <-f.cmdDone:
		requestCancel()
		transport.CloseIdleConnections()
		t.Fatal("private tail client exited while opening an established stream")
	case <-waitCtx.Done():
		requestCancel()
		transport.CloseIdleConnections()
		t.Fatal("established tail stream did not open within twelve seconds")
	}
	select {
	case <-stream.established:
	case <-waitCtx.Done():
		t.Fatal("private tail origin did not confirm the established stream")
	}
}

func (f *liveTailFixture) Chunk(t *testing.T, ctx context.Context) {
	t.Helper()
	f.opMu.Lock()
	defer f.opMu.Unlock()
	if !f.started || f.closed {
		t.Fatal("tail chunks require two active established streams")
	}
	f.requireProcess(t)
	f.requireNoRevokedArrival(t)
	f.chunk(t, ctx, f.target)
	f.chunk(t, ctx, f.control)
	f.requireNoRevokedArrival(t)
	if f.invalidOrigin.Load() != 0 {
		t.Fatal("private tail origin received an invalid request or incomplete write")
	}
}

func (f *liveTailFixture) chunk(t *testing.T, ctx context.Context, stream *liveTailStream) {
	t.Helper()
	stream.sequence++
	payload := liveTailPayload(stream.name, stream.sequence)
	chunkCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	select {
	case stream.chunks <- payload:
	case <-chunkCtx.Done():
		t.Fatal("private tail origin did not accept the requested stream chunk")
	}
	type readResult struct {
		data []byte
		err  error
	}
	readCh := make(chan readResult, 1)
	go func() {
		data := make([]byte, len(payload))
		_, err := io.ReadFull(stream.response.Body, data)
		readCh <- readResult{data: data, err: err}
	}()
	select {
	case result := <-readCh:
		if result.err != nil || !bytes.Equal(result.data, payload) {
			t.Fatal("established tail stream did not receive the commanded bytes exactly")
		}
	case <-f.cmdDone:
		t.Fatal("private tail client exited while an established stream was active")
	case <-chunkCtx.Done():
		t.Fatal("established tail stream did not deliver the commanded bytes within six seconds")
	}
}

func liveTailPayload(name string, sequence uint64) []byte {
	prefix := []byte(fmt.Sprintf("chiral-tail-%s-%08d|", name, sequence))
	payload := make([]byte, liveTailChunkSize)
	for offset := 0; offset < len(payload); offset += len(prefix) {
		copy(payload[offset:], prefix)
	}
	return payload
}

func (f *liveTailFixture) Fresh(t *testing.T, ctx context.Context, targetAllowed bool) {
	t.Helper()
	f.opMu.Lock()
	defer f.opMu.Unlock()
	if f.closed {
		t.Fatal("fresh connection checks cannot run after fixture closure")
	}
	f.requireProcess(t)
	if targetAllowed {
		if f.revoked {
			t.Fatal("tail fixture must not reauthorize a revoked target")
		}
		f.requireFreshSuccess(t, ctx, f.target, 10*time.Second)
	} else {
		if !f.revoked {
			f.revokedFreshCount = f.targetFresh.Load()
			f.revoked = true
		}
		f.requireNoRevokedArrival(t)
		for attempt := 0; attempt < 2; attempt++ {
			f.requireFreshRejection(t, ctx, f.target)
		}
		// Observe cancellation races instead of accepting an instantaneous
		// request error. The same frozen count remains enforced throughout
		// subsequent streaming, deletion, negative probes and final closure.
		quietUntil := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(quietUntil) {
			f.requireNoRevokedArrival(t)
			select {
			case <-ctx.Done():
				t.Fatal("revocation observation exceeded the parent context")
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
	// A contemporaneous positive control prevents a dead client process, lost
	// SOCKS listener, stopped server inbound, or unavailable origin from passing
	// as credential revocation.
	f.requireFreshSuccess(t, ctx, f.control, 3*time.Second)
	f.requireNoRevokedArrival(t)
	if f.invalidOrigin.Load() != 0 {
		t.Fatal("private tail origin received an invalid fresh request")
	}
}

func (f *liveTailFixture) requireFreshSuccess(t *testing.T, ctx context.Context, stream *liveTailStream, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		before := f.freshCount(stream).Load()
		status, body, err := f.freshRequest(ctx, stream, time.Second)
		if err == nil && status == http.StatusOK && bytes.Equal(body, liveTailFreshBody(stream.name)) && f.freshCount(stream).Load() == before+1 {
			return
		}
		f.requireProcess(t)
		if !liveProjectionSOCKSHandshake(stream.socksPort, 500*time.Millisecond) {
			t.Fatal("private tail SOCKS listener became unavailable during a positive connection check")
		}
		if time.Now().After(deadline) {
			t.Fatal("fresh authorized connection did not reach the private origin within its bounded window")
		}
		select {
		case <-ctx.Done():
			t.Fatal("fresh authorized connection exceeded the parent context")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (f *liveTailFixture) requireFreshRejection(t *testing.T, ctx context.Context, stream *liveTailStream) {
	t.Helper()
	before := f.freshCount(stream).Load()
	status, _, err := f.freshRequest(ctx, stream, 750*time.Millisecond)
	if err == nil || status != 0 || f.freshCount(stream).Load() != before {
		t.Fatal("a fresh revoked target connection reached the private origin")
	}
	f.requireProcess(t)
	if !liveProjectionSOCKSHandshake(stream.socksPort, 500*time.Millisecond) {
		t.Fatal("fresh target rejection occurred while its local SOCKS listener was unavailable")
	}
}

func (f *liveTailFixture) requireNoRevokedArrival(t *testing.T) {
	t.Helper()
	if f.hasRevokedArrival() {
		t.Fatal("a revoked target request reached the origin after its request returned")
	}
}

func (f *liveTailFixture) hasRevokedArrival() bool {
	return f.revoked && f.targetFresh.Load() != f.revokedFreshCount
}

func (f *liveTailFixture) freshRequest(ctx context.Context, stream *liveTailStream, timeout time.Duration) (int, []byte, error) {
	proxy := &url.URL{Scheme: "socks5", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(stream.socksPort))}
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true, DisableCompression: true}
	defer transport.CloseIdleConnections()
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, liveTailLogicalURL(f.pathPrefix+"/"+stream.name+"/fresh"), nil)
	if err != nil {
		return 0, nil, err
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, int64(len(liveTailFreshBody(stream.name)))+1))
	if readErr != nil {
		return response.StatusCode, body, readErr
	}
	return response.StatusCode, body, nil
}

func (f *liveTailFixture) freshCount(stream *liveTailStream) *atomic.Int64 {
	if stream == f.target {
		return &f.targetFresh
	}
	return &f.controlFresh
}

func liveTailFreshBody(name string) []byte {
	return []byte("chiral-tail-fresh-" + name)
}

func liveTailLogicalURL(path string) string {
	return "http://8.8.8.8:80" + path
}

func (f *liveTailFixture) handleOrigin(w http.ResponseWriter, r *http.Request) {
	host, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	remote := net.ParseIP(host)
	if splitErr != nil || remote == nil || !remote.IsLoopback() || r.Method != http.MethodGet ||
		(r.Host != "8.8.8.8" && r.Host != "8.8.8.8:80") || r.URL.RawQuery != "" {
		f.invalidOrigin.Add(1)
		http.Error(w, "invalid isolated request", http.StatusBadRequest)
		return
	}
	switch r.URL.Path {
	case f.target.path:
		f.handleEstablishedStream(w, r, f.target)
	case f.control.path:
		f.handleEstablishedStream(w, r, f.control)
	case f.pathPrefix + "/target/fresh":
		f.handleFresh(w, f.target)
	case f.pathPrefix + "/control/fresh":
		f.handleFresh(w, f.control)
	default:
		f.invalidOrigin.Add(1)
		http.NotFound(w, r)
	}
}

func (f *liveTailFixture) handleEstablishedStream(w http.ResponseWriter, r *http.Request, stream *liveTailStream) {
	if !stream.claimed.CompareAndSwap(false, true) {
		f.invalidOrigin.Add(1)
		http.Error(w, "duplicate isolated stream", http.StatusConflict)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		f.invalidOrigin.Add(1)
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	close(stream.established)
	for {
		select {
		case payload := <-stream.chunks:
			written, err := w.Write(payload)
			if err != nil || written != len(payload) {
				f.invalidOrigin.Add(1)
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		case <-f.ctx.Done():
			return
		}
	}
}

func (f *liveTailFixture) handleFresh(w http.ResponseWriter, stream *liveTailStream) {
	f.freshCount(stream).Add(1)
	body := liveTailFreshBody(stream.name)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if written, err := w.Write(body); err != nil || written != len(body) {
		f.invalidOrigin.Add(1)
	}
}

func (f *liveTailFixture) requireProcess(t *testing.T) {
	t.Helper()
	select {
	case <-f.cmdDone:
		t.Fatal("private tail client exited before deliberate fixture closure")
	default:
	}
}

func (f *liveTailFixture) Close(t *testing.T) {
	t.Helper()
	f.closeMu.Lock()
	defer f.closeMu.Unlock()
	if f.closed {
		return
	}
	f.closed = true

	processWasRunning := false
	if f.cmd != nil && f.cmd.Process != nil {
		select {
		case <-f.cmdDone:
			t.Error("private tail client exited before deliberate fixture closure")
		default:
			processWasRunning = true
		}
	}
	for _, stream := range []*liveTailStream{f.target, f.control} {
		if stream == nil {
			continue
		}
		if stream.requestCancel != nil {
			stream.requestCancel()
		}
		if stream.response != nil {
			_ = stream.response.Body.Close()
		}
		if stream.transport != nil {
			stream.transport.CloseIdleConnections()
		}
	}
	if f.cancel != nil {
		f.cancel()
	}
	if f.origin != nil {
		if err := f.origin.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Error("cannot close the private tail origin")
		}
		select {
		case err := <-f.originDone:
			if !errors.Is(err, http.ErrServerClosed) {
				t.Error("private tail origin exited unexpectedly")
			}
		case <-time.After(3 * time.Second):
			t.Error("private tail origin did not stop within three seconds")
		}
	}
	if f.cmd != nil && f.cmd.Process != nil && processWasRunning {
		if err := f.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Error("cannot terminate the private tail client")
		}
		select {
		case <-f.cmdDone:
			var exitErr *exec.ExitError
			if f.cmd.ProcessState == nil || (f.waitErr != nil && !errors.As(f.waitErr, &exitErr)) {
				t.Error("private tail client exit was not observed")
			} else {
				t.Log("fixture_reaped=true")
			}
		case <-time.After(5 * time.Second):
			t.Error("private tail client was not reaped within five seconds")
		}
	}
	if f.invalidOrigin.Load() != 0 {
		t.Error("private tail origin observed an invalid request or incomplete response")
	}
	if f.hasRevokedArrival() {
		t.Error("a revoked target request reached the origin before fixture closure")
	}
}

func TestLiveTailPayloadIsExactFixedSize(t *testing.T) {
	for _, name := range []string{"target", "control"} {
		payload := liveTailPayload(name, 1)
		if len(payload) != 64<<10 {
			t.Fatal("tail fixture payload is not exactly 64 KiB")
		}
	}
}

func TestLiveTailRevocationGuardDoesNotAdoptLateArrival(t *testing.T) {
	f := &liveTailFixture{revoked: true, revokedFreshCount: 1}
	f.targetFresh.Store(1)
	if f.hasRevokedArrival() {
		t.Fatal("the pre-revocation request was classified as a late arrival")
	}
	f.targetFresh.Add(1)
	for i := 0; i < 3; i++ {
		if !f.hasRevokedArrival() || f.revokedFreshCount != 1 {
			t.Fatal("a late arrival was adopted as a new negative-probe baseline")
		}
	}
}

func TestLiveTailClientConfigurationDisablesMux(t *testing.T) {
	for _, outbound := range []map[string]any{
		liveTailVLESSOutbound("target", "credential-one", 12345),
		liveTailVLESSOutbound("control", "credential-two", 12345),
	} {
		raw, err := json.Marshal(outbound["mux"])
		if err != nil || !bytes.Equal(raw, []byte(`{"enabled":false}`)) {
			t.Fatal("tail fixture client outbound mux must remain explicitly disabled")
		}
	}
}
