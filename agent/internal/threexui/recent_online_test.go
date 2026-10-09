package threexui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

const recentOnlineOpenAPI = `{"openapi":"3.0.3","paths":{"/panel/api/server/status":{"get":{}},"/panel/api/clients/onlinesByGuid":{"post":{}},"/panel/api/clients/clientIpsByGuid":{"post":{}}}}`

func TestReadRecentOnlineUsesOnlyExpectedGUIDAndWhitelist(t *testing.T) {
	var paths []string
	statusCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Fatal("missing bearer token")
		}
		switch r.URL.Path {
		case "/node/panel/api/openapi.json":
			fmt.Fprint(w, recentOnlineOpenAPI)
		case "/node/panel/api/server/status":
			statusCalls++
			fmt.Fprint(w, `{"success":true,"obj":{"panelVersion":"3.8.5","panelGuid":"panel-guid","xray":{"state":"running"}}}`)
		case "/node/panel/api/clients/onlinesByGuid":
			if r.Method != http.MethodPost {
				t.Fatal("online route was not POST")
			}
			fmt.Fprint(w, `{"success":true,"obj":{"panel-guid":["local","unmanaged"],"child-guid":["child-only","local"]}}`)
		case "/node/panel/api/clients/clientIpsByGuid":
			if r.Method != http.MethodPost {
				t.Fatal("IP route was not POST")
			}
			fmt.Fprint(w, `{"success":true,"obj":{
				"panel-guid":{"local":[
					{"ip":"2001:db8::2","timestamp":4102444800},
					{"ip":"192.0.2.1","timestamp":1},
					{"ip":"::ffff:192.0.2.1","timestamp":2}],
					"unmanaged":[{"ip":"198.51.100.9","timestamp":3}]},
				"child-guid":{"child-only":[{"ip":"203.0.113.8","timestamp":4}]}}}`)
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL + "/node", Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ReadRecentOnline(context.Background(), "panel-guid", []string{"child-only", "local", "none"})
	if err != nil {
		t.Fatal(err)
	}
	want := []runtimeprovider.RecentOnlineUser{
		{Email: "child-only", IPs: []string{}},
		{Email: "local", ObservedOnline: true, IPs: []string{"192.0.2.1", "2001:db8::2"}},
		{Email: "none", IPs: []string{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recent online = %#v, want %#v", got, want)
	}
	if statusCalls != 2 {
		t.Fatalf("status calls = %d, want 2", statusCalls)
	}
	wantPaths := []string{
		"GET /node/panel/api/openapi.json",
		"GET /node/panel/api/server/status",
		"POST /node/panel/api/clients/onlinesByGuid",
		"POST /node/panel/api/clients/clientIpsByGuid",
		"GET /node/panel/api/server/status",
	}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("request sequence = %v", paths)
	}
}

func TestReadRecentOnlineRequiresCandidateCapabilitiesBeforeReads(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/panel/api/openapi.json" {
			t.Fatalf("called %s before capability rejection", r.URL.Path)
		}
		fmt.Fprint(w, `{"openapi":"3.0.3","paths":{"/panel/api/server/status":{"get":{}},"/panel/api/clients/onlinesByGuid":{"post":{}}}}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ReadRecentOnline(context.Background(), "panel-guid", []string{"client"})
	if got != nil || err == nil || !strings.Contains(err.Error(), "capability_missing") || calls.Load() != 1 {
		t.Fatalf("capability failure returned %#v, %v after %d calls", got, err, calls.Load())
	}
	assertNoToken(t, err)
}

func TestReadRecentOnlineRejectsInvalidRequestsWithoutHTTP(t *testing.T) {
	var calls atomic.Int32
	client, err := New(Config{BaseURL: "http://127.0.0.1:2053", Token: testToken,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, fmt.Errorf("must not make a request")
		})}})
	if err != nil {
		t.Fatal(err)
	}
	tooMany := make([]string, chiralv1.MaxRecentOnlineUsers+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("client-%d", i)
	}
	for _, test := range []struct {
		name   string
		client *Client
		guid   string
		emails []string
	}{
		{"nil client", nil, "panel-guid", nil},
		{"invalid guid", client, "panel/guid", nil},
		{"too many", client, "panel-guid", tooMany},
		{"long email", client, "panel-guid", []string{strings.Repeat("x", chiralv1.MaxRecentOnlineEmailBytes+1)}},
		{"invalid email", client, "panel-guid", []string{"private email"}},
		{"duplicate email", client, "panel-guid", []string{"client", "client"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.client.ReadRecentOnline(context.Background(), test.guid, test.emails)
			if got != nil || err == nil || !strings.Contains(err.Error(), "invalid_request") {
				t.Fatalf("invalid request returned %#v, %v", got, err)
			}
			assertNoToken(t, err)
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests made %d HTTP calls", calls.Load())
	}
}

func TestReadRecentOnlineBracketsProviderIdentity(t *testing.T) {
	for _, test := range []struct {
		name     string
		statuses []string
		wantData bool
	}{
		{"wrong before", []string{"other-guid"}, false},
		{"changed guid", []string{"panel-guid", "other-guid"}, true},
		{"changed version", []string{"panel-guid", "panel-guid"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			statusCall := 0
			dataCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/panel/api/openapi.json":
					fmt.Fprint(w, recentOnlineOpenAPI)
				case "/panel/api/server/status":
					guid := test.statuses[statusCall]
					version := "3.8.5"
					if test.name == "changed version" && statusCall == 1 {
						version = "3.8.6"
					}
					statusCall++
					fmt.Fprintf(w, `{"success":true,"obj":{"panelVersion":%q,"panelGuid":%q,"xray":{"state":"running"}}}`, version, guid)
				case "/panel/api/clients/onlinesByGuid", "/panel/api/clients/clientIpsByGuid":
					dataCalls++
					fmt.Fprint(w, `{"success":true,"obj":{}}`)
				default:
					t.Fatalf("unexpected route %s", r.URL.Path)
				}
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, Token: testToken})
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.ReadRecentOnline(context.Background(), "panel-guid", []string{"client"})
			if got != nil || err == nil || !strings.Contains(err.Error(), "provider_identity_changed") {
				t.Fatalf("identity failure returned %#v, %v", got, err)
			}
			if (dataCalls > 0) != test.wantData {
				t.Fatalf("data calls = %d, wantData=%v", dataCalls, test.wantData)
			}
		})
	}
}

func TestReadRecentOnlineRejectsMalformedOrSensitiveResponses(t *testing.T) {
	var onlineEntries strings.Builder
	for i := 0; i <= chiralv1.MaxRecentOnlineUsers; i++ {
		if i > 0 {
			onlineEntries.WriteByte(',')
		}
		fmt.Fprintf(&onlineEntries, "%q", fmt.Sprintf("client-%d", i))
	}
	tooManyOnline := `{"success":true,"obj":{"panel-guid":[` + onlineEntries.String() + `]}}`
	for _, test := range []struct {
		name   string
		online string
		ips    string
	}{
		{"provider rejection", `{"success":false,"msg":"private-response-secret","obj":null}`, `{"success":true,"obj":{}}`},
		{"invalid online GUID entry", `{"success":true,"obj":{"panel-guid":"private-response-secret"}}`, `{"success":true,"obj":{}}`},
		{"duplicate online email", `{"success":true,"obj":{"panel-guid":["client","client"]}}`, `{"success":true,"obj":{}}`},
		{"too many local online emails", tooManyOnline, `{"success":true,"obj":{}}`},
		{"missing IP timestamp", `{"success":true,"obj":{}}`, `{"success":true,"obj":{"panel-guid":{"client":[{"ip":"192.0.2.1","secret":"private-response-secret"}]}}}`},
		{"invalid IP", `{"success":true,"obj":{}}`, `{"success":true,"obj":{"panel-guid":{"client":[{"ip":"private-response-secret","timestamp":1}]}}}`},
		{"fractional timestamp", `{"success":true,"obj":{}}`, `{"success":true,"obj":{"panel-guid":{"client":[{"ip":"192.0.2.1","timestamp":1.5}]}}}`},
		{"duplicate response key", `{"success":true,"success":true,"obj":{}}`, `{"success":true,"obj":{}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			statusCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/panel/api/openapi.json":
					fmt.Fprint(w, recentOnlineOpenAPI)
				case "/panel/api/server/status":
					statusCalls++
					fmt.Fprint(w, `{"success":true,"obj":{"panelVersion":"3.8.5","panelGuid":"panel-guid","xray":{"state":"running"}}}`)
				case "/panel/api/clients/onlinesByGuid":
					fmt.Fprint(w, test.online)
				case "/panel/api/clients/clientIpsByGuid":
					fmt.Fprint(w, test.ips)
				}
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, Token: testToken})
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.ReadRecentOnline(context.Background(), "panel-guid", []string{"client"})
			if got != nil || err == nil || strings.Contains(err.Error(), "private-response-secret") || strings.Contains(err.Error(), testToken) {
				t.Fatalf("unsafe malformed response result %#v, error %v", got, err)
			}
			if statusCalls > 2 {
				t.Fatal("unexpected status requests")
			}
		})
	}
}

func TestReadRecentOnlineBoundsAggregateIPsWithoutPartialResult(t *testing.T) {
	var entries strings.Builder
	for i := 1; i <= chiralv1.MaxRecentOnlineIPs+1; i++ {
		if i > 1 {
			entries.WriteByte(',')
		}
		// Raw entries count against the frame guard even when canonicalisation
		// would deduplicate all of them to one output address.
		entries.WriteString(`{"ip":"::ffff:192.0.2.1","timestamp":1}`)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/panel/api/openapi.json":
			fmt.Fprint(w, recentOnlineOpenAPI)
		case "/panel/api/server/status":
			fmt.Fprint(w, `{"success":true,"obj":{"panelVersion":"3.8.5","panelGuid":"panel-guid","xray":{"state":"running"}}}`)
		case "/panel/api/clients/onlinesByGuid":
			fmt.Fprint(w, `{"success":true,"obj":{}}`)
		case "/panel/api/clients/clientIpsByGuid":
			fmt.Fprintf(w, `{"success":true,"obj":{"panel-guid":{"client":[%s]}}}`, entries.String())
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ReadRecentOnline(context.Background(), "panel-guid", []string{"client"})
	if got != nil || err == nil || !strings.Contains(err.Error(), "observation_limit_exceeded") {
		t.Fatalf("oversized observation returned %#v, %v", got, err)
	}
}
