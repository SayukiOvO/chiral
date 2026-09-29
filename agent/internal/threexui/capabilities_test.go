package threexui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const completeOpenAPIDocument = `{"openapi":"3.0.3","paths":{"/panel/api/server/status":{"get":{}},"/panel/api/inbounds/list":{"get":{}},"/panel/api/inbounds/add":{"post":{}},"/panel/api/inbounds/update/{id}":{"post":{}},"/panel/api/inbounds/del/{id}":{"post":{}},"/panel/api/clients/list":{"get":{}},"/panel/api/clients/add":{"post":{}},"/panel/api/clients/update/{email}":{"post":{}},"/panel/api/clients/del/{email}":{"post":{}},"/panel/api/clients/traffic/{email}":{"get":{}},"/panel/api/clients/onlines":{"post":{}},"/panel/api/clients/ips/{email}":{"post":{}},"/panel/api/xray/update":{"post":{}},"/panel/api/server/restartXrayService":{"post":{}},"/panel/api/server/getConfigJson":{"get":{}},"/panel/api/server/installXray/{version}":{"post":{}}}}`

func TestDiscoverCapabilitiesMatchesExactOpenAPIOperations(t *testing.T) {
	var gotPath, gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, completeOpenAPIDocument)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL + "/node", Token: testToken})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	capabilities, err := client.DiscoverCapabilities(context.Background())
	if err != nil {
		t.Fatalf("DiscoverCapabilities() error = %v", err)
	}
	if gotPath != "/node/panel/api/openapi.json" {
		t.Errorf("request path = %q", gotPath)
	}
	if gotAuthorization != "Bearer "+testToken {
		t.Errorf("Authorization = %q", gotAuthorization)
	}
	if capabilities.OpenAPIVersion != "3.0.3" {
		t.Errorf("OpenAPIVersion = %q", capabilities.OpenAPIVersion)
	}
	const wantDocumentSHA256 = "b5da71f3309b02a292df369e874c07726accda54cab3478a10e8b9ecb575fd16"
	if capabilities.DocumentSHA256 != wantDocumentSHA256 {
		t.Errorf("DocumentSHA256 = %q, want %q", capabilities.DocumentSHA256, wantDocumentSHA256)
	}
	if missing := capabilities.Missing(); len(missing) != 0 {
		t.Errorf("Missing() = %v", missing)
	}
	for _, capability := range RequiredCapabilities() {
		if !capabilities.Supports(capability) {
			t.Errorf("Supports(%q) = false", capability)
		}
	}
}

func TestDiscoverCapabilitiesReportsMissingMethodOrPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := strings.Replace(completeOpenAPIDocument,
			`"/panel/api/xray/update":{"post":{}}`,
			`"/panel/api/xray/update":{"get":{}}`, 1)
		body = strings.Replace(body,
			`"/panel/api/server/restartXrayService":{"post":{}}`,
			`"/panel/api/server/restartXrayService":{"post":true}`, 1)
		fmt.Fprint(w, body)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, Token: testToken})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	capabilities, err := client.DiscoverCapabilities(context.Background())
	if err != nil {
		t.Fatalf("DiscoverCapabilities() error = %v", err)
	}
	wantMissing := []Capability{CapabilityXrayUpdate, CapabilityXrayRestart}
	if got := capabilities.Missing(); !reflect.DeepEqual(got, wantMissing) {
		t.Errorf("Missing() = %v, want %v", got, wantMissing)
	}
}

func TestDiscoverCapabilitiesRejectsInvalidDocument(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "invalid JSON", body: `{"openapi":`},
		{name: "missing version", body: `{"paths":{}}`},
		{name: "unsupported version", body: `{"openapi":"2.0","paths":{}}`},
		{name: "missing paths", body: `{"openapi":"3.0.3"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, Token: testToken})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if _, err := client.DiscoverCapabilities(context.Background()); err == nil {
				t.Fatal("DiscoverCapabilities() error = nil")
			} else {
				assertNoToken(t, err)
			}
		})
	}
}
