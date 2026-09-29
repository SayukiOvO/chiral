package threexui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type updateFixture struct {
	mu       sync.Mutex
	status   Status
	versions []string
	install  func(http.ResponseWriter, *http.Request)
	paths    map[string]any
	calls    atomic.Int32
	client   *Client
}

func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()
	f := &updateFixture{
		status:   Status{PanelGUID: "provider-guid", PanelVersion: "3.8.5", Xray: XrayStatus{State: "running", Version: "26.8.3"}},
		versions: []string{"v26.9.9", "v26.8.3"},
		paths: map[string]any{
			"/panel/api/server/status":                map[string]any{"get": map[string]any{}},
			"/panel/api/server/getXrayVersion":        map[string]any{"get": map[string]any{}},
			"/panel/api/server/installXray/{version}": map[string]any{"post": map[string]any{}},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Error("update request lost authentication")
		}
		switch r.URL.Path {
		case "/panel/api/openapi.json":
			f.mu.Lock()
			defer f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"openapi": "3.0.3", "paths": f.paths})
		case "/panel/api/server/getXrayVersion":
			f.mu.Lock()
			defer f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": f.versions})
		case "/panel/api/server/status":
			f.mu.Lock()
			defer f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": f.status})
		case "/panel/api/server/installXray/v26.9.9":
			f.calls.Add(1)
			if r.Method != http.MethodPost {
				t.Error("installation did not use POST")
			}
			if f.install != nil {
				f.install(w, r)
				return
			}
			f.setVersion("26.9.9")
			fmt.Fprint(w, `{"success":true}`)
		default:
			t.Errorf("unexpected update route: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	var err error
	f.client, err = New(Config{BaseURL: server.URL, Token: testToken, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *updateFixture) setVersion(version string) {
	f.mu.Lock()
	f.status.Xray.Version = version
	f.mu.Unlock()
}

func updateRequest() XrayUpdateRequest {
	return XrayUpdateRequest{ID: "request-1", Version: "v26.9.9", ExpectedPanelGUID: "provider-guid",
		ExpectedPanelVersion: "3.8.5", ExpectedRunningVersion: "26.8.3"}
}

func openTestUpdates(t *testing.T, path string, client *Client) *XrayUpdates {
	t.Helper()
	u, err := OpenXrayUpdates(path, client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	return u
}

func privateUpdateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func requireUpdateState(t *testing.T, result XrayUpdateResult, err error, state XrayUpdateState) {
	t.Helper()
	if result.State != state {
		t.Fatalf("update result = %+v, error = %v, want state %s", result, err, state)
	}
	if state == XrayUpdateVerified {
		if err != nil {
			t.Fatal(err)
		}
	} else {
		var failure *XrayUpdateFailure
		if !errors.As(err, &failure) || failure.Result != result {
			t.Fatalf("non-verified result must return its retained failure, got %v", err)
		}
	}
}

func TestXrayUpdatesVerifiedExactlyOnceAndReplayAfterReopen(t *testing.T) {
	f := newUpdateFixture(t)
	path := filepath.Join(privateUpdateDir(t), "updates.json")
	u := openTestUpdates(t, path, f.client)
	var probes atomic.Int32
	probe := func(context.Context) error { probes.Add(1); return nil }
	request := updateRequest()
	result, err := u.Run(context.Background(), request, probe)
	requireUpdateState(t, result, err, XrayUpdateVerified)
	if f.calls.Load() != 1 || probes.Load() != 1 {
		t.Fatalf("installs=%d probes=%d", f.calls.Load(), probes.Load())
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	u = openTestUpdates(t, path, f.client)
	replayed, err := u.Run(context.Background(), request, probe)
	if err != nil || replayed != result || f.calls.Load() != 1 || probes.Load() != 1 {
		t.Fatalf("request replay dispatched work: result=%+v err=%v installs=%d probes=%d", replayed, err, f.calls.Load(), probes.Load())
	}
	for _, mutate := range []func(*XrayUpdateRequest){
		func(r *XrayUpdateRequest) { r.Version = "v26.8.3" },
		func(r *XrayUpdateRequest) { r.ExpectedPanelGUID = "another-guid" },
		func(r *XrayUpdateRequest) { r.ExpectedPanelVersion = "3.8.4" },
		func(r *XrayUpdateRequest) { r.ExpectedRunningVersion = "26.9.9" },
	} {
		changed := request
		mutate(&changed)
		if _, err := u.Run(context.Background(), changed, probe); err == nil {
			t.Fatal("accepted changed payload with a retained request identity")
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{testToken, "provider-guid", "request-1", f.client.base.String()} {
		if strings.Contains(string(data), private) {
			t.Fatal("journal retained an identity or credential instead of its hash")
		}
	}
	for _, file := range []string{path, path + ".lock"} {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("journal/lock is not private: %v", err)
		}
	}
}

func TestXrayUpdatesTimeoutDoesNotStopServerOrPermitRedispatch(t *testing.T) {
	f := newUpdateFixture(t)
	f.client.http.Timeout = 100 * time.Millisecond
	release, finished := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	f.install = func(w http.ResponseWriter, _ *http.Request) {
		<-release // Deliberately independent of the caller's HTTP context.
		f.setVersion("26.9.9")
		close(finished)
		fmt.Fprint(w, `{"success":true}`)
	}
	path := filepath.Join(privateUpdateDir(t), "updates.json")
	u := openTestUpdates(t, path, f.client)
	request := updateRequest()
	result, err := u.Run(context.Background(), request, func(context.Context) error {
		t.Error("must not probe an installation with no terminal response")
		return nil
	})
	requireUpdateState(t, result, err, XrayUpdateUnknown)
	if f.calls.Load() != 1 {
		t.Fatal("fixture did not receive exactly one installation")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("server did not finish after the client had timed out")
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	u = openTestUpdates(t, path, f.client)
	observation, err := u.Observe(context.Background(), request.ID)
	if err != nil || !observation.ProviderMatches || !observation.RunningTarget || observation.Recorded.State != XrayUpdateUnknown {
		t.Fatalf("read-only observation lost ambiguous task semantics: %+v, %v", observation, err)
	}
	result, err = u.Run(context.Background(), request, nil)
	requireUpdateState(t, result, err, XrayUpdateUnknown)
	request.ID = "request-2"
	request.ExpectedRunningVersion = "26.9.9"
	if _, err := u.Run(context.Background(), request, nil); err == nil || f.calls.Load() != 1 {
		t.Fatal("observed target version reopened an uncertain installation gate")
	}
}

func TestXrayUpdatesMalformedAndDefiniteFailureKeepGateClosed(t *testing.T) {
	const private = "provider-private-token-and-config"
	for _, test := range []struct {
		name  string
		body  string
		state XrayUpdateState
	}{
		{"malformed", `{"success":true,"msg":"` + private, XrayUpdateUnknown},
		{"failure", `{"success":false,"msg":"` + private + `"}`, XrayUpdateFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newUpdateFixture(t)
			f.install = func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, test.body) }
			path := filepath.Join(privateUpdateDir(t), "updates.json")
			u := openTestUpdates(t, path, f.client)
			request := updateRequest()
			result, err := u.Run(context.Background(), request, nil)
			requireUpdateState(t, result, err, test.state)
			if strings.Contains(err.Error(), private) {
				t.Fatal("provider error leaked into coordinator diagnostics")
			}
			data, _ := os.ReadFile(path)
			if strings.Contains(string(data), private) {
				t.Fatal("provider body was persisted")
			}
			request.ID = "second-request"
			if _, err := u.Run(context.Background(), request, nil); err == nil || f.calls.Load() != 1 {
				t.Fatal("negative installation result allowed a following mutation")
			}
		})
	}
}

func TestXrayUpdatesPreflightRejectsMismatchWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*updateFixture, *XrayUpdateRequest)
		reason string
	}{
		{"capability", func(f *updateFixture, _ *XrayUpdateRequest) {
			delete(f.paths, "/panel/api/server/installXray/{version}")
		}, "capability_preflight_failed"},
		{"version capability", func(f *updateFixture, _ *XrayUpdateRequest) { delete(f.paths, "/panel/api/server/getXrayVersion") }, "capability_preflight_failed"},
		{"guid", func(f *updateFixture, _ *XrayUpdateRequest) { f.status.PanelGUID = "other-instance" }, "provider_identity_changed"},
		{"panel version", func(f *updateFixture, _ *XrayUpdateRequest) { f.status.PanelVersion = "3.8.6" }, "provider_identity_changed"},
		{"current version", func(f *updateFixture, _ *XrayUpdateRequest) { f.status.Xray.Version = "26.8.4" }, "running_version_changed"},
		{"stopped", func(f *updateFixture, _ *XrayUpdateRequest) { f.status.Xray.State = "stopped" }, "running_version_changed"},
		{"exact tag", func(_ *updateFixture, r *XrayUpdateRequest) { r.Version = "26.9.9" }, "target_version_not_offered"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newUpdateFixture(t)
			request := updateRequest()
			test.change(f, &request)
			u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
			result, err := u.Run(context.Background(), request, nil)
			requireUpdateState(t, result, err, XrayUpdateNotStarted)
			if result.Reason != test.reason || f.calls.Load() != 0 {
				t.Fatalf("preflight failed unsafely: %+v installs=%d", result, f.calls.Load())
			}
		})
	}
}

func TestXrayUpdatesNotStartedRetainsReplayButAllowsFreshRequest(t *testing.T) {
	f := newUpdateFixture(t)
	u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
	request := updateRequest()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	first, err := u.Run(ctx, request, nil)
	requireUpdateState(t, first, err, XrayUpdateNotStarted)
	replay, err := u.Run(context.Background(), request, nil)
	requireUpdateState(t, replay, err, XrayUpdateNotStarted)
	if replay != first || f.calls.Load() != 0 {
		t.Fatal("a retained preflight result was replayed as a mutation")
	}
	request.ID = "fresh-request"
	result, err := u.Run(context.Background(), request, func(context.Context) error { return nil })
	requireUpdateState(t, result, err, XrayUpdateVerified)
	if f.calls.Load() != 1 {
		t.Fatal("definite non-dispatch should permit a new request")
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	u = openTestUpdates(t, u.path, f.client)
	result, err = u.Run(context.Background(), request, nil)
	requireUpdateState(t, result, err, XrayUpdateVerified)
	if f.calls.Load() != 1 {
		t.Fatal("reopened preflight history was replayed")
	}
}

func TestXrayUpdatesVerificationCannotInventDataPathSuccess(t *testing.T) {
	const secret = "probe-credential-must-not-be-retained"
	for _, test := range []struct {
		name    string
		probe   XrayUpdateProbe
		version string
		state   XrayUpdateState
	}{
		{"no probe", nil, "26.9.9", XrayUpdateInconclusive},
		{"failed probe", func(context.Context) error { return errors.New(secret) }, "26.9.9", XrayUpdateFailed},
		{"wrong kernel", func(context.Context) error { t.Error("probe must not run against the wrong kernel"); return nil }, "26.8.3", XrayUpdateFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newUpdateFixture(t)
			f.install = func(w http.ResponseWriter, _ *http.Request) {
				f.setVersion(test.version)
				fmt.Fprint(w, `{"success":true}`)
			}
			path := filepath.Join(privateUpdateDir(t), "updates.json")
			u := openTestUpdates(t, path, f.client)
			result, err := u.Run(context.Background(), updateRequest(), test.probe)
			requireUpdateState(t, result, err, test.state)
			data, _ := os.ReadFile(path)
			if strings.Contains(err.Error(), secret) || strings.Contains(string(data), secret) {
				t.Fatal("probe credentials or errors were retained")
			}
		})
	}
}

func TestXrayUpdatesCancelledProbeIsInconclusive(t *testing.T) {
	f := newUpdateFixture(t)
	u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := u.Run(ctx, updateRequest(), func(context.Context) error { cancel(); return context.Canceled })
	requireUpdateState(t, result, err, XrayUpdateInconclusive)
	if result.Reason != "verification_cancelled" {
		t.Fatal(result)
	}
}

func TestXrayUpdatesConcurrentDuplicateRequestsHaveOneDispatch(t *testing.T) {
	f := newUpdateFixture(t)
	u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
	var probes atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := u.Run(context.Background(), updateRequest(), func(context.Context) error { probes.Add(1); return nil })
			if err != nil || result.State != XrayUpdateVerified {
				t.Errorf("concurrent result=%+v err=%v", result, err)
			}
		}()
	}
	wg.Wait()
	if f.calls.Load() != 1 || probes.Load() != 1 {
		t.Fatalf("duplicates performed installs=%d probes=%d", f.calls.Load(), probes.Load())
	}
}

func TestXrayUpdatesJournalBarrierSurvivesLostTerminalPersistence(t *testing.T) {
	f := newUpdateFixture(t)
	path := filepath.Join(privateUpdateDir(t), "updates.json")
	u := openTestUpdates(t, path, f.client)
	var writes int
	u.write = func(path string, data []byte) error {
		writes++
		if writes == 3 { // Successful response was received but not made durable.
			return errors.New("injected disk failure")
		}
		return writeUpdateFile(path, data)
	}
	f.install = func(w http.ResponseWriter, _ *http.Request) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Error(err)
		}
		var journal updateJournal
		if json.Unmarshal(data, &journal) != nil || len(journal.Records) != 1 || journal.Records[0].Result.State != XrayUpdateUnknown {
			t.Error("installation arrived before durable uncertainty barrier")
		}
		f.setVersion("26.9.9")
		fmt.Fprint(w, `{"success":true}`)
	}
	result, err := u.Run(context.Background(), updateRequest(), func(context.Context) error { t.Error("probe ran after disk failure"); return nil })
	if err == nil || result.State == XrayUpdateVerified || f.calls.Load() != 1 {
		t.Fatalf("terminal persistence failure was accepted: %+v %v", result, err)
	}
	if _, err := u.Run(context.Background(), updateRequest(), nil); err == nil {
		t.Fatal("broken writer remained usable")
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	u = openTestUpdates(t, path, f.client)
	result, err = u.Run(context.Background(), updateRequest(), nil)
	requireUpdateState(t, result, err, XrayUpdateUnknown)
	if f.calls.Load() != 1 {
		t.Fatal("reopened crash journal replayed installation")
	}
}

func TestXrayUpdatesDoesNotDispatchWhenBarrierCannotBeSaved(t *testing.T) {
	f := newUpdateFixture(t)
	path := filepath.Join(privateUpdateDir(t), "updates.json")
	u := openTestUpdates(t, path, f.client)
	var writes int
	u.write = func(path string, data []byte) error {
		writes++
		if writes == 2 {
			return errors.New("injected barrier persistence failure")
		}
		return writeUpdateFile(path, data)
	}
	if _, err := u.Run(context.Background(), updateRequest(), nil); err == nil || f.calls.Load() != 0 {
		t.Fatal("dispatch crossed an unpersisted barrier")
	}
}

func TestXrayUpdatesWriterLockAndEndpointIdentity(t *testing.T) {
	f := newUpdateFixture(t)
	path := filepath.Join(privateUpdateDir(t), "updates.json")
	u := openTestUpdates(t, path, f.client)
	if second, err := OpenXrayUpdates(path, f.client); err == nil {
		second.Close()
		t.Fatal("two concurrent writers acquired one journal")
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	other := newUpdateFixture(t)
	if second, err := OpenXrayUpdates(path, other.client); err == nil {
		second.Close()
		t.Fatal("journal accepted a changed endpoint")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if second, err := OpenXrayUpdates(path, f.client); err == nil {
		second.Close()
		t.Fatal("missing journal was silently recreated next to an existing lock")
	}
}

func TestXrayUpdatesRejectsCorruptUnsafeOrOversizedJournal(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{"corrupt", []byte("not JSON"), 0o600},
		{"oversized", bytesOf('x', maxUpdateJournal+1), 0o600},
		{"unsafe permissions", []byte("{}"), 0o644},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newUpdateFixture(t)
			path := filepath.Join(privateUpdateDir(t), "updates.json")
			if err := os.WriteFile(path, test.data, test.mode); err != nil {
				t.Fatal(err)
			}
			if u, err := OpenXrayUpdates(path, f.client); err == nil {
				u.Close()
				t.Fatal("unsafe journal opened")
			}
			after, err := os.ReadFile(path)
			if err != nil || !reflect.DeepEqual(after, test.data) {
				t.Fatal("invalid journal was modified instead of preserved")
			}
		})
	}
}

func TestXrayUpdatesRejectsMissingJournalMetadataWithoutInheritedDefaults(t *testing.T) {
	f := newUpdateFixture(t)
	endpointHash := updateDigest(f.client.base.String())
	for _, data := range []string{
		`{}`,
		`{"endpoint_hash":"` + endpointHash + `","records":[]}`,
		`{"version":1,"records":[]}`,
		`{"version":1,"endpoint_hash":"` + endpointHash + `"}`,
		`{"version":1,"endpoint_hash":"` + endpointHash + `","records":null}`,
	} {
		path := filepath.Join(privateUpdateDir(t), "updates.json")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if u, err := OpenXrayUpdates(path, f.client); err == nil {
			u.Close()
			t.Fatal("missing metadata inherited fresh-journal defaults")
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != data {
			t.Fatal("invalid journal was overwritten")
		}
	}
}

func TestXrayUpdatesRequiresExistingDurablePrivateParent(t *testing.T) {
	f := newUpdateFixture(t)
	parent := filepath.Join(privateUpdateDir(t), "does-not-exist")
	if u, err := OpenXrayUpdates(filepath.Join(parent, "updates.json"), f.client); err == nil {
		u.Close()
		t.Fatal("coordinator created a parent whose directory entry was not durably prepared")
	}
	if _, err := os.Stat(parent); !errors.Is(err, os.ErrNotExist) || f.calls.Load() != 0 {
		t.Fatal("constructor changed state or issued HTTP with an absent parent")
	}
}

func bytesOf(value byte, count int) []byte {
	return []byte(strings.Repeat(string(value), count))
}

func TestXrayUpdatesCapacityNeverEvictsReplayEvidence(t *testing.T) {
	f := newUpdateFixture(t)
	u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
	for i := range maxUpdateRecords {
		request := updateRequest()
		request.ID = fmt.Sprintf("past-request-%d", i)
		u.journal.Records = append(u.journal.Records, updateRecord{
			IDHash: updateDigest(request.ID), RequestHash: updateDigestJSON(request),
			ProviderHash: updateProviderDigest(request.ExpectedPanelGUID, request.ExpectedPanelVersion),
			Result:       XrayUpdateResult{State: XrayUpdateVerified, Reason: "verified", TargetVersion: request.Version},
		})
	}
	if err := u.persist(); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Run(context.Background(), updateRequest(), nil); err == nil || f.calls.Load() != 0 {
		t.Fatal("full journal evicted history to allow a new mutation")
	}
	request := updateRequest()
	request.ID = "past-request-0"
	result, err := u.Run(context.Background(), request, nil)
	requireUpdateState(t, result, err, XrayUpdateVerified)
}
