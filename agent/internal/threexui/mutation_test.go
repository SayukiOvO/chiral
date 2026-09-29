package threexui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func mutationClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Error("missing authenticated request")
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{BaseURL: server.URL + "/base", Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func TestXrayTemplateUsesEncodedReadAndFormWrite(t *testing.T) {
	const raw = `{"inbounds":[{"tag":"owned","settings":{"clients":[{"id":"secret","extra":{"field":1}}]}}],"outbounds":[]}`
	const probeURL = "https://probe.example.test/check?a=1&b=2"
	var read, update atomic.Bool
	client, _ := mutationClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		switch r.URL.Path {
		case "/base/panel/api/xray/":
			read.Store(true)
			inner, _ := json.Marshal(map[string]any{"xraySetting": json.RawMessage(raw), "outboundTestUrl": probeURL})
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": string(inner)})
		case "/base/panel/api/xray/update":
			update.Store(true)
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Error("wrong form encoding")
			}
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				return
			}
			if r.Form.Get("xraySetting") != raw || r.Form.Get("outboundTestUrl") != probeURL {
				t.Error("template or outbound test URL changed")
			}
			fmt.Fprint(w, `{"success":true,"obj":null}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	template, err := client.ReadXrayTemplate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(template.Config) != raw || template.OutboundTestURL != probeURL {
		t.Fatal("template read lost data")
	}
	if err := client.UpdateXrayTemplate(context.Background(), template); err != nil {
		t.Fatal(err)
	}
	if !read.Load() || !update.Load() {
		t.Fatal("both API operations must succeed")
	}
}

func TestReadAssembledConfigPreservesObjectsAndSuppressesProviderSecrets(t *testing.T) {
	const raw = `{"inbounds":[{"tag":"owned","settings":{"clients":[{"id":"private-credential"}]}}],"unknown":{"retain":true}}`
	var fail atomic.Bool
	client, _ := mutationClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/base/panel/api/server/getConfigJson" {
			t.Error("unexpected assembled configuration request")
		}
		if fail.Load() {
			fmt.Fprint(w, `{"success":false,"msg":"private-credential"}`)
			return
		}
		fmt.Fprintf(w, `{"success":true,"obj":%s}`, raw)
	})
	got, err := client.ReadAssembledConfig(context.Background())
	if err != nil || string(got) != raw {
		t.Fatal("assembled configuration read lost object data")
	}
	fail.Store(true)
	_, err = client.ReadAssembledConfig(context.Background())
	if err == nil || strings.Contains(err.Error(), "private-credential") {
		t.Fatal("assembled configuration error was accepted or leaked a secret")
	}
}

func TestInboundMutationsPreserveNestedFieldsAndReadback(t *testing.T) {
	const nested = `{"clients":[{"id":"private-id","email":"alice@node","future":{"enabled":true}}],"decryption":"none"}`
	inbound := Inbound{Tag: "chiral-owned", Protocol: "vless", Listen: "127.0.0.1", Port: 12345, Enable: true,
		Settings: json.RawMessage(nested), StreamSettings: json.RawMessage(`{"network":"xhttp","xhttpSettings":{"extra":{"opaque":1}}}`)}
	var mutations atomic.Int32
	client, _ := mutationClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/base/panel/api/inbounds/list":
			if r.Method != http.MethodGet {
				t.Error("list method")
			}
			fmt.Fprintf(w, `{"success":true,"obj":[{"id":7,"tag":"chiral-owned","protocol":"vless","settings":%s,"unmodelled":{"keep":true}}]}`, nested)
		case "/base/panel/api/inbounds/add", "/base/panel/api/inbounds/update/7":
			mutations.Add(1)
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
				t.Error("write method or content type")
			}
			var got Inbound
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Error(err)
				return
			}
			if string(got.Settings) != nested || !reflect.DeepEqual(got.StreamSettings, inbound.StreamSettings) {
				t.Error("nested JSON changed")
			}
			if got.TrafficReset != "never" {
				t.Error("unexpected traffic reset default")
			}
			if strings.Contains(r.URL.Path, "update") && got.ID != 7 {
				t.Error("update identity missing")
			}
			fmt.Fprintf(w, `{"success":true,"obj":{"id":7,"tag":"chiral-owned","settings":%s,"unmodelled":{"keep":true}}}`, nested)
		case "/base/panel/api/inbounds/del/7":
			mutations.Add(1)
			if r.Method != http.MethodPost {
				t.Error("delete method")
			}
			fmt.Fprint(w, `{"success":true,"obj":7}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	rows, err := client.ListInbounds(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !strings.Contains(string(rows[0].Raw), `"unmodelled"`) {
		t.Fatal("inbound readback evidence lost")
	}
	created, err := client.AddInbound(context.Background(), inbound)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(created.Raw), `"unmodelled"`) {
		t.Fatal("mutation readback evidence lost")
	}
	if _, err := client.UpdateInbound(context.Background(), 7, inbound); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteInbound(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if mutations.Load() != 3 {
		t.Fatalf("mutation count = %d", mutations.Load())
	}
}

func TestClientMutationsEncodeIdentityAndKeepTraffic(t *testing.T) {
	email := "alice?query=1#fragment%2f@node"
	account, _ := json.Marshal(map[string]any{"email": email, "id": "secret-uuid", "enable": false, "future": map[string]any{"raw": "preserved-in-transit"}})
	var created, updated, deleted atomic.Bool
	client, _ := mutationClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/base/panel/api/clients/list":
			fmt.Fprint(w, `{"success":true,"obj":[{"id":12,"email":"alice@node","uuid":"private-uuid","inboundIds":[7],"unknown":{"preserve":1}}]}`)
		case "/base/panel/api/clients/add":
			created.Store(true)
			var payload struct {
				Client     json.RawMessage `json:"client"`
				InboundIDs []int64         `json:"inboundIds"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
				return
			}
			if string(payload.Client) != string(account) || !reflect.DeepEqual(payload.InboundIDs, []int64{7}) {
				t.Error("client create payload changed")
			}
			fmt.Fprint(w, `{"success":true,"obj":null}`)
		case "/base/panel/api/clients/update/" + email:
			updated.Store(true)
			if r.URL.RawQuery != "" || r.URL.Fragment != "" {
				t.Error("client identity escaped into URL query or fragment")
			}
			if !strings.HasSuffix(r.URL.EscapedPath(), url.PathEscape(email)) {
				t.Error("client identity was not one encoded segment")
			}
			body, _ := io.ReadAll(r.Body)
			if string(body) != string(account) {
				t.Error("client update payload changed")
			}
			fmt.Fprint(w, `{"success":true}`)
		case "/base/panel/api/clients/del/" + email:
			deleted.Store(true)
			if r.URL.Query().Get("keepTraffic") != "1" || len(r.URL.Query()) != 1 {
				t.Error("invalid keepTraffic query")
			}
			fmt.Fprint(w, `{"success":true}`)
		default:
			t.Errorf("unexpected client route %s", r.URL.Path)
			http.NotFound(w, r)
		}
		if r.URL.Path != "/base/panel/api/clients/list" && r.Method != http.MethodPost {
			t.Error("client mutation method")
		}
	})
	rows, err := client.ListClients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != 12 || !strings.Contains(string(rows[0].Raw), `"unknown"`) || !strings.Contains(string(rows[0].Raw), `"uuid"`) {
		t.Fatal("client readback evidence lost")
	}
	if err := client.AddClient(context.Background(), account, []int64{7}); err != nil {
		t.Fatal(err)
	}
	if err := client.UpdateClient(context.Background(), email, account); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteClient(context.Background(), email, true); err != nil {
		t.Fatal(err)
	}
	if !created.Load() || !updated.Load() || !deleted.Load() {
		t.Fatal("not every client mutation succeeded")
	}
}

func TestMutationValidationFailsBeforeHTTP(t *testing.T) {
	var requests atomic.Int32
	client, _ := mutationClient(t, func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); fmt.Fprint(w, `{"success":true}`) })
	ctx := context.Background()
	valid := json.RawMessage(`{"email":"alice@node","enable":false,"id":"private-id"}`)
	checks := []func() error{
		func() error { return client.DeleteInbound(ctx, 0) },
		func() error { _, err := client.UpdateInbound(ctx, -1, Inbound{}); return err },
		func() error { _, err := client.UpdateInbound(ctx, 4, Inbound{ID: 5}); return err },
		func() error { _, err := client.AddInbound(ctx, Inbound{ID: 1}); return err },
		func() error {
			_, err := client.AddInbound(ctx, Inbound{Tag: "owned", Protocol: "vless", Port: 65536})
			return err
		},
		func() error { return client.AddClient(ctx, valid, nil) },
		func() error { return client.AddClient(ctx, valid, []int64{1, 1}) },
		func() error { return client.AddClient(ctx, valid, []int64{-1}) },
		func() error { return client.UpdateClient(ctx, "different@node", valid) },
		func() error { return client.AddClient(ctx, json.RawMessage(`{"email":"alice@node"}`), []int64{1}) },
		func() error {
			return client.UpdateXrayTemplate(ctx, XrayTemplate{Config: json.RawMessage(`[]`), OutboundTestURL: "https://example.test"})
		},
		func() error { return client.UpdateXrayTemplate(ctx, XrayTemplate{Config: json.RawMessage(`{}`)}) },
	}
	for _, email := range []string{"", ".", "..", "../", "alice/bob", "alice\\bob", "alice\n", "alice bob"} {
		email := email
		checks = append(checks, func() error { return client.DeleteClient(ctx, email, true) })
	}
	for i, check := range checks {
		if check() == nil {
			t.Errorf("invalid request %d was accepted", i)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid requests reached HTTP: %d", requests.Load())
	}
}

func TestMutationErrorsNeverIncludeResponseSecrets(t *testing.T) {
	const secret = "sensitive-private-client-key"
	for _, body := range []string{
		`{"success":false,"msg":"` + secret + `"}`,
		`{"success":true,"obj":{"id":"` + secret + `"}}`,
		`{"success":"` + secret + `"}`,
		`{"success":true,"obj":` + secret,
	} {
		t.Run(body[:15], func(t *testing.T) {
			client, _ := mutationClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) })
			_, err := client.AddInbound(context.Background(), Inbound{Tag: "owned", Protocol: "vless", Port: 1234, Settings: json.RawMessage(`{}`)})
			if err == nil {
				t.Fatal("invalid response was accepted")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), testToken) {
				t.Fatal("mutation error leaked a secret")
			}
		})
	}
}

func TestMutationTransportErrorsAreRedactedAndRemainDetectable(t *testing.T) {
	const secret = "private-body-value"
	client, err := New(Config{BaseURL: "http://127.0.0.1", Token: testToken, HTTPClient: &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(r.Body)
			return nil, fmt.Errorf("%s %s: %w", r.Header.Get("Authorization"), body, context.DeadlineExceeded)
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	err = client.UpdateClient(context.Background(), "alice@node", json.RawMessage(`{"email":"alice@node","enable":true,"password":"`+secret+`"}`))
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("timeout sentinel was lost")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), testToken) {
		t.Fatal("transport diagnostic leaked a secret")
	}
}

func TestMutationLimitsRequestAndResponseSizes(t *testing.T) {
	var requests atomic.Int32
	client, server := mutationClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, strings.Repeat("x", 129))
	})
	oversized := json.RawMessage(`{"email":"alice@node","enable":true,"password":"` + strings.Repeat("x", maxMutationRequestBytes) + `"}`)
	err := client.AddClient(context.Background(), oversized, []int64{1})
	var requestErr *RequestTooLargeError
	if !errors.As(err, &requestErr) || requests.Load() != 0 {
		t.Fatal("oversized request was not rejected before HTTP")
	}
	client, err = New(Config{BaseURL: server.URL, Token: testToken, MaxResponseBytes: 128})
	if err != nil {
		t.Fatal(err)
	}
	err = client.RestartXray(context.Background())
	var responseErr *ResponseTooLargeError
	if !errors.As(err, &responseErr) {
		t.Fatalf("response limit error = %v", err)
	}
}

func TestMutationDoesNotFollowRedirects(t *testing.T) {
	var destinationRequests atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destinationRequests.Add(1)
		fmt.Fprint(w, `{"success":true}`)
	}))
	defer destination.Close()
	client, _ := mutationClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	})
	err := client.RestartXray(context.Background())
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusTemporaryRedirect {
		t.Fatal("redirect was not rejected")
	}
	if destinationRequests.Load() != 0 {
		t.Fatal("mutation followed a redirect")
	}
}

func TestRestartUsesExplicitEndpointAndHonoursCancellation(t *testing.T) {
	var calls atomic.Int32
	client, _ := mutationClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/base/panel/api/server/restartXrayService" {
			t.Error("unexpected restart request")
		}
		fmt.Fprint(w, `{"success":true}`)
	})
	if err := client.RestartXray(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := client.RestartXray(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("cancellation was not preserved")
	}
	if calls.Load() != 1 {
		t.Fatal("cancelled restart reached provider")
	}
}
