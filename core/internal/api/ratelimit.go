package api

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// A fixed-window rate limiter for the handful of routes that answer before any
// credential has been checked.
//
// Those routes are the expensive ones: POST /api/login runs 210,000 rounds of
// PBKDF2 against a database handle pinned to a single connection
// (store.Open sets MaxOpenConns(1)), the same handle that serves config
// assembly, traffic ingest and the sweep. Unlimited, that is a CPU and
// write-lock denial of service anyone can fire without an account.
//
// Fixed windows are the deliberate choice over a token bucket: the counters are
// small, the reset is easy to explain in a Retry-After, and the worst case —
// twice the quota across a window boundary — is irrelevant at these limits.

// limit is one route's budget.
type limit struct {
	n      int
	window time.Duration
}

var (
	// Login is the one an ordinary person retries by hand; 10 in 5 minutes is
	// generous for typos and still cuts an online guessing attack to a crawl.
	limitLogin = limit{n: 10, window: 5 * time.Minute}
	// The MFA step is reached only with a valid challenge, which carries its
	// own attempt budget (store.MaxChallengeAttempts). This limit exists to
	// stop challenge-minting churn, not to guard the code.
	limitMFA = limit{n: 30, window: 5 * time.Minute}
	// Sending mail costs an outbound connection and someone else's inbox.
	limitEmailCode = limit{n: 5, window: 15 * time.Minute}
	// Subscriptions are polled by clients on a schedule; this is an abuse
	// ceiling, not a usage limit. Note that everyone behind one NAT shares
	// this bucket, which is why it is set well above what any single client
	// needs rather than tight against it.
	limitSubscription = limit{n: 60, window: time.Minute}
)

// bucket is one (route, key) counter.
type bucket struct {
	count int
	reset time.Time
}

// limiter is a process-local fixed-window counter.
//
// Process-local is a real limitation and worth naming: two Core replicas would
// each grant the full budget. Chiral is a single-binary panel with a SQLite
// file, so there is exactly one process by construction; if that ever stops
// being true this needs to move to the database or a shared cache.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	// max bounds memory. An attacker rotating source addresses would otherwise
	// grow this map without limit, which is the same denial of service one
	// level up.
	max int
}

func newLimiter(max int) *limiter {
	return &limiter{buckets: make(map[string]*bucket), max: max}
}

// allow records an attempt and reports whether it fits the budget, along with
// how long until the window resets.
func (l *limiter) allow(key string, lim limit, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok || now.After(b.reset) {
		if len(l.buckets) >= l.max {
			l.evict(now)
		}
		l.buckets[key] = &bucket{count: 1, reset: now.Add(lim.window)}
		return true, 0
	}
	if b.count >= lim.n {
		// Measured against the caller's clock, not the wall clock: `now` is a
		// parameter so this is testable, and reaching past it for time.Now()
		// would quietly make the two disagree.
		return false, b.reset.Sub(now)
	}
	b.count++
	return true, 0
}

// evict drops expired buckets, and if that frees nothing, drops the one
// closest to resetting.
//
// Dropping a live bucket forgives an attacker some attempts, which is the
// right trade: the alternative is unbounded memory, and reaching this path at
// all means the map is already under a spraying attack that per-IP counting
// cannot answer anyway.
func (l *limiter) evict(now time.Time) {
	for key, b := range l.buckets {
		if now.After(b.reset) {
			delete(l.buckets, key)
		}
	}
	if len(l.buckets) < l.max {
		return
	}
	var oldestKey string
	var oldest time.Time
	for key, b := range l.buckets {
		if oldestKey == "" || b.reset.Before(oldest) {
			oldestKey, oldest = key, b.reset
		}
	}
	delete(l.buckets, oldestKey)
}

// prune drops expired buckets. Called from the sweep so an idle panel does not
// hold yesterday's addresses in memory.
func (l *limiter) prune(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, b := range l.buckets {
		if now.After(b.reset) {
			delete(l.buckets, key)
		}
	}
}

// throttle wraps a handler with a per-client-IP budget.
//
// The name is part of the key so a person who just mistyped their password
// still has their subscription budget intact.
func (s *Server) throttle(name string, lim limit, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := name + "|" + clientIP(r)
		if ok, retry := s.limiter.allow(key, lim, time.Now()); !ok {
			// Seconds, rounded up: a Retry-After of 0 invites an immediate
			// retry that is guaranteed to fail.
			seconds := int(retry.Seconds()) + 1
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			writeErr(w, http.StatusTooManyRequests, "too many attempts; try again later")
			return
		}
		h(w, r)
	}
}
