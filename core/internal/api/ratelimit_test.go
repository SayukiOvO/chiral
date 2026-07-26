package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestLimiterAllowsUpToBudgetThenRefuses(t *testing.T) {
	l := newLimiter(16)
	lim := limit{n: 3, window: time.Minute}
	now := time.Unix(1_700_000_000, 0)

	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("k", lim, now); !ok {
			t.Fatalf("attempt %d refused inside the budget", i+1)
		}
	}
	ok, retry := l.allow("k", lim, now)
	if ok {
		t.Fatal("fourth attempt allowed past a budget of three")
	}
	if retry <= 0 || retry > time.Minute {
		t.Fatalf("Retry-After %v is not inside the window", retry)
	}
}

func TestLimiterWindowResets(t *testing.T) {
	l := newLimiter(16)
	lim := limit{n: 1, window: time.Minute}
	now := time.Unix(1_700_000_000, 0)

	l.allow("k", lim, now)
	if ok, _ := l.allow("k", lim, now); ok {
		t.Fatal("second attempt allowed inside the window")
	}
	if ok, _ := l.allow("k", lim, now.Add(time.Minute+time.Second)); !ok {
		t.Fatal("attempt refused after the window elapsed")
	}
}

// Keys must not bleed: exhausting the login budget cannot cost the same
// address its subscription budget, and one address cannot exhaust another's.
func TestLimiterKeysAreIndependent(t *testing.T) {
	l := newLimiter(16)
	lim := limit{n: 1, window: time.Minute}
	now := time.Unix(1_700_000_000, 0)

	l.allow("login|10.0.0.1", lim, now)
	if ok, _ := l.allow("sub|10.0.0.1", lim, now); !ok {
		t.Fatal("one route's budget consumed another's")
	}
	if ok, _ := l.allow("login|10.0.0.2", lim, now); !ok {
		t.Fatal("one address consumed another's budget")
	}
}

// The bound is the point: without it, an attacker rotating source addresses
// grows the map without limit, which is the denial of service one level up.
func TestLimiterIsBounded(t *testing.T) {
	const max = 32
	l := newLimiter(max)
	lim := limit{n: 5, window: time.Minute}
	now := time.Unix(1_700_000_000, 0)

	for i := 0; i < max*10; i++ {
		l.allow("login|10.0.0."+strconv.Itoa(i), lim, now)
	}
	if len(l.buckets) > max {
		t.Fatalf("limiter holds %d buckets, above its cap of %d", len(l.buckets), max)
	}
}

func TestLimiterPruneDropsOnlyExpired(t *testing.T) {
	l := newLimiter(16)
	lim := limit{n: 5, window: time.Minute}
	now := time.Unix(1_700_000_000, 0)

	l.allow("old", lim, now)
	l.allow("new", limit{n: 5, window: time.Hour}, now)
	l.prune(now.Add(2 * time.Minute))

	if _, ok := l.buckets["old"]; ok {
		t.Error("expired bucket survived the prune")
	}
	if _, ok := l.buckets["new"]; !ok {
		t.Error("live bucket was pruned")
	}
}

// --- client IP ---

func req(remoteAddr, forwarded string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	r.RemoteAddr = remoteAddr
	if forwarded != "" {
		r.Header.Set("X-Forwarded-For", forwarded)
	}
	return r
}

// The header is attacker-controlled. Honouring it without a trusted proxy
// would give every request a bucket of its own and silently disable every
// limit in this file.
func TestClientIPIgnoresForwardedHeaderWithoutTrustedProxy(t *testing.T) {
	defer withTrustedProxies(t, nil)()
	if got := clientIP(req("203.0.113.9:41234", "1.2.3.4")); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q, want the peer address; the forged header won", got)
	}
}

func TestClientIPHonoursForwardedHeaderFromTrustedProxy(t *testing.T) {
	defer withTrustedProxies(t, parseTrustedProxies("10.0.0.0/8"))()
	if got := clientIP(req("10.1.2.3:5000", "198.51.100.7")); got != "198.51.100.7" {
		t.Fatalf("clientIP = %q, want the forwarded address", got)
	}
}

// A client can prepend entries; the proxy appends what it saw. The rightmost
// value is the only one our own infrastructure vouched for.
func TestClientIPTakesLastForwardedEntry(t *testing.T) {
	defer withTrustedProxies(t, parseTrustedProxies("10.0.0.0/8"))()
	got := clientIP(req("10.1.2.3:5000", "1.1.1.1, 2.2.2.2, 198.51.100.7"))
	if got != "198.51.100.7" {
		t.Fatalf("clientIP = %q, want the rightmost entry", got)
	}
}

func TestClientIPFallsBackOnGarbage(t *testing.T) {
	defer withTrustedProxies(t, parseTrustedProxies("10.0.0.0/8"))()
	if got := clientIP(req("10.1.2.3:5000", "not-an-ip")); got != "10.1.2.3" {
		t.Fatalf("clientIP = %q, want the peer address when the header is junk", got)
	}
}

func TestParseTrustedProxiesAcceptsBareAddresses(t *testing.T) {
	nets := parseTrustedProxies("127.0.0.1, 10.0.0.0/8 , , garbage")
	if len(nets) != 2 {
		t.Fatalf("parsed %d networks, want 2", len(nets))
	}
	defer withTrustedProxies(t, nets)()
	if got := clientIP(req("127.0.0.1:9000", "198.51.100.7")); got != "198.51.100.7" {
		t.Fatalf("clientIP = %q; a bare address should be trusted as a /32", got)
	}
}

func withTrustedProxies(t *testing.T, nets []*net.IPNet) func() {
	t.Helper()
	saved := trustedProxies
	trustedProxies = nets
	return func() { trustedProxies = saved }
}
