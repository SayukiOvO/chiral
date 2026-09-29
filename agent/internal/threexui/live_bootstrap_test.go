package threexui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestLive3XUIBillingBootstrapContract tests a limited first-activation
// hypothesis, not a production bootstrap procedure: private probe traffic
// establishes the official job's baseline before a new whole inbound opens.
// The first request for a distinct user must then be billed, with no readiness
// or warm-up requests under that user's identity. All provider interaction is
// official HTTP; no fixture Xray binary is started. Panel/job recreation and
// client-only hot updates are deliberately outside this experiment's scope.
func TestLive3XUIBillingBootstrapContract(t *testing.T) {
	optIn := os.Getenv("CHIRAL_3XUI_TEST_WRITE")
	if optIn == "" {
		t.Skip("set CHIRAL_3XUI_TEST_WRITE=isolated-empty-instance to enable the billing bootstrap experiment")
	}
	if optIn != "isolated-empty-instance" {
		t.Fatal("billing bootstrap requires explicit isolated-empty-instance opt-in")
	}
	liveHTTPProbeRequireLoopbackNamespace(t)
	baseURL, tokenFile := os.Getenv("CHIRAL_3XUI_TEST_URL"), os.Getenv("CHIRAL_3XUI_TEST_TOKEN_FILE")
	if baseURL == "" || tokenFile == "" {
		t.Fatal("billing bootstrap requires the isolated URL and token file")
	}
	client, err := New(Config{BaseURL: baseURL, Token: readLive3XUIToken(t, tokenFile), Timeout: 40 * time.Second})
	if err != nil {
		t.Fatal("cannot construct the isolated bootstrap client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	liveHTTPProbeRequireEmpty(t, ctx, client)
	baseline, err := client.ReadAssembledConfig(ctx)
	if err != nil {
		t.Fatal("cannot read the empty bootstrap configuration")
	}
	liveWriteRequireAPIOnly(t, baseline)
	identity, err := liveUpdateWaitState(ctx, client, "running")
	if err != nil {
		t.Fatal("cannot confirm the bootstrap provider identity")
	}
	caps, err := client.DiscoverCapabilities(ctx)
	if err != nil || !caps.Supports(CapabilityOutboundProbe) || !caps.Supports(CapabilityXrayUpdate) {
		t.Fatal("bootstrap requires the official HTTP probe and template operations")
	}
	snapshot, err := client.ReadXrayTemplate(ctx)
	if err != nil || snapshot.OutboundTestURL == "" {
		t.Fatal("cannot read a restorable bootstrap template")
	}
	liveWriteRequireAPIOnly(t, snapshot.Config)
	prefix := "chiral-bootstrap-" + liveWriteRandomID(t)
	targetURL := "http://8.8.8.8/" + prefix
	body := bytes.Repeat([]byte("chiral-bootstrap-payload|"), 2048)
	origin := liveHTTPProbeStartOriginWithBody(t, "8.8.8.8", "/"+prefix, body)
	defer origin.close(t)
	warm := liveBootstrapAccount{Prefix: prefix + "-warm", Credential: liveWriteUUID(t), Port: liveProjectionFreePort(t)}
	user := liveBootstrapAccount{Prefix: prefix + "-user", Credential: liveWriteUUID(t), Port: liveProjectionFreePort(t)}
	if warm.Port == user.Port || warm.Credential == user.Credential {
		t.Fatal("bootstrap fixture identities or ports collided")
	}
	warmConfig := liveBootstrapConfig(t, baseline, warm, origin.address)
	userConfig := liveBootstrapConfig(t, warmConfig, user, origin.address)
	userConfig = liveBootstrapReuseOutbound(t, warmConfig, userConfig, warm.Prefix+"-redirect", user.Prefix+"-redirect")
	// Recovery is registered before even disabled metadata is created. Only
	// exact owned templates and object sets may be restored or removed.
	defer liveBootstrapRestore(t, client, identity, snapshot, baseline, targetURL,
		[]json.RawMessage{warmConfig, userConfig}, []liveBootstrapAccount{warm, user})
	warmRow := liveBootstrapAddMetadata(t, ctx, client, warm)
	liveBootstrapRequireObjects(t, ctx, client, []liveBootstrapAccount{warm})
	liveBootstrapRequireSnapshots(t, ctx, client, snapshot.Config, baseline, snapshot.OutboundTestURL)
	if err := client.UpdateXrayTemplate(ctx, XrayTemplate{Config: warmConfig, OutboundTestURL: targetURL}); err != nil {
		t.Fatal("cannot apply the private bootstrap probe")
	}
	liveBootstrapRequireConfig(t, ctx, client, warmConfig, targetURL)
	liveBootstrapRequireObjects(t, ctx, client, []liveBootstrapAccount{warm})
	initialWarm := liveBootstrapCounter(t, ctx, client, warm, warmRow.ID)
	if initialWarm.Up != 0 || initialWarm.Down != 0 {
		t.Fatal("private probe counter did not start at zero")
	}
	probe := func(account liveBootstrapAccount) {
		t.Helper()
		hits, written := origin.hits.Load(), origin.written.Load()
		result, err := client.ProbeOutbound(ctx, liveHTTPProbeOutbound(t, account.Prefix+"-client", account.Credential, account.Port), nil, targetURL)
		if err != nil || result.HTTPStatus != http.StatusOK || origin.hits.Load() != hits+1 ||
			origin.written.Load() != written+int64(len(body)) || origin.invalid.Load() != 0 || origin.writeErrors.Load() != 0 {
			t.Fatal("bootstrap probe did not make exactly one authenticated origin request and complete the response write")
		}
		liveHTTPProbeRequireIdentity(t, ctx, client, identity)
	}
	var warmed ClientTraffic
	for attempt := 1; attempt <= 6; attempt++ {
		probe(warm)
		var counted bool
		warmed, counted = liveBootstrapAwaitCounter(t, ctx, client, warm, warmRow.ID, initialWarm, uint64(len(body)), 6*time.Second)
		t.Logf("private_probe_attempt=%d counted=%t probe_up=%d probe_down=%d", attempt, counted, warmed.Up, warmed.Down)
		if counted {
			break
		}
	}
	if warmed.Up == 0 || warmed.Down < uint64(len(body)) {
		t.Fatal("private probe failed to establish an observable accounting baseline")
	}
	warmed = liveBootstrapSettleCounter(t, ctx, client, warm, warmRow.ID, warmed)
	// Metadata remains disabled and cannot admit the new user. No request uses
	// the user's credential until after the complete new inbound is applied.
	userRow := liveBootstrapAddMetadata(t, ctx, client, user)
	liveBootstrapRequireObjects(t, ctx, client, []liveBootstrapAccount{warm, user})
	liveBootstrapRequireConfig(t, ctx, client, warmConfig, targetURL)
	if err := client.UpdateXrayTemplate(ctx, XrayTemplate{Config: userConfig, OutboundTestURL: targetURL}); err != nil {
		t.Fatal("cannot apply the new complete user inbound after private warm-up")
	}
	liveBootstrapRequireConfig(t, ctx, client, userConfig, targetURL)
	initialUser := liveBootstrapCounter(t, ctx, client, user, userRow.ID)
	if initialUser.Up != 0 || initialUser.Down != 0 {
		t.Fatal("new user already had traffic before the single acceptance request")
	}
	probe(user) // Exactly once; no retries or readiness traffic under this identity.
	current, counted := liveBootstrapAwaitCounter(t, ctx, client, user, userRow.ID, initialUser, uint64(len(body)), 40*time.Second)
	if !counted {
		t.Logf("first_user_up=%d first_user_down=%d expected_body_bytes=%d", current.Up, current.Down, len(body))
		t.Fatal("the first user request was not fully accounted after private probe warm-up")
	}
	if current.Up > 4096 || current.Down > uint64(len(body))+4096 {
		t.Fatal("the first user's counters exceed the isolated single-request bound")
	}
	finalWarm := liveBootstrapCounter(t, ctx, client, warm, warmRow.ID)
	if finalWarm != warmed {
		t.Fatal("the user's request changed the independent private-probe counter")
	}
	liveBootstrapRequireObjects(t, ctx, client, []liveBootstrapAccount{warm, user})
	liveBootstrapRequireConfig(t, ctx, client, userConfig, targetURL)
	t.Logf("first_user_request_counted=true user_requests=1 user_up=%d user_down=%d origin_body_bytes=%d credential_isolation_verified=true", current.Up, current.Down, len(body))
	t.Log("http_only=true no_local_xray_process=true first_activation_only=true panel_restart_covered=false hot_client_update_covered=false")
}

type liveBootstrapAccount struct {
	Prefix, Credential string
	Port               int
}

func TestBillingBootstrapFixtureRetainsGlobalsAndIndependentLevel3Users(t *testing.T) {
	baseline := json.RawMessage(`{"inbounds":[{"tag":"api"}],"outbounds":[{"tag":"direct","protocol":"freedom"}],"routing":{"rules":[{"type":"field","inboundTag":["api"],"outboundTag":"api"}]},"policy":{"levels":{"0":{"statsUserUplink":true,"statsUserDownlink":true}},"system":{"statsInboundUplink":true}},"stats":{}}`)
	before := append([]byte(nil), baseline...)
	warm := liveBootstrapAccount{Prefix: "private-probe", Credential: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Port: 31001}
	user := liveBootstrapAccount{Prefix: "first-user", Credential: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Port: 31002}
	warmConfig := liveBootstrapConfig(t, baseline, warm, "127.0.0.1:31003")
	warmBefore := append([]byte(nil), warmConfig...)
	next := liveBootstrapConfig(t, warmConfig, user, "127.0.0.1:31003")
	next = liveBootstrapReuseOutbound(t, warmConfig, next, warm.Prefix+"-redirect", user.Prefix+"-redirect")
	if !bytes.Equal(baseline, before) || !bytes.Equal(warmConfig, warmBefore) {
		t.Fatal("bootstrap fixture mutated its input snapshots")
	}
	var config struct {
		Inbounds []struct {
			Tag      string
			Settings struct {
				Clients []struct {
					ID, Email string
					Level     int
				}
			}
		}
		Outbounds []struct{ Tag string }
		Policy    struct {
			Levels map[string]struct{ StatsUserUplink, StatsUserDownlink bool }
		}
		Routing struct {
			Rules []struct {
				InboundTag  []string
				OutboundTag string
			}
		}
	}
	if json.Unmarshal(next, &config) != nil || len(config.Inbounds) != 3 || len(config.Outbounds) != 2 || len(config.Routing.Rules) != 3 {
		t.Fatal("bootstrap fixture did not retain the exact expected object set")
	}
	for i, account := range []liveBootstrapAccount{warm, user} {
		inbound := config.Inbounds[i+1]
		if inbound.Tag != account.tag() || len(inbound.Settings.Clients) != 1 {
			t.Fatal("bootstrap fixture lost an independent user inbound")
		}
		entry := inbound.Settings.Clients[0]
		if entry.ID != account.Credential || entry.Email != account.email() || entry.Level != 3 {
			t.Fatal("bootstrap fixture altered credential identity or runtime level")
		}
	}
	if config.Outbounds[1].Tag != warm.Prefix+"-redirect" || config.Policy.Levels["0"].StatsUserUplink || config.Policy.Levels["0"].StatsUserDownlink ||
		!config.Policy.Levels["3"].StatsUserUplink || !config.Policy.Levels["3"].StatsUserDownlink {
		t.Fatal("bootstrap fixture changed the shared outbound or discriminating statistics policy")
	}
	for i, expected := range []string{"api", user.tag(), warm.tag()} {
		rule := config.Routing.Rules[i]
		wantOutbound := warm.Prefix + "-redirect"
		if i == 0 {
			wantOutbound = "api"
		}
		if len(rule.InboundTag) != 1 || rule.InboundTag[0] != expected || rule.OutboundTag != wantOutbound {
			t.Fatal("bootstrap fixture changed API routing or independent probe paths")
		}
	}
}

func (a liveBootstrapAccount) email() string { return a.Prefix + "@invalid" }
func (a liveBootstrapAccount) tag() string   { return a.Prefix + "-in" }

func liveBootstrapConfig(t *testing.T, baseline json.RawMessage, account liveBootstrapAccount, originAddress string) json.RawMessage {
	t.Helper()
	config := liveWriteObject(t, liveHTTPProbeConfig(t, baseline, account.Prefix, account.Credential, account.Port, originAddress))
	var inbounds []json.RawMessage
	if json.Unmarshal(config["inbounds"], &inbounds) != nil || len(inbounds) == 0 {
		t.Fatal("bootstrap config lacks inbounds")
	}
	inbound := liveWriteObject(t, inbounds[len(inbounds)-1])
	inbound["settings"] = liveWriteJSON(t, map[string]any{"decryption": "none", "clients": []any{
		map[string]any{"id": account.Credential, "email": account.email(), "level": 3},
	}})
	inbounds[len(inbounds)-1] = liveWriteJSON(t, inbound)
	config["inbounds"] = liveWriteJSON(t, inbounds)
	policy := liveWriteObject(t, config["policy"])
	levels := liveWriteObject(t, policy["levels"])
	level0 := liveWriteObject(t, levels["0"])
	level0["statsUserUplink"], level0["statsUserDownlink"] = json.RawMessage("false"), json.RawMessage("false")
	levels["0"] = liveWriteJSON(t, level0)
	levels["3"] = liveWriteJSON(t, map[string]any{"statsUserUplink": true, "statsUserDownlink": true, "statsUserOnline": true})
	policy["levels"] = liveWriteJSON(t, levels)
	config["policy"] = liveWriteJSON(t, policy)
	return liveWriteJSON(t, config)
}

func liveBootstrapReuseOutbound(t *testing.T, warm, next json.RawMessage, existingTag, unusedTag string) json.RawMessage {
	t.Helper()
	config := liveWriteObject(t, next)
	config["outbounds"] = liveWriteObject(t, warm)["outbounds"]
	routing := liveWriteObject(t, config["routing"])
	var rules []json.RawMessage
	if json.Unmarshal(routing["rules"], &rules) != nil {
		t.Fatal("bootstrap configuration lacks routing rules")
	}
	changed := 0
	for i, raw := range rules {
		rule := liveWriteObject(t, raw)
		var tag string
		if json.Unmarshal(rule["outboundTag"], &tag) == nil && tag == unusedTag {
			rule["outboundTag"] = liveWriteJSON(t, existingTag)
			rules[i] = liveWriteJSON(t, rule)
			changed++
		}
	}
	if changed != 1 {
		t.Fatal("bootstrap fixture did not replace exactly one new outbound reference")
	}
	routing["rules"] = liveWriteJSON(t, rules)
	config["routing"] = liveWriteJSON(t, routing)
	// Only the new inbound and its routing rule may differ at activation.
	globals := liveWriteObject(t, warm)
	check := liveWriteObject(t, liveWriteJSON(t, config))
	delete(globals, "inbounds")
	delete(globals, "routing")
	delete(check, "inbounds")
	delete(check, "routing")
	liveProjectionRequireEquivalent(t, liveWriteJSON(t, globals), liveWriteJSON(t, check), "bootstrap global configuration")
	return liveWriteJSON(t, config)
}

func liveBootstrapAddMetadata(t *testing.T, ctx context.Context, client *Client, account liveBootstrapAccount) Inbound {
	t.Helper()
	metadata := map[string]any{"id": account.Credential, "email": account.email(), "enable": false,
		"limitIp": 0, "totalGB": 0, "expiryTime": 0, "reset": 0, "resetDay": 0, "resetMax": 0,
		"subId": liveWriteRandomID(t), "security": "auto"}
	row, err := client.AddInbound(ctx, Inbound{Tag: account.tag(), Remark: "Disabled private billing experiment",
		Enable: false, Listen: "127.0.0.1", Port: account.Port, Protocol: "vless", TrafficReset: "never",
		Settings:       liveWriteJSON(t, map[string]any{"decryption": "none", "clients": []any{metadata}}),
		StreamSettings: liveWriteJSON(t, map[string]any{"network": "tcp", "security": "none"}),
		Sniffing:       liveWriteJSON(t, map[string]any{"enabled": false})})
	if err != nil || row.ID <= 0 || row.Tag != account.tag() || row.Enable {
		t.Fatal("cannot create the owned disabled billing metadata")
	}
	return row
}

func liveBootstrapRequireObjects(t *testing.T, ctx context.Context, client *Client, accounts []liveBootstrapAccount) {
	t.Helper()
	rows, err := client.ListInbounds(ctx)
	if err != nil || len(rows) != len(accounts) {
		t.Fatal("bootstrap metadata inbound set changed unexpectedly")
	}
	clients, err := client.ListClients(ctx)
	if err != nil || len(clients) != len(accounts) {
		t.Fatal("bootstrap metadata client set changed unexpectedly")
	}
	for _, account := range accounts {
		var id int64
		for _, row := range rows {
			if row.Tag == account.tag() {
				if id != 0 || row.Enable || row.Listen != "127.0.0.1" || row.Port != account.Port || row.Protocol != "vless" {
					t.Fatal("bootstrap metadata inbound identity or disabled state changed")
				}
				id = row.ID
				var settings struct {
					Clients []json.RawMessage `json:"clients"`
				}
				if json.Unmarshal(row.Settings, &settings) != nil || len(settings.Clients) != 1 {
					t.Fatal("bootstrap metadata inbound has an unexpected client set")
				}
				liveWriteRequireFields(t, settings.Clients[0], liveWriteJSON(t, map[string]any{
					"id": account.Credential, "email": account.email(), "enable": false,
					"limitIp": 0, "totalGB": 0, "expiryTime": 0, "reset": 0, "resetDay": 0, "resetMax": 0}), "disabled bootstrap inbound client")
			}
		}
		found := false
		for _, row := range clients {
			if row.Email == account.email() {
				if found || id <= 0 || len(row.InboundIDs) != 1 || row.InboundIDs[0] != id {
					t.Fatal("bootstrap metadata client identity changed")
				}
				found = true
				liveWriteRequireFields(t, row.Raw, liveWriteJSON(t, map[string]any{
					"uuid": account.Credential, "email": account.email(), "enable": false, "limitIp": 0, "totalGB": 0, "expiryTime": 0,
					"reset": 0, "resetDay": 0, "resetMax": 0}), "disabled bootstrap client")
			}
		}
		if !found {
			t.Fatal("bootstrap metadata client disappeared")
		}
	}
}

func liveBootstrapRequireConfig(t *testing.T, ctx context.Context, client *Client, expected json.RawMessage, targetURL string) {
	t.Helper()
	liveBootstrapRequireSnapshots(t, ctx, client, expected, expected, targetURL)
}

func liveBootstrapRequireSnapshots(t *testing.T, ctx context.Context, client *Client, storedExpected, assembledExpected json.RawMessage, targetURL string) {
	t.Helper()
	stored, err := client.ReadXrayTemplate(ctx)
	if err != nil || stored.OutboundTestURL != targetURL {
		t.Fatal("cannot read the expected bootstrap template and test URL")
	}
	liveProjectionRequireEquivalent(t, storedExpected, stored.Config, "bootstrap stored template")
	assembled, err := client.ReadAssembledConfig(ctx)
	if err != nil {
		t.Fatal("cannot read the expected bootstrap assembled configuration")
	}
	liveProjectionRequireEquivalent(t, assembledExpected, assembled, "bootstrap assembled configuration")
}

func liveBootstrapCounter(t *testing.T, ctx context.Context, client *Client, account liveBootstrapAccount, inboundID int64) ClientTraffic {
	t.Helper()
	rows, err := client.ClientTraffics(ctx)
	if err != nil {
		t.Fatal("cannot read official bootstrap client counters")
	}
	return liveProjectionOwnedCounter(t, rows, account.email(), inboundID)
}

func liveBootstrapAwaitCounter(t *testing.T, ctx context.Context, client *Client, account liveBootstrapAccount, inboundID int64, initial ClientTraffic, minDown uint64, budget time.Duration) (ClientTraffic, bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	last := initial
	for {
		current := liveBootstrapCounter(t, ctx, client, account, inboundID)
		if current.ID != initial.ID || current.Up < last.Up || current.Down < last.Down {
			t.Fatal("bootstrap counter identity changed or cumulative values decreased")
		}
		last = current
		if current.Up > initial.Up && current.Down-initial.Down >= minDown {
			return current, true
		}
		if !time.Now().Before(deadline) {
			return current, false
		}
		select {
		case <-ctx.Done():
			t.Fatal("bootstrap accounting observation exceeded its parent deadline")
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func liveBootstrapSettleCounter(t *testing.T, ctx context.Context, client *Client, account liveBootstrapAccount, inboundID int64, previous ClientTraffic) ClientTraffic {
	t.Helper()
	deadline, stableSince := time.Now().Add(20*time.Second), time.Now()
	for {
		current := liveBootstrapCounter(t, ctx, client, account, inboundID)
		if current.ID != previous.ID || current.Up < previous.Up || current.Down < previous.Down {
			t.Fatal("private probe counter changed identity or decreased while settling")
		}
		if current != previous {
			stableSince = time.Now()
			previous = current
		}
		// The audited official job runs every five seconds. Stability alone is
		// not proof of job health; prior actual increments establish warm-up.
		if time.Since(stableSince) >= 6*time.Second {
			return current
		}
		if time.Now().After(deadline) {
			t.Fatal("private probe counter did not settle within the bounded observation")
		}
		select {
		case <-ctx.Done():
			t.Fatal("private probe counter settling exceeded the parent deadline")
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func liveBootstrapRestore(t *testing.T, client *Client, identity Status, snapshot XrayTemplate, baseline json.RawMessage, targetURL string, candidates []json.RawMessage, accounts []liveBootstrapAccount) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	actual, err := client.Status(ctx)
	if err != nil || actual.PanelGUID != identity.PanelGUID || actual.PanelVersion != identity.PanelVersion {
		t.Error("bootstrap cleanup refused an unconfirmed provider identity")
		return
	}
	stored, err := client.ReadXrayTemplate(ctx)
	if err != nil || (stored.OutboundTestURL != targetURL && stored.OutboundTestURL != snapshot.OutboundTestURL) {
		t.Error("bootstrap cleanup refused an unconfirmed test URL")
		return
	}
	owned := false
	for _, candidate := range append(candidates, snapshot.Config) {
		comparison, err := CompareConfig(candidate, stored.Config)
		owned = owned || (err == nil && comparison.Equivalent)
	}
	if !owned {
		t.Error("bootstrap cleanup refused to overwrite an unowned template")
		return
	}
	rows, err := client.ListInbounds(ctx)
	if err != nil {
		t.Error("bootstrap cleanup cannot enumerate metadata inbounds")
		return
	}
	clients, err := client.ListClients(ctx)
	if err != nil {
		t.Error("bootstrap cleanup cannot enumerate metadata clients")
		return
	}
	// Compare the entire present object set before the first cleanup mutation.
	present := make([]liveBootstrapAccount, 0, len(rows))
	for _, row := range rows {
		found := false
		for _, account := range accounts {
			if row.Tag == account.tag() {
				present = append(present, account)
				found = true
			}
		}
		if !found {
			t.Error("bootstrap cleanup refused foreign metadata")
			return
		}
	}
	if len(clients) != len(present) {
		t.Error("bootstrap cleanup refused an unexpected client set")
		return
	}
	liveBootstrapRequireObjects(t, ctx, client, present)
	if err := client.UpdateXrayTemplate(ctx, snapshot); err != nil {
		t.Error("bootstrap cleanup cannot restore the original template before removing metadata")
		return
	}
	liveBootstrapRequireSnapshots(t, ctx, client, snapshot.Config, baseline, snapshot.OutboundTestURL)
	for _, row := range clients {
		if err := client.DeleteClient(ctx, row.Email, false); err != nil {
			t.Error("bootstrap cleanup cannot remove owned client metadata")
			return
		}
	}
	for _, row := range rows {
		if err := client.DeleteInbound(ctx, row.ID); err != nil {
			t.Error("bootstrap cleanup cannot remove owned inbound metadata")
			return
		}
	}
	// Official inbound deletion may rewrite routing even for disabled rows.
	if err := client.UpdateXrayTemplate(ctx, snapshot); err != nil {
		t.Error("bootstrap cleanup cannot restore the final original template")
		return
	}
	liveBootstrapRequireSnapshots(t, ctx, client, snapshot.Config, baseline, snapshot.OutboundTestURL)
	liveHTTPProbeRequireEmpty(t, ctx, client)
	liveHTTPProbeRequireIdentity(t, ctx, client, identity)
	t.Log("cleanup_verified=true template_restored=true metadata_removed=true original_url_restored=true assembled_config_unchanged=true")
}
