package xray

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Online tracking, against a real kernel.
//
// None of what follows can be established from the config alone. Measured on
// Xray 26.3.27, a config missing policy.levels."0".statsUserOnline passes
// `xray -test`, starts, proxies traffic and counts bytes — while every online
// query answers NotFound or `{}` with exit 0. And the semantics of what IS
// reported are equally unguessable: the count is of distinct source addresses
// rather than sessions, and loopback sources are omitted entirely.
//
// So this test runs two kernels and puts real bytes through them.

// liveOnlineXray brings up a server kernel with online stats enabled, listening
// on a non-loopback address, plus a client kernel that tunnels to it.
//
// The non-loopback part is essential and is why this test can skip: Xray does
// not record loopback source addresses at all, so the same setup on 127.0.0.1
// reports zero addresses and looks exactly like a broken feature.
// liveDeadline bounds the waits in these tests. They poll for a condition and
// return the moment it holds, so this only bounds failure — and a value tight
// enough to trip on a loaded machine turns a passing test into an intermittent
// one, which is worse than a slow failure.
const liveDeadline = 30 * time.Second

func liveOnlineXray(t *testing.T) (*Manager, string, int) {
	t.Helper()
	bin := testBin(t)
	host := nonLoopbackIPv4(t)

	apiPort, vlessPort, entryPort := freePort(t), freePort(t), freePort(t)
	originAddr := holdOpenOrigin(t)
	originHost, originPortStr, err := net.SplitHostPort(originAddr)
	if err != nil {
		t.Fatal(err)
	}
	originPort := mustAtoi(t, originPortStr)

	const uuid = "1f1e4e2c-1f6a-4a1a-9a4e-2b7c8d9e0f11"
	const email = "probe.u1@p1.n1"

	serverCfg := map[string]any{
		"log":   map[string]any{"loglevel": "warning"},
		"api":   map[string]any{"tag": "api", "services": []string{"HandlerService", "StatsService", "RoutingService"}},
		"stats": map[string]any{},
		"policy": map[string]any{"levels": map[string]any{"0": map[string]any{
			"statsUserUplink": true, "statsUserDownlink": true, "statsUserOnline": true,
		}}},
		"inbounds": []any{
			map[string]any{"tag": "chiral-api", "listen": "127.0.0.1", "port": apiPort,
				"protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1"}},
			map[string]any{"tag": "vless-in", "listen": host, "port": vlessPort, "protocol": "vless",
				"settings": map[string]any{
					"clients":    []any{map[string]any{"id": uuid, "email": email}},
					"decryption": "none",
				}},
		},
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}},
		"routing": map[string]any{"rules": []any{
			map[string]any{"type": "field", "inboundTag": []string{"chiral-api"}, "outboundTag": "api"},
		}},
	}

	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "config.json"), serverCfg)
	m := New(bin, dir, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})), nil)
	if err := m.Start(); err != nil {
		t.Fatalf("starting the server kernel: %v", err)
	}
	t.Cleanup(m.Stop)
	serverAddr := net.JoinHostPort(host, itoa(vlessPort))
	if err := waitForPort(context.Background(), serverAddr, liveDeadline); err != nil {
		t.Fatalf("the server kernel's VLESS inbound never became reachable: %v", err)
	}

	// A dokodemo-door entry rather than SOCKS: dialling it is a plain TCP
	// connect, with no client-side handshake to get wrong.
	clientCfg := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag": "entry", "listen": "127.0.0.1", "port": entryPort, "protocol": "dokodemo-door",
			"settings": map[string]any{"address": originHost, "port": originPort, "network": "tcp"},
		}},
		"outbounds": []any{map[string]any{
			"protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{
				"address": host, "port": vlessPort,
				"users": []any{map[string]any{"id": uuid, "encryption": "none"}},
			}}},
			"streamSettings": map[string]any{"network": "tcp"},
		}},
	}
	clientDir := t.TempDir()
	clientPath := filepath.Join(clientDir, "config.json")
	writeJSON(t, clientPath, clientCfg)
	client := exec.Command(bin, "run", "-config", clientPath)
	if err := client.Start(); err != nil {
		t.Fatalf("starting the client kernel: %v", err)
	}
	t.Cleanup(func() {
		client.Process.Kill()
		client.Wait()
	})

	// Generous on purpose. These deadlines only ever elapse when something is
	// wrong, so a longer one costs nothing on a healthy run and stops the test
	// failing for being run on a busy machine — which is when the whole suite
	// runs, alongside every other package.
	apiReady := false
	deadline := time.Now().Add(liveDeadline)
	for time.Now().Before(deadline) {
		if _, err := m.Stats(context.Background()); err == nil {
			apiReady = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !apiReady {
		t.Fatalf("the server kernel's API never became reachable within %s", liveDeadline)
	}
	entryAddr := net.JoinHostPort("127.0.0.1", itoa(entryPort))
	if err := waitForPort(context.Background(), entryAddr, liveDeadline); err != nil {
		t.Fatalf("the client kernel's entry never became reachable: %v", err)
	}
	return m, email, entryPort
}

func TestOnlineUsersReportsARealConnection(t *testing.T) {
	m, email, entryPort := liveOnlineXray(t)

	conn := dialThrough(t, entryPort)
	defer conn.Close()

	users := waitForOnline(t, m, email)
	if len(users[0].IPs) != 1 {
		t.Fatalf("addresses = %v, want exactly one", users[0].IPs)
	}
	for ip, at := range users[0].IPs {
		if net.ParseIP(ip) == nil {
			t.Errorf("reported address %q does not parse", ip)
		}
		if at <= 0 {
			t.Errorf("last-seen for %s is %d, want a unix timestamp", ip, at)
		}
	}
}

// The count is of distinct ADDRESSES, not sessions. This is what makes
// device_limit meaningful — and what makes it a poor proxy for "devices", since
// a household behind one NAT is indistinguishable from one machine.
func TestConcurrentSessionsFromOneAddressCountOnce(t *testing.T) {
	m, email, entryPort := liveOnlineXray(t)

	for i := 0; i < 3; i++ {
		conn := dialThrough(t, entryPort)
		defer conn.Close()
	}

	users := waitForOnline(t, m, email)
	if len(users[0].IPs) != 1 {
		t.Fatalf("three concurrent sessions from one address reported %d addresses: %v",
			len(users[0].IPs), users[0].IPs)
	}
}

// Everything downstream treats "complete" as permission to act on the report,
// so a healthy round must actually set it.
func TestCompleteRoundIsReportedComplete(t *testing.T) {
	m, email, entryPort := liveOnlineXray(t)
	conn := dialThrough(t, entryPort)
	defer conn.Close()
	waitForOnline(t, m, email)

	_, complete, err := m.OnlineUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("a round that enumerated everything was reported incomplete")
	}
}

// --- helpers ---

func waitForOnline(t *testing.T, m *Manager, email string) []OnlineUser {
	t.Helper()
	deadline := time.Now().Add(liveDeadline)
	for time.Now().Before(deadline) {
		users, _, err := m.OnlineUsers(context.Background())
		if err != nil {
			t.Fatalf("reading online users: %v", err)
		}
		for _, u := range users {
			if u.Email == email && len(u.IPs) > 0 {
				return []OnlineUser{u}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s never showed up as online within %s", email, liveDeadline)
	return nil
}

// dialThrough opens a connection through the tunnel and sends a byte, so the
// server kernel actually admits the user rather than just accepting TCP.
func dialThrough(t *testing.T, entryPort int) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(entryPort)), 5*time.Second)
	if err != nil {
		t.Fatalf("dialling the tunnel: %v", err)
	}
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatalf("writing through the tunnel: %v", err)
	}
	return conn
}

// holdOpenOrigin is a TCP server that accepts and then does nothing, keeping
// connections established for the duration of the test.
func holdOpenOrigin(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	// Accepted connections are collected rather than registered with
	// t.Cleanup from the accept goroutine: Cleanup panics if it runs after
	// the test finishes, and this goroutine outlives the test body.
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()

	t.Cleanup(func() {
		l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	return l.Addr().String()
}

// nonLoopbackIPv4 finds an address Xray will actually account for.
func nonLoopbackIPv4(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("cannot enumerate interfaces: %v", err)
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.To4() == nil || n.IP.IsLinkLocalUnicast() {
			continue
		}
		return n.IP.String()
	}
	t.Skip("no non-loopback IPv4 address; Xray does not account for loopback sources, " +
		"so online tracking cannot be exercised here")
	return ""
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("%q is not a number", s)
		}
		n = n*10 + int(c-'0')
	}
	return n
}
