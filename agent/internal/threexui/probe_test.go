package threexui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	probeTestURL      = "https://probe.example.test/check?token=private-target"
	probeTestOutbound = `{"tag":"node-probe","protocol":"vless","settings":{"vnext":[{"address":"node.example.test","port":443,"users":[{"id":"private-credential"}]}]},"streamSettings":{"network":"xhttp","xhttpSettings":{"extra":{"retained":"a&b"}}}}`
	probeTestSuccess  = `{"success":true,"obj":{"tag":"node-probe","success":true,"mode":"real","httpStatus":204,"delay":17}}`
)

func writeProbeTemplate(w http.ResponseWriter, target string) {
	inner, _ := json.Marshal(map[string]any{"xraySetting": json.RawMessage(`{"outbounds":[]}`), "outboundTestUrl": target})
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": string(inner)})
}

func probeClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	return mutationClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/base/panel/api/openapi.json" {
			if r.Method != http.MethodGet {
				t.Error("capability discovery must use GET")
			}
			fmt.Fprint(w, `{"openapi":"3.0.3","paths":{"/panel/api/xray/testOutbound":{"post":{}}}}`)
			return
		}
		handler(w, r)
	})
}

func TestProbeOutboundUsesRealOfficialFormAndConfiguredTarget(t *testing.T) {
	// Context remains byte-preserved, including direct dependencies that are
	// not the test route, while the matching tested object cannot be replaced.
	const contextJSON = `[{"tag":"direct-dependency","protocol":"freedom"},` + probeTestOutbound + `]`
	var paths []string
	client, _ := probeClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != http.MethodPost {
			t.Error("probe must use the official POST routes")
		}
		switch r.URL.Path {
		case "/base/panel/api/xray/":
			writeProbeTemplate(w, probeTestURL)
		case "/base/panel/api/xray/testOutbound":
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.ParseForm() != nil {
				t.Error("probe must send a form")
			}
			if len(r.PostForm) != 3 || r.PostForm.Get("mode") != "real" ||
				r.PostForm.Get("outbound") != probeTestOutbound || r.PostForm.Get("allOutbounds") != contextJSON {
				t.Error("probe changed the payload, mode, or invented a URL form field")
			}
			fmt.Fprint(w, probeTestSuccess)
		default:
			t.Error("unexpected probe route")
			http.NotFound(w, r)
		}
	})
	got, err := client.ProbeOutbound(context.Background(), json.RawMessage(probeTestOutbound), json.RawMessage(contextJSON), probeTestURL)
	if err != nil || got != (OutboundProbeResult{HTTPStatus: 204, DelayMillis: 17}) {
		t.Fatalf("probe result = %+v, error = %v", got, err)
	}
	want := []string{"/base/panel/api/xray/", "/base/panel/api/xray/testOutbound", "/base/panel/api/xray/"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("requests = %v", paths)
	}
}

func TestProbeOutboundRequiresAdvertisedCapabilityBeforeOtherRequests(t *testing.T) {
	for _, body := range []string{
		`{"openapi":"3.0.3","paths":{}}`,
		`{"openapi":"3.0.3","paths":{"/panel/api/xray/testOutbound":{"get":{}}}}`,
		`{"private-credential":`,
	} {
		var requests int
		client, _ := mutationClient(t, func(w http.ResponseWriter, r *http.Request) {
			requests++
			if r.URL.Path != "/base/panel/api/openapi.json" || r.Method != http.MethodGet {
				t.Error("probe continued without the official capability")
			}
			fmt.Fprint(w, body)
		})
		got, err := client.ProbeOutbound(context.Background(), json.RawMessage(probeTestOutbound), nil, probeTestURL)
		if err == nil || got != (OutboundProbeResult{}) || requests != 1 {
			t.Fatal("missing or malformed capability accepted")
		}
		assertProbeErrorRedacted(t, err)
	}
}

func TestProbeOutboundRejectsLocalInputsWithoutHTTP(t *testing.T) {
	tests := []struct {
		name, outbound, all, target string
	}{
		{"missing_target", probeTestOutbound, "", ""},
		{"relative_target", probeTestOutbound, "", "/check"},
		{"wrong_scheme", probeTestOutbound, "", "file:///private-target"},
		{"userinfo", probeTestOutbound, "", "https://private-user:private-password@probe.example.test/"},
		{"fragment", probeTestOutbound, "", "https://probe.example.test/#private-fragment"},
		{"empty_fragment", probeTestOutbound, "", "https://probe.example.test/#"},
		{"opaque", probeTestOutbound, "", "https:private-target"},
		{"whitespace", probeTestOutbound, "", " " + probeTestURL},
		{"backslash", probeTestOutbound, "", "https://probe.example.test/\\private-target"},
		{"bad_port", probeTestOutbound, "", "https://probe.example.test:99999/"},
		{"too_long_target", probeTestOutbound, "", "https://probe.example.test/" + strings.Repeat("x", maxProbeURLBytes)},
		{"malformed", `{"protocol":"private-credential"`, "", probeTestURL},
		{"array", `[]`, "", probeTestURL},
		{"null", `null`, "", probeTestURL},
		{"trailing_json", probeTestOutbound + `{}`, "", probeTestURL},
		{"no_tag", `{"protocol":"vless"}`, "", probeTestURL},
		{"no_protocol", `{"tag":"node-probe"}`, "", probeTestURL},
		{"direct", `{"tag":"node-probe","protocol":"direct"}`, "", probeTestURL},
		{"freedom", `{"tag":"node-probe","protocol":"freedom"}`, "", probeTestURL},
		{"blackhole", `{"tag":"node-probe","protocol":"blackhole"}`, "", probeTestURL},
		{"dns", `{"tag":"node-probe","protocol":"dns"}`, "", probeTestURL},
		{"loopback", `{"tag":"node-probe","protocol":"loopback"}`, "", probeTestURL},
		{"unknown_protocol", `{"tag":"node-probe","protocol":"private-unknown"}`, "", probeTestURL},
		{"duplicate_protocol", `{"tag":"node-probe","protocol":"freedom","protocol":"vless"}`, "", probeTestURL},
		{"duplicate_escaped_tag", `{"tag":"private-other","\u0074ag":"node-probe","protocol":"vless"}`, "", probeTestURL},
		{"duplicate_nested_credential", `{"tag":"node-probe","protocol":"vless","settings":{"id":"private-one","id":"private-two"}}`, "", probeTestURL},
		{"invalid_unicode", `{"tag":"node-probe","protocol":"vless","settings":{"id":"\ud800"}}`, "", probeTestURL},
		{"invalid_utf8", "{\"tag\":\"node-probe\",\"protocol\":\"vless\",\"private\":\"\xff\"}", "", probeTestURL},
		{"excessive_depth", `{"tag":"node-probe","protocol":"vless","settings":` + strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64) + `}`, "", probeTestURL},
		{"blocked_tag", `{"tag":"blocked","protocol":"vless"}`, "", probeTestURL},
		{"control_tag", `{"tag":"private\ncredential","protocol":"vless"}`, "", probeTestURL},
		{"context_null", probeTestOutbound, `null`, probeTestURL},
		{"context_object", probeTestOutbound, `{}`, probeTestURL},
		{"context_nonobject", probeTestOutbound, `[null]`, probeTestURL},
		{"context_missing_tag", probeTestOutbound, `[{"protocol":"freedom"}]`, probeTestURL},
		{"context_duplicate", probeTestOutbound, `[{"tag":"other","protocol":"freedom"},{"tag":"other","protocol":"vless"}]`, probeTestURL},
		{"context_duplicate_key", probeTestOutbound, `[{"tag":"other","protocol":"freedom","protocol":"vless"}]`, probeTestURL},
		{"context_direct_override", probeTestOutbound, `[{"tag":"node-probe","protocol":"freedom"}]`, probeTestURL},
		{"context_credential_override", probeTestOutbound, `[{"tag":"node-probe","protocol":"vless","settings":{"id":"private-other"}}]`, probeTestURL},
		{"outbound_size", strings.Repeat("x", maxProbeJSONBytes+1), "", probeTestURL},
		{"context_size", probeTestOutbound, strings.Repeat("x", maxProbeJSONBytes+1), probeTestURL},
	}
	var requests atomic.Int32
	client, _ := mutationClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		t.Error("invalid input dispatched an HTTP request")
	})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := client.ProbeOutbound(context.Background(), json.RawMessage(tt.outbound), json.RawMessage(tt.all), tt.target)
			if err == nil || got != (OutboundProbeResult{}) {
				t.Fatal("invalid local input accepted")
			}
			assertProbeErrorRedacted(t, err)
		})
	}
	if requests.Load() != 0 {
		t.Fatal("invalid local input made requests")
	}
}

func TestProbeOutboundRejectsTooManyContextEntriesAndEncodedPayload(t *testing.T) {
	var entries []string
	for i := 0; i <= maxProbeOutbounds; i++ {
		entries = append(entries, fmt.Sprintf(`{"tag":"dependency-%d","protocol":"freedom"}`, i))
	}
	padding := strings.Repeat("&", 750000)
	tests := []struct{ outbound, all string }{
		{probeTestOutbound, "[" + strings.Join(entries, ",") + "]"},
		{`{"tag":"node-probe","protocol":"vless","padding":"` + padding + `"}`, `[{"tag":"other","protocol":"freedom","padding":"` + padding + `"}]`},
	}
	client, _ := mutationClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("oversized input made a request") })
	for _, tt := range tests {
		if _, err := client.ProbeOutbound(context.Background(), json.RawMessage(tt.outbound), json.RawMessage(tt.all), probeTestURL); err == nil {
			t.Fatal("oversized input accepted")
		}
	}
}

func TestProbeOutboundRefusesMismatchedOrChangingConfiguredTarget(t *testing.T) {
	for _, changeAfterProbe := range []bool{false, true} {
		t.Run(fmt.Sprint(changeAfterProbe), func(t *testing.T) {
			var reads, probes int
			client, _ := probeClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/base/panel/api/xray/":
					reads++
					target := "https://private-other-target.example.test/secret"
					if changeAfterProbe && reads == 1 {
						target = probeTestURL
					}
					writeProbeTemplate(w, target)
				case "/base/panel/api/xray/testOutbound":
					probes++
					fmt.Fprint(w, probeTestSuccess)
				default:
					t.Error("unexpected target-matching route")
				}
			})
			got, err := client.ProbeOutbound(context.Background(), json.RawMessage(probeTestOutbound), nil, probeTestURL)
			if err == nil || got != (OutboundProbeResult{}) || strings.Contains(err.Error(), "private-") {
				t.Fatal("target mismatch accepted or leaked target")
			}
			if (!changeAfterProbe && (reads != 1 || probes != 0)) || (changeAfterProbe && (reads != 2 || probes != 1)) {
				t.Fatalf("reads=%d probes=%d", reads, probes)
			}
		})
	}
}

func TestProbeOutboundRejectsIncompleteOrUnsuccessfulEvidence(t *testing.T) {
	cases := map[string]string{
		"outer_false":     `{"success":false,"msg":"private-credential"}`,
		"missing_outer":   `{"obj":{"success":true}}`,
		"malformed":       `{"success":true,"obj":"private-credential`,
		"null_result":     `{"success":true,"obj":null}`,
		"inner_false":     strings.Replace(probeTestSuccess, `"mode":"real"`, `"error":"private-credential","mode":"real"`, 1),
		"missing_success": strings.Replace(probeTestSuccess, `"tag":"node-probe","success":true,`, `"tag":"node-probe",`, 1),
		"missing_tag":     strings.Replace(probeTestSuccess, `"tag":"node-probe",`, "", 1),
		"wrong_tag":       strings.Replace(probeTestSuccess, "node-probe", "private-wrong-tag", 1),
		"missing_mode":    strings.Replace(probeTestSuccess, `"mode":"real",`, "", 1),
		"tcp":             strings.Replace(probeTestSuccess, `"real"`, `"tcp"`, 1),
		"ping":            strings.Replace(probeTestSuccess, `"real"`, `"ping"`, 1),
		"warm_http":       strings.Replace(probeTestSuccess, `"real"`, `"http"`, 1),
		"missing_status":  strings.Replace(probeTestSuccess, `"httpStatus":204,`, "", 1),
		"informational":   strings.Replace(probeTestSuccess, "204", "199", 1),
		"bad_status":      strings.Replace(probeTestSuccess, "204", "400", 1),
		"server_error":    strings.Replace(probeTestSuccess, "204", "503", 1),
		"string_status":   strings.Replace(probeTestSuccess, "204", `"private-status"`, 1),
		"missing_delay":   strings.Replace(probeTestSuccess, `,"delay":17`, "", 1),
		"negative_delay":  strings.Replace(probeTestSuccess, `"delay":17`, `"delay":-1`, 1),
		"overflow_delay":  strings.Replace(probeTestSuccess, `"delay":17`, `"delay":9223372036854775808`, 1),
	}
	// Exercise a genuine inner false as well as contradictory success + error.
	cases["contradictory_error"] = cases["inner_false"]
	cases["inner_false"] = strings.Replace(cases["inner_false"], `"tag":"node-probe","success":true`, `"tag":"node-probe","success":false`, 1)
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var probes, reads int
			client, _ := probeClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/base/panel/api/xray/" {
					reads++
					writeProbeTemplate(w, probeTestURL)
					return
				}
				probes++
				fmt.Fprint(w, body)
			})
			got, err := client.ProbeOutbound(context.Background(), json.RawMessage(probeTestOutbound), nil, probeTestURL)
			if err == nil || got != (OutboundProbeResult{}) || probes != 1 || reads != 1 {
				t.Fatalf("invalid evidence accepted or retried: got=%+v reads=%d probes=%d", got, reads, probes)
			}
			assertProbeErrorRedacted(t, err)
		})
	}
}

func TestProbeOutboundAcceptsOnlySuccessfulHTTPRange(t *testing.T) {
	for _, status := range []int{200, 204, 301, 399} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			client, _ := probeClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/base/panel/api/xray/" {
					writeProbeTemplate(w, probeTestURL)
					return
				}
				fmt.Fprint(w, strings.Replace(probeTestSuccess, "204", fmt.Sprint(status), 1))
			})
			got, err := client.ProbeOutbound(context.Background(), json.RawMessage(probeTestOutbound), nil, probeTestURL)
			if err != nil || got.HTTPStatus != status {
				t.Fatalf("valid HTTP response rejected: %v", err)
			}
		})
	}
}

func TestProbeOutboundCancellationNeverRetries(t *testing.T) {
	var probes atomic.Int32
	release := make(chan struct{})
	client, _ := probeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/base/panel/api/xray/" {
			writeProbeTemplate(w, probeTestURL)
			return
		}
		probes.Add(1)
		<-release // Like upstream, work can outlive the request context.
		fmt.Fprint(w, probeTestSuccess)
	})
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	got, err := client.ProbeOutbound(ctx, json.RawMessage(probeTestOutbound), nil, probeTestURL)
	if !errors.Is(err, context.DeadlineExceeded) || got != (OutboundProbeResult{}) || probes.Load() != 1 {
		t.Fatalf("timeout result=%+v err=%v probes=%d", got, err, probes.Load())
	}
	assertProbeErrorRedacted(t, err)
}

func TestProbeOutboundAlreadyCancelledMakesNoRequest(t *testing.T) {
	client, _ := mutationClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("cancelled probe made a request") })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.ProbeOutbound(ctx, json.RawMessage(probeTestOutbound), nil, probeTestURL); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled probe returned %v", err)
	}
}

func TestProbeOutboundLostResponseNeverRedispatches(t *testing.T) {
	var probes atomic.Int32
	client, _ := probeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/base/panel/api/xray/" {
			writeProbeTemplate(w, probeTestURL)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		probes.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close() // Provider may have completed, but no result arrives.
	})
	got, err := client.ProbeOutbound(context.Background(), json.RawMessage(probeTestOutbound), nil, probeTestURL)
	if err == nil || got != (OutboundProbeResult{}) || probes.Load() != 1 {
		t.Fatal("lost probe response accepted or retried")
	}
	assertProbeErrorRedacted(t, err)
}

func TestProbeOutboundTransportDiagnosticNeverExposesRequest(t *testing.T) {
	client, _ := probeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/base/panel/api/xray/" {
			t.Error("faulted probe unexpectedly reached the server")
		}
		writeProbeTemplate(w, probeTestURL)
	})
	transport := client.http.Transport
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/base/panel/api/xray/testOutbound" {
			return nil, fmt.Errorf("private-credential %s %s", probeTestURL, r.Header.Get("Authorization"))
		}
		return transport.RoundTrip(r)
	})
	got, err := client.ProbeOutbound(context.Background(), json.RawMessage(probeTestOutbound), nil, probeTestURL)
	if err == nil || got != (OutboundProbeResult{}) {
		t.Fatal("transport error accepted")
	}
	assertProbeErrorRedacted(t, err)
}

func TestProbeOutboundMatchingContextIgnoresOnlyObjectOrder(t *testing.T) {
	const outbound = `{"tag":"node-probe","protocol":"vless","settings":{"level":1}}`
	const equal = `[{"settings":{"level":1},"protocol":"vless","tag":"node-probe"}]`
	if tag, err := validateOutboundProbe(json.RawMessage(outbound), json.RawMessage(equal), probeTestURL); err != nil || tag != "node-probe" {
		t.Fatal("matching context rejected")
	}
	const changed = `[{"settings":{"level":1.0},"protocol":"vless","tag":"node-probe"}]`
	if _, err := validateOutboundProbe(json.RawMessage(outbound), json.RawMessage(changed), probeTestURL); err == nil {
		t.Fatal("conservatively different JSON number spelling accepted")
	}
}

func TestProbeOutboundHTTPAndResponseBoundsAreSanitized(t *testing.T) {
	for _, status := range []int{200, 302, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var probes int
			client, _ := probeClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/base/panel/api/xray/" {
					writeProbeTemplate(w, probeTestURL)
					return
				}
				probes++
				w.Header().Set("Location", "https://private-redirect.example.test/")
				w.WriteHeader(status)
				fmt.Fprint(w, strings.Repeat("private-credential ", 100))
			})
			client.maxResponseBytes = 512
			got, err := client.ProbeOutbound(context.Background(), json.RawMessage(probeTestOutbound), nil, probeTestURL)
			if err == nil || got != (OutboundProbeResult{}) || probes != 1 {
				t.Fatal("HTTP failure, redirect or oversized response accepted/retried")
			}
			assertProbeErrorRedacted(t, err)
		})
	}
}

func assertProbeErrorRedacted(t *testing.T, err error) {
	t.Helper()
	for current := err; current != nil; current = errors.Unwrap(current) {
		if strings.Contains(current.Error(), "private-") || strings.Contains(current.Error(), testToken) ||
			strings.Contains(current.Error(), "example.test") {
			t.Fatal("probe diagnostic leaked provider or request data")
		}
	}
}
