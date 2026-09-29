package threexui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func newTrafficTestClient(t *testing.T, objects map[string]string) *Client {
	t.Helper()
	client, err := New(Config{BaseURL: "http://127.0.0.1:2053/private/", Token: testToken,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+testToken {
				t.Fatalf("unexpected traffic request method or credentials")
			}
			object, ok := objects[strings.TrimPrefix(r.URL.Path, "/private/")]
			if !ok {
				t.Fatalf("unexpected traffic endpoint %s", r.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"success":true,"obj":` + object + `}`)), Header: make(http.Header)}, nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestTrafficSnapshotUsesRawLocalCountersAndDeduplicatesSharedClients(t *testing.T) {
	client := newTrafficTestClient(t, map[string]string{
		inboundTrafficEndpoint: `[
			{"id":2,"tag":"second","up":99,"down":88,"total":99999,"clientStats":[
				{"id":7,"inboundId":1,"email":"alice@profile.node","up":9007199254740993,"down":20,"uuid":"ignored-secret","total":88888}]},
			{"id":1,"tag":"first","up":10,"down":20,"clientStats":[
				{"id":7,"inboundId":1,"email":"alice@profile.node","up":9007199254740993,"down":20}]}]`,
		outboundTrafficEndpoint: `[{"id":4,"tag":"direct","up":105,"down":220,"total":325}]`,
	})
	got, err := client.TrafficSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []ClientTraffic{{ID: 7, InboundID: 1, Email: "alice@profile.node", Up: 9007199254740993, Down: 20}}; !reflect.DeepEqual(got.Clients, want) {
		t.Fatalf("client counters = %#v", got.Clients)
	}
	if want := []TaggedTraffic{{ID: 1, Tag: "first", Up: 10, Down: 20}, {ID: 2, Tag: "second", Up: 99, Down: 88}}; !reflect.DeepEqual(got.Inbounds, want) {
		t.Fatalf("inbound counters = %#v", got.Inbounds)
	}
	if want := []TaggedTraffic{{ID: 4, Tag: "direct", Up: 105, Down: 220}}; !reflect.DeepEqual(got.Outbounds, want) {
		t.Fatalf("outbound counters = %#v", got.Outbounds)
	}
}

func TestTrafficListsAcceptExplicitEmptyArraysAndNull(t *testing.T) {
	for _, empty := range []string{`[]`, `null`} {
		t.Run(empty, func(t *testing.T) {
			client := newTrafficTestClient(t, map[string]string{inboundTrafficEndpoint: empty, outboundTrafficEndpoint: empty})
			got, err := client.TrafficSnapshot(context.Background())
			if err != nil || len(got.Clients)+len(got.Inbounds)+len(got.Outbounds) != 0 {
				t.Fatalf("empty traffic = %#v, error %v", got, err)
			}
		})
	}
}

func TestTrafficRejectsAmbiguousMalformedAndForeignCounters(t *testing.T) {
	for name, object := range map[string]string{
		"object instead of array": `{}`,
		"null row":                `[null]`,
		"missing counters":        `[{"id":1,"tag":"entry"}]`,
		"negative":                `[{"id":1,"tag":"entry","up":-1,"down":0}]`,
		"unsigned overflow":       `[{"id":1,"tag":"entry","up":9223372036854775808,"down":0}]`,
		"sum overflow":            `[{"id":1,"tag":"entry","up":9223372036854775807,"down":1}]`,
		"fraction":                `[{"id":1,"tag":"entry","up":1.5,"down":0}]`,
		"remote node":             `[{"id":1,"nodeId":2,"tag":"entry","up":1,"down":0}]`,
		"duplicate tag":           `[{"id":1,"tag":"entry","up":1,"down":0},{"id":2,"tag":"entry","up":1,"down":0}]`,
		"duplicate id":            `[{"id":1,"tag":"entry","up":1,"down":0},{"id":1,"tag":"other","up":1,"down":0}]`,
		"missing client counters": `[{"id":1,"tag":"entry","up":1,"down":0,"clientStats":[{"id":2,"inboundId":1,"email":"alice"}]}]`,
		"conflicting client":      `[{"id":1,"tag":"entry","up":1,"down":0,"clientStats":[{"id":2,"inboundId":1,"email":"alice","up":2,"down":1},{"id":2,"inboundId":1,"email":"alice","up":3,"down":1}]}]`,
		"reused client id":        `[{"id":1,"tag":"entry","up":1,"down":0,"clientStats":[{"id":2,"inboundId":1,"email":"alice","up":2,"down":1},{"id":2,"inboundId":1,"email":"bob","up":2,"down":1}]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			client := newTrafficTestClient(t, map[string]string{inboundTrafficEndpoint: object})
			got, err := client.ClientTraffics(context.Background())
			var contract *ContractError
			if !errors.As(err, &contract) || got != nil {
				t.Fatalf("invalid response returned counters %#v, error %v", got, err)
			}
		})
	}
}

func TestTrafficSnapshotDoesNotExposePartialRound(t *testing.T) {
	client := newTrafficTestClient(t, map[string]string{
		inboundTrafficEndpoint:  `[{"id":1,"tag":"entry","up":1,"down":0}]`,
		outboundTrafficEndpoint: `[{"id":2,"tag":"direct","up":-1,"down":0}]`,
	})
	got, err := client.TrafficSnapshot(context.Background())
	if err == nil || got.Clients != nil || got.Inbounds != nil || got.Outbounds != nil {
		t.Fatalf("partial failed snapshot = %#v, error %v", got, err)
	}
}

func TestTrafficHonoursResponseLimit(t *testing.T) {
	client := newTrafficTestClient(t, map[string]string{outboundTrafficEndpoint: `[{"id":1,"tag":"direct","up":1,"down":0}]`})
	client.maxResponseBytes = 16
	_, err := client.OutboundTraffics(context.Background())
	var tooLarge *ResponseTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected size limit, got %v", err)
	}
}

func TestTrafficErrorsDoNotEchoCredentialFields(t *testing.T) {
	client := newTrafficTestClient(t, map[string]string{inboundTrafficEndpoint: `[{"id":1,"tag":"entry","up":"unrelated-private-key","down":0}]`})
	_, err := client.InboundTraffics(context.Background())
	if err == nil || strings.Contains(err.Error(), "unrelated-private-key") || strings.Contains(err.Error(), testToken) {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestTrafficProviderFailuresCannotEchoArbitrarySecrets(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusInternalServerError} {
		client, err := New(Config{BaseURL: "http://127.0.0.1:2053/private/", Token: testToken,
			HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(
					`{"success":false,"msg":"unrelated-private-key","obj":{"password":"arbitrary-password"}}`))}, nil
			})}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.ClientTraffics(context.Background())
		if err == nil || strings.Contains(err.Error(), "unrelated-private-key") || strings.Contains(err.Error(), "arbitrary-password") {
			t.Fatalf("provider failure leaked sensitive material: %v", err)
		}
	}
}
