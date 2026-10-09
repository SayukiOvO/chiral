package main

import (
	"context"
	"crypto/tls"
	"net"
)

const (
	dnsTLSAddress    = "1.1.1.1:853"
	dnsTLSServerName = "one.one.one.one"
)

func dnsResolver(overTLS bool) *net.Resolver {
	if !overTLS {
		return net.DefaultResolver
	}
	return dnsOverTLSResolver(newDNSOverTLSDialer())
}

func newDNSOverTLSDialer() *tls.Dialer {
	return &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: resolveTimeout},
		Config: &tls.Config{
			ServerName: dnsTLSServerName,
			MinVersion: tls.VersionTLS12,
		},
	}
}

func dnsOverTLSResolver(dialer contextDialer) *net.Resolver {
	return &net.Resolver{
		PreferGo:     true,
		StrictErrors: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			// Ignore the system resolver address and UDP/TCP suggestion. A TLS
			// stream makes net.Resolver use length-prefixed DNS messages. The
			// fixed IP needs no bootstrap DNS or HTTP proxy; failures never
			// fall back to unencrypted DNS or disable certificate validation.
			return dialer.DialContext(ctx, "tcp4", dnsTLSAddress)
		},
	}
}
