package threexui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func TestObserverReturnsStatusAndStableCapabilitySnapshot(t *testing.T) {
	var statusRequests, openAPIRequests atomic.Int32
	server := newObserverServer(t, &statusRequests, &openAPIRequests, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, completeOpenAPIDocument)
	})
	defer server.Close()

	observer := newTestObserver(t, server.URL, ObserverConfig{})
	observation := observer.Observe(context.Background())
	if observation.Error != nil {
		t.Fatalf("Observe() error = %v", observation.Error)
	}
	if observation.PanelVersion != "v3.7.0" || observation.XrayState != "running" || observation.XrayVersion != "25.8.3" {
		t.Errorf("Observe() status = %#v", observation)
	}
	if !reflect.DeepEqual(observation.SupportedCapabilities, RequiredCapabilities()) {
		t.Errorf("SupportedCapabilities = %v, want %v", observation.SupportedCapabilities, RequiredCapabilities())
	}
	if len(observation.MissingCapabilities) != 0 {
		t.Errorf("MissingCapabilities = %v", observation.MissingCapabilities)
	}
	const wantDigest = "9f6c27610743b07726933e5bb31ade43aa9871dff45e036d5b5992108e08c08d"
	if observation.ContractDigest != wantDigest {
		t.Errorf("ContractDigest = %q, want %q", observation.ContractDigest, wantDigest)
	}
	if observation.ContractObservedAt.IsZero() {
		t.Fatal("successful contract discovery has no observation time")
	}
	if statusRequests.Load() != 1 || openAPIRequests.Load() != 1 {
		t.Errorf("request counts: status=%d openapi=%d", statusRequests.Load(), openAPIRequests.Load())
	}
}

func TestObserverDoesNotRefreshCachedContractTimestamp(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_000, 0)}
	var statusRequests, openAPIRequests atomic.Int32
	server := newObserverServer(t, &statusRequests, &openAPIRequests, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, completeOpenAPIDocument)
	})
	defer server.Close()
	observer := newTestObserver(t, server.URL, ObserverConfig{SuccessTTL: time.Minute, Now: clock.Now})
	first := observer.Observe(context.Background())
	clock.Advance(20 * time.Second)
	second := observer.Observe(context.Background())
	if !second.ContractObservedAt.Equal(first.ContractObservedAt) {
		t.Fatalf("cached contract time moved from %v to %v", first.ContractObservedAt, second.ContractObservedAt)
	}
}

func TestObserverOnlyReadsObservationalEndpointsAcrossDiscoveryLifecycle(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_000, 0)}
	var statusRequests, openAPIRequests, canaryRequests, forbiddenRequests atomic.Int32
	var version atomic.Value
	version.Store("v3.7.0")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+testToken {
			forbiddenRequests.Add(1)
			http.Error(w, "unexpected method or authentication", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/panel/api/server/status":
			statusRequests.Add(1)
			fmt.Fprintf(w, `{"success":true,"msg":"","obj":{"panelVersion":%q,"xray":{"state":"running"}}}`, version.Load().(string))
		case "/panel/api/openapi.json":
			openAPIRequests.Add(1)
			fmt.Fprint(w, completeOpenAPIDocument)
		case "/panel/api/xray/getOutboundsTraffic":
			canaryRequests.Add(1)
			fmt.Fprint(w, `{"success":true,"msg":"","obj":[]}`)
		default:
			// Includes assembled/template GETs as well as every mutation path.
			forbiddenRequests.Add(1)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	observer := newTestObserver(t, server.URL, ObserverConfig{
		SuccessTTL: 5 * time.Minute,
		Now:        clock.Now,
	})
	for i, phase := range []struct {
		name     string
		advance  time.Duration
		version  string
		openAPIs int32
		canaries int32
	}{
		{name: "cold", openAPIs: 1, canaries: 1},
		{name: "cached", advance: time.Minute, openAPIs: 1, canaries: 1},
		{name: "refresh", advance: 4 * time.Minute, openAPIs: 2, canaries: 1},
		{name: "version change", version: "v3.8.0", openAPIs: 3, canaries: 2},
	} {
		t.Run(phase.name, func(t *testing.T) {
			clock.Advance(phase.advance)
			if phase.version != "" {
				version.Store(phase.version)
			}
			observation := observer.Observe(context.Background())
			if observation.Error != nil {
				t.Fatal(observation.Error)
			}
			if len(RequiredCapabilities()) != 17 || !reflect.DeepEqual(observation.SupportedCapabilities, RequiredCapabilities()) || len(observation.MissingCapabilities) != 0 {
				t.Fatalf("the existing 17 route-shape gates changed: %#v", observation)
			}
			if statusRequests.Load() != int32(i+1) || openAPIRequests.Load() != phase.openAPIs || canaryRequests.Load() != phase.canaries || forbiddenRequests.Load() != 0 {
				t.Fatalf("request counts: status=%d openapi=%d canary=%d forbidden=%d", statusRequests.Load(), openAPIRequests.Load(), canaryRequests.Load(), forbiddenRequests.Load())
			}
		})
	}
}

func TestObserverCachesSuccessfulDiscoveryButAlwaysRefreshesStatus(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_000, 0)}
	var statusRequests, openAPIRequests atomic.Int32
	server := newObserverServer(t, &statusRequests, &openAPIRequests, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, completeOpenAPIDocument)
	})
	defer server.Close()

	observer := newTestObserver(t, server.URL, ObserverConfig{
		SuccessTTL: 5 * time.Minute,
		FailureTTL: 30 * time.Second,
		Now:        clock.Now,
	})
	for range 2 {
		if observation := observer.Observe(context.Background()); observation.Error != nil {
			t.Fatalf("Observe() error = %v", observation.Error)
		}
	}
	clock.Advance(5*time.Minute - time.Nanosecond)
	if observation := observer.Observe(context.Background()); observation.Error != nil {
		t.Fatalf("Observe() before expiry error = %v", observation.Error)
	}
	if got := openAPIRequests.Load(); got != 1 {
		t.Fatalf("OpenAPI requests before expiry = %d, want 1", got)
	}

	clock.Advance(time.Nanosecond)
	if observation := observer.Observe(context.Background()); observation.Error != nil {
		t.Fatalf("Observe() after expiry error = %v", observation.Error)
	}
	if got := statusRequests.Load(); got != 4 {
		t.Errorf("status requests = %d, want 4", got)
	}
	if got := openAPIRequests.Load(); got != 2 {
		t.Errorf("OpenAPI requests = %d, want 2", got)
	}
}

func TestObserverRediscoversContractImmediatelyWhenPanelVersionChanges(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_500, 0)}
	var openAPIRequests atomic.Int32
	var version atomic.Value
	version.Store("v3.7.0")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/panel/api/server/status":
			fmt.Fprintf(w, `{"success":true,"msg":"","obj":{"panelVersion":%q,"xray":{"state":"running"}}}`, version.Load().(string))
		case "/panel/api/openapi.json":
			openAPIRequests.Add(1)
			fmt.Fprint(w, completeOpenAPIDocument)
		case "/panel/api/xray/getOutboundsTraffic":
			fmt.Fprint(w, `{"success":true,"msg":"","obj":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	observer := newTestObserver(t, server.URL, ObserverConfig{SuccessTTL: time.Hour, Now: clock.Now})
	if observation := observer.Observe(context.Background()); observation.Error != nil {
		t.Fatal(observation.Error)
	}
	version.Store("v3.8.0")
	if observation := observer.Observe(context.Background()); observation.Error != nil {
		t.Fatal(observation.Error)
	}
	if got := openAPIRequests.Load(); got != 2 {
		t.Fatalf("OpenAPI requests after panel upgrade = %d, want 2", got)
	}
}

func TestObserverRetriesFailedDiscoveryAfterShortCache(t *testing.T) {
	clock := &fakeClock{now: time.Unix(2_000, 0)}
	var statusRequests, openAPIRequests atomic.Int32
	server := newObserverServer(t, &statusRequests, &openAPIRequests, func(w http.ResponseWriter, _ *http.Request) {
		if openAPIRequests.Load() == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, "temporarily unavailable "+testToken)
			return
		}
		fmt.Fprint(w, completeOpenAPIDocument)
	})
	defer server.Close()

	observer := newTestObserver(t, server.URL, ObserverConfig{
		SuccessTTL: 5 * time.Minute,
		FailureTTL: 30 * time.Second,
		Now:        clock.Now,
	})
	first := observer.Observe(context.Background())
	if first.Error == nil || !strings.Contains(first.Error.Error(), "HTTP 503 Service Unavailable") {
		t.Fatalf("first Observe() error = %v", first.Error)
	}
	assertNoToken(t, first.Error)
	if len(first.SupportedCapabilities) != 0 || first.ContractDigest != "" {
		t.Errorf("failed initial discovery returned capabilities: %#v", first)
	}

	clock.Advance(29 * time.Second)
	second := observer.Observe(context.Background())
	if second.Error == nil {
		t.Fatal("cached discovery failure was not reported")
	}
	if got := openAPIRequests.Load(); got != 1 {
		t.Fatalf("OpenAPI requests during failure cache = %d, want 1", got)
	}

	clock.Advance(time.Second)
	third := observer.Observe(context.Background())
	if third.Error != nil {
		t.Fatalf("recovery Observe() error = %v", third.Error)
	}
	if !reflect.DeepEqual(third.SupportedCapabilities, RequiredCapabilities()) {
		t.Errorf("recovered SupportedCapabilities = %v", third.SupportedCapabilities)
	}
	if got := statusRequests.Load(); got != 3 {
		t.Errorf("status requests = %d, want 3", got)
	}
	if got := openAPIRequests.Load(); got != 2 {
		t.Errorf("OpenAPI requests after retry = %d, want 2", got)
	}
}

func TestObserverRejectsTokenDeniedPersistedCounterRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/panel/api/server/status":
			fmt.Fprint(w, `{"success":true,"msg":"","obj":{"panelVersion":"v3.7.0","xray":{"state":"stopped"}}}`)
		case "/panel/api/openapi.json":
			fmt.Fprint(w, completeOpenAPIDocument)
		case "/panel/api/xray/getOutboundsTraffic":
			w.WriteHeader(http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	observation := newTestObserver(t, server.URL, ObserverConfig{}).Observe(context.Background())
	if observation.Error == nil || !strings.Contains(observation.Error.Error(), "HTTP 403 Forbidden") {
		t.Fatalf("restricted token looked compatible: %#v", observation)
	}
	assertNoToken(t, observation.Error)
	if observation.ContractDigest != "" || len(observation.SupportedCapabilities) != 0 || !observation.ContractObservedAt.IsZero() {
		t.Fatalf("denied initial canary published a successful contract: %#v", observation)
	}
}

func TestObserverRejectsInvalidCanarySchemaAndRetriesAfterFailureCache(t *testing.T) {
	for name, response := range map[string]string{
		"missing object":          `{"success":true}`,
		"object instead of array": `{"success":true,"obj":{}}`,
		"missing counters":        `{"success":true,"obj":[{"id":1,"tag":"outbound"}]}`,
		"negative counters":       `{"success":true,"obj":[{"id":1,"tag":"outbound","up":-1,"down":0}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			clock := &fakeClock{now: time.Unix(3_000, 0)}
			var canaryRequests, forbiddenRequests atomic.Int32
			var canaryResponse atomic.Value
			canaryResponse.Store(response)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					forbiddenRequests.Add(1)
					http.Error(w, "unexpected method", http.StatusBadRequest)
					return
				}
				switch r.URL.Path {
				case "/panel/api/server/status":
					fmt.Fprint(w, `{"success":true,"obj":{"panelVersion":"v3.8.5","xray":{"state":"running"}}}`)
				case "/panel/api/openapi.json":
					fmt.Fprint(w, completeOpenAPIDocument)
				case "/panel/api/xray/getOutboundsTraffic":
					canaryRequests.Add(1)
					fmt.Fprint(w, canaryResponse.Load().(string))
				default:
					forbiddenRequests.Add(1)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			observer := newTestObserver(t, server.URL, ObserverConfig{FailureTTL: 30 * time.Second, Now: clock.Now})
			for range 2 {
				observation := observer.Observe(context.Background())
				var contractError *ContractError
				if !errors.As(observation.Error, &contractError) || observation.ContractDigest != "" || len(observation.SupportedCapabilities) != 0 || !observation.ContractObservedAt.IsZero() {
					t.Fatalf("invalid canary looked compatible: %#v", observation)
				}
			}
			if canaryRequests.Load() != 1 {
				t.Fatalf("canary requests during failure cache = %d, want 1", canaryRequests.Load())
			}
			canaryResponse.Store(`{"success":true,"obj":null}`)
			clock.Advance(30 * time.Second)
			observation := observer.Observe(context.Background())
			if observation.Error != nil || observation.ContractDigest == "" || !reflect.DeepEqual(observation.SupportedCapabilities, RequiredCapabilities()) || canaryRequests.Load() != 2 || forbiddenRequests.Load() != 0 {
				t.Fatalf("failed canary did not recover safely: observation=%#v canaries=%d forbidden=%d", observation, canaryRequests.Load(), forbiddenRequests.Load())
			}
		})
	}
}

func TestObserverCoalescesConcurrentCapabilityDiscovery(t *testing.T) {
	const callers = 32
	var statusRequests, openAPIRequests atomic.Int32
	discoveryStarted := make(chan struct{})
	releaseDiscovery := make(chan struct{})
	var startOnce sync.Once
	server := newObserverServer(t, &statusRequests, &openAPIRequests, func(w http.ResponseWriter, r *http.Request) {
		startOnce.Do(func() { close(discoveryStarted) })
		select {
		case <-releaseDiscovery:
			fmt.Fprint(w, completeOpenAPIDocument)
		case <-r.Context().Done():
		}
	})
	defer server.Close()

	observer := newTestObserver(t, server.URL, ObserverConfig{})
	start := make(chan struct{})
	results := make(chan Observation, callers)
	var callersReady sync.WaitGroup
	callersReady.Add(callers)
	for range callers {
		go func() {
			callersReady.Done()
			<-start
			results <- observer.Observe(context.Background())
		}()
	}
	callersReady.Wait()
	close(start)
	select {
	case <-discoveryStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("capability discovery did not start")
	}
	close(releaseDiscovery)

	for range callers {
		select {
		case observation := <-results:
			if observation.Error != nil {
				t.Errorf("Observe() error = %v", observation.Error)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent Observe() did not finish")
		}
	}
	if got := statusRequests.Load(); got != callers {
		t.Errorf("status requests = %d, want %d", got, callers)
	}
	if got := openAPIRequests.Load(); got != 1 {
		t.Errorf("OpenAPI requests = %d, want 1", got)
	}
}

func TestNewObserverRejectsInvalidConfiguration(t *testing.T) {
	client, err := New(Config{BaseURL: "http://127.0.0.1", Token: testToken})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	for _, test := range []struct {
		name string
		cfg  ObserverConfig
	}{
		{name: "negative success TTL", cfg: ObserverConfig{SuccessTTL: -time.Second}},
		{name: "negative failure TTL", cfg: ObserverConfig{FailureTTL: -time.Second}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if observer, err := NewObserver(client, test.cfg); err == nil {
				t.Fatalf("NewObserver() = %#v, want error", observer)
			}
		})
	}
	if observer, err := NewObserver(nil, ObserverConfig{}); err == nil {
		t.Fatalf("NewObserver(nil) = %#v, want error", observer)
	}
}

func newObserverServer(
	t *testing.T,
	statusRequests *atomic.Int32,
	openAPIRequests *atomic.Int32,
	serveOpenAPI func(http.ResponseWriter, *http.Request),
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/panel/api/server/status":
			statusRequests.Add(1)
			fmt.Fprint(w, `{"success":true,"msg":"","obj":{"panelVersion":"v3.7.0","xray":{"state":"running","version":"25.8.3"}}}`)
		case "/panel/api/openapi.json":
			openAPIRequests.Add(1)
			serveOpenAPI(w, r)
		case "/panel/api/xray/getOutboundsTraffic":
			fmt.Fprint(w, `{"success":true,"msg":"","obj":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func newTestObserver(t *testing.T, baseURL string, cfg ObserverConfig) *Observer {
	t.Helper()
	client, err := New(Config{BaseURL: baseURL, Token: testToken})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	observer, err := NewObserver(client, cfg)
	if err != nil {
		t.Fatalf("NewObserver() error = %v", err)
	}
	return observer
}
