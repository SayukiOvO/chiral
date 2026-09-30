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

// TestLive3XUITailAccountingContract observes revocation on an otherwise empty,
// network-none official instance. The simple managed level-zero fixture does
// not certify arbitrary-field preservation, atomic configuration replacement,
// final settlement, or an Agent/Core integration. Only HTTP controls the
// provider. A separate disposable Xray client is a data-plane test instrument.
// The runner stops and retains the whole lab, including its accounting rows;
// this test deliberately does not purge those rows to claim clean settlement.
func TestLive3XUITailAccountingContract(t *testing.T) {
	optIn := os.Getenv("CHIRAL_3XUI_TEST_WRITE")
	if optIn == "" {
		t.Skip("set CHIRAL_3XUI_TEST_WRITE=isolated-empty-instance to enable the tail accounting experiment")
	}
	if optIn != "isolated-empty-instance" {
		t.Fatal("tail accounting requires explicit isolated-empty-instance opt-in")
	}
	liveHTTPProbeRequireLoopbackNamespace(t)
	baseURL, tokenFile := os.Getenv("CHIRAL_3XUI_TEST_URL"), os.Getenv("CHIRAL_3XUI_TEST_TOKEN_FILE")
	if baseURL == "" || tokenFile == "" {
		t.Fatal("tail accounting requires an isolated URL and token file")
	}
	client, err := New(Config{BaseURL: baseURL, Token: readLive3XUIToken(t, tokenFile), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal("cannot construct the isolated tail accounting client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	liveHTTPProbeRequireEmpty(t, ctx, client)
	baseline, err := client.ReadAssembledConfig(ctx)
	if err != nil {
		t.Fatal("cannot read the empty tail accounting configuration")
	}
	liveWriteRequireAPIOnly(t, baseline)
	identity, err := liveUpdateWaitState(ctx, client, "running")
	if err != nil {
		t.Fatal("cannot read the isolated provider identity")
	}
	snapshot, err := client.ReadXrayTemplate(ctx)
	if err != nil || snapshot.OutboundTestURL == "" {
		t.Fatal("cannot read the empty tail accounting template")
	}
	liveWriteRequireAPIOnly(t, snapshot.Config)
	restoreSettings := liveTailDisableRestart(t, ctx, client)
	defer restoreSettings()

	prefix, inboundPort := "chiral-tail-"+liveWriteRandomID(t), liveProjectionFreePort(t)
	targetID, controlID := liveWriteUUID(t), liveWriteUUID(t)
	if targetID == controlID {
		t.Fatal("tail fixture credentials collided")
	}
	fixture := liveTailStartFixture(t, ctx, targetID, controlID, inboundPort)
	defer fixture.Close(t)
	config := liveWriteObject(t, liveHTTPProbeConfig(t, baseline, prefix, targetID, inboundPort, fixture.OriginAddress()))
	// The template owns only routing/outbound and stats policy. The inbound and
	// both accounts below are genuinely managed objects, not raw-template users
	// hidden behind disabled metadata.
	config["inbounds"] = liveWriteObject(t, baseline)["inbounds"]
	policy := liveWriteObject(t, config["policy"])
	levels := liveWriteObject(t, policy["levels"])
	level := liveWriteObject(t, levels["0"])
	level["statsUserUplink"], level["statsUserDownlink"] = json.RawMessage("true"), json.RawMessage("true")
	levels["0"] = liveWriteJSON(t, level)
	policy["levels"] = liveWriteJSON(t, levels)
	config["policy"] = liveWriteJSON(t, policy)
	if err := client.UpdateXrayTemplate(ctx, XrayTemplate{Config: liveWriteJSON(t, config), OutboundTestURL: snapshot.OutboundTestURL}); err != nil {
		t.Fatal("cannot install the owned tail fixture routing")
	}
	account := func(id, suffix string) map[string]any {
		return map[string]any{"id": id, "email": prefix + "-" + suffix + "@invalid", "enable": true,
			"security": "auto", "limitIp": 0, "totalGB": 0, "expiryTime": 0, "reset": 0,
			"resetDay": 0, "resetMax": 0, "subId": liveWriteRandomID(t), "trafficReset": "never"}
	}
	target, control := account(targetID, "target"), account(controlID, "control")
	row, err := client.AddInbound(ctx, Inbound{Tag: prefix + "-in", Remark: "Isolated revocation tail accounting experiment",
		Enable: true, Listen: "127.0.0.1", Port: inboundPort, Protocol: "vless", TrafficReset: "never",
		Settings:       liveWriteJSON(t, map[string]any{"decryption": "none", "clients": []any{target, control}}),
		StreamSettings: json.RawMessage(`{"network":"tcp","security":"none"}`), Sniffing: json.RawMessage(`{"enabled":false}`)})
	if err != nil || row.ID <= 0 || row.Tag != prefix+"-in" {
		t.Fatal("cannot create the exact managed tail fixture")
	}
	targetEmail, controlEmail := target["email"].(string), control["email"].(string)
	liveTailRequireClients(t, ctx, client, row, target, control)
	fixture.Fresh(t, ctx, true)
	fixture.StartStreams(t, ctx)
	// Warming is explicit and confined to this post-baseline experiment. The
	// accepted upstream cold-start loss is neither repaired nor measured here.
	var before ClientTraffic
	warmDeadline := time.Now().Add(35 * time.Second)
	for {
		fixture.Chunk(t, ctx)
		var targetPresent bool
		before, targetPresent = liveTailLocalCounter(t, ctx, client, targetEmail, row.ID)
		other, controlPresent := liveTailLocalCounter(t, ctx, client, controlEmail, row.ID)
		if targetPresent && controlPresent && before.Down > 0 && other.Down > 0 {
			break
		}
		if time.Now().After(warmDeadline) {
			t.Fatal("both official local counters did not become positive before revocation")
		}
		liveTailPause(t, ctx)
	}
	t.Log("tail_baseline_verified=true")
	// Allow already issued fixture bytes several collection rounds without
	// generating more traffic. This bounds baseline attribution noise; even a
	// stable value is NOT evidence of a provider-side final-sample barrier.
	quietDeadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(quietDeadline) {
		current, present := liveTailLocalCounter(t, ctx, client, targetEmail, row.ID)
		if !present || current.ID != before.ID || current.Up < before.Up || current.Down < before.Down {
			t.Fatal("target baseline counter disappeared or reset before revocation")
		}
		before = current
		liveTailPause(t, ctx)
	}
	target["enable"] = false
	if err := client.UpdateClient(ctx, targetEmail, liveWriteJSON(t, target)); err != nil {
		t.Fatal("disable result is unknown or failed; no retry is permitted")
	}
	liveTailRequireClients(t, ctx, client, row, target, control)
	fixture.Fresh(t, ctx, false)
	disabled := liveTailObserveWindow(t, ctx, client, fixture, targetEmail, controlEmail, row.ID, before, 20*time.Second)
	fixture.Fresh(t, ctx, false)
	t.Logf("disable_observation_complete=true target_local_row_present=%t target_counter_advanced=%t target_down_before=%d target_down_after=%d",
		disabled.Present, disabled.Present && disabled.Last.Down > before.Down, before.Down, disabled.Last.Down)

	if err := client.DeleteClient(ctx, targetEmail, true); err != nil {
		t.Fatal("keepTraffic deletion result is unknown or failed; no retry is permitted")
	}
	remaining, err := client.ListClients(ctx)
	if err != nil || len(remaining) != 1 || remaining[0].Email != controlEmail ||
		len(remaining[0].InboundIDs) != 1 || remaining[0].InboundIDs[0] != row.ID {
		t.Fatal("keepTraffic deletion did not remove only the owned target account")
	}
	liveTailRequireClients(t, ctx, client, row, control)
	deleted := liveTailObserveWindow(t, ctx, client, fixture, targetEmail, controlEmail, row.ID, disabled.Last, 15*time.Second)
	fixture.Fresh(t, ctx, false)
	// This alternate route can contain master aggregates. It is diagnostic
	// evidence in a new standalone instance only, never a billing fallback.
	u, err := client.clientEndpoint("panel/api/clients/traffic", targetEmail)
	if err != nil {
		t.Fatal("cannot form the exact owned traffic diagnostic URL")
	}
	// The normal mutation/read helper correctly rejects null objects. This
	// diagnostic explicitly distinguishes a successful null from HTTP/API or
	// schema failure, without weakening that production helper's contract.
	body, err := client.request(ctx, http.MethodGet, u, nil, "", true)
	if err != nil {
		t.Fatal("cannot read the post-delete traffic diagnostic")
	}
	var response envelope
	if json.Unmarshal(body, &response) != nil || response.Success == nil {
		t.Fatal("post-delete traffic diagnostic did not return a boolean result envelope")
	}
	diagnostic := response.Object
	diagnosticState := "api_rejected"
	if *response.Success {
		if len(bytes.TrimSpace(diagnostic)) == 0 {
			t.Fatal("successful post-delete traffic diagnostic omitted its object")
		}
		diagnosticState = "absent"
		if !bytes.Equal(bytes.TrimSpace(diagnostic), []byte("null")) {
			diagnosticState = "present"
		}
	}
	if diagnosticState == "present" {
		var retained clientTrafficWire
		if json.Unmarshal(diagnostic, &retained) != nil || !validTrafficCounters(retained.trafficCounters) ||
			retained.Email != targetEmail || retained.InboundID == nil || *retained.InboundID != row.ID || *retained.ID != before.ID {
			t.Fatal("post-delete traffic diagnostic returned an unexpected object")
		}
	}
	liveHTTPProbeRequireIdentity(t, ctx, client, identity)
	t.Logf("delete_observation_complete=true target_local_row_present=%t target_seen_during_window=%t target_down_before=%d target_down_after=%d alternate_diagnostic_state=%s",
		deleted.Present, deleted.Seen, disabled.Last.Down, deleted.Last.Down, diagnosticState)
	t.Log("established_streams_survived=true new_target_connections_denied=true new_control_connections_verified=true")
	t.Log("http_only_provider_control=true tail_observation_complete=true final_settlement_verified=false core_integration_verified=false arbitrary_field_preservation_verified=false")
}

type liveTailObservation struct {
	Last          ClientTraffic
	Present, Seen bool
}

func liveTailRequireClients(t *testing.T, ctx context.Context, client *Client, inbound Inbound, accounts ...map[string]any) {
	t.Helper()
	rows, err := client.ListClients(ctx)
	if err != nil || len(rows) != len(accounts) {
		t.Fatal("tail fixture managed account set differs from the expected set")
	}
	wanted := make(map[string]map[string]any, len(accounts))
	for _, account := range accounts {
		wanted[account["email"].(string)] = account
	}
	for _, row := range rows {
		account, ok := wanted[row.Email]
		if !ok || len(row.InboundIDs) != 1 || row.InboundIDs[0] != inbound.ID {
			t.Fatal("tail fixture account ownership or attachment changed")
		}
		fields := liveWriteObject(t, liveWriteJSON(t, account))
		fields["uuid"] = fields["id"]
		delete(fields, "id")
		liveWriteRequireFields(t, row.Raw, liveWriteJSON(t, fields), "tail fixture account")
	}
	assembled, err := client.ReadAssembledConfig(ctx)
	if err != nil {
		t.Fatal("cannot read assembled tail fixture accounts")
	}
	var inbounds []struct {
		Tag      string `json:"tag"`
		Settings struct {
			Clients []struct{ ID, Email string } `json:"clients"`
		} `json:"settings"`
	}
	if json.Unmarshal(liveWriteObject(t, assembled)["inbounds"], &inbounds) != nil {
		t.Fatal("cannot decode assembled tail fixture accounts")
	}
	for _, row := range inbounds {
		if row.Tag != inbound.Tag {
			continue
		}
		enabled := 0
		for _, account := range accounts {
			if account["enable"] == true {
				enabled++
			}
		}
		if len(row.Settings.Clients) != enabled {
			t.Fatal("assembled tail fixture contains an unexpected number of enabled accounts")
		}
		seen := make(map[string]bool)
		for _, entry := range row.Settings.Clients {
			account, ok := wanted[entry.Email]
			if !ok || seen[entry.Email] || account["enable"] != true || account["id"] != entry.ID {
				t.Fatal("assembled tail fixture has an unexpected credential or enable state")
			}
			seen[entry.Email] = true
		}
		return
	}
	t.Fatal("managed tail fixture inbound disappeared from the assembled configuration")
}

func liveTailLocalCounter(t *testing.T, ctx context.Context, client *Client, email string, inboundID int64) (ClientTraffic, bool) {
	t.Helper()
	rows, err := client.ClientTraffics(ctx)
	if err != nil {
		t.Fatal("cannot read official raw local client counters")
	}
	for _, row := range rows {
		if row.Email == email {
			if row.InboundID != inboundID {
				t.Fatal("tail counter moved to an unexpected inbound")
			}
			return row, true
		}
	}
	return ClientTraffic{}, false
}

func liveTailObserveWindow(t *testing.T, ctx context.Context, client *Client, fixture *liveTailFixture, target, control string, inboundID int64, initial ClientTraffic, duration time.Duration) liveTailObservation {
	t.Helper()
	result := liveTailObservation{Last: initial}
	controlBefore, ok := liveTailLocalCounter(t, ctx, client, control, inboundID)
	if !ok {
		t.Fatal("control counter disappeared before the observation window")
	}
	deadline := time.Now().Add(duration)
	var controlLast ClientTraffic
	for {
		// Each chunk is requested only after the mutation's complete response.
		// Buffered pre-mutation bytes cannot satisfy this continuity check.
		fixture.Chunk(t, ctx)
		current, present := liveTailLocalCounter(t, ctx, client, target, inboundID)
		result.Present = present
		if present {
			if current.ID != initial.ID || current.Up < result.Last.Up || current.Down < result.Last.Down {
				t.Fatal("target counter identity changed or reset during the bounded observation")
			}
			result.Last, result.Seen = current, true
		}
		controlLast, ok = liveTailLocalCounter(t, ctx, client, control, inboundID)
		if !ok || controlLast.ID != controlBefore.ID || controlLast.Up < controlBefore.Up || controlLast.Down < controlBefore.Down {
			t.Fatal("control counter disappeared, changed identity or reset")
		}
		if time.Now().After(deadline) {
			break
		}
		liveTailPause(t, ctx)
	}
	if controlLast.Down <= controlBefore.Down {
		t.Fatal("official collector did not advance the control counter during the observation window")
	}
	return result
}

func liveTailPause(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
		t.Fatal("tail observation exceeded its context budget")
	case <-time.After(400 * time.Millisecond):
	}
}
