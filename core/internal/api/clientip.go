package api

import (
	"net"
	"net/http"
	"os"
	"strings"
)

// Working out who a request came from, for rate limiting.
//
// The whole value of a per-IP limit is that the attacker cannot pick their own
// bucket. `X-Forwarded-For` is attacker-controlled unless a proxy we trust
// wrote it, so honouring it unconditionally would turn every limit here into a
// no-op: one extra header per request and each attempt lands in a fresh bucket.
//
// So the header is read only when CHIRAL_TRUSTED_PROXY names the peer that
// delivered the request. A panel behind nginx or Caddy sets it; a panel exposed
// directly does not, and gets RemoteAddr.

// trustedProxies holds the CIDRs (or bare addresses) whose X-Forwarded-For we
// believe. Read once at startup: it is deployment configuration, and re-parsing
// it per request would be pure waste.
var trustedProxies = parseTrustedProxies(os.Getenv("CHIRAL_TRUSTED_PROXY"))

func parseTrustedProxies(raw string) []*net.IPNet {
	var out []*net.IPNet
	for _, field := range strings.Split(raw, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if _, network, err := net.ParseCIDR(field); err == nil {
			out = append(out, network)
			continue
		}
		// A bare address is the common case ("127.0.0.1"); treat it as a /32
		// or /128 so the same matching code handles both forms.
		if ip := net.ParseIP(field); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return out
}

func isTrustedProxy(ip net.IP) bool {
	for _, network := range trustedProxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP returns the address a request should be rate-limited against.
//
// The returned string is only ever used as a bucket key, so an unparseable
// RemoteAddr degrades to the raw value rather than to an empty key that every
// caller would share.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil || !isTrustedProxy(peer) {
		return host
	}

	// Behind a proxy we trust. Take the last entry rather than the first: a
	// client can prepend forged entries to the header, and the proxy appends
	// the address it actually saw, so the rightmost value is the one our own
	// infrastructure vouched for.
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded == "" {
		return host
	}
	parts := strings.Split(forwarded, ",")
	last := strings.TrimSpace(parts[len(parts)-1])
	if net.ParseIP(last) == nil {
		return host
	}
	return last
}
