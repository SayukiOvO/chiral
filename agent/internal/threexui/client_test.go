package threexui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "top-secret-api-token"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestStatusUsesNormalisedBasePathAndBearerToken(t *testing.T) {
	var gotPath, gotAuthorization, gotAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthorization = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"success": true,
			"msg": "",
			"obj": {
				"cpu": 12.5,
				"cpuCores": 4,
				"logicalPro": 8,
				"cpuSpeedMhz": 3200.5,
				"mem": {"current": 1024, "total": 4096},
				"swap": {"current": 10, "total": 20},
				"disk": {"current": 30, "total": 40},
				"diskIO": {"read": 50, "write": 60},
				"xray": {"state": "running", "errorMsg": "", "version": "25.8.3"},
				"panelVersion": "v3.5.0",
				"panelGuid": "panel-1",
				"uptime": 70,
				"loads": [0.1, 0.2, 0.3],
				"tcpCount": 80,
				"udpCount": 90,
				"netIO": {"up": 100, "down": 110, "pktUp": 120, "pktDown": 130},
				"netTraffic": {"sent": 140, "recv": 150, "pktSent": 160, "pktRecv": 170},
				"publicIP": {"ipv4": "203.0.113.10", "ipv6": "2001:db8::10"},
				"appStats": {"threads": 18, "mem": 190, "uptime": 200}
			}
		}`)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL + "/node/", Token: testToken})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	if gotPath != "/node/panel/api/server/status" {
		t.Errorf("request path = %q", gotPath)
	}
	if gotAuthorization != "Bearer "+testToken {
		t.Errorf("Authorization = %q", gotAuthorization)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q", gotAccept)
	}
	if status.PanelVersion != "v3.5.0" || status.Xray.State != "running" || status.Xray.Version != "25.8.3" {
		t.Errorf("Status() = %#v", status)
	}
	if status.Memory.Current != 1024 || status.NetIO.PacketDown != 130 || status.AppStats.Threads != 18 {
		t.Errorf("Status() nested fields = %#v", status)
	}
}

func TestNewRejectsUnsafeOrAmbiguousConfiguration(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "missing URL", cfg: Config{Token: testToken}},
		{name: "surrounding URL whitespace", cfg: Config{BaseURL: " http://127.0.0.1:2053", Token: testToken}},
		{name: "relative URL", cfg: Config{BaseURL: "127.0.0.1:2053", Token: testToken}},
		{name: "unsupported scheme", cfg: Config{BaseURL: "ftp://127.0.0.1", Token: testToken}},
		{name: "userinfo", cfg: Config{BaseURL: "http://user:pass@127.0.0.1", Token: testToken}},
		{name: "query", cfg: Config{BaseURL: "http://127.0.0.1?token=no", Token: testToken}},
		{name: "fragment", cfg: Config{BaseURL: "http://127.0.0.1#fragment", Token: testToken}},
		{name: "encoded path", cfg: Config{BaseURL: "http://127.0.0.1/%2e%2e/admin", Token: testToken}},
		{name: "empty path segment", cfg: Config{BaseURL: "http://127.0.0.1/base//nested", Token: testToken}},
		{name: "dot path segment", cfg: Config{BaseURL: "http://127.0.0.1/base/../nested", Token: testToken}},
		{name: "public IP by default", cfg: Config{BaseURL: "https://8.8.8.8", Token: testToken}},
		{name: "private IP by default", cfg: Config{BaseURL: "https://10.0.0.2", Token: testToken}},
		{name: "DNS name by default", cfg: Config{BaseURL: "https://panel.example.com", Token: testToken}},
		{name: "plaintext non-loopback even when explicit", cfg: Config{BaseURL: "http://10.0.0.2", Token: testToken, AllowPublic: true}},
		{name: "missing token", cfg: Config{BaseURL: "http://127.0.0.1"}},
		{name: "token whitespace", cfg: Config{BaseURL: "http://127.0.0.1", Token: " " + testToken}},
		{name: "token control", cfg: Config{BaseURL: "http://127.0.0.1", Token: testToken + "\n"}},
		{name: "negative timeout", cfg: Config{BaseURL: "http://127.0.0.1", Token: testToken, Timeout: -time.Second}},
		{name: "negative limit", cfg: Config{BaseURL: "http://127.0.0.1", Token: testToken, MaxResponseBytes: -1}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := New(test.cfg)
			if err == nil {
				t.Fatalf("New() = %#v, want error", client)
			}
			assertNoToken(t, err)
		})
	}
}

func TestNewAllowsExplicitPublicHost(t *testing.T) {
	client, err := New(Config{
		BaseURL:     "https://panel.example.com/custom/",
		Token:       testToken,
		AllowPublic: true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := client.base.String(); got != "https://panel.example.com/custom" {
		t.Errorf("normalised base URL = %q", got)
	}
}

func TestNewDoesNotMutateSuppliedHTTPClient(t *testing.T) {
	originalRedirect := func(_ *http.Request, _ []*http.Request) error { return nil }
	original := &http.Client{Timeout: time.Minute, CheckRedirect: originalRedirect}
	client, err := New(Config{
		BaseURL:    "http://127.0.0.1",
		Token:      testToken,
		Timeout:    time.Second,
		HTTPClient: original,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if original.Timeout != time.Minute {
		t.Errorf("supplied HTTP client timeout changed to %v", original.Timeout)
	}
	if original.CheckRedirect == nil {
		t.Fatal("supplied HTTP client redirect policy was cleared")
	}
	if client.http == original {
		t.Fatal("New() retained and mutated the supplied HTTP client")
	}
	if client.http.Timeout != time.Second {
		t.Errorf("client timeout = %v", client.http.Timeout)
	}
}

func TestNewDefaultTransportDoesNotUseEnvironmentProxy(t *testing.T) {
	client, err := New(Config{BaseURL: "http://127.0.0.1", Token: testToken})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	transport, ok := client.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("default transport = %T", client.http.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("default transport can send the API token through an environment proxy")
	}
}

func TestStatusDiagnosesResponsesWithoutLeakingToken(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		maxBytes   int64
		want       string
		checkType  func(*testing.T, error)
	}{
		{
			name:       "non-2xx",
			statusCode: http.StatusUnauthorized,
			body:       `request carried ` + testToken,
			want:       "HTTP 401 Unauthorized",
			checkType: func(t *testing.T, err error) {
				var statusErr *HTTPStatusError
				if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusUnauthorized {
					t.Errorf("error = %T %v", err, err)
				}
			},
		},
		{
			name:       "success false",
			statusCode: http.StatusOK,
			body:       `{"success":false,"msg":"bad token ` + testToken + `","obj":null}`,
			want:       "bad token [REDACTED]",
			checkType: func(t *testing.T, err error) {
				var apiErr *APIError
				if !errors.As(err, &apiErr) {
					t.Errorf("error = %T %v", err, err)
				}
			},
		},
		{
			name:       "invalid JSON",
			statusCode: http.StatusOK,
			body:       `{"success":true,"obj":` + testToken,
			want:       "decoding the 3x-ui response envelope",
			checkType: func(t *testing.T, err error) {
				var contractErr *ContractError
				if !errors.As(err, &contractErr) {
					t.Errorf("error = %T %v", err, err)
				}
			},
		},
		{
			name:       "missing success",
			statusCode: http.StatusOK,
			body:       `{"obj":{}}`,
			want:       "no boolean success field",
		},
		{
			name:       "missing object",
			statusCode: http.StatusOK,
			body:       `{"success":true,"obj":null}`,
			want:       "has no object",
		},
		{
			name:       "missing panel version",
			statusCode: http.StatusOK,
			body:       `{"success":true,"obj":{}}`,
			want:       "has no panelVersion",
		},
		{
			name:       "missing Xray state",
			statusCode: http.StatusOK,
			body:       `{"success":true,"obj":{"panelVersion":"v3.7.0"}}`,
			want:       "has no xray.state",
		},
		{
			name:       "invalid object",
			statusCode: http.StatusOK,
			body:       `{"success":true,"obj":{"cpu":"` + testToken + `"}}`,
			want:       "decoding the 3x-ui response object",
		},
		{
			name:       "oversized",
			statusCode: http.StatusOK,
			body:       strings.Repeat("x", 65),
			maxBytes:   64,
			want:       "64-byte limit",
			checkType: func(t *testing.T, err error) {
				var sizeErr *ResponseTooLargeError
				if !errors.As(err, &sizeErr) || sizeErr.Limit != 64 {
					t.Errorf("error = %T %v", err, err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.statusCode)
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()

			client, err := New(Config{
				BaseURL:          server.URL,
				Token:            testToken,
				MaxResponseBytes: test.maxBytes,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			_, err = client.Status(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Status() error = %v, want substring %q", err, test.want)
			}
			assertNoToken(t, err)
			if test.checkType != nil {
				test.checkType(t, err)
			}
		})
	}
}

func TestRemoteDiagnosticIsBoundedAndCannotForgeLogLines(t *testing.T) {
	message := strings.Repeat("x", 4<<10) + "\nforged=entry\t" + testToken
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"success":false,"msg":%q,"obj":null}`, message)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Status(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Status() error = %T %v", err, err)
	}
	if len(apiErr.Message) > maxDiagnosticBytes {
		t.Fatalf("remote diagnostic has %d bytes, limit %d", len(apiErr.Message), maxDiagnosticBytes)
	}
	if strings.ContainsAny(err.Error(), "\r\n\t") {
		t.Fatalf("remote diagnostic retained log controls: %q", err)
	}
	assertNoToken(t, err)
}

func TestStatusTimeoutIsDiagnostic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL: server.URL,
		Token:   testToken,
		Timeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = client.Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Status() error = %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Status() error does not preserve deadline: %v", err)
	}
	assertNoToken(t, err)
}

func TestTransportErrorCannotExposeToken(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("transport echoed %s", request.Header.Get("Authorization"))
	})}
	client, err := New(Config{
		BaseURL:    "http://127.0.0.1",
		Token:      testToken,
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = client.Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("Status() error = %v", err)
	}
	assertNoToken(t, err)
	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		t.Fatalf("transport error can be unwrapped and leak its text: %v", unwrapped)
	}
}

func TestRedirectIsNotFollowedOrReauthenticated(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests.Add(1)
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("origin Authorization = %q", got)
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	client, err := New(Config{BaseURL: origin.URL, Token: testToken})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = client.Status(context.Background())
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("Status() error = %T %v", err, err)
	}
	if got := targetRequests.Load(); got != 0 {
		t.Fatalf("redirect target received %d requests", got)
	}
	assertNoToken(t, err)
}

func assertNoToken(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), testToken) {
		t.Fatalf("error leaked API token: %v", err)
	}
}
