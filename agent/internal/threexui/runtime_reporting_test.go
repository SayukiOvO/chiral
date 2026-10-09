//go:build linux || darwin

package threexui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"
)

type runtimeReportingFixture struct {
	mu       sync.Mutex
	t        *testing.T
	client   *Client
	store    *OwnershipStore
	reporter *RuntimeReporting
	dir      string
	binding  OwnershipStoreBinding
	keys     OwnedKeys
	status   Status
	inbounds []Inbound
	clients  []GlobalClient
	counters []ClientTraffic
	calls    map[string]int
	down     bool
	onlines  map[string]any
	ips      map[string]any
	hook     func(*runtimeReportingFixture, string)
}

func newRuntimeReportingFixture(t *testing.T) *runtimeReportingFixture {
	t.Helper()
	f := &runtimeReportingFixture{t: t, dir: filepath.Join(t.TempDir(), "owner"), calls: map[string]int{},
		status: Status{PanelGUID: "runtime-reporting-panel", PanelVersion: "3.8.5", Xray: XrayStatus{State: "running", Version: "26.9.9"}}}
	client, err := New(Config{BaseURL: "http://127.0.0.1:2053/base", Token: testToken,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			path := strings.TrimPrefix(request.URL.Path, "/base/panel/api/")
			f.calls[path]++
			if f.down {
				return nil, fmt.Errorf("provider unavailable")
			}
			if f.hook != nil {
				f.hook(f, path)
			}
			if request.Header.Get("Authorization") != "Bearer "+testToken {
				t.Error("HTTP reporter did not authenticate")
			}
			method := http.MethodGet
			if path == "clients/onlinesByGuid" || path == "clients/clientIpsByGuid" {
				method = http.MethodPost
			}
			if request.Method != method {
				t.Error("reporting changed an endpoint method")
			}
			var object any
			switch path {
			case "server/status":
				object = f.status
			case "inbounds/list":
				rows := make([]map[string]any, 0, len(f.inbounds))
				for _, inbound := range f.inbounds {
					raw, _ := json.Marshal(inbound)
					var row map[string]any
					_ = json.Unmarshal(raw, &row)
					row["up"], row["down"], row["nodeId"] = 0, 0, 0
					stats := make([]map[string]any, 0)
					for _, counter := range f.counters {
						if counter.InboundID == inbound.ID {
							stats = append(stats, map[string]any{"id": counter.ID, "inboundId": counter.InboundID,
								"email": counter.Email, "up": counter.Up, "down": counter.Down})
						}
					}
					row["clientStats"] = stats
					rows = append(rows, row)
				}
				object = rows
			case "clients/list":
				object = f.clients
			case "openapi.json":
				return fixtureReportingResponse(`{"openapi":"3.0.3","paths":{"/panel/api/server/status":{"get":{}},"/panel/api/clients/onlinesByGuid":{"post":{}},"/panel/api/clients/clientIpsByGuid":{"post":{}}}}`), nil
			case "clients/onlinesByGuid":
				object = f.onlines
			case "clients/clientIpsByGuid":
				object = f.ips
			default:
				t.Errorf("reporter used an unapproved route: %s", path)
				return nil, fmt.Errorf("unexpected route")
			}
			raw, _ := json.Marshal(map[string]any{"success": true, "obj": object})
			return fixtureReportingResponse(string(raw)), nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	f.client = client
	f.binding = OwnershipStoreBinding{EndpointHash: updateDigest(client.base.String()), PanelGUID: f.status.PanelGUID}
	f.store, err = CreateOwnershipStore(f.dir, f.binding,
		OwnershipCheckpoint{TemplateDigest: updateDigest("empty-template"), ConfigDigest: updateDigest("empty-config")})
	if err != nil {
		t.Fatal(err)
	}
	f.reporter, err = NewRuntimeReporting(client, f.store, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.reporter.Close(); _ = f.store.Close() })
	scope, err := f.store.Scope()
	if err != nil {
		t.Fatal(err)
	}
	inboundMarker, _ := scope.InboundMarker("owned-inbound")
	clientMarker, _ := scope.ClientMarker("owned@credential")
	f.keys = OwnedKeys{InboundTags: []string{"owned-inbound"}, Clients: []OwnedClientKey{{Email: "owned@credential", InboundTag: "owned-inbound"}}}
	f.inbounds = []Inbound{{ID: 7, Tag: "owned-inbound", Remark: inboundMarker}, {ID: 8, Tag: "foreign-inbound", Remark: "external-operator"}}
	f.clients = []GlobalClient{{ID: 9, Email: "owned@credential", Comment: clientMarker, InboundIDs: []int64{7}},
		{ID: 10, Email: "foreign@credential", Comment: "external-operator", InboundIDs: []int64{8}}}
	f.counters = []ClientTraffic{{ID: 11, InboundID: 7, Email: "owned@credential", Up: 100, Down: 200},
		{ID: 12, InboundID: 8, Email: "foreign@credential", Up: 99999, Down: 88888}}
	f.onlines = map[string]any{f.binding.PanelGUID: []string{"owned@credential", "foreign@credential"}, "descendant-guid": []string{"owned@credential"}}
	f.ips = map[string]any{f.binding.PanelGUID: map[string]any{"owned@credential": []map[string]any{{"ip": "192.0.2.1", "timestamp": 1234}}},
		"descendant-guid": map[string]any{"owned@credential": []map[string]any{{"ip": "192.0.2.2", "timestamp": 1234}}}}
	f.complete("create", f.keys)
	if err := f.reporter.CaptureOwnership(context.Background(), f.keys); err != nil {
		t.Fatal(err)
	}
	return f
}

func fixtureReportingResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func (f *runtimeReportingFixture) complete(operation string, keys OwnedKeys) {
	f.t.Helper()
	_, generation, err := f.store.Checkpoint()
	if err != nil {
		f.t.Fatal(err)
	}
	after := OwnershipCheckpoint{Keys: keys, TemplateDigest: updateDigest("template-" + operation), ConfigDigest: updateDigest("config-" + operation)}
	if err := f.store.Prepare(operation, generation, updateDigest("request-"+operation), after); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.Complete(operation, true, after); err != nil {
		f.t.Fatal(err)
	}
	f.keys = observationKeys(keys)
}

func (f *runtimeReportingFixture) sample() *runtimeprovider.AccountingBatch {
	f.t.Helper()
	batch, err := f.reporter.StatsBatch(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return batch
}

func TestRuntimeReportingPersistsOriginalBindingsAndOnlyBillsLocalOwnedCounters(t *testing.T) {
	f := newRuntimeReportingFixture(t)
	if batch := f.sample(); batch != nil {
		t.Fatal("first official cumulative sample must establish a baseline")
	}
	f.mu.Lock()
	f.counters[0].Up += 10
	f.counters[0].Down += 20
	f.counters[1].Down += 1000
	f.mu.Unlock()
	batch := f.sample()
	if batch == nil || !reflect.DeepEqual(batch.Entries, []runtimeprovider.CounterDelta{{Key: "user>>>owned@credential", Up: 10, Down: 20}}) {
		t.Fatal("HTTP cumulative sample did not retain only the owned local delta")
	}
	_, _, state, _, err := f.store.reportingSnapshot()
	if err != nil || state == nil || !reflect.DeepEqual(state.Owners, []TrafficOwnership{{PanelGUID: f.binding.PanelGUID,
		InboundTag: "owned-inbound", InboundID: 7, ClientID: 9, StatID: 11, Email: "owned@credential"}}) {
		t.Fatal("original panel, inbound, account and statistic identities were not persisted")
	}
	if _, err := NewRuntimeReporting(f.client, f.store, false); err == nil {
		t.Fatal("a second independent reporter opened the same locked journal")
	}
	f.mu.Lock()
	calls := len(f.calls)
	f.down = true
	f.mu.Unlock()
	if replay := f.sample(); !reflect.DeepEqual(batch, replay) {
		t.Fatal("provider outage changed the retained batch")
	}
	f.mu.Lock()
	if len(f.calls) != calls {
		t.Fatal("pending replay unnecessarily depended on provider availability")
	}
	f.mu.Unlock()
	batch.Entries[0].Down = 99999
	if replay := f.sample(); replay.Entries[0].Down != 20 {
		t.Fatal("a caller rewrote an immutable retained batch")
	}
}

func TestRuntimeReportingRetirementRetainsPendingAndAttributesLaterBytesAcrossRestart(t *testing.T) {
	f := newRuntimeReportingFixture(t)
	if f.sample() != nil {
		t.Fatal("initial snapshot produced a bill")
	}
	f.counters[0].Down += 20
	first := f.sample()
	f.counters[0].Down += 25
	if err := f.reporter.PrepareRetirement(context.Background(), "owned-inbound", "owned@credential"); err != nil {
		t.Fatal(err)
	}
	_, _, state, _, _ := f.store.reportingSnapshot()
	if len(state.Owners) != 1 || !state.Owners[0].Retired || !reflect.DeepEqual(first, f.sample()) {
		t.Fatal("retirement lost its retained owner or pretended a pending sample was fresh")
	}
	// Simulate the complete official deletion response followed by checkpoint
	// completion. Retained raw local statistics remain on the original inbound.
	f.clients = f.clients[1:]
	keys := observationKeys(f.keys)
	keys.Clients = []OwnedClientKey{}
	f.complete("delete", keys)
	if err := f.reporter.CaptureOwnership(context.Background(), keys); err != nil {
		t.Fatal(err)
	}
	f.counters[0].Down += 15
	if err := f.reporter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.store, err = OpenOwnershipStore(f.dir, f.binding)
	if err != nil {
		t.Fatal(err)
	}
	f.reporter, err = NewRuntimeReporting(f.client, f.store, false)
	if err != nil {
		t.Fatal(err)
	}
	if replay := f.sample(); !reflect.DeepEqual(first, replay) {
		t.Fatal("restart did not replay the exact durable batch")
	}
	if err := f.reporter.AcknowledgeStats(strings.Repeat("b", 32), first.Sequence); err == nil {
		t.Fatal("another reporter's ACK released retained bytes")
	}
	if err := f.reporter.AcknowledgeStats(first.ReporterID, first.Sequence+1); err == nil {
		t.Fatal("another sequence's ACK released retained bytes")
	}
	if err := f.reporter.AcknowledgeStats(first.ReporterID, first.Sequence); err != nil {
		t.Fatal(err)
	}
	tail := f.sample()
	if tail == nil || tail.Sequence != first.Sequence+1 || len(tail.Entries) != 1 || tail.Entries[0].Down != 40 {
		t.Fatal("pre-delete unsampled bytes and post-delete bytes did not retain the original owner")
	}
	if err := f.reporter.AcknowledgeStats(tail.ReporterID, tail.Sequence); err != nil {
		t.Fatal(err)
	}
	retained := f.counters[0]
	f.counters = f.counters[1:]
	if f.sample() != nil {
		t.Fatal("an absent retired counter fabricated zero or a delta")
	}
	retained.Down += 30
	f.counters = append(f.counters, retained)
	if restored := f.sample(); restored == nil || restored.Entries[0].Down != 30 {
		t.Fatal("temporarily absent retired statistics discarded the original watermark")
	}
	if err := f.reporter.CheckClientKey("owned-inbound", "owned@credential"); err == nil {
		t.Fatal("retired email was available for automatic reassignment")
	}
	if err := f.reporter.CheckClientKey("owned-inbound", "new-authorized@credential"); err != nil {
		t.Fatal("a fresh distinct credential identity was incorrectly rejected")
	}
}

func TestRuntimeReportingRejectsChangedOrUnknownOwnershipBeforeAdvancingWatermarks(t *testing.T) {
	for name, edit := range map[string]func(*runtimeReportingFixture){
		"panel_replaced":    func(f *runtimeReportingFixture) { f.status.PanelGUID = "another-panel" },
		"counter_recreated": func(f *runtimeReportingFixture) { f.counters[0].ID += 100 },
		"counter_missing":   func(f *runtimeReportingFixture) { f.counters = f.counters[1:] },
		"client_recreated":  func(f *runtimeReportingFixture) { f.clients[0].ID += 100 },
		"account_moved":     func(f *runtimeReportingFixture) { f.clients[0].InboundIDs = []int64{8} },
		"marker_changed":    func(f *runtimeReportingFixture) { f.clients[0].Comment = "external-operator" },
		"unknown_owned_counter": func(f *runtimeReportingFixture) {
			f.counters = append(f.counters, ClientTraffic{ID: 50, InboundID: 7, Email: "unknown@credential", Up: 1000})
		},
		"row_reused": func(f *runtimeReportingFixture) { f.counters[0].Email = "new@credential" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newRuntimeReportingFixture(t)
			if f.sample() != nil {
				t.Fatal("initial snapshot produced a bill")
			}
			original, err := os.ReadFile(filepath.Join(f.dir, runtimeAccountingFilename))
			if err != nil {
				t.Fatal(err)
			}
			f.counters[0].Down += 40
			edit(f)
			if err := f.reporter.VerifyOwnership(context.Background()); err == nil {
				t.Fatal("changed numeric or unknown ownership passed the pre-write guard")
			}
			if batch, err := f.reporter.StatsBatch(context.Background()); err == nil || batch != nil {
				t.Fatal("changed or unknown ownership produced a batch")
			}
			current, _ := os.ReadFile(filepath.Join(f.dir, runtimeAccountingFilename))
			if string(current) != string(original) {
				t.Fatal("rejected inventory advanced accounting watermarks")
			}
		})
	}
}

func TestRuntimeReportingRecentObservationsExcludeRetiredAndDescendantIdentities(t *testing.T) {
	f := newRuntimeReportingFixture(t)
	if f.sample() != nil {
		t.Fatal("initial snapshot produced a bill")
	}
	sample, err := f.reporter.RecentOnline(context.Background())
	if err != nil || sample.RetrievedAt.IsZero() || !reflect.DeepEqual(sample.Users,
		[]runtimeprovider.RecentOnlineUser{{Email: "owned@credential", ObservedOnline: true, IPs: []string{"192.0.2.1"}}}) {
		t.Fatal("recent observation did not retain only the owned local cached information")
	}
	if err := f.reporter.PrepareRetirement(context.Background(), "owned-inbound", "owned@credential"); err != nil {
		t.Fatal(err)
	}
	f.clients = f.clients[1:]
	keys := observationKeys(f.keys)
	keys.Clients = []OwnedClientKey{}
	f.complete("retire", keys)
	if err := f.reporter.CaptureOwnership(context.Background(), keys); err != nil {
		t.Fatal(err)
	}
	if retired, err := f.reporter.RecentOnline(context.Background()); err != nil || len(retired.Users) != 0 {
		t.Fatal("a retired billing owner was reused as an online authorization identity")
	}
}

func TestRuntimeReportingMissingOrChangedJournalCannotBeResetOrOverwritten(t *testing.T) {
	for name, mutate := range map[string]func(string) error{
		"missing":    os.Remove,
		"corrupt":    func(path string) error { return os.WriteFile(path, []byte(`{"secret":"do-not-print"}`), 0o600) },
		"permissive": func(path string) error { return os.Chmod(path, 0o644) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newRuntimeReportingFixture(t)
			path := filepath.Join(f.dir, runtimeAccountingFilename)
			if err := mutate(path); err != nil {
				t.Fatal(err)
			}
			before, readErr := os.ReadFile(path)
			if _, err := f.reporter.StatsBatch(context.Background()); err == nil {
				t.Fatal("lost or damaged live journal continued sampling")
			}
			_ = f.reporter.Close()
			if _, err := NewRuntimeReporting(f.client, f.store, false); err == nil {
				t.Fatal("restore silently reset a lost or damaged journal")
			}
			if _, err := NewRuntimeReporting(f.client, f.store, true); err == nil {
				t.Fatal("initialization silently reset a previous owner")
			}
			after, afterErr := os.ReadFile(path)
			if (readErr == nil) != (afterErr == nil) || string(before) != string(after) {
				t.Fatal("failed restore overwrote the retained journal")
			}
		})
	}
}

func TestRuntimeReportingInventoryDriftReturnsNoPartialSample(t *testing.T) {
	f := newRuntimeReportingFixture(t)
	f.sample()
	f.counters[0].Down += 40
	f.hook = func(f *runtimeReportingFixture, path string) {
		if path == "clients/list" && f.calls[path]%2 == 0 {
			f.clients[0].ID++
		}
	}
	if batch, err := f.reporter.StatsBatch(context.Background()); err == nil || batch != nil {
		t.Fatal("changing original client identity within a round produced a partial batch")
	}
}

func TestRuntimeReportingCaptureUnderPendingManifestKeepsTerminalBarrier(t *testing.T) {
	f := newRuntimeReportingFixture(t)
	f.sample()
	keys := observationKeys(f.keys)
	keys.Clients = append(keys.Clients, OwnedClientKey{Email: "new@credential", InboundTag: "owned-inbound"})
	checkpoint, generation, err := f.store.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	after := cloneOwnershipCheckpoint(checkpoint)
	after.Keys = keys
	after.ConfigDigest = updateDigest("candidate-with-new-client")
	if err := f.store.Prepare("add-under-barrier", generation, updateDigest("request"), after); err != nil {
		t.Fatal(err)
	}
	scope, _ := f.store.Scope()
	marker, _ := scope.ClientMarker("new@credential")
	f.clients = append(f.clients, GlobalClient{ID: 30, Email: "new@credential", Comment: marker, InboundIDs: []int64{7}})
	f.counters = append(f.counters, ClientTraffic{ID: 40, Email: "new@credential", InboundID: 7, Down: 10})
	wrongKeys := observationKeys(keys)
	wrongKeys.Clients[1].Email = "unrecorded@credential"
	if err := f.reporter.CaptureOwnership(context.Background(), wrongKeys); err == nil {
		t.Fatal("an unrecorded desired identity was adopted")
	}
	if err := f.reporter.CaptureOwnership(context.Background(), keys); err != nil {
		t.Fatal(err)
	}
	retained, err := f.store.Pending()
	if err != nil || retained == nil || retained.OperationIDHash != updateDigest("add-under-barrier") {
		t.Fatal("capturing a pending identity released an unconfirmed write")
	}
	current, _, _ := f.store.Checkpoint()
	if !reflect.DeepEqual(current, checkpoint) {
		t.Fatal("capturing pending identities advanced the configuration checkpoint")
	}
	if batch, err := f.reporter.StatsBatch(context.Background()); err == nil || batch != nil {
		t.Fatal("unconfirmed new manifest advanced accounting counters")
	}
	if err := f.store.Complete("add-under-barrier", false, after); err == nil {
		t.Fatal("capturing ownership substituted for the missing successful HTTP response")
	}
	if err := f.store.Complete("add-under-barrier", true, after); err != nil {
		t.Fatal(err)
	}
	if batch := f.sample(); batch == nil || len(batch.Entries) != 1 || batch.Entries[0].Key != "user>>>new@credential" || batch.Entries[0].Down != 10 {
		t.Fatal("explicitly completed new identity did not become available for billing")
	}
}

func TestRuntimeReportingRejectsForeignJournalBeforeReplayingIt(t *testing.T) {
	f := newRuntimeReportingFixture(t)
	f.sample()
	f.counters[0].Down += 30
	if f.sample() == nil {
		t.Fatal("test did not retain a pending delivery")
	}
	_ = f.reporter.Close()
	path := filepath.Join(f.dir, runtimeAccountingFilename)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state["provider_identity"] = "different-provider"
	foreign, _ := json.Marshal(state)
	if err := os.WriteFile(path, foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRuntimeReporting(f.client, f.store, false); err == nil {
		t.Fatal("a pending delivery from another provider was opened for replay")
	}
	retained, _ := os.ReadFile(path)
	if string(retained) != string(foreign) {
		t.Fatal("foreign-provider journal was rewritten during rejection")
	}
}

func TestRuntimeReportingRetiredIdentitiesCannotBeClaimedByExternalRows(t *testing.T) {
	for _, useEmail := range []bool{true, false} {
		t.Run(fmt.Sprintf("same_email_%t", useEmail), func(t *testing.T) {
			f := newRuntimeReportingFixture(t)
			f.sample()
			if err := f.reporter.PrepareRetirement(context.Background(), "owned-inbound", "owned@credential"); err != nil {
				t.Fatal(err)
			}
			f.clients = f.clients[1:]
			keys := observationKeys(f.keys)
			keys.Clients = []OwnedClientKey{}
			f.complete("retire", keys)
			if err := f.reporter.CaptureOwnership(context.Background(), keys); err != nil {
				t.Fatal(err)
			}
			external := GlobalClient{ID: 9, Email: "unrelated@credential", Comment: "external-operator", InboundIDs: []int64{8}}
			if useEmail {
				external.ID, external.Email = 50, "owned@credential"
			}
			f.clients = append(f.clients, external)
			if batch, err := f.reporter.StatsBatch(context.Background()); err == nil || batch != nil {
				t.Fatal("an external same-email or recycled-ID row claimed a retained owner")
			}
		})
	}
}

func TestRuntimeReportingExactAcknowledgementDoesNotWaitForHTTPObservation(t *testing.T) {
	f := newRuntimeReportingFixture(t)
	f.sample()
	f.counters[0].Down += 30
	pending := f.sample()
	if pending == nil {
		t.Fatal("test has no durable delivery to acknowledge")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var blockOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	f.hook = func(_ *runtimeReportingFixture, path string) {
		if path == "server/status" {
			blockOnce.Do(func() { close(entered); <-release })
		}
	}
	observed := make(chan error, 1)
	go func() { observed <- f.reporter.CaptureOwnership(context.Background(), f.keys) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP observation did not reach its controlled blocker")
	}
	acknowledged := make(chan error, 1)
	go func() { acknowledged <- f.reporter.AcknowledgeStats(pending.ReporterID, pending.Sequence) }()
	select {
	case err := <-acknowledged:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("exact Core receipt waited behind provider HTTP I/O")
	}
	if retained, err := f.reporter.accounting.PendingBatch(); err != nil || retained != nil {
		t.Fatal("the independent exact receipt was not persisted")
	}
	unblock()
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP capture did not finish after its blocker was released")
	}
}
