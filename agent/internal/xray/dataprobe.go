package xray

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Proving that a customer can get online, rather than that the kernel is alive.
//
// The distinction is the whole reason this file exists. `xray api statsquery`
// answers whenever the process parsed its config and bound its API inbound —
// which a kernel with a broken transport does perfectly, while every subscriber
// on it is dark. An upgrade that breaks REALITY, or xhttp, or a flow, produces
// exactly that: a healthy-looking node carrying no traffic. Calling it ACTIVE
// and inviting an operator to roll it to the fleet is the worst outcome the
// canary exists to prevent.
//
// So the agent becomes a client. It runs a second, throwaway kernel — the same
// binary under test — configured with a local SOCKS inbound and the client
// outbound Core rendered from the very template a subscriber's config comes
// from, and it fetches a URL through it. Bytes come back, or they do not.

const (
	// DefaultProbeURL is fetched when Core names none. A 204 endpoint: small,
	// unauthenticated, and widely mirrored.
	DefaultProbeURL = "http://cp.cloudflare.com/generate_204"
	// probeStartTimeout bounds waiting for the throwaway kernel's SOCKS port.
	probeStartTimeout = 10 * time.Second
	// probeFetchTimeout bounds the request itself. Generous: it traverses the
	// node's own proxy stack and then the internet, on hardware that is busy
	// restarting a kernel.
	probeFetchTimeout = 15 * time.Second
)

// SetProbe records the client outbound and fetch target Core sent with a
// config. Empty outbound means the panel had nothing to render one from, which
// the caller reports rather than treating as a pass.
func (m *Manager) SetProbe(outboundJSON []byte, probeURL string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.probeOutbound = append([]byte(nil), outboundJSON...)
	m.probeURL = probeURL
}

func (m *Manager) probeSpec() ([]byte, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.probeURL
	if u == "" {
		u = DefaultProbeURL
	}
	return m.probeOutbound, u
}

// DataPath runs one end-to-end check with the given binary: start a throwaway
// kernel holding Core's client outbound, and fetch through it.
//
// Three outcomes, kept apart on purpose. nil means traffic flowed. ErrNoProbe
// means there was nothing to test with, which is not a failure and must never
// be rolled back on — it is the honest "nobody checked". Any other error means
// the fetch was attempted and did not work.
func (m *Manager) DataPath(ctx context.Context, bin string) error {
	outbound, target := m.probeSpec()
	if len(outbound) == 0 {
		return ErrNoProbe
	}
	if _, err := url.Parse(target); err != nil {
		return fmt.Errorf("the probe URL %q is not usable: %w", target, err)
	}

	port, err := freeLocalPort()
	if err != nil {
		return fmt.Errorf("no local port for the probe: %w", err)
	}
	cfg, err := probeConfig(outbound, port)
	if err != nil {
		return err
	}

	dir, err := os.MkdirTemp("", "chiral-probe-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "probe.json")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		return err
	}

	// Its own process, not the live one. The live kernel is serving customers
	// and must not gain a local SOCKS inbound for the sake of a health check —
	// an unauthenticated open proxy on the box, permanent, so that an upgrade
	// can be verified twice a month.
	cmd := exec.CommandContext(ctx, bin, "run", "-c", path, "-format", "json")
	var diag stderrTail
	diag.max = 20
	cmd.Stdout = io.Discard
	cmd.Stderr = &diag
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("the probe kernel would not start: %w", err)
	}
	// Reap it however this returns, including a fetch that hangs to its
	// deadline. A leaked kernel holds the port and poisons the next probe.
	var once sync.Once
	stop := func() {
		once.Do(func() {
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
			cmd.Wait()
		})
	}
	defer stop()

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if err := waitForPort(ctx, addr, probeStartTimeout); err != nil {
		msg := "the probe kernel never listened"
		if tail := diag.Tail(10); tail != "" {
			msg += ":\n" + tail
		}
		return fmt.Errorf("%s", msg)
	}

	if err := fetchThrough(ctx, addr, target); err != nil {
		if tail := diag.Tail(10); tail != "" {
			return fmt.Errorf("%w\n%s", err, tail)
		}
		return err
	}
	return nil
}

// ErrNoProbe means no client outbound was available to test with.
var ErrNoProbe = errNoProbe{}

type errNoProbe struct{}

func (errNoProbe) Error() string {
	return "the panel sent no client outbound to test with, so nothing checked whether traffic flows"
}

// probeConfig wraps Core's outbound in a throwaway client config.
//
// Everything is routed to the probe outbound by tag rather than by relying on
// it being first: a template that renders its own "freedom" fallback alongside
// the real outbound would otherwise send the probe straight out of the node's
// own uplink and report a glowing success for a proxy that never carried it.
func probeConfig(outbound []byte, port int) ([]byte, error) {
	var out map[string]any
	if err := json.Unmarshal(outbound, &out); err != nil {
		return nil, fmt.Errorf("the client outbound the panel sent is not a JSON object: %w", err)
	}
	tag, _ := out["tag"].(string)
	if tag == "" {
		tag = "probe-out"
		out["tag"] = tag
	}
	cfg := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag":      "probe-in",
			"listen":   "127.0.0.1",
			"port":     port,
			"protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false},
		}},
		"outbounds": []any{out},
		"routing": map[string]any{"rules": []any{map[string]any{
			"type":        "field",
			"inboundTag":  []any{"probe-in"},
			"outboundTag": tag,
		}}},
	}
	return json.Marshal(cfg)
}

// fetchThrough makes the request the whole check comes down to.
func fetchThrough(ctx context.Context, socksAddr, target string) error {
	proxy, err := url.Parse("socks5://" + socksAddr)
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout: probeFetchTimeout,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxy),
			// No connection reuse and no idle pool: this transport lives for
			// one request against a proxy that is about to be killed.
			DisableKeepAlives: true,
		},
	}
	ctx, cancel := context.WithTimeout(ctx, probeFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("nothing got through the node's own proxy: %w", trimProxyNoise(err))
	}
	defer resp.Body.Close()
	// Drain a little so the proxy sees a completed exchange rather than a
	// client that vanished mid-response.
	io.CopyN(io.Discard, resp.Body, 4096)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("traffic reached %s but it answered %s", target, resp.Status)
	}
	return nil
}

// trimProxyNoise shortens the deeply wrapped errors net/http produces, which
// otherwise bury the useful sentence under three layers of URL and dialer.
func trimProxyNoise(err error) error {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && len(s)-i < 120 {
		return fmt.Errorf("%s", strings.TrimSpace(s[i+2:]))
	}
	return err
}

// waitForPort blocks until something accepts on addr.
func waitForPort(ctx context.Context, addr string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return fmt.Errorf("nothing listened on %s within %s", addr, d)
}

// freeLocalPort asks the OS for an unused port and hands it straight back.
// Inherently racy, and the alternative — a fixed port — collides with whatever
// else the operator runs on the box, which is worse and permanent.
func freeLocalPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
