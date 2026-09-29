package threexui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLive3XUIWriteContract is destructive ONLY to its newly created test
// objects. Run against a disposable, otherwise idle official 3x-ui instance:
//
//	CHIRAL_3XUI_TEST_WRITE=isolated-empty-instance
//	CHIRAL_3XUI_TEST_URL=<isolated instance URL>
//	CHIRAL_3XUI_TEST_TOKEN_FILE=<private mode-0600 token file>
//	CHIRAL_3XUI_TEST_XRAY_BIN=<absolute fixture client binary path>
//
// The instance must have no managed inbounds or clients, no non-API template
// inbounds, and free loopback ports 39081 and 39082. The test must run in the
// same network namespace as that instance. No Xray management API access,
// managed filesystem edits, production access, or upstream patches are used.
// A disposable client process uses only its own temporary fixture config. Official
// writes may cause 3x-ui to restart Xray; no uninterrupted-session claim is made.
// The final subtest deliberately fails if an Xray client field is discarded.
func TestLive3XUIWriteContract(t *testing.T) {
	optIn := os.Getenv("CHIRAL_3XUI_TEST_WRITE")
	if optIn == "" {
		t.Skip("set CHIRAL_3XUI_TEST_WRITE=isolated-empty-instance to allow writes to a disposable empty instance")
	}
	if optIn != "isolated-empty-instance" {
		t.Fatal("CHIRAL_3XUI_TEST_WRITE must equal isolated-empty-instance")
	}
	baseURL, tokenFile := os.Getenv("CHIRAL_3XUI_TEST_URL"), os.Getenv("CHIRAL_3XUI_TEST_TOKEN_FILE")
	if baseURL == "" || tokenFile == "" {
		t.Fatal("write opt-in also requires CHIRAL_3XUI_TEST_URL and CHIRAL_3XUI_TEST_TOKEN_FILE")
	}
	client, err := New(Config{BaseURL: baseURL, Token: readLive3XUIToken(t, tokenFile), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal("cannot construct the isolated live write client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// These GET checks precede even the template POST read, which can heal
	// legacy storage in upstream. A nonempty managed instance is never mutated.
	inbounds, err := client.ListInbounds(ctx)
	if err != nil || len(inbounds) != 0 {
		t.Fatal("write preflight requires a successfully read empty inbound list")
	}
	clients, err := client.ListClients(ctx)
	if err != nil || len(clients) != 0 {
		t.Fatal("write preflight requires a successfully read empty client list")
	}
	before, err := client.ReadAssembledConfig(ctx)
	if err != nil {
		t.Fatalf("read initial assembled configuration: %v", err)
	}
	liveWriteRequireAPIOnly(t, before)
	before = normaliseLive3XUIConfig(t, before)
	snapshot, err := client.ReadXrayTemplate(ctx)
	if err != nil {
		t.Fatalf("read isolated template snapshot: %v", err)
	}
	liveWriteRequireAPIOnly(t, snapshot.Config)
	if snapshot.OutboundTestURL == "" {
		t.Fatal("cannot safely restore a template with an empty outbound test URL")
	}
	baselineTemplate := normaliseLive3XUIConfig(t, snapshot.Config)
	prefix := "chiral-live-" + liveWriteRandomID(t)
	ownedTags := map[string]bool{}
	ownedEmails := map[string]bool{}
	templateAttempted := false

	// Register recovery before the first write, and resolve objects by exact
	// random ownership keys after a partial or ambiguous HTTP failure. Never
	// delete an unrecognised object. Cleanup uses its own non-cancelled context.
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cleanupCancel()
		cleanupOK := true
		cleanupErrorf := func(format string, args ...any) {
			cleanupOK = false
			t.Errorf(format, args...)
		}
		rows, listErr := client.ListClients(cleanupCtx)
		if listErr != nil {
			cleanupErrorf("cleanup could not enumerate clients: %v", listErr)
		} else {
			for _, row := range rows {
				if !ownedEmails[row.Email] {
					cleanupErrorf("unexpected client appeared in the isolated instance; it was not deleted")
					continue
				}
				if err := client.DeleteClient(cleanupCtx, row.Email, false); err != nil {
					cleanupErrorf("cleanup could not remove an owned client: %v", err)
				}
			}
		}
		inboundRows, listErr := client.ListInbounds(cleanupCtx)
		if listErr != nil {
			cleanupErrorf("cleanup could not enumerate inbounds: %v", listErr)
		} else {
			for _, row := range inboundRows {
				if !ownedTags[row.Tag] {
					cleanupErrorf("unexpected inbound appeared in the isolated instance; it was not deleted")
					continue
				}
				if err := client.DeleteInbound(cleanupCtx, row.ID); err != nil {
					cleanupErrorf("cleanup could not remove an owned inbound: %v", err)
				}
			}
		}
		if templateAttempted {
			if err := client.UpdateXrayTemplate(cleanupCtx, snapshot); err != nil {
				cleanupErrorf("cleanup could not restore the template snapshot: %v", err)
			}
		}
		remainingClients, err := client.ListClients(cleanupCtx)
		if err != nil || len(remainingClients) != 0 {
			cleanupErrorf("cleanup did not verify an empty client list")
		}
		remainingInbounds, err := client.ListInbounds(cleanupCtx)
		if err != nil || len(remainingInbounds) != 0 {
			cleanupErrorf("cleanup did not verify an empty inbound list")
		}
		restored, err := client.ReadXrayTemplate(cleanupCtx)
		if err != nil {
			cleanupErrorf("cleanup could not read the restored template: %v", err)
		} else if restored.OutboundTestURL != snapshot.OutboundTestURL || !bytes.Equal(baselineTemplate, normaliseLive3XUIConfig(t, restored.Config)) {
			liveWriteReportTemplateDifferences(t, snapshot, restored)
			cleanupErrorf("cleanup template differs from its original snapshot")
		}
		after, err := client.ReadAssembledConfig(cleanupCtx)
		if err != nil {
			cleanupErrorf("cleanup could not read final assembled configuration: %v", err)
		} else if !bytes.Equal(before, normaliseLive3XUIConfig(t, after)) {
			liveWriteReportTemplateDifferences(t, XrayTemplate{Config: before}, XrayTemplate{Config: after})
			cleanupErrorf("cleanup assembled configuration differs from the initial canonical JSON")
		}
		if cleanupOK {
			t.Log("cleanup_verified=true empty_objects=true template_restored=true assembled_config_unchanged=true")
		}
	}()

	if !t.Run("template_outbound_and_routing", func(t *testing.T) {
		config := liveWriteObject(t, snapshot.Config)
		outbound := liveWriteJSON(t, map[string]any{"tag": prefix + "-block", "protocol": "blackhole", "settings": map[string]any{}})
		fixtureOutbound := liveWriteJSON(t, map[string]any{"tag": prefix + "-loopback", "protocol": "freedom", "settings": map[string]any{
			"finalRules": []any{map[string]any{"action": "allow", "ip": []string{"127.0.0.1/32", "::1/128"}}, map[string]any{"action": "block"}},
		}})
		var outbounds []json.RawMessage
		if err := json.Unmarshal(config["outbounds"], &outbounds); err != nil {
			t.Fatal("template must contain an outbound array")
		}
		config["outbounds"] = liveWriteJSON(t, append(outbounds, outbound, fixtureOutbound))
		routing := liveWriteObject(t, config["routing"])
		var rules []json.RawMessage
		if err := json.Unmarshal(routing["rules"], &rules); err != nil {
			t.Fatal("template must contain a routing rule array")
		}
		rule := liveWriteJSON(t, map[string]any{"type": "field", "domain": []string{"full:chiral-live.invalid"}, "outboundTag": prefix + "-block"})
		fixtureRule := liveWriteJSON(t, map[string]any{"type": "field", "inboundTag": []string{prefix + "-in-0", prefix + "-in-1"},
			"ip": []string{"127.0.0.1/32", "::1/128"}, "outboundTag": prefix + "-loopback"})
		// Upstream reserves rule zero for its API. Require the snapshot to
		// already have that exact rule, then insert only our owned-inbound
		// exception after it. Every existing rule retains its relative order.
		fixtureRules, err := liveWriteInsertFixtureRule(rules, fixtureRule)
		if err != nil {
			t.Fatal(err)
		}
		routing["rules"] = liveWriteJSON(t, append(fixtureRules, rule))
		config["routing"] = liveWriteJSON(t, routing)
		candidate := XrayTemplate{Config: liveWriteJSON(t, config), OutboundTestURL: snapshot.OutboundTestURL}
		templateAttempted = true
		if err := client.UpdateXrayTemplate(ctx, candidate); err != nil {
			t.Fatalf("write isolated template: %v", err)
		}
		readback, err := client.ReadXrayTemplate(ctx)
		if err != nil {
			t.Fatalf("read candidate template: %v", err)
		}
		if readback.OutboundTestURL != candidate.OutboundTestURL || !bytes.Equal(normaliseLive3XUIConfig(t, candidate.Config), normaliseLive3XUIConfig(t, readback.Config)) {
			liveWriteReportTemplateDifferences(t, candidate, readback)
			t.Fatal("candidate template did not round-trip all submitted fields")
		}
		assembled, err := client.ReadAssembledConfig(ctx)
		if err != nil {
			t.Fatalf("read candidate assembled configuration: %v", err)
		}
		assembledObject := liveWriteObject(t, assembled)
		liveWriteRequireArrayMember(t, assembledObject["outbounds"], outbound, "assembled outbound")
		liveWriteRequireArrayMember(t, assembledObject["outbounds"], fixtureOutbound, "assembled fixture outbound")
		liveWriteRequireArrayMember(t, liveWriteObject(t, assembledObject["routing"])["rules"], rule, "assembled routing rule")
		liveWriteRequireArrayMember(t, liveWriteObject(t, assembledObject["routing"])["rules"], fixtureRule, "assembled fixture routing rule")
	}) {
		return
	}

	var created []Inbound
	crudCompleted := false
	t.Run("managed_inbound_and_client_crud", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			inbound := Inbound{Tag: fmt.Sprintf("%s-in-%d", prefix, i), Remark: "Reserved live API contract test", Enable: true,
				Listen: "127.0.0.1", Port: 39081 + i, Protocol: "vless", Settings: json.RawMessage(`{"clients":[],"decryption":"none"}`),
				StreamSettings: json.RawMessage(`{"network":"tcp","security":"none","tcpSettings":{"header":{"type":"none"}}}`),
				Sniffing:       json.RawMessage(`{"enabled":false,"destOverride":["http","tls"],"metadataOnly":false,"routeOnly":false}`),
				TrafficReset:   "never", TrafficResetDay: 1, SubSortIndex: 1}
			ownedTags[inbound.Tag] = true
			added, err := client.AddInbound(ctx, inbound)
			if err != nil {
				t.Fatalf("create isolated inbound: %v", err)
			}
			liveWriteRequireFields(t, added.Raw, liveWriteJSON(t, inbound), "created inbound")
			liveWriteCheckInbound(t, ctx, client, added.ID, inbound)
			created = append(created, added)
		}
		updatedInbound := created[0]
		updatedInbound.Remark = "Updated reserved live API contract test"
		updated, err := client.UpdateInbound(ctx, updatedInbound.ID, updatedInbound)
		if err != nil {
			t.Fatalf("update isolated inbound: %v", err)
		}
		liveWriteRequireFields(t, updated.Raw, liveWriteJSON(t, updatedInbound), "updated inbound")
		liveWriteCheckInbound(t, ctx, client, updated.ID, updatedInbound)
		created[0] = updated

		email := prefix + "-client@invalid"
		ownedEmails[email] = true
		account := map[string]any{"id": liveWriteUUID(t), "email": email, "enable": true, "security": "auto", "limitIp": 0,
			"totalGB": 0, "expiryTime": 0, "tgId": 0, "subId": liveWriteRandomID(t), "comment": "Reserved live API contract test",
			"reset": 0, "resetDay": 0, "resetMax": 0, "trafficReset": "never", "trafficResetDay": 1}
		if err := client.AddClient(ctx, liveWriteJSON(t, account), []int64{created[0].ID}); err != nil {
			t.Fatalf("create isolated client: %v", err)
		}
		liveWriteCheckClient(t, ctx, client, account, []int64{created[0].ID}, created)
		// Preserve a cold-start accounting failure as a failed subtest, but
		// collect independent steady-state and CRUD evidence on this same
		// owned account. No accounting failure is converted to a pass or skip.
		t.Run("cold_start_dataplane_and_official_http_billing", func(t *testing.T) {
			liveWriteTrafficContract(t, ctx, client, account, created[0])
		})
		t.Run("steady_state_dataplane_and_official_http_billing", func(t *testing.T) {
			liveWriteTrafficContract(t, ctx, client, account, created[0])
		})
		account["id"], account["enable"] = liveWriteUUID(t), false
		if err := client.UpdateClient(ctx, email, liveWriteJSON(t, account)); err != nil {
			t.Fatalf("update isolated credential and disable client: %v", err)
		}
		liveWriteCheckClient(t, ctx, client, account, []int64{created[0].ID}, created)
		account["enable"] = true
		if err := client.UpdateClient(ctx, email, liveWriteJSON(t, account)); err != nil {
			t.Fatalf("re-enable isolated client: %v", err)
		}
		if err := liveWriteAttachment(ctx, client, email, created[1].ID, true); err != nil {
			t.Fatalf("attach isolated client to second owned inbound: %v", err)
		}
		liveWriteCheckClient(t, ctx, client, account, []int64{created[0].ID, created[1].ID}, created)
		if err := liveWriteAttachment(ctx, client, email, created[0].ID, false); err != nil {
			t.Fatalf("detach isolated client from first owned inbound: %v", err)
		}
		liveWriteCheckClient(t, ctx, client, account, []int64{created[1].ID}, created)
		if err := client.DeleteClient(ctx, email, false); err != nil {
			t.Fatalf("delete isolated client: %v", err)
		}
		rows, err := client.ListClients(ctx)
		if err != nil || len(rows) != 0 {
			t.Fatal("client deletion did not produce an empty client list")
		}
		assembled, err := client.ReadAssembledConfig(ctx)
		if err != nil {
			t.Fatalf("read assembled configuration after client deletion: %v", err)
		}
		var remaining []struct {
			Settings struct {
				Clients []json.RawMessage `json:"clients"`
			} `json:"settings"`
		}
		if err := json.Unmarshal(liveWriteObject(t, assembled)["inbounds"], &remaining); err != nil {
			t.Fatal("assembled inbound array after deletion is missing or invalid")
		}
		for _, inbound := range remaining {
			if len(inbound.Settings.Clients) != 0 {
				t.Fatal("client deletion left clients in the assembled configuration")
			}
		}
		crudCompleted = true
	})
	// A fatal API/readback failure stops dependent field-fidelity checks.
	// Accounting subtest failures alone must not hide that independent result.
	if !crudCompleted {
		return
	}
	if len(created) != 2 {
		t.Fatal("run the complete live write contract without filtering out the managed CRUD subtest")
	}

	// level is a real Xray client-entry field, not a 3x-ui management field.
	// Success for the supported CRUD corpus cannot hide its loss in a typed
	// upstream model. Keep this failure separate and never downgrade it to SKIP.
	t.Run("strict_client_level_fidelity", func(t *testing.T) {
		email := prefix + "-fidelity@invalid"
		ownedEmails[email] = true
		account := map[string]any{"id": liveWriteUUID(t), "email": email, "enable": true, "level": 3}
		if err := client.AddClient(ctx, liveWriteJSON(t, account), []int64{created[0].ID}); err != nil {
			t.Fatalf("submit isolated client field corpus: %v", err)
		}
		liveWriteCheckClient(t, ctx, client, account, []int64{created[0].ID}, created)
	})
}

func liveWriteRandomID(t *testing.T) string {
	t.Helper()
	var data [12]byte
	if _, err := rand.Read(data[:]); err != nil {
		t.Fatal("cannot create an isolated ownership identifier")
	}
	return hex.EncodeToString(data[:])
}

func liveWriteUUID(t *testing.T) string {
	t.Helper()
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		t.Fatal("cannot create an isolated client credential")
	}
	data[6], data[8] = (data[6]&0x0f)|0x40, (data[8]&0x3f)|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", data[:4], data[4:6], data[6:8], data[8:10], data[10:])
}

func liveWriteJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal("cannot encode isolated test JSON")
	}
	return encoded
}

func liveWriteObject(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		t.Fatal("live API returned a missing or invalid JSON object")
	}
	return object
}

func liveWriteRequireAPIOnly(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var inbounds []struct{ Tag, Listen, Protocol string }
	if json.Unmarshal(liveWriteObject(t, raw)["inbounds"], &inbounds) != nil || len(inbounds) > 1 {
		t.Fatal("write preflight requires no non-API template inbounds")
	}
	for _, inbound := range inbounds {
		if inbound.Tag != "api" || (inbound.Listen != "127.0.0.1" && inbound.Listen != "::1") || (inbound.Protocol != "tunnel" && inbound.Protocol != "dokodemo-door") {
			t.Fatal("write preflight refuses a non-loopback or non-API template inbound")
		}
	}
}

func liveWriteEqual(a, b json.RawMessage) bool {
	decode := func(raw json.RawMessage) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		err := decoder.Decode(&value)
		return value, err
	}
	aa, aerr := decode(a)
	bb, berr := decode(b)
	return aerr == nil && berr == nil && reflect.DeepEqual(aa, bb)
}

func liveWriteRequireFields(t *testing.T, got, want json.RawMessage, scope string) {
	t.Helper()
	actual, expected := liveWriteObject(t, got), liveWriteObject(t, want)
	for key, value := range expected {
		if !liveWriteEqual(actual[key], value) {
			// Field names come only from this test's explicit corpus; never log
			// values, request bodies, credentials, or provider response bodies.
			t.Fatalf("%s did not preserve submitted field %q", scope, key)
		}
	}
}

func liveWriteRequireArrayMember(t *testing.T, raw, member json.RawMessage, scope string) {
	t.Helper()
	var rows []json.RawMessage
	if json.Unmarshal(raw, &rows) != nil {
		t.Fatalf("%s array is missing or invalid", scope)
	}
	for _, row := range rows {
		if liveWriteEqual(row, member) {
			return
		}
	}
	t.Fatalf("%s is missing or changed", scope)
}

func liveWriteAttachment(ctx context.Context, client *Client, email string, id int64, attach bool) error {
	// This test-only helper exposes exactly the official attachment operation,
	// not a production generic HTTP proxy or an alternate control plane.
	u, err := client.clientEndpoint("panel/api/clients", email)
	if err != nil {
		return err
	}
	suffix := "/detach"
	if attach {
		suffix = "/attach"
	}
	u.Path, u.RawPath = u.Path+suffix, u.EscapedPath()+suffix
	body, err := marshalSensitive(struct {
		InboundIDs []int64 `json:"inboundIds"`
	}{[]int64{id}})
	if err != nil {
		return err
	}
	return client.sensitiveEnvelope(ctx, http.MethodPost, u, body, "application/json", nil)
}

func liveWriteCheckClient(t *testing.T, ctx context.Context, client *Client, account map[string]any, expectedIDs []int64, ownedInbounds []Inbound) {
	t.Helper()
	email := account["email"].(string)
	rows, err := client.ListClients(ctx)
	if err != nil || len(rows) != 1 || rows[0].Email != email {
		t.Fatal("client readback did not return exactly the owned client")
	}
	actualIDs := append([]int64(nil), rows[0].InboundIDs...)
	expectedIDs = append([]int64(nil), expectedIDs...)
	slices.Sort(actualIDs)
	slices.Sort(expectedIDs)
	if !slices.Equal(actualIDs, expectedIDs) {
		t.Fatal("client attachment readback differs from the requested ownership")
	}
	fields := liveWriteObject(t, liveWriteJSON(t, account))
	fields["uuid"] = fields["id"] // API list uses a numeric database id.
	delete(fields, "id")
	// The management list has no documented Xray level column. Test that
	// field in the assembled client below, not in an unrelated list schema.
	delete(fields, "level")
	liveWriteRequireFields(t, rows[0].Raw, liveWriteJSON(t, fields), "global client")
	assembled, err := client.ReadAssembledConfig(ctx)
	if err != nil {
		t.Fatalf("read assembled client configuration: %v", err)
	}
	var inbounds []struct {
		Tag      string `json:"tag"`
		Settings struct {
			Clients []json.RawMessage `json:"clients"`
		} `json:"settings"`
	}
	if json.Unmarshal(liveWriteObject(t, assembled)["inbounds"], &inbounds) != nil {
		t.Fatal("assembled inbound array is invalid")
	}
	for _, owned := range ownedInbounds {
		foundInbound, matches := false, 0
		for _, inbound := range inbounds {
			if inbound.Tag != owned.Tag {
				continue
			}
			foundInbound = true
			for _, raw := range inbound.Settings.Clients {
				entry := liveWriteObject(t, raw)
				if !liveWriteEqual(entry["email"], liveWriteJSON(t, email)) {
					continue
				}
				matches++
				want := map[string]any{"id": account["id"], "email": email}
				if level, ok := account["level"]; ok {
					want["level"] = level
				}
				liveWriteRequireFields(t, raw, liveWriteJSON(t, want), "assembled Xray client")
			}
		}
		if !foundInbound {
			t.Fatal("owned enabled inbound is absent from the assembled configuration")
		}
		wantMatches := 0
		if account["enable"] == true && slices.Contains(expectedIDs, owned.ID) {
			wantMatches = 1
		}
		if matches != wantMatches {
			t.Fatal("assembled client presence differs from its enable state or attachment")
		}
	}
}

func liveWriteCheckInbound(t *testing.T, ctx context.Context, client *Client, id int64, expected Inbound) {
	t.Helper()
	rows, err := client.ListInbounds(ctx)
	if err != nil {
		t.Fatalf("read persisted isolated inbound: %v", err)
	}
	for _, row := range rows {
		if row.ID == id {
			liveWriteRequireFields(t, row.Raw, liveWriteJSON(t, expected), "persisted inbound")
			return
		}
	}
	t.Fatal("the successful inbound write is absent from persisted readback")
}

func liveWriteInsertFixtureRule(snapshotRules []json.RawMessage, fixtureRule json.RawMessage) ([]json.RawMessage, error) {
	apiRule := json.RawMessage(`{"type":"field","inboundTag":["api"],"outboundTag":"api"}`)
	if len(snapshotRules) == 0 || !liveWriteEqual(snapshotRules[0], apiRule) {
		return nil, fmt.Errorf("isolated template must already contain the exact API rule at routing index zero")
	}
	result := make([]json.RawMessage, 0, len(snapshotRules)+1)
	result = append(result, snapshotRules[0], fixtureRule)
	return append(result, snapshotRules[1:]...), nil
}

func TestLiveWriteFixtureRulePreservesExistingRoutingOrder(t *testing.T) {
	api := json.RawMessage(`{ "inboundTag": ["api"], "outboundTag": "api", "type": "field" }`)
	businessA := json.RawMessage(`{"type":"field","ip":["geoip:private"],"outboundTag":"blocked"}`)
	businessB := json.RawMessage(`{"type":"field","protocol":["bittorrent"],"outboundTag":"blocked"}`)
	fixture := json.RawMessage(`{"type":"field","inboundTag":["owned-fixture"],"outboundTag":"owned-loopback"}`)
	snapshot := []json.RawMessage{api, businessA, businessB}
	got, err := liveWriteInsertFixtureRule(snapshot, fixture)
	if err != nil {
		t.Fatal(err)
	}
	want := []json.RawMessage{api, fixture, businessA, businessB}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(snapshot, []json.RawMessage{api, businessA, businessB}) {
		t.Fatal("fixture insertion changed original rule bytes or relative order")
	}
	for _, invalid := range [][]json.RawMessage{
		nil,
		{businessA, api},
		{json.RawMessage(`{"type":"field","inboundTag":["api","other"],"outboundTag":"api"}`)},
		{json.RawMessage(`{"type":"field","inboundTag":["api"],"outboundTag":"api","enabled":false}`)},
	} {
		if _, err := liveWriteInsertFixtureRule(invalid, fixture); err == nil {
			t.Fatal("fixture insertion accepted a missing, displaced, or modified API rule")
		}
	}
}

func liveWriteReportTemplateDifferences(t *testing.T, expected, actual XrayTemplate) {
	t.Helper()
	for _, path := range liveWriteTemplateDifferencePaths(expected, actual) {
		t.Logf("template_difference_path=%s", path)
	}
}

// These diagnostics supplement, and never replace, the strict canonical
// comparison. Only fixed schema labels and array indices may reach the log;
// values and arbitrary provider-controlled map keys are never printed.
func liveWriteTemplateDifferencePaths(expected, actual XrayTemplate) []string {
	const maxPaths = 20
	paths := make([]string, 0, maxPaths)
	if expected.OutboundTestURL != actual.OutboundTestURL {
		paths = append(paths, "OutboundTestURL (changed)")
	}
	decode := func(raw json.RawMessage) (any, bool) {
		if !json.Valid(raw) {
			return nil, false
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		err := decoder.Decode(&value)
		return value, err == nil
	}
	want, wantOK := decode(expected.Config)
	got, gotOK := decode(actual.Config)
	if !wantOK || !gotOK {
		return append(paths, "Config (invalid JSON)")
	}
	var walk func(any, any, string, int)
	walk = func(want, got any, path string, depth int) {
		if len(paths) >= maxPaths || reflect.DeepEqual(want, got) {
			return
		}
		if depth >= 64 {
			paths = append(paths, path+" (changed; depth limit)")
			return
		}
		wantMap, wantMapOK := want.(map[string]any)
		gotMap, gotMapOK := got.(map[string]any)
		if wantMapOK && gotMapOK {
			keys := make([]string, 0, len(wantMap)+len(gotMap))
			for key := range wantMap {
				keys = append(keys, key)
			}
			for key := range gotMap {
				if _, exists := wantMap[key]; !exists {
					keys = append(keys, key)
				}
			}
			slices.Sort(keys)
			for _, key := range keys {
				if len(paths) >= maxPaths {
					return
				}
				childPath := path + "." + liveWriteSafeDifferenceKey(key)
				wantValue, existsWanted := wantMap[key]
				gotValue, existsActual := gotMap[key]
				switch {
				case !existsWanted:
					paths = append(paths, childPath+" (added)")
				case !existsActual:
					paths = append(paths, childPath+" (removed)")
				default:
					walk(wantValue, gotValue, childPath, depth+1)
				}
			}
			return
		}
		wantArray, wantArrayOK := want.([]any)
		gotArray, gotArrayOK := got.([]any)
		if wantArrayOK && gotArrayOK {
			for i := 0; i < max(len(wantArray), len(gotArray)) && len(paths) < maxPaths; i++ {
				childPath := path + "[" + strconv.Itoa(i) + "]"
				switch {
				case i >= len(wantArray):
					paths = append(paths, childPath+" (added)")
				case i >= len(gotArray):
					paths = append(paths, childPath+" (removed)")
				default:
					walk(wantArray[i], gotArray[i], childPath, depth+1)
				}
			}
			return
		}
		paths = append(paths, path+" (changed)")
	}
	walk(want, got, "Config", 0)
	return paths
}

func liveWriteSafeDifferenceKey(key string) string {
	if len(key) == 0 || len(key) > 32 {
		return "<redacted-key>"
	}
	for _, r := range key {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return "<redacted-key>"
		}
	}
	// Alphanumeric alone is insufficient: an email, UUID, token, or short
	// secret can itself be a map key. Only these static schema names pass.
	switch key {
	case "api", "services", "tag", "inbounds", "outbounds", "listen", "port", "protocol", "settings", "streamSettings",
		"sniffing", "clients", "id", "email", "password", "level", "enable", "flow", "decryption", "network", "security",
		"tcpSettings", "rawSettings", "xhttpSettings", "tlsSettings", "realitySettings", "header", "type", "extra",
		"privateKey", "publicKey", "shortIds", "serverNames", "dest", "target", "routing", "rules", "inboundTag",
		"outboundTag", "balancerTag", "balancers", "domain", "ip", "source", "sourcePort", "user", "domainStrategy",
		"domainMatcher", "attrs", "ruleTag", "enabled", "finalRules", "action", "dns", "servers", "hosts", "address",
		"queryStrategy", "queryType", "nonIPQuery", "disableCache", "disableFallback", "expectIPs", "skipFallback",
		"policy", "levels", "system", "statsUserUplink", "statsUserDownlink", "statsUserOnline", "statsInboundUplink",
		"statsInboundDownlink", "statsOutboundUplink", "statsOutboundDownlink", "handshake", "connIdle", "uplinkOnly",
		"downlinkOnly", "bufferSize", "stats", "log", "access", "error", "loglevel", "dnsLog", "maskAddress", "metrics",
		"reverse", "bridges", "portals", "observatory", "burstObservatory", "fakedns", "transport", "rewriteAddress",
		"destOverride", "metadataOnly", "routeOnly", "0":
		return key
	default:
		return "<redacted-key>"
	}
}

func TestLiveWriteTemplateDifferencePaths(t *testing.T) {
	expected := XrayTemplate{OutboundTestURL: "https://old.invalid/private", Config: json.RawMessage(`{"inbounds":[{"listen":"127.0.0.1","port":9007199254740993,"settings":{"clients":[{"id":"old-secret"}]}}],"outbounds":[]}`)}
	actual := XrayTemplate{OutboundTestURL: "https://new.invalid/private", Config: json.RawMessage(`{"inbounds":[{"port":9007199254740992,"protocol":"vless","settings":{"clients":[{"id":"new-secret"}]}}],"outbounds":[{"tag":"new-private-tag"}]}`)}
	want := []string{"OutboundTestURL (changed)", "Config.inbounds[0].listen (removed)", "Config.inbounds[0].port (changed)",
		"Config.inbounds[0].protocol (added)", "Config.inbounds[0].settings.clients[0].id (changed)", "Config.outbounds[0] (added)"}
	got := liveWriteTemplateDifferencePaths(expected, actual)
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected safe difference paths: %v", got)
	}
	if len(liveWriteTemplateDifferencePaths(expected, expected)) != 0 {
		t.Fatal("equal templates produced a difference")
	}
}

func TestLiveWriteTemplateDifferencePathsNeverExposeValuesOrUnknownKeys(t *testing.T) {
	expected := XrayTemplate{Config: json.RawMessage(`{"privateKey":"VALUESECRETOLD","BearerSecretShort":"VALUESECRET","invalid\nkey":"VALUESECRET","extra":{"id":"CREDENTIALOLD"}}`)}
	actual := XrayTemplate{Config: json.RawMessage(`{"privateKey":"VALUESECRETNEW","BearerSecretShort":"VALUESECRETCHANGED","anotherArbitraryKey":"VALUESECRET","extra":{"id":"CREDENTIALNEW"}}`)}
	output := strings.Join(liveWriteTemplateDifferencePaths(expected, actual), "\n")
	for _, secret := range []string{"VALUESECRET", "CREDENTIAL", "BearerSecretShort", "invalid", "anotherArbitraryKey"} {
		if strings.Contains(output, secret) {
			t.Fatal("difference diagnostics leaked a value or arbitrary map key")
		}
	}
	if !strings.Contains(output, "Config.privateKey (changed)") || !strings.Contains(output, "<redacted-key>") {
		t.Fatal("safe schema path or redacted-key marker is missing")
	}
	for _, key := range []string{"", "schema.with.dot", "schema/key", "schema\nkey", strings.Repeat("a", 33), "secret"} {
		if liveWriteSafeDifferenceKey(key) != "<redacted-key>" {
			t.Fatal("unsafe or unknown map key was allowed into diagnostics")
		}
	}
}

func TestLiveWriteTemplateDifferencePathsAreBounded(t *testing.T) {
	actual := XrayTemplate{Config: liveWriteJSON(t, map[string]any{"inbounds": make([]int, 100)})}
	paths := liveWriteTemplateDifferencePaths(XrayTemplate{Config: json.RawMessage(`{"inbounds":[]}`)}, actual)
	if len(paths) != 20 || paths[19] != "Config.inbounds[19] (added)" {
		t.Fatal("difference diagnostics exceeded or did not honour the twenty-path bound")
	}
	paths = liveWriteTemplateDifferencePaths(XrayTemplate{Config: json.RawMessage(`{broken-secret`)}, actual)
	if !slices.Equal(paths, []string{"Config (invalid JSON)"}) {
		t.Fatal("invalid JSON leaked parser diagnostics")
	}
}
