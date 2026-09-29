package threexui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// TestLive3XUIRawProjectionContract is a destructive experiment against a
// fresh, disposable official 3x-ui instance. It tests one projection hypothesis
// only; passing it does not enable or select a production provider:
//
//   - the complete, authoritative inbound remains in the raw Xray template;
//   - one disabled managed inbound with the same tag indexes traffic;
//   - its metadata client is permanently disabled and has no quota lifecycle;
//   - only raw-template edits grant or revoke data-plane access.
//
// Run it with the same explicit isolation boundary as the live write contract:
//
//	CHIRAL_3XUI_TEST_WRITE=isolated-empty-instance
//	CHIRAL_3XUI_TEST_URL=<isolated instance URL>
//	CHIRAL_3XUI_TEST_TOKEN_FILE=<private mode-0600 token file>
//	CHIRAL_3XUI_TEST_XRAY_BIN=<absolute fixture client binary path>
//
// The first accounting subtest intentionally records the upstream cold-start
// behaviour as a real pass/fail result. Its failure does not prevent the
// independent steady-state, configuration-fidelity, and authorization checks.
func TestLive3XUIRawProjectionContract(t *testing.T) {
	optIn := os.Getenv("CHIRAL_3XUI_TEST_WRITE")
	if optIn == "" {
		t.Skip("set CHIRAL_3XUI_TEST_WRITE=isolated-empty-instance to enable the raw projection experiment")
	}
	if optIn != "isolated-empty-instance" {
		t.Fatal("CHIRAL_3XUI_TEST_WRITE must equal isolated-empty-instance")
	}
	baseURL, tokenFile := os.Getenv("CHIRAL_3XUI_TEST_URL"), os.Getenv("CHIRAL_3XUI_TEST_TOKEN_FILE")
	if baseURL == "" || tokenFile == "" {
		t.Fatal("raw projection opt-in also requires CHIRAL_3XUI_TEST_URL and CHIRAL_3XUI_TEST_TOKEN_FILE")
	}
	_ = liveProjectionXrayBinary(t)

	client, err := New(Config{BaseURL: baseURL, Token: readLive3XUIToken(t, tokenFile), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal("cannot construct the isolated raw projection client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Read-only emptiness checks precede even ReadXrayTemplate, whose upstream
	// implementation may heal legacy storage. Never mutate a reused instance.
	inbounds, err := client.ListInbounds(ctx)
	if err != nil || len(inbounds) != 0 {
		t.Fatal("raw projection preflight requires a successfully read empty inbound list")
	}
	clients, err := client.ListClients(ctx)
	if err != nil || len(clients) != 0 {
		t.Fatal("raw projection preflight requires a successfully read empty client list")
	}
	baseline, err := client.ReadAssembledConfig(ctx)
	if err != nil {
		t.Fatalf("read initial assembled configuration: %v", err)
	}
	liveWriteRequireAPIOnly(t, baseline)
	templateSnapshot, err := client.ReadXrayTemplate(ctx)
	if err != nil {
		t.Fatalf("read isolated template snapshot: %v", err)
	}
	liveWriteRequireAPIOnly(t, templateSnapshot.Config)
	if templateSnapshot.OutboundTestURL == "" {
		t.Fatal("cannot safely restore a template with an empty outbound test URL")
	}

	prefix := "chiral-projection-" + liveWriteRandomID(t)
	tag, outboundTag := prefix+"-in", prefix+"-loopback"
	userA := liveProjectionUser{Email: prefix + "-control@invalid", Credential: liveWriteUUID(t)}
	userB := liveProjectionUser{Email: prefix + "-hot-add@invalid", Credential: liveWriteUUID(t)}
	port := liveProjectionFreePort(t)
	ownedTags, ownedEmails := map[string]bool{tag: true}, map[string]bool{userA.Email: true}
	templateAttempted := false
	metadataAttempted := false

	// Recovery is registered before the first write. The raw template is always
	// restored and verified before deleting same-tag metadata; otherwise
	// upstream's managed-inbound deletion could rewrite live routing references.
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cleanupCancel()
		cleanupOK := true
		cleanupErrorf := func(format string, args ...any) {
			cleanupOK = false
			t.Errorf(format, args...)
		}

		templateRestored := !templateAttempted
		if templateAttempted {
			if err := client.UpdateXrayTemplate(cleanupCtx, templateSnapshot); err != nil {
				cleanupErrorf("cleanup could not restore the raw template snapshot: %v", err)
			} else if restored, err := client.ReadAssembledConfig(cleanupCtx); err != nil {
				cleanupErrorf("cleanup could not read the assembled configuration before metadata deletion: %v", err)
			} else {
				templateRestored = liveProjectionComparisonOK(t, baseline, restored, "cleanup pre-delete assembled configuration")
				cleanupOK = cleanupOK && templateRestored
			}
		}

		// Deleting the disabled row is only safe after the authoritative raw tag
		// and its routing rule are absent again. On ambiguous restoration, leave
		// owned metadata behind for inspection instead of risking another write.
		if metadataAttempted && templateRestored {
			clientRows, listErr := client.ListClients(cleanupCtx)
			if listErr != nil {
				cleanupErrorf("cleanup could not enumerate projection clients: %v", listErr)
			} else {
				for _, row := range clientRows {
					if !ownedEmails[row.Email] {
						cleanupErrorf("unexpected client appeared in the isolated instance; it was not deleted")
						continue
					}
					if err := client.DeleteClient(cleanupCtx, row.Email, false); err != nil {
						cleanupErrorf("cleanup could not delete the disabled owned metadata client: %v", err)
					}
				}
			}
			inboundRows, listErr := client.ListInbounds(cleanupCtx)
			if listErr != nil {
				cleanupErrorf("cleanup could not enumerate projection inbounds: %v", listErr)
			} else {
				for _, row := range inboundRows {
					if !ownedTags[row.Tag] {
						cleanupErrorf("unexpected inbound appeared in the isolated instance; it was not deleted")
						continue
					}
					if err := client.DeleteInbound(cleanupCtx, row.ID); err != nil {
						cleanupErrorf("cleanup could not delete the disabled owned metadata inbound: %v", err)
					}
				}
			}
		}

		remainingClients, clientsErr := client.ListClients(cleanupCtx)
		if clientsErr != nil || (templateRestored && len(remainingClients) != 0) {
			cleanupErrorf("cleanup did not verify an empty client list")
		}
		remainingInbounds, inboundsErr := client.ListInbounds(cleanupCtx)
		if inboundsErr != nil || (templateRestored && len(remainingInbounds) != 0) {
			cleanupErrorf("cleanup did not verify an empty inbound list")
		}
		// Managed-inbound deletion has its own routing-reference rewrite even
		// when the row is disabled. Restore the authoritative snapshot a second
		// time after deletion, then use full comparisons as the final word.
		if templateAttempted && templateRestored {
			if err := client.UpdateXrayTemplate(cleanupCtx, templateSnapshot); err != nil {
				cleanupErrorf("cleanup could not perform the final template restoration: %v", err)
			}
		}
		if restored, err := client.ReadXrayTemplate(cleanupCtx); err != nil {
			cleanupErrorf("cleanup could not read the restored template: %v", err)
		} else {
			if restored.OutboundTestURL != templateSnapshot.OutboundTestURL {
				cleanupErrorf("cleanup restored template changed the outbound test URL")
			}
			if !liveProjectionComparisonOK(t, templateSnapshot.Config, restored.Config, "cleanup stored template") {
				cleanupOK = false
			}
		}
		if assembled, err := client.ReadAssembledConfig(cleanupCtx); err != nil {
			cleanupErrorf("cleanup could not read the final assembled configuration: %v", err)
		} else if !liveProjectionComparisonOK(t, baseline, assembled, "cleanup final assembled configuration") {
			cleanupOK = false
		}
		if cleanupOK {
			t.Log("cleanup_verified=true raw_template_restored=true metadata_removed=true assembled_config_unchanged=true")
		}
	}()

	metadataA := map[string]any{
		"id": userA.Credential, "email": userA.Email, "enable": false, "security": "auto",
		"limitIp": 0, "totalGB": 0, "expiryTime": 0, "tgId": 0,
		"subId": liveWriteRandomID(t), "comment": "Disabled Chiral projection metadata",
		"reset": 0, "resetDay": 0, "resetMax": 0,
	}
	metadata := Inbound{
		Tag: tag, Remark: "Disabled Chiral projection metadata", Enable: false,
		Listen: "127.0.0.1", Port: port, Protocol: "vless",
		Settings:       liveWriteJSON(t, map[string]any{"clients": []any{metadataA}, "decryption": "none"}),
		StreamSettings: liveWriteJSON(t, map[string]any{"network": "tcp", "security": "none", "tcpSettings": map[string]any{"header": map[string]any{"type": "none"}}}),
		Sniffing:       liveWriteJSON(t, map[string]any{"enabled": false, "destOverride": []string{"http", "tls"}, "metadataOnly": false, "routeOnly": false}),
		TrafficReset:   "never", TrafficResetDay: 0,
	}
	metadataAttempted = true
	managed, err := client.AddInbound(ctx, metadata)
	if err != nil {
		t.Fatalf("create disabled same-tag metadata inbound: %v", err)
	}
	if managed.Enable || managed.Tag != tag || managed.Port != port {
		t.Fatal("managed projection readback changed its disabled identity")
	}
	liveProjectionRequireMetadataState(t, ctx, client, managed.ID, map[string]map[string]any{userA.Email: metadataA})
	indexedTraffic, err := client.ClientTraffics(ctx)
	if err != nil || len(indexedTraffic) != 1 || indexedTraffic[0].Email != userA.Email || indexedTraffic[0].InboundID != managed.ID {
		t.Fatal("disabled metadata client did not create one official traffic index")
	}
	assembled, err := client.ReadAssembledConfig(ctx)
	if err != nil {
		t.Fatalf("read configuration after disabled metadata creation: %v", err)
	}
	liveProjectionRequireEquivalent(t, baseline, assembled, "disabled metadata exclusion")

	desiredA := liveProjectionConfig(t, baseline, tag, outboundTag, port, []liveProjectionUser{userA})
	desiredAB := liveProjectionConfig(t, baseline, tag, outboundTag, port, []liveProjectionUser{userA, userB})
	templateAttempted = true
	if err := client.UpdateXrayTemplate(ctx, XrayTemplate{Config: desiredA, OutboundTestURL: templateSnapshot.OutboundTestURL}); err != nil {
		t.Fatalf("write authoritative raw-template inbound: %v", err)
	}
	liveProjectionRequireStoredAndAssembled(t, ctx, client, desiredA, templateSnapshot.OutboundTestURL, "initial raw projection")
	liveProjectionRequireRawClients(t, desiredA, tag, []liveProjectionUser{userA})
	liveProjectionRequireMetadataState(t, ctx, client, managed.ID, map[string]map[string]any{userA.Email: metadataA})

	// This is only fixture input for a disposable client process. It is
	// intentionally enabled because the real raw-template account is enabled;
	// the provider metadata above remains disabled throughout the experiment.
	fixtureA := map[string]any{"id": userA.Credential, "email": userA.Email, "enable": true, "level": 3}
	t.Run("cold_start_dataplane_and_official_http_billing", func(t *testing.T) {
		liveWriteTrafficContract(t, ctx, client, fixtureA, managed)
	})
	t.Run("steady_state_dataplane_and_official_http_billing", func(t *testing.T) {
		liveWriteTrafficContract(t, ctx, client, fixtureA, managed)
	})

	metadataB := map[string]any{
		"id": userB.Credential, "email": userB.Email, "enable": false, "security": "auto",
		"limitIp": 0, "totalGB": 0, "expiryTime": 0, "tgId": 0,
		"subId": liveWriteRandomID(t), "comment": "Disabled hot-add projection metadata",
		"reset": 0, "resetDay": 0, "resetMax": 0,
	}
	ownedEmails[userB.Email] = true
	if err := client.AddClient(ctx, liveWriteJSON(t, metadataB), []int64{managed.ID}); err != nil {
		t.Fatalf("create disabled metadata for fresh hot-add user: %v", err)
	}
	expectedMetadata := map[string]map[string]any{userA.Email: metadataA, userB.Email: metadataB}
	liveProjectionRequireMetadataState(t, ctx, client, managed.ID, expectedMetadata)
	indexedTraffic, err = client.ClientTraffics(ctx)
	if err != nil {
		t.Fatalf("read fresh disabled metadata counter: %v", err)
	}
	initialB := liveProjectionOwnedCounter(t, indexedTraffic, userB.Email, managed.ID)
	if initialB.Up != 0 || initialB.Down != 0 {
		t.Fatal("fresh disabled metadata counter did not start at zero")
	}
	assembled, err = client.ReadAssembledConfig(ctx)
	if err != nil {
		t.Fatalf("read configuration after fresh disabled metadata creation: %v", err)
	}
	liveProjectionRequireEquivalent(t, desiredA, assembled, "fresh disabled metadata exclusion")
	liveProjectionRequireMetadataState(t, ctx, client, managed.ID, expectedMetadata)

	if err := client.UpdateXrayTemplate(ctx, XrayTemplate{Config: desiredAB, OutboundTestURL: templateSnapshot.OutboundTestURL}); err != nil {
		t.Fatalf("hot-add fresh authoritative raw-template client: %v", err)
	}
	liveProjectionRequireStoredAndAssembled(t, ctx, client, desiredAB, templateSnapshot.OutboundTestURL, "fresh raw client hot-add")
	liveProjectionRequireRawClients(t, desiredAB, tag, []liveProjectionUser{userA, userB})
	liveProjectionRequireMetadataState(t, ctx, client, managed.ID, expectedMetadata)
	t.Run("fresh_level3_hot_add_runtime_billing", func(t *testing.T) {
		liveProjectionBillingProbe(t, ctx, client, userB, managed.ID, port)
	})

	if !t.Run("raw_template_removal_revokes_only_target_user", func(t *testing.T) {
		if err := client.UpdateXrayTemplate(ctx, XrayTemplate{Config: desiredA, OutboundTestURL: templateSnapshot.OutboundTestURL}); err != nil {
			t.Fatalf("remove target authoritative raw-template client: %v", err)
		}
		liveProjectionRequireStoredAndAssembled(t, ctx, client, desiredA, templateSnapshot.OutboundTestURL, "target raw client removal")
		liveProjectionRequireRawClients(t, desiredA, tag, []liveProjectionUser{userA})
		liveProjectionRequireMetadataState(t, ctx, client, managed.ID, expectedMetadata)
		if ctx.Err() != nil {
			t.Fatal("raw revocation cannot be evaluated after the parent context ended")
		}
		_ = liveProjectionProbe(t, ctx, userA.Credential, port, true)
		_ = liveProjectionProbe(t, ctx, userB.Credential, port, false)
		_ = liveProjectionProbe(t, ctx, userA.Credential, port, true)
	}) {
		return
	}

	if !t.Run("raw_template_restore_reauthorizes_target_user", func(t *testing.T) {
		if err := client.UpdateXrayTemplate(ctx, XrayTemplate{Config: desiredAB, OutboundTestURL: templateSnapshot.OutboundTestURL}); err != nil {
			t.Fatalf("restore target authoritative raw-template client: %v", err)
		}
		liveProjectionRequireStoredAndAssembled(t, ctx, client, desiredAB, templateSnapshot.OutboundTestURL, "target raw client restoration")
		liveProjectionRequireRawClients(t, desiredAB, tag, []liveProjectionUser{userA, userB})
		liveProjectionRequireMetadataState(t, ctx, client, managed.ID, expectedMetadata)
		_ = liveProjectionProbe(t, ctx, userB.Credential, port, true)
	}) {
		return
	}

	// Level 0 deliberately has user byte counters disabled while level 3 has
	// them enabled. A post-restore billing pass therefore catches a hot AddUser
	// path that authenticates successfully but silently drops the raw level=3.
	t.Run("restored_level3_runtime_billing", func(t *testing.T) {
		liveProjectionBillingProbe(t, ctx, client, userB, managed.ID, port)
	})
	liveProjectionRequireMetadataState(t, ctx, client, managed.ID, expectedMetadata)
}

type liveProjectionUser struct {
	Email      string
	Credential string
}

func liveProjectionConfig(t *testing.T, baseline json.RawMessage, tag, outboundTag string, port int, users []liveProjectionUser) json.RawMessage {
	t.Helper()
	config := liveWriteObject(t, baseline)
	var inbounds, outbounds []json.RawMessage
	if json.Unmarshal(config["inbounds"], &inbounds) != nil || json.Unmarshal(config["outbounds"], &outbounds) != nil {
		t.Fatal("initial assembled configuration lacks inbound or outbound arrays")
	}
	clients := make([]any, 0, len(users))
	for _, user := range users {
		clients = append(clients, map[string]any{"id": user.Credential, "email": user.Email, "level": 3, "flow": ""})
	}
	rawInbound := liveWriteJSON(t, map[string]any{
		"tag": tag, "listen": "127.0.0.1", "port": port, "protocol": "vless",
		"settings":       map[string]any{"clients": clients, "decryption": "none"},
		"streamSettings": map[string]any{"network": "tcp", "security": "none", "tcpSettings": map[string]any{"header": map[string]any{"type": "none"}}},
		"sniffing":       map[string]any{"enabled": false, "destOverride": []string{"http", "tls"}, "metadataOnly": false, "routeOnly": false},
	})
	config["inbounds"] = liveWriteJSON(t, append(inbounds, rawInbound))
	loopbackOutbound := liveWriteJSON(t, map[string]any{
		"tag": outboundTag, "protocol": "freedom",
		"settings": map[string]any{"finalRules": []any{
			map[string]any{"action": "allow", "ip": []string{"127.0.0.1/32", "::1/128"}},
			map[string]any{"action": "block"},
		}},
	})
	config["outbounds"] = liveWriteJSON(t, append(outbounds, loopbackOutbound))

	routing := liveWriteObject(t, config["routing"])
	var rules []json.RawMessage
	if json.Unmarshal(routing["rules"], &rules) != nil {
		t.Fatal("initial assembled configuration lacks routing rules")
	}
	fixtureRule := liveWriteJSON(t, map[string]any{
		"type": "field", "inboundTag": []string{tag},
		"ip": []string{"127.0.0.1/32", "::1/128"}, "outboundTag": outboundTag,
	})
	var err error
	rules, err = liveWriteInsertFixtureRule(rules, fixtureRule)
	if err != nil {
		t.Fatal(err)
	}
	routing["rules"] = liveWriteJSON(t, rules)
	config["routing"] = liveWriteJSON(t, routing)

	policy := liveWriteObject(t, config["policy"])
	levels := liveWriteObject(t, policy["levels"])
	level0 := map[string]json.RawMessage{}
	if raw, ok := levels["0"]; ok {
		level0 = liveWriteObject(t, raw)
	}
	level0["statsUserUplink"] = json.RawMessage("false")
	level0["statsUserDownlink"] = json.RawMessage("false")
	level0["statsUserOnline"] = json.RawMessage("true")
	levels["0"] = liveWriteJSON(t, level0)
	levels["3"] = liveWriteJSON(t, map[string]any{
		"statsUserUplink": true, "statsUserDownlink": true, "statsUserOnline": true,
	})
	policy["levels"] = liveWriteJSON(t, levels)
	config["policy"] = liveWriteJSON(t, policy)
	return liveWriteJSON(t, config)
}

func liveProjectionRequireMetadataState(t *testing.T, ctx context.Context, client *Client, inboundID int64, expected map[string]map[string]any) {
	t.Helper()
	inbounds, err := client.ListInbounds(ctx)
	if err != nil || len(inbounds) != 1 || inbounds[0].ID != inboundID || inbounds[0].Enable {
		t.Fatal("managed projection did not remain one disabled inbound")
	}
	var wire struct {
		Clients []json.RawMessage `json:"clients"`
	}
	if json.Unmarshal(inbounds[0].Settings, &wire) != nil || len(wire.Clients) != len(expected) {
		t.Fatal("managed projection metadata client set changed unexpectedly")
	}
	seen := make(map[string]bool, len(wire.Clients))
	for _, raw := range wire.Clients {
		var identity struct {
			Email string `json:"email"`
		}
		if json.Unmarshal(raw, &identity) != nil || seen[identity.Email] || expected[identity.Email] == nil {
			t.Fatal("managed projection contains an unknown or duplicate metadata client")
		}
		seen[identity.Email] = true
		liveWriteRequireFields(t, raw, liveWriteJSON(t, expected[identity.Email]), "disabled metadata client")
	}
	listed, err := client.ListClients(ctx)
	if err != nil || len(listed) != len(expected) {
		t.Fatal("global projection client index changed unexpectedly")
	}
	for _, row := range listed {
		want := expected[row.Email]
		if want == nil || len(row.InboundIDs) != 1 || row.InboundIDs[0] != inboundID {
			t.Fatal("global projection client is not attached to exactly the owned metadata inbound")
		}
		neutral := map[string]any{
			"email": row.Email, "enable": false, "limitIp": 0,
			"totalGB": 0, "expiryTime": 0, "reset": 0, "resetDay": 0, "resetMax": 0,
		}
		liveWriteRequireFields(t, row.Raw, liveWriteJSON(t, neutral), "global disabled metadata client")
	}
}

func liveProjectionRequireRawClients(t *testing.T, config json.RawMessage, tag string, users []liveProjectionUser) {
	t.Helper()
	var inbounds []struct {
		Tag      string `json:"tag"`
		Settings struct {
			Clients []json.RawMessage `json:"clients"`
		} `json:"settings"`
	}
	if json.Unmarshal(liveWriteObject(t, config)["inbounds"], &inbounds) != nil {
		t.Fatal("strict raw projection config has no valid inbound array")
	}
	matches := 0
	for _, inbound := range inbounds {
		if inbound.Tag != tag {
			continue
		}
		matches++
		if len(inbound.Settings.Clients) != len(users) {
			t.Fatal("authoritative raw inbound client count changed")
		}
		for i, user := range users {
			liveWriteRequireFields(t, inbound.Settings.Clients[i], liveWriteJSON(t, map[string]any{
				"id": user.Credential, "email": user.Email, "level": 3, "flow": "",
			}), "authoritative raw client")
		}
	}
	if matches != 1 {
		t.Fatal("strict raw projection config does not contain exactly one authoritative inbound")
	}
}

func liveProjectionRequireStoredAndAssembled(t *testing.T, ctx context.Context, client *Client, expected json.RawMessage, outboundTestURL, scope string) {
	t.Helper()
	stored, err := client.ReadXrayTemplate(ctx)
	if err != nil {
		t.Fatalf("%s: read stored template: %v", scope, err)
	}
	if stored.OutboundTestURL != outboundTestURL {
		t.Fatalf("%s: outbound test URL changed", scope)
	}
	liveProjectionRequireEquivalent(t, expected, stored.Config, scope+" stored template")
	assembled, err := client.ReadAssembledConfig(ctx)
	if err != nil {
		t.Fatalf("%s: read assembled configuration: %v", scope, err)
	}
	liveProjectionRequireEquivalent(t, expected, assembled, scope+" assembled configuration")
}

func liveProjectionRequireEquivalent(t *testing.T, expected, actual []byte, scope string) {
	t.Helper()
	if !liveProjectionComparisonOK(t, expected, actual, scope) {
		t.FailNow()
	}
}

func liveProjectionComparisonOK(t *testing.T, expected, actual []byte, scope string) bool {
	t.Helper()
	comparison, err := CompareConfig(expected, actual)
	if err != nil {
		t.Errorf("%s: strict configuration comparison failed: %v", scope, err)
		return false
	}
	if comparison.Equivalent {
		return true
	}
	t.Logf("%s: expected_digest=%s actual_digest=%s truncated=%t", scope, comparison.ExpectedDigest, comparison.ActualDigest, comparison.Truncated)
	for _, difference := range comparison.Differences {
		t.Logf("%s: section=%s kind=%s path_digest=%s", scope, difference.Section, difference.Kind, difference.PathDigest)
	}
	t.Errorf("%s: complete configuration differs", scope)
	return false
}

func liveProjectionFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("cannot reserve an isolated raw projection port")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal("cannot release the isolated raw projection port")
	}
	return port
}

func liveProjectionXrayBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("CHIRAL_3XUI_TEST_XRAY_BIN")
	info, err := os.Stat(bin)
	if bin == "" || !filepath.IsAbs(bin) || err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatal("CHIRAL_3XUI_TEST_XRAY_BIN must be an executable regular file at an absolute path")
	}
	return bin
}

func liveProjectionOwnedCounter(t *testing.T, rows []ClientTraffic, email string, inboundID int64) ClientTraffic {
	t.Helper()
	var found ClientTraffic
	matches := 0
	for _, row := range rows {
		if row.Email != email {
			continue
		}
		if row.InboundID != inboundID {
			t.Fatal("owned projection counter points at an unexpected inbound")
		}
		found = row
		matches++
	}
	if matches != 1 {
		t.Fatal("official traffic snapshot does not contain exactly one owned projection counter")
	}
	return found
}

func liveProjectionBillingProbe(t *testing.T, ctx context.Context, provider *Client, user liveProjectionUser, inboundID int64, inboundPort int) {
	t.Helper()
	rows, err := provider.ClientTraffics(ctx)
	if err != nil {
		t.Fatal("cannot read the fresh hot-add traffic baseline")
	}
	baseline := liveProjectionOwnedCounter(t, rows, user.Email, inboundID)
	downloadBytes := liveProjectionProbe(t, ctx, user.Credential, inboundPort, true)
	pollCtx, pollCancel := context.WithTimeout(ctx, 40*time.Second)
	defer pollCancel()
	last := baseline
	for {
		rows, err := provider.ClientTraffics(pollCtx)
		if err == nil {
			current := liveProjectionOwnedCounter(t, rows, user.Email, inboundID)
			last = current
			if current.ID != baseline.ID || current.Up < baseline.Up || current.Down < baseline.Down {
				t.Fatal("fresh hot-add counter identity changed or decreased")
			}
			up, down := current.Up-baseline.Up, current.Down-baseline.Down
			if up > 0 && down >= uint64(downloadBytes) {
				t.Logf("hot_add_billing_verified=true user_up_delta=%d user_down_delta=%d", up, down)
				return
			}
		}
		select {
		case <-pollCtx.Done():
			if ctx.Err() != nil {
				t.Fatal("fresh hot-add billing cannot be evaluated after the parent context ended")
			}
			t.Logf("hot_add_billing_baseline_up=%d hot_add_billing_baseline_down=%d hot_add_billing_last_up=%d hot_add_billing_last_down=%d",
				baseline.Up, baseline.Down, last.Up, last.Down)
			t.Fatal("official HTTP counters did not attribute the fresh level-3 hot-add transfer")
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// liveProjectionProbe starts a new disposable Xray client process every time.
// In the negative case the process must remain healthy while every fresh SOCKS
// request fails; a fixture-process crash is not accepted as proof of revocation.
func liveProjectionProbe(t *testing.T, ctx context.Context, credential string, inboundPort int, wantSuccess bool) int {
	t.Helper()
	download := bytes.Repeat([]byte("chiral-level-three|"), 8192)
	var originRequests atomic.Int64
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originRequests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/ready" {
			http.Error(w, "unexpected fixture request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(download)))
		_, _ = w.Write(download)
	}))
	_ = origin.Listener.Close()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("cannot allocate the projection probe origin")
	}
	origin.Listener = listener
	origin.Start()
	defer origin.Close()

	socksPort := liveProjectionFreePort(t)
	fixtureConfig := liveWriteJSON(t, map[string]any{
		"log": map[string]any{"loglevel": "none"},
		"inbounds": []any{map[string]any{
			"tag": "fixture-socks", "listen": "127.0.0.1", "port": socksPort, "protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false},
		}},
		"outbounds": []any{map[string]any{
			"tag": "fixture-vless", "protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{
				"address": "127.0.0.1", "port": inboundPort,
				"users": []any{map[string]any{"id": credential, "encryption": "none"}},
			}}},
			"streamSettings": map[string]any{"network": "tcp", "security": "none"},
		}},
		"routing": map[string]any{"rules": []any{map[string]any{
			"type": "field", "inboundTag": []string{"fixture-socks"}, "outboundTag": "fixture-vless",
		}}},
	})
	configPath := filepath.Join(t.TempDir(), "projection-client.json")
	if err := os.WriteFile(configPath, fixtureConfig, 0o600); err != nil {
		t.Fatal("cannot write the private projection probe configuration")
	}
	cmd := exec.CommandContext(ctx, liveProjectionXrayBinary(t), "run", "-c", configPath, "-format", "json")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal("cannot start the projection probe client")
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		select {
		case <-done:
			t.Error("projection probe client exited before deliberate termination")
			return
		default:
		}
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Error("cannot terminate the projection probe client")
		}
		select {
		case <-done:
			var exitErr *exec.ExitError
			if cmd.ProcessState == nil || (waitErr != nil && !errors.As(waitErr, &exitErr)) {
				t.Error("projection probe client exit was not confirmed")
			}
		case <-time.After(5 * time.Second):
			t.Error("projection probe client was not reaped within five seconds")
		}
	}
	defer stop()
	readyDeadline := time.Now().Add(10 * time.Second)
	for !liveProjectionSOCKSHandshake(socksPort, 300*time.Millisecond) {
		select {
		case <-done:
			t.Fatal("projection probe client exited before its SOCKS listener became ready")
		case <-ctx.Done():
			t.Fatal("projection probe context ended before its SOCKS listener became ready")
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(readyDeadline) {
			t.Fatal("projection probe SOCKS listener did not become ready within ten seconds")
		}
	}

	proxyURL := &url.URL{Scheme: "socks5", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(socksPort))}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true, DisableCompression: true}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{
		Transport: transport, Timeout: time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	window := 20 * time.Second
	if !wantSuccess {
		window = 8 * time.Second
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, window)
	defer probeCancel()
	attempts := 0
	for {
		select {
		case <-done:
			t.Fatal("projection probe client exited before authorization was determined")
		default:
		}
		requestCtx, requestCancel := context.WithTimeout(probeCtx, 900*time.Millisecond)
		req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, origin.URL+"/ready", nil)
		if err != nil {
			requestCancel()
			t.Fatal("cannot create the projection probe request")
		}
		response, requestErr := httpClient.Do(req)
		var body []byte
		if response != nil {
			body, _ = io.ReadAll(io.LimitReader(response.Body, int64(len(download))+1))
			_ = response.Body.Close()
		}
		succeeded := requestErr == nil && response != nil && response.StatusCode == http.StatusOK && bytes.Equal(body, download)
		requestCancel()
		attempts++
		if !wantSuccess && (response != nil || originRequests.Load() != 0) {
			t.Fatal("revoked credentials reached the origin or returned a complete HTTP response")
		}
		if succeeded {
			if !wantSuccess {
				t.Fatal("a new connection succeeded after the authoritative raw client was removed")
			}
			t.Log("new_connection_authorized=true")
			return len(download)
		}
		// A failed proxied request proves remote authentication/path failure only
		// while the local fixture still accepts a SOCKS5 no-auth handshake. Treat
		// listener startup or listener loss as fixture failure, never revocation.
		if !liveProjectionSOCKSHandshake(socksPort, time.Second) {
			t.Fatal("projection probe request failed while its local SOCKS listener was unavailable")
		}
		select {
		case <-probeCtx.Done():
			if ctx.Err() != nil {
				t.Fatal("projection authorization cannot be evaluated after the parent context ended")
			}
			if wantSuccess {
				t.Fatal("a new connection did not succeed after the authoritative raw client was restored")
			}
			if attempts < 2 {
				t.Fatal("revocation probe did not make repeated new-connection attempts")
			}
			if originRequests.Load() != 0 {
				t.Fatal("revoked credentials reached the origin during the negative window")
			}
			t.Logf("new_connection_revoked=true attempts=%d", attempts)
			return 0
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func liveProjectionSOCKSHandshake(port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		return false
	}
	var response [2]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		return false
	}
	return response == [2]byte{5, 0}
}
