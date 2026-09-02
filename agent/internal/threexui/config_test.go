package threexui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConfigJSONUsesAuthenticatedReadOnlyEndpoint(t *testing.T) {
	var method, path, authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		authorization = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"success":true,"msg":"","obj":{"inbounds":[]}}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	config, err := client.ConfigJSON(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet || path != "/panel/api/server/getConfigJson" || authorization != "Bearer "+testToken {
		t.Fatalf("request = %s %s auth %q", method, path, authorization)
	}
	if string(config) != `{"inbounds":[]}` {
		t.Fatalf("config = %s", config)
	}
}

func TestConfigJSONRejectsNonObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":true,"msg":"","obj":[]}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ConfigJSON(context.Background()); err == nil {
		t.Fatal("non-object assembled config was accepted")
	}
}
