package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type resolverStub struct {
	addresses []netip.Addr
	err       error
	calls     atomic.Int64
	network   string
	host      string
}

func (r *resolverStub) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	r.calls.Add(1)
	r.network, r.host = network, host
	return append([]netip.Addr(nil), r.addresses...), r.err
}

type dialerFunc func(context.Context, string, string) (net.Conn, error)

func (f dialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}

func connectRequest(authority string) *http.Request {
	return &http.Request{
		Method:     http.MethodConnect,
		URL:        &url.URL{Host: authority},
		Host:       authority,
		RequestURI: authority,
		Header:     make(http.Header),
		Body:       http.NoBody,
	}
}

func testProxy(resolver ipResolver, dialer contextDialer, logger *log.Logger) *connectProxy {
	p := newConnectProxy(resolver, dialer, logger)
	p.resolveTimeout = time.Second
	p.dialTimeout = time.Second
	p.idleTimeout = time.Second
	p.maxLifetime = 2 * time.Second
	return p
}

func TestConnectDomainAllowsOnlyCanonicalGitHubAuthorities(t *testing.T) {
	for _, authority := range []string{
		"api.github.com:443",
		"github.com:443",
		"release-assets.githubusercontent.com:443",
	} {
		t.Run(authority, func(t *testing.T) {
			got, err := connectDomain(connectRequest(authority))
			if err != nil || got != strings.TrimSuffix(authority, ":443") {
				t.Fatalf("connectDomain(%q) = %q, %v", authority, got, err)
			}
		})
	}
}

func TestConnectDomainAcceptsNetHTTPAuthorityForm(t *testing.T) {
	raw := "CONNECT github.com:443 HTTP/1.1\r\nHost: github.com:443\r\n\r\n"
	request, err := http.ReadRequest(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("parse CONNECT request: %v", err)
	}
	defer request.Body.Close()
	if got, err := connectDomain(request); err != nil || got != "github.com" {
		t.Fatalf("parsed authority-form CONNECT = %q, %v", got, err)
	}
}

func TestConnectDomainRejectsAmbiguousOrUnlistedTargets(t *testing.T) {
	for _, authority := range []string{
		"GitHub.com:443",
		"github.com.:443",
		"github.com:0443",
		"github.com:80",
		"github.com",
		"api.github.com.evil.test:443",
		"objects.githubusercontent.com:443",
		"*.github.com:443",
		"user@github.com:443",
		"140.82.112.4:443",
		"[2606:50c0:8000::154]:443",
		"github.com:443/path",
		"github.com:443?query=secret",
		"%67ithub.com:443",
	} {
		t.Run(authority, func(t *testing.T) {
			if got, err := connectDomain(connectRequest(authority)); err == nil {
				t.Fatalf("accepted %q as %q", authority, got)
			}
		})
	}

	withUser := connectRequest("github.com:443")
	withUser.URL.User = url.User("operator")
	if got, err := connectDomain(withUser); err == nil {
		t.Fatalf("accepted URL user information as %q", got)
	}
	disagrees := connectRequest("github.com:443")
	disagrees.Host = "api.github.com:443"
	if got, err := connectDomain(disagrees); err == nil {
		t.Fatalf("accepted disagreeing authorities as %q", got)
	}
}

func TestValidateListenAddressRequiresPrivateCanonicalIPv4AndFixedPort(t *testing.T) {
	for _, address := range []string{"10.0.0.2:3128", "172.20.0.9:3128", "192.168.50.4:3128"} {
		if got, err := validateListenAddress(address); err != nil || got != address {
			t.Errorf("validateListenAddress(%q) = %q, %v", address, got, err)
		}
	}
	for _, address := range []string{
		"", "0.0.0.0:3128", "127.0.0.1:3128", "169.254.1.2:3128",
		"1.1.1.1:3128", "172.20.0.9:8080", "[fd00::2]:3128", "proxy:3128",
		"172.020.0.9:3128",
	} {
		if got, err := validateListenAddress(address); err == nil {
			t.Errorf("accepted listen address %q as %q", address, got)
		}
	}
}

func TestPublicIPv4RejectsSpecialAndIPv6Addresses(t *testing.T) {
	rejected := []string{
		"0.0.0.1", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.1.1",
		"172.16.0.1", "192.0.0.1", "192.0.2.1", "192.31.196.1",
		"192.52.193.1", "192.88.99.1", "192.168.1.1", "192.175.48.1",
		"198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1",
		"240.0.0.1", "255.255.255.255", "::1", "2001:4860:4860::8888",
		"::ffff:8.8.8.8",
	}
	for _, raw := range rejected {
		if publicIPv4(netip.MustParseAddr(raw)) {
			t.Errorf("accepted special or non-IPv4 address %s", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "140.82.112.4"} {
		if !publicIPv4(netip.MustParseAddr(raw)) {
			t.Errorf("rejected public IPv4 address %s", raw)
		}
	}
}

func TestMixedSpecialDNSAnswerFailsClosedWithoutDial(t *testing.T) {
	resolver := &resolverStub{addresses: []netip.Addr{
		netip.MustParseAddr("1.1.1.1"),
		netip.MustParseAddr("127.0.0.1"),
	}}
	var dialCalls atomic.Int64
	p := testProxy(resolver, dialerFunc(func(context.Context, string, string) (net.Conn, error) {
		dialCalls.Add(1)
		return nil, errors.New("must not dial")
	}), nil)
	recorder := httptest.NewRecorder()
	p.ServeHTTP(recorder, connectRequest("github.com:443"))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	if resolver.calls.Load() != 1 || dialCalls.Load() != 0 {
		t.Fatalf("resolver calls=%d dial calls=%d", resolver.calls.Load(), dialCalls.Load())
	}
}

func TestVerifiedAddressIsDialedLiterallyWithoutSecondDNSLookup(t *testing.T) {
	resolver := &resolverStub{addresses: []netip.Addr{
		netip.MustParseAddr("8.8.8.8"),
		netip.MustParseAddr("1.1.1.1"),
	}}
	var dialCalls atomic.Int64
	var dialNetwork, dialAddress string
	p := testProxy(resolver, dialerFunc(func(_ context.Context, network, address string) (net.Conn, error) {
		dialCalls.Add(1)
		dialNetwork, dialAddress = network, address
		return nil, errors.New("fixture stops before hijack")
	}), nil)
	recorder := httptest.NewRecorder()
	p.ServeHTTP(recorder, connectRequest("api.github.com:443"))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	if resolver.calls.Load() != 1 || resolver.network != "ip4" || resolver.host != "api.github.com" {
		t.Fatalf("unexpected resolver calls=%d network=%q host=%q", resolver.calls.Load(), resolver.network, resolver.host)
	}
	if dialCalls.Load() != 1 || dialNetwork != "tcp4" || dialAddress != "1.1.1.1:443" {
		t.Fatalf("dial calls=%d network=%q address=%q", dialCalls.Load(), dialNetwork, dialAddress)
	}
	host, _, err := net.SplitHostPort(dialAddress)
	if err != nil || net.ParseIP(host) == nil {
		t.Fatalf("dial target was not a fixed IP literal: %q", dialAddress)
	}
}

func TestRejectedRequestsDoNotResolveOrDial(t *testing.T) {
	resolver := &resolverStub{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	var dialCalls atomic.Int64
	var logs bytes.Buffer
	p := testProxy(resolver, dialerFunc(func(context.Context, string, string) (net.Conn, error) {
		dialCalls.Add(1)
		return nil, errors.New("must not dial")
	}), log.New(&logs, "", 0))

	ordinary := httptest.NewRequest(http.MethodGet, "http://github.com/", nil)
	ordinaryRecorder := httptest.NewRecorder()
	p.ServeHTTP(ordinaryRecorder, ordinary)
	if ordinaryRecorder.Code != http.StatusMethodNotAllowed || ordinaryRecorder.Header().Get("Allow") != http.MethodConnect {
		t.Fatalf("ordinary HTTP status=%d allow=%q", ordinaryRecorder.Code, ordinaryRecorder.Header().Get("Allow"))
	}
	deniedRecorder := httptest.NewRecorder()
	p.ServeHTTP(deniedRecorder, connectRequest("user@github.com:443"))
	if deniedRecorder.Code != http.StatusForbidden {
		t.Fatalf("userinfo CONNECT status=%d", deniedRecorder.Code)
	}
	unlistedRecorder := httptest.NewRecorder()
	p.ServeHTTP(unlistedRecorder, connectRequest("token-fragment.example:443"))
	if unlistedRecorder.Code != http.StatusForbidden {
		t.Fatalf("unlisted CONNECT status=%d", unlistedRecorder.Code)
	}
	if resolver.calls.Load() != 0 || dialCalls.Load() != 0 {
		t.Fatalf("rejected requests resolved=%d dialed=%d", resolver.calls.Load(), dialCalls.Load())
	}
	if strings.Contains(logs.String(), "user@") || strings.Contains(logs.String(), "token-fragment") ||
		!strings.Contains(logs.String(), "domain=invalid decision=reject reason=target_rejected bytes=0") {
		t.Fatalf("unsafe or incomplete audit log: %q", logs.String())
	}
}

func TestAuditDistinguishesFixedNetworkFailureReasons(t *testing.T) {
	tests := []struct {
		name     string
		resolver *resolverStub
		dialer   contextDialer
		reason   string
	}{
		{
			name:     "DNS failure",
			resolver: &resolverStub{err: errors.New("secret resolver diagnostic")},
			dialer: dialerFunc(func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("must not dial")
			}),
			reason: reasonDNSFailed,
		},
		{
			name:     "non-public DNS answer",
			resolver: &resolverStub{addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}},
			dialer: dialerFunc(func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("must not dial")
			}),
			reason: reasonDNSNonPublic,
		},
		{
			name:     "dial failure",
			resolver: &resolverStub{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}},
			dialer: dialerFunc(func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("secret dial diagnostic")
			}),
			reason: reasonDialFailed,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			proxy := testProxy(tc.resolver, tc.dialer, log.New(&logs, "", 0))
			recorder := httptest.NewRecorder()
			proxy.ServeHTTP(recorder, connectRequest("api.github.com:443"))
			want := "domain=api.github.com decision=reject reason=" + tc.reason + " bytes=0\n"
			if got := logs.String(); got != want {
				t.Fatalf("audit log = %q, want %q", got, want)
			}
			if strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), "127.0.0.1") ||
				strings.Contains(logs.String(), "1.1.1.1") {
				t.Fatalf("audit log leaked a diagnostic or resolved IP: %q", logs.String())
			}
		})
	}
}

func TestConcurrentLimitRejectsBeforeResolution(t *testing.T) {
	resolver := &resolverStub{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	p := testProxy(resolver, dialerFunc(func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("must not dial")
	}), nil)
	for i := 0; i < cap(p.limit); i++ {
		p.limit <- struct{}{}
	}
	recorder := httptest.NewRecorder()
	p.ServeHTTP(recorder, connectRequest("github.com:443"))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if resolver.calls.Load() != 0 {
		t.Fatalf("over-capacity request resolved %d times", resolver.calls.Load())
	}
}

type pipeHijacker struct {
	conn   net.Conn
	header http.Header
}

func (w *pipeHijacker) Header() http.Header { return w.header }
func (w *pipeHijacker) Write(p []byte) (int, error) {
	return w.conn.Write(p)
}
func (w *pipeHijacker) WriteHeader(int) {}
func (w *pipeHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

func TestConnectTunnelCopiesOpaqueBytesEndToEnd(t *testing.T) {
	client, proxyClient := net.Pipe()
	proxyUpstream, origin := net.Pipe()
	defer client.Close()
	defer origin.Close()
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := origin.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}

	resolver := &resolverStub{addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	var dialCalls atomic.Int64
	var logs bytes.Buffer
	p := testProxy(resolver, dialerFunc(func(_ context.Context, network, address string) (net.Conn, error) {
		dialCalls.Add(1)
		if network != "tcp4" || address != "1.1.1.1:443" {
			return nil, errors.New("unexpected fixed dial target")
		}
		return proxyUpstream, nil
	}), log.New(&logs, "", 0))

	originDone := make(chan error, 1)
	go func() {
		request := make([]byte, 4)
		if _, err := io.ReadFull(origin, request); err != nil {
			originDone <- err
			return
		}
		if string(request) != "ping" {
			originDone <- errors.New("origin received unexpected bytes")
			return
		}
		_, err := origin.Write([]byte("pong"))
		originDone <- err
		_ = origin.Close()
	}()

	handlerDone := make(chan struct{})
	go func() {
		p.ServeHTTP(&pipeHijacker{conn: proxyClient, header: make(http.Header)}, connectRequest("github.com:443"))
		close(handlerDone)
	}()
	reader := bufio.NewReader(client)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if status != "HTTP/1.1 200 Connection Established\r\n" {
		t.Fatalf("CONNECT status line = %q", status)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read CONNECT headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatalf("write opaque request bytes: %v", err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(reader, response); err != nil {
		t.Fatalf("read opaque response bytes: %v", err)
	}
	if string(response) != "pong" {
		t.Fatalf("opaque response = %q", response)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close client tunnel: %v", err)
	}
	if err := <-originDone; err != nil {
		t.Fatalf("origin exchange: %v", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("CONNECT handler did not clean up")
	}
	if dialCalls.Load() != 1 {
		t.Fatalf("dial calls = %d, want 1", dialCalls.Load())
	}
	if got := logs.String(); got != "domain=github.com decision=allow reason=complete bytes=8\n" {
		t.Fatalf("audit log = %q", got)
	}
}

func TestRelayClosesIdlePipesWithinBound(t *testing.T) {
	client, clientPeer := net.Pipe()
	upstream, upstreamPeer := net.Pipe()
	defer clientPeer.Close()
	defer upstreamPeer.Close()
	done := make(chan int64, 1)
	go func() { done <- relay(client, upstream, 25*time.Millisecond, time.Second) }()
	select {
	case transferred := <-done:
		if transferred != 0 {
			t.Fatalf("idle relay transferred %d bytes", transferred)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("idle relay did not close within its bound")
	}
}
