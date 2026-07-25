package alert

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

type fakeLive struct {
	mu     sync.Mutex
	online map[string]bool
}

func (f *fakeLive) IsOnline(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.online[id]
}

func (f *fakeLive) set(id string, up bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.online[id] = up
}

// recorder is a webhook that remembers what it received.
type recorder struct {
	mu       sync.Mutex
	payloads []map[string]any
	status   int
}

func (r *recorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var m map[string]any
		json.Unmarshal(body, &m)
		r.mu.Lock()
		r.payloads = append(r.payloads, m)
		status := r.status
		r.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if status >= 400 {
			w.Write([]byte("nope"))
		}
	}
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.payloads)
}

// clock lets a test move past the debounce without sleeping through it.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newFixture(t *testing.T) (*Service, *store.Store, *fakeLive, *recorder, string, *clock) {
	t.Helper()
	box, err := secret.NewBox("alert-test-key-0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	rec := &recorder{}
	srv := httptest.NewServer(rec.handler())
	t.Cleanup(srv.Close)

	live := &fakeLive{online: map[string]bool{}}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc := NewService(st, live, logger)
	clk := &clock{t: time.Now()}
	svc.now = clk.now
	return svc, st, live, rec, srv.URL, clk
}

// settle runs the two sweeps a change needs: one to observe it, and — after
// the debounce has elapsed — one to announce it. Advancing the clock before
// the first sweep would instead record the change as having just happened,
// which is exactly what the debounce is there to sit out.
func settle(t *testing.T, svc *Service, clk *clock) int {
	t.Helper()
	if _, err := svc.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	clk.advance(Debounce + time.Minute)
	sent, err := svc.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return sent
}

func TestOfflineIsAnnouncedOnlyAfterTheDebounce(t *testing.T) {
	svc, st, live, rec, url, clk := newFixture(t)
	n, _ := st.CreateNode("tokyo-1", "h")
	if _, err := st.CreateAlertTarget(store.AlertWebhook, "hook", url); err != nil {
		t.Fatal(err)
	}

	// Seen online first: a first sighting is recorded as already-announced,
	// so adopting a fleet does not fire an alert per node.
	live.set(n.ID, true)
	if sent, err := svc.Sweep(context.Background()); err != nil || sent != 0 {
		t.Fatalf("first sighting sent %d alerts (err %v)", sent, err)
	}

	// Goes offline: observed, but too fresh to announce.
	live.set(n.ID, false)
	if sent, _ := svc.Sweep(context.Background()); sent != 0 {
		t.Fatalf("announced a change that had not settled")
	}
	if rec.count() != 0 {
		t.Fatalf("delivered %d messages during the debounce", rec.count())
	}

	// Once it has held that state, it is worth telling someone.
	clk.advance(Debounce + time.Minute)
	sent, err := svc.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sent != 1 {
		t.Fatalf("expected one alert, got %d", sent)
	}
	if rec.count() != 1 {
		t.Fatalf("expected one delivery, got %d", rec.count())
	}
	rec.mu.Lock()
	payload := rec.payloads[0]
	rec.mu.Unlock()
	if payload["online"] != false {
		t.Errorf("payload does not say the node is offline: %+v", payload)
	}
	if !strings.Contains(payload["body"].(string), "tokyo-1") {
		t.Errorf("payload does not name the node: %+v", payload)
	}
}

// A flapping link must not produce a message per heartbeat — that is the
// failure mode that trains people to ignore the channel.
func TestFlappingProducesNothing(t *testing.T) {
	svc, st, live, rec, url, _ := newFixture(t)
	n, _ := st.CreateNode("tokyo-1", "h")
	st.CreateAlertTarget(store.AlertWebhook, "hook", url)

	live.set(n.ID, true)
	svc.Sweep(context.Background())
	for i := 0; i < 10; i++ {
		live.set(n.ID, i%2 == 0)
		if sent, _ := svc.Sweep(context.Background()); sent != 0 {
			t.Fatalf("a flap produced an alert on iteration %d", i)
		}
	}
	if rec.count() != 0 {
		t.Errorf("delivered %d messages for a flapping node", rec.count())
	}
}

// Announcing the same state twice is noise; only a change counts.
func TestSteadyStateIsSilent(t *testing.T) {
	svc, st, live, rec, url, clk := newFixture(t)
	n, _ := st.CreateNode("tokyo-1", "h")
	st.CreateAlertTarget(store.AlertWebhook, "hook", url)

	live.set(n.ID, true)
	svc.Sweep(context.Background())
	live.set(n.ID, false)
	if settle(t, svc, clk) != 1 {
		t.Fatal("setup: the change should have been announced")
	}
	before := rec.count()

	for i := 0; i < 5; i++ {
		clk.advance(Debounce)
		if sent, _ := svc.Sweep(context.Background()); sent != 0 {
			t.Fatal("a node that has not changed produced an alert")
		}
	}
	if rec.count() != before {
		t.Errorf("delivered extra messages for an unchanged node")
	}
}

func TestRecoveryIsAnnounced(t *testing.T) {
	svc, st, live, rec, url, clk := newFixture(t)
	n, _ := st.CreateNode("tokyo-1", "h")
	st.CreateAlertTarget(store.AlertWebhook, "hook", url)

	live.set(n.ID, true)
	svc.Sweep(context.Background())
	live.set(n.ID, false)
	if settle(t, svc, clk) != 1 {
		t.Fatal("setup: going offline should have been announced")
	}

	live.set(n.ID, true)
	if settle(t, svc, clk) != 1 {
		t.Fatal("recovery was not announced")
	}
	rec.mu.Lock()
	last := rec.payloads[len(rec.payloads)-1]
	rec.mu.Unlock()
	if last["online"] != true {
		t.Errorf("recovery payload does not say online: %+v", last)
	}
	if !strings.Contains(last["title"].(string), "online") {
		t.Errorf("recovery title is wrong: %+v", last)
	}
}

// A delivery failure must be recorded where an operator will see it, and must
// not wedge the state machine into re-sending forever.
func TestDeliveryFailureIsRecordedAndNotRetriedForever(t *testing.T) {
	svc, st, live, rec, url, clk := newFixture(t)
	n, _ := st.CreateNode("tokyo-1", "h")
	target, _ := st.CreateAlertTarget(store.AlertWebhook, "hook", url)
	rec.status = http.StatusInternalServerError

	live.set(n.ID, true)
	svc.Sweep(context.Background())
	live.set(n.ID, false)
	if settle(t, svc, clk) != 1 {
		t.Fatal("setup: the change should have been announced")
	}

	got, err := st.GetAlertTarget(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastError == "" {
		t.Error("a failed delivery was not recorded on the target")
	}
	// The state was marked announced regardless, so the next sweep is quiet:
	// a broken target must not queue the same message forever.
	clk.advance(Debounce)
	if sent, _ := svc.Sweep(context.Background()); sent != 0 {
		t.Error("a failed delivery is being retried indefinitely")
	}
}

func TestDisabledTargetsAreSkipped(t *testing.T) {
	svc, st, live, rec, url, clk := newFixture(t)
	n, _ := st.CreateNode("tokyo-1", "h")
	target, _ := st.CreateAlertTarget(store.AlertWebhook, "hook", url)
	if err := st.SetAlertTargetEnabled(target.ID, false); err != nil {
		t.Fatal(err)
	}

	live.set(n.ID, true)
	svc.Sweep(context.Background())
	live.set(n.ID, false)
	settle(t, svc, clk)

	if rec.count() != 0 {
		t.Errorf("delivered to a disabled target")
	}
}

func TestTelegramConfigIsValidated(t *testing.T) {
	svc, _, _, _, _, _ := newFixture(t)
	err := svc.deliverTelegram(context.Background(), "no-colon-here", Event{})
	if err == nil || !strings.Contains(err.Error(), "bot-token") {
		t.Errorf("a malformed telegram config should be reported clearly, got: %v", err)
	}
}

func TestUnknownTargetKindIsReported(t *testing.T) {
	svc, _, _, _, _, _ := newFixture(t)
	err := svc.Deliver(context.Background(), store.AlertTarget{Kind: "carrier-pigeon"}, Event{})
	if err == nil || !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Errorf("got %v", err)
	}
}

// A node deleted between sweeps must not leave the state machine confused.
func TestDeletedNodeDropsItsAlertState(t *testing.T) {
	svc, st, live, _, url, _ := newFixture(t)
	n, _ := st.CreateNode("tokyo-1", "h")
	st.CreateAlertTarget(store.AlertWebhook, "hook", url)
	live.set(n.ID, true)
	svc.Sweep(context.Background())

	if err := st.DeleteNode(n.ID); err != nil {
		t.Fatal(err)
	}
	states, err := st.NodeAlertStates()
	if err != nil {
		t.Fatal(err)
	}
	if _, present := states[n.ID]; present {
		t.Error("alert state outlived its node")
	}
	if _, err := svc.Sweep(context.Background()); err != nil {
		t.Errorf("sweeping after a deletion failed: %v", err)
	}
}
