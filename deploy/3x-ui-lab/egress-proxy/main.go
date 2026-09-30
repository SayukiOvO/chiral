// Command egress-proxy is a deliberately narrow CONNECT proxy for the
// isolated 3x-ui upgrade experiment. It does not implement any 3x-ui API,
// cache downloads, or inspect TLS.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	proxyPort             = 3128
	maxTargetLength       = 260
	maxConcurrentTunnels  = 8
	resolveTimeout        = 10 * time.Second
	dialTimeout           = 15 * time.Second
	tunnelIdleTimeout     = 90 * time.Second
	maxTunnelLifetime     = 15 * time.Minute
	serverShutdownTimeout = 5 * time.Second
	maxHeaderBytes        = 8 << 10

	reasonComplete          = "complete"
	reasonMethodRejected    = "method_rejected"
	reasonTargetRejected    = "target_rejected"
	reasonCapacityRejected  = "capacity_rejected"
	reasonDNSFailed         = "dns_failed"
	reasonDNSNonPublic      = "dns_non_public"
	reasonDialFailed        = "dial_failed"
	reasonTunnelSetupFailed = "tunnel_setup_failed"
	reasonStartupFailed     = "startup_failed"
)

var (
	errDNSFailed    = errors.New("DNS resolution failed")
	errDNSNonPublic = errors.New("DNS resolution included a non-public address")
)

var allowedDomains = map[string]struct{}{
	"api.github.com":                       {},
	"github.com":                           {},
	"release-assets.githubusercontent.com": {},
}

// These ranges are not acceptable download destinations even when a trusted
// allow-listed name resolves to them. The list intentionally rejects whole
// special-purpose allocations rather than trying to preserve exceptional
// anycast addresses that this experiment never needs.
var deniedIPv4Prefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"),
	netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("192.175.48.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

type ipResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type contextDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type connectProxy struct {
	resolver       ipResolver
	dialer         contextDialer
	logger         *log.Logger
	limit          chan struct{}
	resolveTimeout time.Duration
	dialTimeout    time.Duration
	idleTimeout    time.Duration
	maxLifetime    time.Duration
}

func newConnectProxy(resolver ipResolver, dialer contextDialer, logger *log.Logger) *connectProxy {
	return &connectProxy{
		resolver:       resolver,
		dialer:         dialer,
		logger:         logger,
		limit:          make(chan struct{}, maxConcurrentTunnels),
		resolveTimeout: resolveTimeout,
		dialTimeout:    dialTimeout,
		idleTimeout:    tunnelIdleTimeout,
		maxLifetime:    maxTunnelLifetime,
	}
}

func (p *connectProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	domain := requestLogDomain(r)
	if r.Method != http.MethodConnect {
		w.Header().Set("Allow", http.MethodConnect)
		writeRejection(w, http.StatusMethodNotAllowed)
		p.audit(domain, "reject", reasonMethodRejected, 0)
		return
	}

	domain, err := connectDomain(r)
	if err != nil {
		writeRejection(w, http.StatusForbidden)
		p.audit(requestLogDomain(r), "reject", reasonTargetRejected, 0)
		return
	}
	select {
	case p.limit <- struct{}{}:
		defer func() { <-p.limit }()
	default:
		writeRejection(w, http.StatusServiceUnavailable)
		p.audit(domain, "reject", reasonCapacityRejected, 0)
		return
	}

	resolveCtx, cancelResolve := context.WithTimeout(r.Context(), p.resolveTimeout)
	address, err := p.resolvePublicIPv4(resolveCtx, domain)
	cancelResolve()
	if err != nil {
		writeRejection(w, http.StatusBadGateway)
		reason := reasonDNSFailed
		if errors.Is(err, errDNSNonPublic) {
			reason = reasonDNSNonPublic
		}
		p.audit(domain, "reject", reason, 0)
		return
	}

	dialCtx, cancelDial := context.WithTimeout(r.Context(), p.dialTimeout)
	upstream, err := p.dialer.DialContext(dialCtx, "tcp4", net.JoinHostPort(address.String(), "443"))
	cancelDial()
	if err != nil {
		writeRejection(w, http.StatusBadGateway)
		p.audit(domain, "reject", reasonDialFailed, 0)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		writeRejection(w, http.StatusInternalServerError)
		p.audit(domain, "reject", reasonTunnelSetupFailed, 0)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		p.audit(domain, "reject", reasonTunnelSetupFailed, 0)
		return
	}
	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		p.audit(domain, "reject", reasonTunnelSetupFailed, 0)
		return
	}
	if err := buffered.Flush(); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		p.audit(domain, "reject", reasonTunnelSetupFailed, 0)
		return
	}

	// Retain bytes already read past the CONNECT headers. The tunnel remains
	// opaque; this reader merely prevents coalesced TLS bytes being discarded.
	client = &bufferedConn{Conn: client, reader: buffered.Reader}
	transferred := relay(client, upstream, p.idleTimeout, p.maxLifetime)
	p.audit(domain, "allow", reasonComplete, transferred)
}

func writeRejection(w http.ResponseWriter, status int) {
	http.Error(w, http.StatusText(status), status)
}

func (p *connectProxy) audit(domain, decision, reason string, bytes int64) {
	if p.logger == nil {
		return
	}
	p.logger.Printf("domain=%s decision=%s reason=%s bytes=%d", domain, decision, reason, bytes)
}

func connectDomain(r *http.Request) (string, error) {
	if r == nil || r.Method != http.MethodConnect || len(r.RequestURI) == 0 || len(r.RequestURI) > maxTargetLength {
		return "", errors.New("request is not a canonical CONNECT")
	}
	if r.ContentLength > 0 || len(r.TransferEncoding) != 0 || r.URL == nil ||
		r.URL.Scheme != "" || r.URL.User != nil || r.URL.Path != "" ||
		r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.Fragment != "" || r.URL.Opaque != "" {
		return "", errors.New("CONNECT target is not an authority")
	}
	authority := r.RequestURI
	if r.Host != authority || r.URL.Host != authority {
		return "", errors.New("CONNECT authorities disagree")
	}
	host, port, err := net.SplitHostPort(authority)
	if err != nil || port != "443" || !validLowerDNSName(host) || net.ParseIP(host) != nil {
		return "", errors.New("CONNECT target is not an allowed HTTPS authority")
	}
	if authority != net.JoinHostPort(host, "443") {
		return "", errors.New("CONNECT authority is not canonical")
	}
	if _, ok := allowedDomains[host]; !ok {
		return "", errors.New("CONNECT domain is not allowed")
	}
	return host, nil
}

func requestLogDomain(r *http.Request) string {
	if r == nil || len(r.RequestURI) == 0 || len(r.RequestURI) > maxTargetLength {
		return "invalid"
	}
	host, port, err := net.SplitHostPort(r.RequestURI)
	if err != nil || port != "443" || !validLowerDNSName(host) || net.ParseIP(host) != nil {
		return "invalid"
	}
	if _, ok := allowedDomains[host]; !ok {
		return "invalid"
	}
	return host
}

func validLowerDNSName(host string) bool {
	if host == "" || len(host) > 253 || strings.HasSuffix(host, ".") || strings.ToLower(host) != host {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
				return false
			}
		}
	}
	return true
}

func (p *connectProxy) resolvePublicIPv4(ctx context.Context, domain string) (netip.Addr, error) {
	addresses, err := p.resolver.LookupNetIP(ctx, "ip4", domain)
	if err != nil || len(addresses) == 0 {
		return netip.Addr{}, errDNSFailed
	}
	seen := make(map[netip.Addr]struct{}, len(addresses))
	verified := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		if !publicIPv4(address) {
			// Reject the complete response when it mixes public and special
			// addresses. Selecting only the convenient answer would make DNS
			// rebinding races depend on resolver ordering.
			return netip.Addr{}, errDNSNonPublic
		}
		if _, duplicate := seen[address]; duplicate {
			continue
		}
		seen[address] = struct{}{}
		verified = append(verified, address)
	}
	if len(verified) == 0 {
		return netip.Addr{}, errDNSFailed
	}
	sort.Slice(verified, func(i, j int) bool { return verified[i].Less(verified[j]) })
	return verified[0], nil
}

func publicIPv4(address netip.Addr) bool {
	if !address.IsValid() || !address.Is4() || !address.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range deniedIPv4Prefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

type activityConn struct {
	net.Conn
	activity chan<- struct{}
}

func (c *activityConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.touch()
	}
	return n, err
}

func (c *activityConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.touch()
	}
	return n, err
}

func (c *activityConn) touch() {
	select {
	case c.activity <- struct{}{}:
	default:
	}
}

func relay(client, upstream net.Conn, idleTimeout, maxLifetime time.Duration) int64 {
	defer client.Close()
	defer upstream.Close()
	activity := make(chan struct{}, 1)
	clientTracked := &activityConn{Conn: client, activity: activity}
	upstreamTracked := &activityConn{Conn: upstream, activity: activity}
	bytesCopied := make(chan int64, 2)
	pump := func(dst, src net.Conn) {
		buf := make([]byte, 32<<10)
		n, _ := io.CopyBuffer(dst, src, buf)
		closeWrite(dst)
		bytesCopied <- n
	}
	go pump(upstreamTracked, clientTracked)
	go pump(clientTracked, upstreamTracked)

	idle := time.NewTimer(idleTimeout)
	maximum := time.NewTimer(maxLifetime)
	defer idle.Stop()
	defer maximum.Stop()
	var total int64
	remaining := 2
	closed := false
	for remaining > 0 {
		select {
		case n := <-bytesCopied:
			total += n
			remaining--
		case <-activity:
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(idleTimeout)
		case <-idle.C:
			if !closed {
				closed = true
				_ = client.Close()
				_ = upstream.Close()
			}
		case <-maximum.C:
			if !closed {
				closed = true
				_ = client.Close()
				_ = upstream.Close()
			}
		}
	}
	return total
}

func closeWrite(conn net.Conn) {
	switch wrapped := conn.(type) {
	case *activityConn:
		closeWrite(wrapped.Conn)
		return
	case *bufferedConn:
		closeWrite(wrapped.Conn)
		return
	}
	type closeWriter interface{ CloseWrite() error }
	if writer, ok := conn.(closeWriter); ok {
		_ = writer.CloseWrite()
	}
}

func validateListenAddress(raw string) (string, error) {
	address, err := netip.ParseAddrPort(raw)
	if err != nil || address.String() != raw || !address.Addr().Is4() ||
		!address.Addr().IsPrivate() || address.Port() != proxyPort {
		return "", errors.New("listen must be a canonical private IPv4 address on port 3128")
	}
	return address.String(), nil
}

func run(ctx context.Context, listenAddress string, logger *log.Logger) error {
	listenAddress, err := validateListenAddress(listenAddress)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", listenAddress)
	if err != nil {
		return errors.New("proxy listener could not start")
	}
	dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	proxy := newConnectProxy(net.DefaultResolver, dialer, logger)
	server := &http.Server{
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	err = server.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.New("proxy server stopped unexpectedly")
	}
	<-shutdownDone
	return nil
}

func main() {
	listenAddress := flag.String("listen", "", "canonical private IPv4 listen address on port 3128")
	flag.Parse()
	logger := log.New(os.Stdout, "", 0)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *listenAddress, logger); err != nil {
		logger.Printf("domain=proxy decision=reject reason=%s bytes=0", reasonStartupFailed)
		os.Exit(1)
	}
}

// Compile-time checks keep the production path on direct DNS and TCP dialing;
// neither type consults HTTP(S)_PROXY or another HTTP transport.
var (
	_ ipResolver    = (*net.Resolver)(nil)
	_ contextDialer = (*net.Dialer)(nil)
)
