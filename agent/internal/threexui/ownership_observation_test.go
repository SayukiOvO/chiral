package threexui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const observationOpenAPI = `{"openapi":"3.0.3","paths":{"/panel/api/server/status":{"get":{}},"/panel/api/inbounds/list":{"get":{}},"/panel/api/clients/list":{"get":{}},"/panel/api/xray/":{"post":{}},"/panel/api/server/getConfigJson":{"get":{}}}}`

type ownershipObservationFixture struct {
	mu        sync.Mutex
	client    *Client
	scope     *OwnershipScope
	request   OwnershipObservationRequest
	inbounds  []Inbound
	clients   []GlobalClient
	template  json.RawMessage
	config    json.RawMessage
	url       string
	calls     map[string]int
	sequence  []string
	hook      func(string, int, *ownershipObservationFixture)
	status    Status
	document  string
	failRoute string
}

func newOwnershipObservationFixture(t *testing.T) *ownershipObservationFixture {
	t.Helper()
	scope, keys, inbounds, clients := ownershipFixture(t)
	f := &ownershipObservationFixture{
		scope: scope, inbounds: inbounds, clients: clients,
		template: json.RawMessage(`{"inbounds":[],"outbounds":[{"tag":"direct","protocol":"freedom"}]}`),
		config:   json.RawMessage(`{"inbounds":[{"tag":"owned-inbound","settings":{"clients":[{"id":"private-fixture-credential"}]}}],"stats":{}}`),
		url:      "https://probe.example.test/check", calls: make(map[string]int), document: observationOpenAPI,
		status: Status{PanelGUID: "fixture-panel", PanelVersion: "3.8.5", Xray: XrayStatus{State: "running", Version: "26.9.9"}},
	}
	f.client, _ = mutationClient(t, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/base/panel/api/")
		f.calls[path]++
		f.sequence = append(f.sequence, path)
		if f.hook != nil {
			f.hook(path, f.calls[path], f)
		}
		method := http.MethodGet
		if path == "xray/" {
			method = http.MethodPost
		}
		if r.Method != method {
			t.Error("candidate observation changed a route method")
		}
		if f.failRoute == path {
			fmt.Fprint(w, `{"success":false,"msg":"private-provider-fixture-credential"}`)
			return
		}
		write := func(value any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": value}) }
		switch path {
		case "openapi.json":
			fmt.Fprint(w, f.document)
		case "server/status":
			write(f.status)
		case "inbounds/list":
			write(f.inbounds)
		case "clients/list":
			write(f.clients)
		case "xray/":
			data, _ := json.Marshal(map[string]any{"xraySetting": f.template, "outboundTestUrl": f.url})
			write(string(data))
		case "server/getConfigJson":
			write(f.config)
		default:
			t.Error("candidate observation dispatched an unexpected route")
			http.NotFound(w, r)
		}
	})
	f.request = OwnershipObservationRequest{
		Binding:              OwnershipStoreBinding{EndpointHash: updateDigest(f.client.base.String()), PanelGUID: f.status.PanelGUID},
		ExpectedPanelVersion: f.status.PanelVersion, ExpectedOpenAPIDigest: comparisonDigest([]byte(f.document)), Keys: keys,
	}
	return f
}

func TestOwnershipObservationUsesOnlyExplicitCandidateReadback(t *testing.T) {
	f := newOwnershipObservationFixture(t)
	got, err := f.client.ObserveOwnership(context.Background(), f.scope, f.request)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{"openapi.json", "server/status", "inbounds/list", "clients/list", "xray/", "server/getConfigJson",
		"inbounds/list", "clients/list", "xray/", "server/getConfigJson", "openapi.json", "server/status"}
	if !reflect.DeepEqual(f.sequence, wantCalls) {
		t.Fatal("candidate readback order or route set differs")
	}
	config, _ := CompareConfig(f.config, f.config)
	if got.Checkpoint.ConfigDigest != config.ActualDigest || !ownershipHex(got.Checkpoint.TemplateDigest, 32) ||
		!reflect.DeepEqual(got.Checkpoint.Keys, f.request.Keys) || got.Inventory.InboundIDs["owned-inbound"] != 7 ||
		got.Inventory.ClientIDs["owned@credential"] != 9 {
		t.Fatal("candidate evidence did not preserve exact keys, resolved IDs and configuration digest")
	}
	got.Checkpoint.Keys.InboundTags[0] = "changed"
	got.Checkpoint.Keys.Clients[0].Email = "changed"
	if f.request.Keys.InboundTags[0] != "owned-inbound" || f.request.Keys.Clients[0].Email != "owned@credential" {
		t.Fatal("candidate evidence shares caller-owned mutable slices")
	}
}

func TestOwnershipObservationRejectsInvalidRequestsBeforeHTTP(t *testing.T) {
	for _, change := range []func(*ownershipObservationFixture){
		func(f *ownershipObservationFixture) { f.scope = nil },
		func(f *ownershipObservationFixture) { f.request.Binding.EndpointHash = strings.Repeat("0", 64) },
		func(f *ownershipObservationFixture) { f.request.Binding.PanelGUID = "other-instance" },
		func(f *ownershipObservationFixture) { f.request.ExpectedOpenAPIDigest = "invalid" },
		func(f *ownershipObservationFixture) { f.request.ExpectedPanelVersion = "latest" },
		func(f *ownershipObservationFixture) {
			f.request.Keys.InboundTags = append(f.request.Keys.InboundTags, "owned-inbound")
		},
		func(f *ownershipObservationFixture) { f.request.Keys.Clients[0].InboundTag = "foreign-inbound" },
	} {
		f := newOwnershipObservationFixture(t)
		change(f)
		_, err := f.client.ObserveOwnership(context.Background(), f.scope, f.request)
		if err == nil || len(f.sequence) != 0 {
			t.Fatal("invalid precondition dispatched HTTP")
		}
	}
	f := newOwnershipObservationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.client.ObserveOwnership(ctx, f.scope, f.request); err == nil || len(f.sequence) != 0 {
		t.Fatal("cancelled precondition dispatched HTTP")
	}
	for _, client := range []*Client{nil, {}} {
		if _, err := client.ObserveOwnership(context.Background(), f.scope, f.request); err == nil {
			t.Fatal("uninitialized client returned evidence")
		}
	}
}

func TestOwnershipObservationRejectsUnreviewedOrMissingContractBeforeInventory(t *testing.T) {
	for _, missing := range []string{"unreviewed", "server/status", "inbounds/list", "clients/list", "xray/", "server/getConfigJson"} {
		t.Run(missing, func(t *testing.T) {
			f := newOwnershipObservationFixture(t)
			if missing == "unreviewed" {
				f.document += " "
			} else {
				f.document = strings.Replace(f.document, `"/panel/api/`+missing+`"`, `"/panel/api/unavailable"`, 1)
				f.request.ExpectedOpenAPIDigest = comparisonDigest([]byte(f.document))
			}
			if _, err := f.client.ObserveOwnership(context.Background(), f.scope, f.request); err == nil || len(f.sequence) != 1 {
				t.Fatal("unknown contract reached inventory or produced evidence")
			}
		})
	}
}

func TestOwnershipObservationRejectsIdentityAndStateDrift(t *testing.T) {
	for _, tc := range []struct {
		name  string
		route string
		edit  func(*ownershipObservationFixture)
	}{
		{"instance", "server/status", func(f *ownershipObservationFixture) { f.status.PanelGUID = "changed" }},
		{"version", "server/status", func(f *ownershipObservationFixture) { f.status.PanelVersion = "3.8.6" }},
		{"process", "server/status", func(f *ownershipObservationFixture) { f.status.Xray.State = "stopped" }},
		{"kernel", "server/status", func(f *ownershipObservationFixture) { f.status.Xray.Version = "26.9.8" }},
		{"contract", "openapi.json", func(f *ownershipObservationFixture) { f.document += " " }},
		{"configuration", "server/getConfigJson", func(f *ownershipObservationFixture) { f.config = json.RawMessage(`{"stats":{},"inbounds":[]}`) }},
		{"template", "xray/", func(f *ownershipObservationFixture) { f.template = json.RawMessage(`{"inbounds":[]}`) }},
		{"probe_url", "xray/", func(f *ownershipObservationFixture) { f.url += "?changed=true" }},
		{"foreign_identity", "inbounds/list", func(f *ownershipObservationFixture) { f.inbounds[1].Remark = "new-foreign-remark" }},
		{"owned_marker", "inbounds/list", func(f *ownershipObservationFixture) { f.inbounds[0].Remark = "foreign" }},
		{"client_marker", "clients/list", func(f *ownershipObservationFixture) { f.clients[0].Comment = "foreign" }},
		{"cross_attachment", "clients/list", func(f *ownershipObservationFixture) { f.clients[1].InboundIDs = []int64{7} }},
		{"changed_ids", "inbounds/list", func(f *ownershipObservationFixture) {
			f.inbounds[0].ID, f.clients[0].ID = 107, 109
			f.clients[0].InboundIDs = []int64{107}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOwnershipObservationFixture(t)
			f.hook = func(path string, count int, f *ownershipObservationFixture) {
				if path == tc.route && count == 2 {
					tc.edit(f)
				}
			}
			got, err := f.client.ObserveOwnership(context.Background(), f.scope, f.request)
			if err == nil || !reflect.DeepEqual(got, OwnershipObservation{}) {
				t.Fatal("drift returned success or partial evidence")
			}
		})
	}
}

func TestOwnershipObservationIgnoresListOrderAndCounterChanges(t *testing.T) {
	f := newOwnershipObservationFixture(t)
	f.hook = func(path string, count int, f *ownershipObservationFixture) {
		if count != 2 {
			return
		}
		switch path {
		case "inbounds/list":
			f.inbounds[0], f.inbounds[1] = f.inbounds[1], f.inbounds[0]
		case "clients/list":
			f.clients[0], f.clients[1] = f.clients[1], f.clients[0]
		case "server/getConfigJson":
			// Same JSON object fields in a different order.
			f.config = json.RawMessage(`{"stats":{},"inbounds":[{"settings":{"clients":[{"id":"private-fixture-credential"}]},"tag":"owned-inbound"}]}`)
		}
	}
	if _, err := f.client.ObserveOwnership(context.Background(), f.scope, f.request); err != nil {
		t.Fatal(err)
	}
	inbounds := append([]Inbound{}, f.inbounds...)
	inbounds[0].Raw = json.RawMessage(`{"up":42,"down":67}`)
	clients := append([]GlobalClient{}, f.clients...)
	clients[0].Raw = json.RawMessage(`{"up":42,"down":67}`)
	if observationInventoryDigest(inbounds, clients) != observationInventoryDigest(f.inbounds, f.clients) {
		t.Fatal("changing telemetry modified the identity fingerprint")
	}
}

func TestOwnershipObservationRedactsFailuresAndRejectsAmbiguousConfig(t *testing.T) {
	for _, route := range []string{"inbounds/list", "clients/list", "xray/", "server/getConfigJson"} {
		f := newOwnershipObservationFixture(t)
		f.failRoute = route
		got, err := f.client.ObserveOwnership(context.Background(), f.scope, f.request)
		if err == nil || !reflect.DeepEqual(got, OwnershipObservation{}) || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), testToken) {
			t.Fatal("provider failure returned partial data or leaked an input")
		}
	}
	for _, badTemplate := range []bool{true, false} {
		f := newOwnershipObservationFixture(t)
		if badTemplate {
			f.template = json.RawMessage(`{"private-secret":1,"private-secret":2}`)
		} else {
			f.config = json.RawMessage(`{"private-secret":1,"private-secret":2}`)
		}
		if _, err := f.client.ObserveOwnership(context.Background(), f.scope, f.request); err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("ambiguous JSON accepted or leaked a key")
		}
	}
	// A foreign ownership conflict must stop before maintenance-capable reads.
	f := newOwnershipObservationFixture(t)
	f.inbounds[0].Remark = "foreign"
	if _, err := f.client.ObserveOwnership(context.Background(), f.scope, f.request); err == nil || f.calls["xray/"] != 0 || f.calls["server/getConfigJson"] != 0 {
		t.Fatal("ownership conflict reached maintenance-capable readback")
	}
}

func TestOwnershipStoreAndHTTPReadbackRetainIdentityAcrossRestart(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("durable ownership store is supported only on Linux and macOS")
	}
	f := newOwnershipObservationFixture(t)
	wantInbounds, wantClients := f.inbounds, f.clients
	wantConfig := f.config
	wantKeys := observationKeys(f.request.Keys)
	f.inbounds, f.clients = []Inbound{}, []GlobalClient{}
	f.config = json.RawMessage(`{"inbounds":[],"stats":{}}`)
	f.request.Keys = OwnedKeys{}
	initial, err := f.client.ObserveOwnership(context.Background(), f.scope, f.request)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ownership")
	store, err := CreateOwnershipStore(path, f.request.Binding, initial.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope, err := store.Scope()
	if err != nil {
		t.Fatal(err)
	}
	wantInbounds[0].Remark, _ = scope.InboundMarker(wantInbounds[0].Tag)
	wantClients[0].Comment, _ = scope.ClientMarker(wantClients[0].Email)
	comparison, _ := CompareConfig(wantConfig, wantConfig)
	after := OwnershipCheckpoint{Keys: wantKeys, TemplateDigest: initial.Checkpoint.TemplateDigest, ConfigDigest: comparison.ActualDigest}
	if err := store.Prepare("fixture-change", 0, comparison.ActualDigest, after); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenOwnershipStore(path, f.request.Binding)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.Pending()
	if err != nil || pending == nil || pending.ExpectedGeneration != 0 {
		t.Fatal("pending intent was lost across restart")
	}
	scope, err = store.Scope()
	if err != nil {
		t.Fatal(err)
	}
	marker, _ := scope.InboundMarker(wantInbounds[0].Tag)
	if marker != wantInbounds[0].Remark {
		t.Fatal("restart regenerated the marker identity")
	}
	// Simulate provider readback after an ambiguous request. The test does not
	// dispatch a provider mutation or claim that a live operation completed.
	f.inbounds, f.clients, f.config = wantInbounds, wantClients, wantConfig
	f.request.Keys = pending.After.Keys
	observed, err := f.client.ObserveOwnership(context.Background(), scope, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete("fixture-change", false, observed.Checkpoint); err == nil {
		t.Fatal("matching readback cleared an unacknowledged request")
	}
	if err := store.Prepare("different-change", 0, comparison.ActualDigest, after); err == nil {
		t.Fatal("pending request permitted another write intent")
	}
	// Explicit synthetic terminal evidence exercises checkpoint advancement;
	// it is not inferred from observed equality or a current version number.
	if err := store.Complete("fixture-change", true, observed.Checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenOwnershipStore(path, f.request.Binding)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, generation, err := store.Checkpoint()
	if err != nil || generation != 1 {
		t.Fatal("completed checkpoint was not retained")
	}
	if err := store.Prepare("fixture-change", generation, comparison.ActualDigest, after); err == nil {
		t.Fatal("completed operation was replayable after restart")
	}
	f.request.Keys = checkpoint.Keys
	// Database IDs change, but stable metadata and proxy configuration do not.
	f.inbounds[0].ID, f.clients[0].ID = 107, 109
	f.clients[0].InboundIDs = []int64{107}
	observed, err = f.client.ObserveOwnership(context.Background(), scope, f.request)
	if err != nil || observed.Inventory.InboundIDs["owned-inbound"] != 107 || observed.Inventory.ClientIDs["owned@credential"] != 109 ||
		observed.Checkpoint.ConfigDigest != checkpoint.ConfigDigest || observed.Checkpoint.TemplateDigest != checkpoint.TemplateDigest {
		t.Fatal("stable checkpoint did not survive a database ID change")
	}
}
