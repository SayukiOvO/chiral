package main

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestDNSOverTLSIsOptInAndRequiresVerifiedTLS(t *testing.T) {
	if dnsResolver(false) != net.DefaultResolver {
		t.Fatal("default resolver changed without explicit opt-in")
	}
	resolver := dnsResolver(true)
	if resolver == net.DefaultResolver || !resolver.PreferGo || !resolver.StrictErrors || resolver.Dial == nil {
		t.Fatal("DNS-over-TLS resolver does not enforce the dedicated Go transport")
	}
	dialer := newDNSOverTLSDialer()
	config := dialer.Config
	if dialer.NetDialer == nil || dialer.NetDialer.Timeout != resolveTimeout || config == nil {
		t.Fatal("DNS-over-TLS handshake is not bounded")
	}
	if config.ServerName != "one.one.one.one" || config.MinVersion != tls.VersionTLS12 ||
		config.InsecureSkipVerify || config.RootCAs != nil || config.VerifyPeerCertificate != nil || config.VerifyConnection != nil {
		t.Fatal("DNS-over-TLS must use the fixed hostname and ordinary system-root certificate verification")
	}
}

func TestDNSOverTLSUsesFixedAddressAndPreservesContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	wantErr := errors.New("fixture transport failure")
	var calls int
	resolver := dnsOverTLSResolver(dialerFunc(func(got context.Context, network, address string) (net.Conn, error) {
		calls++
		if got != ctx || network != "tcp4" || address != "1.1.1.1:853" {
			t.Fatal("resolver changed the context or fixed TLS destination")
		}
		return nil, wantErr
	}))
	for _, target := range []struct{ network, address string }{
		{"udp", "127.0.0.1:53"}, {"tcp", "192.168.1.1:53"}, {"udp6", "[::1]:53"},
	} {
		conn, err := resolver.Dial(ctx, target.network, target.address)
		if conn != nil || !errors.Is(err, wantErr) {
			t.Fatal("resolver replaced the failed TLS result")
		}
	}
	cancel()
	if _, err := resolver.Dial(ctx, "udp", "127.0.0.1:53"); !errors.Is(err, wantErr) || ctx.Err() != context.Canceled {
		t.Fatal("canceled caller context was not preserved")
	}
	if calls != 4 {
		t.Fatalf("TLS dial calls = %d, want 4 without another resolver", calls)
	}
}

func TestDNSOverTLSResolutionFailureKeepsUsingTheFixedTransport(t *testing.T) {
	var calls atomic.Int64
	var unexpectedTarget atomic.Bool
	resolver := dnsOverTLSResolver(dialerFunc(func(_ context.Context, network, address string) (net.Conn, error) {
		calls.Add(1)
		if network != "tcp4" || address != "1.1.1.1:853" {
			unexpectedTarget.Store(true)
		}
		return nil, errors.New("fixture TLS verification or transport failure")
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	proxy := newConnectProxy(resolver, nil, nil)
	address, err := proxy.resolvePublicIPv4(ctx, "chiral-dot-fixture.invalid.")
	if !errors.Is(err, errDNSFailed) || address.IsValid() {
		t.Fatal("failed TLS resolution did not fail closed")
	}
	if calls.Load() == 0 || unexpectedTarget.Load() {
		t.Fatal("DNS resolution bypassed the fixed TLS transport")
	}
}

func TestDNSOverTLSFramedAnswersRetainPublicAddressGuards(t *testing.T) {
	for _, tc := range []struct {
		name      string
		addresses [][4]byte
		wantErr   error
	}{
		{"public", [][4]byte{{140, 82, 112, 4}}, nil},
		{"private", [][4]byte{{127, 0, 0, 1}}, errDNSNonPublic},
		{"mixed", [][4]byte{{140, 82, 112, 4}, {198, 18, 0, 1}}, errDNSNonPublic},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			exchanged := make(chan error, 8)
			resolver := dnsOverTLSResolver(dialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
				calls.Add(1)
				if network != "tcp4" || address != "1.1.1.1:853" {
					return nil, errors.New("unexpected DNS transport destination")
				}
				client, server := net.Pipe()
				deadline, ok := ctx.Deadline()
				if !ok {
					_ = client.Close()
					_ = server.Close()
					return nil, errors.New("DNS exchange has no deadline")
				}
				_ = server.SetDeadline(deadline)
				go func() {
					defer server.Close()
					exchanged <- replyToFramedDNS(server, tc.addresses)
				}()
				return client, nil
			}))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			proxy := newConnectProxy(resolver, nil, nil)
			address, err := proxy.resolvePublicIPv4(ctx, "chiral-dot-fixture.invalid.")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("framed DNS result = %v, want %v", err, tc.wantErr)
			}
			if err == nil && address != netip.MustParseAddr("140.82.112.4") {
				t.Fatal("framed DNS response returned an unexpected address")
			}
			if calls.Load() != 1 {
				t.Fatalf("DNS transport calls = %d, want 1", calls.Load())
			}
			select {
			case err := <-exchanged:
				if err != nil {
					t.Fatalf("DNS framing: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("DNS framing fixture did not finish")
			}
		})
	}
}

func replyToFramedDNS(conn net.Conn, addresses [][4]byte) error {
	var size [2]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return err
	}
	queryBytes := make([]byte, int(binary.BigEndian.Uint16(size[:])))
	if _, err := io.ReadFull(conn, queryBytes); err != nil {
		return err
	}
	var query dnsmessage.Message
	if err := query.Unpack(queryBytes); err != nil {
		return err
	}
	if query.Header.Response || len(query.Questions) != 1 || query.Questions[0].Name.String() != "chiral-dot-fixture.invalid." ||
		query.Questions[0].Type != dnsmessage.TypeA || query.Questions[0].Class != dnsmessage.ClassINET {
		return errors.New("unexpected framed DNS query")
	}
	response := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: query.Header.ID, Response: true, RecursionDesired: true, RecursionAvailable: true},
		Questions: query.Questions,
	}
	for _, address := range addresses {
		response.Answers = append(response.Answers, dnsmessage.Resource{
			Header: dnsmessage.ResourceHeader{Name: query.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
			Body:   &dnsmessage.AResource{A: address},
		})
	}
	packed, err := response.Pack()
	if err != nil {
		return err
	}
	_, err = conn.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(packed))), packed...))
	return err
}
