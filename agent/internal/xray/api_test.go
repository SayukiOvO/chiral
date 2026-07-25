package xray

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testBin(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("CHIRAL_XRAY_BIN"); p != "" {
		return p
	}
	p, err := exec.LookPath("xray")
	if err != nil {
		t.Skip("no xray binary; set CHIRAL_XRAY_BIN to exercise the management API")
	}
	return p
}

// liveXray brings up a real kernel with the API enabled and returns a manager
// pointed at it. The whole point of these tests is that `xray api` lies with
// its exit codes, which only a real kernel can demonstrate.
func liveXray(t *testing.T) *Manager {
	t.Helper()
	bin := testBin(t)
	dir := t.TempDir()

	cfg := map[string]any{
		"log":   map[string]any{"loglevel": "warning"},
		"api":   map[string]any{"tag": "api", "services": []string{"HandlerService", "StatsService"}},
		"stats": map[string]any{},
		"policy": map[string]any{
			"levels": map[string]any{"0": map[string]any{"statsUserUplink": true, "statsUserDownlink": true}},
		},
		"inbounds": []any{
			map[string]any{"tag": "chiral-api", "listen": "127.0.0.1", "port": freePort(t),
				"protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1"}},
			map[string]any{"tag": "vless-in", "listen": "127.0.0.1", "port": freePort(t), "protocol": "vless",
				"settings": map[string]any{
					"clients":    []any{map[string]any{"id": "8673288c-253e-4a63-bfc0-2828a6203a97", "email": "seed@node"}},
					"decryption": "none",
				}},
		},
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}},
		"routing": map[string]any{"rules": []any{
			map[string]any{"type": "field", "inboundTag": []string{"chiral-api"}, "outboundTag": "api"},
		}},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	m := New(bin, dir, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})), nil)
	if err := m.Start(); err != nil {
		t.Fatalf("starting xray: %v", err)
	}
	t.Cleanup(m.Stop)

	// Wait for the API to answer rather than sleeping a fixed amount.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := m.Stats(context.Background()); err == nil {
			return m
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("xray API never became reachable")
	return nil
}

// freePort asks the OS for an unused port and hands it straight back. There is
// an inherent race, but a fixed port would collide with a developer's own
// running xray, which is worse.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestAPIAddressIsReadFromTheAppliedConfig(t *testing.T) {
	cfg := []byte(`{"api":{"tag":"api"},
	  "inbounds":[{"tag":"chiral-api","listen":"127.0.0.1","port":10085,"protocol":"dokodemo-door"}],
	  "routing":{"rules":[{"type":"field","inboundTag":["chiral-api"],"outboundTag":"api"}]}}`)
	if got := apiAddressOf(cfg); got != "127.0.0.1:10085" {
		t.Errorf("got %q", got)
	}
	// An operator's own endpoint on another port must be found too.
	custom := []byte(`{"api":{"tag":"myapi"},
	  "inbounds":[{"tag":"mine","listen":"127.0.0.1","port":9999,"protocol":"dokodemo-door"}],
	  "routing":{"rules":[{"type":"field","inboundTag":["mine"],"outboundTag":"myapi"}]}}`)
	if got := apiAddressOf(custom); got != "127.0.0.1:9999" {
		t.Errorf("operator endpoint not found: %q", got)
	}
	if got := apiAddressOf([]byte(`{"inbounds":[]}`)); got != "" {
		t.Errorf("a config without an api should yield no address, got %q", got)
	}
}

func TestAddAndRemoveUserAgainstLiveXray(t *testing.T) {
	m := liveXray(t)
	ctx := context.Background()
	account := []byte(`{"id":"11111111-2222-3333-4444-555555555555","email":"alice@live"}`)

	if err := m.AddUser(ctx, "vless-in", "alice@live", account); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	// Adding the same user twice must be reported, not silently swallowed.
	if err := m.AddUser(ctx, "vless-in", "alice@live", account); err == nil {
		t.Error("adding a duplicate user should be reported as a failure")
	}
	if err := m.RemoveUser(ctx, "vless-in", "alice@live"); err != nil {
		t.Fatalf("RemoveUser: %v", err)
	}
}

// The measured hazard: `xray api rmu` prints an rpc error and still exits 0
// for a user who is not there. Absent is the desired state, so this is fine —
// but it must not be reported as success for a DIFFERENT failure.
func TestRemovingAnAbsentUserSucceeds(t *testing.T) {
	m := liveXray(t)
	if err := m.RemoveUser(context.Background(), "vless-in", "ghost@nowhere"); err != nil {
		t.Errorf("removing an absent user should be a no-op, got: %v", err)
	}
}

// Removing from an inbound that does not exist is a real failure and must not
// be mistaken for "already absent".
func TestRemovingFromAnUnknownInboundFails(t *testing.T) {
	m := liveXray(t)
	if err := m.RemoveUser(context.Background(), "no-such-inbound", "alice@live"); err == nil {
		t.Error("removing from an unknown inbound should fail")
	}
}

func TestAddUserRejectsANonObjectAccount(t *testing.T) {
	m := liveXray(t)
	if err := m.AddUser(context.Background(), "vless-in", "x@y", []byte(`"not an object"`)); err == nil {
		t.Error("a non-object account should be rejected before it reaches xray")
	}
}

func TestStatsAreDeltasBecauseTheyReset(t *testing.T) {
	m := liveXray(t)
	ctx := context.Background()
	if _, err := m.Stats(ctx); err != nil {
		t.Fatal(err)
	}
	// The API inbound's own traffic is counted, so a second read right after
	// the first must not report the first read's bytes again.
	first, err := m.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sum := func(s []Stat) int64 {
		var total int64
		for _, x := range s {
			total += x.Value
		}
		return total
	}
	// Each read covers only the interval since the previous one, so totals
	// stay small and bounded rather than growing without limit.
	if sum(second) > sum(first)*10+10000 {
		t.Errorf("counters look cumulative, not reset: first=%d second=%d", sum(first), sum(second))
	}
}

// --- pure parsing, no kernel needed ---

func TestParseStatsTreatsMissingValueAsZero(t *testing.T) {
	// Xray omits "value" entirely for a zero counter.
	out := `{"stat": [
	  {"name": "inbound>>>vless-in>>>traffic>>>uplink"},
	  {"name": "inbound>>>vless-in>>>traffic>>>downlink", "value": 42}
	]}`
	stats, err := parseStats(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 || stats[0].Value != 0 || stats[1].Value != 42 {
		t.Errorf("got %+v", stats)
	}
}

func TestParseStatsSkipsLogPreamble(t *testing.T) {
	out := "Xray 26.7.11 starting\n[Info] something\n" + `{"stat":[{"name":"a>>>b>>>traffic>>>uplink","value":7}]}`
	stats, err := parseStats(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].Value != 7 {
		t.Errorf("got %+v", stats)
	}
}

func TestParseStatsOnEmptyResult(t *testing.T) {
	stats, err := parseStats(`{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 0 {
		t.Errorf("got %+v", stats)
	}
}

func TestUserTrafficFoldsBothDirections(t *testing.T) {
	got := UserTrafficFrom([]Stat{
		{Name: "user>>>alice@p.n>>>traffic>>>uplink", Value: 10},
		{Name: "user>>>alice@p.n>>>traffic>>>downlink", Value: 90},
		{Name: "user>>>bob@p.n>>>traffic>>>uplink", Value: 5},
		{Name: "inbound>>>vless-in>>>traffic>>>uplink", Value: 999},
		{Name: "outbound>>>direct>>>traffic>>>downlink", Value: 999},
	})
	if len(got) != 2 {
		t.Fatalf("expected two users, got %+v", got)
	}
	if got[0].Email != "alice@p.n" || got[0].Up != 10 || got[0].Down != 90 {
		t.Errorf("alice: %+v", got[0])
	}
	if got[1].Email != "bob@p.n" || got[1].Up != 5 {
		t.Errorf("bob: %+v", got[1])
	}
}

// An email containing an '@' and dots must survive; the separator is ">>>".
func TestUserTrafficHandlesRealisticEmails(t *testing.T) {
	got := UserTrafficFrom([]Stat{
		{Name: "user>>>alice@tokyo-reality.5f3ea2e478073188>>>traffic>>>uplink", Value: 1},
	})
	if len(got) != 1 || got[0].Email != "alice@tokyo-reality.5f3ea2e478073188" {
		t.Errorf("got %+v", got)
	}
}

func TestUserTrafficSkipsIdleUsers(t *testing.T) {
	got := UserTrafficFrom([]Stat{
		{Name: "user>>>idle@p.n>>>traffic>>>uplink", Value: 0},
		{Name: "user>>>idle@p.n>>>traffic>>>downlink", Value: 0},
	})
	if len(got) != 0 {
		t.Errorf("a user with no traffic should not be reported: %+v", got)
	}
}

func TestUserTrafficIgnoresMalformedNames(t *testing.T) {
	got := UserTrafficFrom([]Stat{
		{Name: "user>>>alice", Value: 5},
		{Name: "nonsense", Value: 5},
		{Name: "", Value: 5},
	})
	if len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}

func TestCountFrom(t *testing.T) {
	if n, ok := countFrom(addedRE, "add user: a\nresult: ok\nAdded 1 user(s) in total.\n"); !ok || n != 1 {
		t.Errorf("n=%d ok=%v", n, ok)
	}
	// The silent no-op that makes exit codes untrustworthy.
	if n, ok := countFrom(addedRE, "Added 0 user(s) in total.\n"); !ok || n != 0 {
		t.Errorf("n=%d ok=%v", n, ok)
	}
	if _, ok := countFrom(addedRE, "something else entirely"); ok {
		t.Error("an unparseable output must not be read as a count")
	}
}

func TestProtocolForAccountShape(t *testing.T) {
	vless := map[string]json.RawMessage{"id": json.RawMessage(`"x"`)}
	if got := protocolFor(vless); got != "vless" {
		t.Errorf("got %q", got)
	}
	trojan := map[string]json.RawMessage{"password": json.RawMessage(`"x"`)}
	if got := protocolFor(trojan); got != "trojan" {
		t.Errorf("got %q", got)
	}
}

func TestNoAPIEndpointIsAClearError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"inbounds":[]}`), 0o600)
	m := New("xray", dir, slog.Default(), nil)
	err := m.AddUser(context.Background(), "in", "a@b", []byte(`{"id":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "API endpoint") {
		t.Errorf("expected a clear no-endpoint error, got: %v", err)
	}
}

// The two "not found" replies look alike but mean opposite things; conflating
// them would let a typo'd inbound tag masquerade as a completed ban.
func TestUserAlreadyAbsentDistinguishesTheTwoNotFounds(t *testing.T) {
	absentUser := "remove user: ghost@x\nrpc error: code = Unknown desc = proxy/vless: User ghost@x not found.\nRemoved 0 user(s) in total.\n"
	if !userAlreadyAbsent(absentUser) {
		t.Error("an absent user should be treated as already removed")
	}
	unknownInbound := "remove user: alice@x\nrpc error: code = Unknown desc = app/proxyman/command: failed to get handler: nope > app/proxyman/inbound: handler not found: nope\nRemoved 0 user(s) in total.\n"
	if userAlreadyAbsent(unknownInbound) {
		t.Error("an unknown inbound must stay an error, not look like a completed removal")
	}
	if userAlreadyAbsent("Removed 0 user(s) in total.\n") {
		t.Error("a bare zero count is not evidence the user was absent")
	}
}
