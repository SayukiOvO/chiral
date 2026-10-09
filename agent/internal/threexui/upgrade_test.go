package threexui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestXrayVersionsReturnsExactReleaseList(t *testing.T) {
	want := []string{"v26.9.9", "26.8.3", "v26.6.27"}
	var calls atomic.Int32
	client, _ := mutationClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/base/panel/api/server/getXrayVersion" || r.URL.RawQuery != "" {
			t.Error("unexpected Xray versions request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": want})
	})
	got, err := client.XrayVersions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) || calls.Load() != 1 {
		t.Fatalf("release tags = %v; calls = %d", got, calls.Load())
	}
}

func TestXrayVersionsRejectsInvalidContractWithoutPartialList(t *testing.T) {
	const secret = "private-provider-value"
	tooMany := make([]string, maxXrayVersions+1)
	for i := range tooMany {
		tooMany[i] = "v26.9.9"
	}
	largeList, _ := json.Marshal(tooMany)
	cases := []struct {
		name string
		body string
	}{
		{"missing object", `{"success":true}`},
		{"null object", `{"success":true,"obj":null}`},
		{"current version string", `{"success":true,"obj":"v26.9.9"}`},
		{"wrong element type", `{"success":true,"obj":["v26.9.9",1]}`},
		{"invalid element", `{"success":true,"obj":["v26.9.9","` + secret + `"]}`},
		{"unsafe tag", `{"success":true,"obj":["v26.9.9/../stopXrayService"]}`},
		{"too many tags", `{"success":true,"obj":` + string(largeList) + `}`},
		{"false envelope", `{"success":false,"msg":"` + secret + " " + testToken + `"}`},
		{"malformed envelope", `{"success":true,"obj":` + secret},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := mutationClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, tt.body) })
			versions, err := client.XrayVersions(context.Background())
			if err == nil || versions != nil {
				t.Fatal("invalid version list was accepted or exposed partial entries")
			}
			assertUpgradeErrorRedacted(t, err, secret)
		})
	}
}

func TestXrayVersionsAcceptsExplicitEmptyList(t *testing.T) {
	client, _ := mutationClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":true,"obj":[]}`)
	})
	versions, err := client.XrayVersions(context.Background())
	if err != nil || versions == nil || len(versions) != 0 {
		t.Fatalf("explicit empty list = %v, %v", versions, err)
	}
}

func TestInstallXrayUsesExactTagAndOnlyOnePost(t *testing.T) {
	for _, tag := range []string{"v26.9.9", "26.9.9"} {
		t.Run(tag, func(t *testing.T) {
			var calls atomic.Int32
			client, _ := mutationClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/base/panel/api/server/installXray/"+tag ||
					r.URL.RawQuery != "" || r.URL.Fragment != "" || r.Header.Get("Accept") != "application/json" {
					t.Error("unexpected update request")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || len(body) != 0 {
					t.Error("Xray update must not send a request payload")
				}
				fmt.Fprint(w, `{"success":true,"msg":"ignored private response","obj":null}`)
			})
			if err := client.InstallXray(context.Background(), tag); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatalf("calls = %d; expected no hidden lookup or retry", calls.Load())
			}
		})
	}
}

func TestInstallXrayRejectsUnsafeVersionsBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	client, _ := mutationClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"success":true}`)
	})
	for _, tag := range []string{
		"", "v", "latest", "main", ".", "..", "../", "v26.9.9/../restartXrayService",
		"v26.9.9?key=value", "v26.9.9#fragment", "v26.9.9%2f..", "v26.9.9\\extra",
		" v26.9.9", "v26.9.9 ", "v26.9.9\n", "v26.9.9\x00", "v26.９.9",
		"vv26.9.9", "V26.9.9", "v26.9", "v26.9.9.1", "v26..9", "v26.9.-1",
		"v26.9.9-beta", "v26.9.9+build", strings.Repeat("1", maxXrayVersionBytes) + ".9.9",
	} {
		err := client.InstallXray(context.Background(), tag)
		assertInstallOutcome(t, err, XrayInstallNotStarted)
		assertUpgradeErrorRedacted(t, err, "key=value")
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid versions reached HTTP: %d", calls.Load())
	}
}

func TestInstallXrayOnlyEnvelopeFailureIsTerminal(t *testing.T) {
	const secret = "private-provider-message"
	cases := []struct {
		name    string
		status  int
		body    string
		outcome XrayInstallOutcome
	}{
		{"provider failure", 200, `{"success":false,"msg":"` + secret + " " + testToken + `"}`, XrayInstallFailed},
		{"malformed envelope", 200, `{"success":true,"msg":` + secret, XrayInstallUnknown},
		{"missing success", 200, `{"obj":null,"msg":"` + secret + `"}`, XrayInstallUnknown},
		{"invalid success", 200, `{"success":"` + secret + `"}`, XrayInstallUnknown},
		{"empty response", 204, "", XrayInstallUnknown},
		{"authentication status", 401, secret + testToken, XrayInstallUnknown},
		{"forbidden status", 403, secret + testToken, XrayInstallUnknown},
		{"proxy error", 502, `{"success":false,"msg":"` + secret + `"}`, XrayInstallUnknown},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			client, _ := mutationClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			})
			err := client.InstallXray(context.Background(), "v26.9.9")
			assertInstallOutcome(t, err, tt.outcome)
			assertUpgradeErrorRedacted(t, err, secret)
			var apiErr *APIError
			if errors.As(err, &apiErr) != (tt.outcome == XrayInstallFailed) {
				t.Fatal("terminal envelope evidence was lost or fabricated")
			}
			if calls.Load() != 1 {
				t.Fatalf("update retried: %d calls", calls.Load())
			}
		})
	}
}

func TestInstallXrayCancellationBeforeDispatchIsNotStarted(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		var calls atomic.Int32
		client, _ := mutationClient(t, func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			fmt.Fprint(w, `{"success":true}`)
		})
		ctx, cancel := context.WithCancel(context.Background())
		if deadline {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		}
		cancel()
		err := client.InstallXray(ctx, "v26.9.9")
		assertInstallOutcome(t, err, XrayInstallNotStarted)
		if !errors.Is(err, ctx.Err()) || calls.Load() != 0 {
			t.Fatal("pre-dispatch cancellation was not preserved")
		}
	}
}

func TestInstallXrayTimeoutCanLeaveProviderRunning(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	completed := make(chan struct{})
	var calls atomic.Int32
	client, server := mutationClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		close(started)
		<-release // Deliberately models upstream work independent of request context.
		fmt.Fprint(w, `{"success":true}`)
		close(completed)
	})
	t.Cleanup(func() { close(release) }) // Release the handler before server.Close.
	client, err := New(Config{BaseURL: server.URL + "/base", Token: testToken, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	err = client.InstallXray(context.Background(), "v26.9.9")
	assertInstallOutcome(t, err, XrayInstallUnknown)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "may still be executing") {
		t.Fatal("ambiguous timeout lost its sentinel or execution warning")
	}
	select {
	case <-started:
	default:
		t.Fatal("request did not reach the provider before timing out")
	}
	select {
	case <-completed:
		t.Fatal("timeout unexpectedly stopped the simulated upstream update")
	default:
	}
	if calls.Load() != 1 {
		t.Fatalf("update retried after timeout: %d", calls.Load())
	}
}

func TestUpgradeTransportFailuresAreRedacted(t *testing.T) {
	const secret = "provider-private-transport-data"
	for _, cause := range []error{context.DeadlineExceeded, context.Canceled, io.ErrUnexpectedEOF} {
		var calls atomic.Int32
		client, err := New(Config{BaseURL: "http://127.0.0.1", Token: testToken, HTTPClient: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				return nil, fmt.Errorf("%s %s: %w", r.Header.Get("Authorization"), secret, cause)
			}),
		}})
		if err != nil {
			t.Fatal(err)
		}
		err = client.InstallXray(context.Background(), "v26.9.9")
		assertInstallOutcome(t, err, XrayInstallUnknown)
		assertUpgradeErrorRedacted(t, err, secret)
		if !errors.Is(err, cause) || calls.Load() != 1 {
			t.Fatal("transport sentinel was lost or update was retried")
		}
		_, err = client.XrayVersions(context.Background())
		assertUpgradeErrorRedacted(t, err, secret)
		if !errors.Is(err, cause) || calls.Load() != 2 {
			t.Fatal("version-list transport failure was lost or retried")
		}
	}
}

func TestUpgradeResponseLimitsApplyToListAndUpdate(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if chunked {
				w.(http.Flusher).Flush()
			}
			fmt.Fprint(w, strings.Repeat("x", 129))
		}))
		t.Cleanup(server.Close)
		client, err := New(Config{BaseURL: server.URL, Token: testToken, MaxResponseBytes: 128})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.XrayVersions(context.Background())
		var tooLarge *ResponseTooLargeError
		if !errors.As(err, &tooLarge) {
			t.Fatalf("version-list response was not bounded: %v", err)
		}
		err = client.InstallXray(context.Background(), "v26.9.9")
		assertInstallOutcome(t, err, XrayInstallUnknown)
		if !errors.As(err, &tooLarge) {
			t.Fatal("oversized update response lost its size-limit evidence")
		}
	}
}

func TestInstallXrayDoesNotFollowRedirects(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destinationCalls.Add(1)
		fmt.Fprint(w, `{"success":true}`)
	}))
	defer destination.Close()
	client, _ := mutationClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	})
	err := client.InstallXray(context.Background(), "v26.9.9")
	assertInstallOutcome(t, err, XrayInstallUnknown)
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusTemporaryRedirect || destinationCalls.Load() != 0 {
		t.Fatal("update redirect was not safely rejected")
	}
}

func assertInstallOutcome(t *testing.T, err error, want XrayInstallOutcome) {
	t.Helper()
	var installErr *XrayInstallError
	if !errors.As(err, &installErr) || installErr.Outcome != want {
		t.Fatalf("install error = %v; want outcome %s", err, want)
	}
}

func assertUpgradeErrorRedacted(t *testing.T, err error, secret string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a redacted error")
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		for _, marker := range []string{secret, testToken} {
			if strings.Contains(current.Error(), marker) {
				t.Fatal("update diagnostic or unwrapped cause exposed private data")
			}
		}
	}
}
