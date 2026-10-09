package threexui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// This contract exercises the actual HTTP Runtime, not just its adapter. It
// provisions a fresh candidate, performs normal credential writes, preserves
// two established response streams, and verifies durable accounting replay.
// The disposable client binary is a data-plane instrument only. No production
// configuration apply, Agent/Core end-to-end result or final settlement is
// inferred from PASS. All lab objects remain in the runner's stopped volumes.
func TestLive3XUIRuntimeContract(t *testing.T) {
	optIn := os.Getenv("CHIRAL_3XUI_TEST_WRITE")
	if optIn == "" {
		t.Skip("set CHIRAL_3XUI_TEST_WRITE=isolated-empty-instance to enable the HTTP Runtime contract")
	}
	if optIn != "isolated-empty-instance" {
		t.Fatal("HTTP Runtime contract requires explicit isolated-empty-instance opt-in")
	}
	liveHTTPProbeRequireLoopbackNamespace(t)
	baseURL, tokenFile := os.Getenv("CHIRAL_3XUI_TEST_URL"), os.Getenv("CHIRAL_3XUI_TEST_TOKEN_FILE")
	if baseURL == "" || tokenFile == "" {
		t.Fatal("HTTP Runtime contract requires the isolated URL and private token file")
	}
	api, err := New(Config{BaseURL: baseURL, Token: readLive3XUIToken(t, tokenFile), Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal("cannot construct the isolated HTTP Runtime client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	liveHTTPProbeRequireEmpty(t, ctx, api)
	baseline, err := api.ReadAssembledConfig(ctx)
	if err != nil {
		t.Fatal("cannot read the empty Runtime fixture configuration")
	}
	liveWriteRequireAPIOnly(t, baseline)
	identity, err := liveUpdateWaitState(ctx, api, "running")
	if err != nil {
		t.Fatal("cannot confirm the Runtime fixture identity")
	}
	restoreSettings := liveTailDisableRestart(t, ctx, api)
	defer restoreSettings()
	prefix, port := "chiral-runtime-"+liveWriteRandomID(t), liveProjectionFreePort(t)
	targetID, controlID := liveWriteUUID(t), liveWriteUUID(t)
	fixture := liveTailStartFixture(t, ctx, targetID, controlID, port)
	defer fixture.Close(t)
	probeURL := liveTailLogicalURL(fixture.pathPrefix + "/control/fresh")
	// Only the test environment prepares routing before ownership exists. The
	// Runtime does not modify templates to compensate for unsupported fields.
	fixtureConfig := liveWriteObject(t, liveHTTPProbeConfig(t, baseline, prefix, targetID, port, fixture.OriginAddress()))
	fixtureConfig["inbounds"] = liveWriteObject(t, baseline)["inbounds"]
	policy := liveWriteObject(t, fixtureConfig["policy"])
	levels := liveWriteObject(t, policy["levels"])
	levelZero := liveWriteObject(t, levels["0"])
	levelZero["statsUserUplink"], levelZero["statsUserDownlink"] = json.RawMessage("true"), json.RawMessage("true")
	levels["0"] = liveWriteJSON(t, levelZero)
	policy["levels"] = liveWriteJSON(t, levels)
	fixtureConfig["policy"] = liveWriteJSON(t, policy)
	if err := api.UpdateXrayTemplate(ctx, XrayTemplate{Config: liveWriteJSON(t, fixtureConfig), OutboundTestURL: probeURL}); err != nil {
		t.Fatal("cannot prepare the empty isolated Runtime routing fixture")
	}
	dir := filepath.Join(t.TempDir(), "http-runtime")
	store, err := InitializeRuntimeStore(ctx, api, dir)
	if err != nil {
		t.Fatalf("cannot initialize candidate ownership: %v", err)
	}
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	rt, err := NewRuntime(ctx, api, store, RuntimeConfig{InitializeReporting: true, CandidateUserWrites: true})
	if err != nil {
		t.Fatalf("cannot create candidate Runtime: %v", err)
	}
	defer func() {
		if rt != nil {
			_ = rt.Close()
		}
	}()
	tag := prefix + "-in"
	inbound := liveWriteJSON(t, map[string]any{"tag": tag, "listen": "127.0.0.1", "port": port, "protocol": "vless",
		"settings":       map[string]any{"decryption": "none", "clients": []any{}},
		"streamSettings": map[string]any{"network": "tcp", "security": "none"}, "sniffing": map[string]any{"enabled": false}})
	if err := rt.ProvisionInbound(ctx, inbound); err != nil {
		t.Fatalf("candidate Runtime bootstrap did not confirm exact readback: %v", err)
	}
	email, controlEmail := prefix+"-target@credential", prefix+"-control@credential"
	account := liveWriteJSON(t, map[string]any{"id": targetID, "email": email})
	if err := rt.AddUser(ctx, tag, email, account); err != nil {
		t.Fatalf("candidate Runtime account creation failed: %v", err)
	}
	if err := rt.AddUser(ctx, tag, controlEmail, liveWriteJSON(t, map[string]any{"id": controlID, "email": controlEmail})); err != nil {
		t.Fatalf("candidate Runtime control account creation failed: %v", err)
	}
	if err := rt.AddUser(ctx, tag, email, account); err != nil {
		t.Fatalf("exact current account did not reconcile idempotently: %v", err)
	}
	if _, err := rt.StatsBatch(ctx); err != nil {
		t.Fatalf("candidate cumulative baseline failed: %v", err)
	}
	fixture.StartStreams(t, ctx)
	fixture.Chunk(t, ctx)
	fixture.Fresh(t, ctx, true)
	var batch *runtimeprovider.AccountingBatch
	pollCtx, stopPolling := context.WithTimeout(ctx, 45*time.Second)
	defer stopPolling()
	for batch == nil {
		batch, err = rt.StatsBatch(pollCtx)
		if err != nil {
			t.Fatalf("candidate Runtime cumulative sampling failed: %v", err)
		}
		if batch == nil {
			// Upstream's first nonempty collection may establish its own
			// baseline and lose those bytes. Generate later controlled traffic,
			// never estimate or backfill that accepted initialization gap.
			fixture.Chunk(t, pollCtx)
			liveTailPause(t, pollCtx)
		}
	}
	if batch.ReporterID == "" || batch.Sequence == 0 || len(batch.Entries) == 0 {
		t.Fatal("real Runtime traffic did not create a durable identified batch")
	}
	for _, entry := range batch.Entries {
		if entry.Key != "user>>>"+email && entry.Key != "user>>>"+controlEmail {
			t.Fatal("Runtime billed an unowned counter")
		}
	}
	if err := rt.Close(); err != nil {
		t.Fatal("cannot close candidate reporter before restart verification")
	}
	if err := store.Close(); err != nil {
		t.Fatal("cannot close candidate owner before restart verification")
	}
	binding, err := RuntimeStoreBinding(api, identity.PanelGUID)
	if err != nil {
		t.Fatal(err)
	}
	store, err = OpenOwnershipStore(dir, binding)
	if err != nil {
		t.Fatal("cannot reopen the existing candidate owner")
	}
	rt, err = NewRuntime(ctx, api, store, RuntimeConfig{CandidateUserWrites: true})
	if err != nil {
		t.Fatal("cannot restore the existing candidate Runtime")
	}
	if replay, err := rt.StatsBatch(ctx); err != nil || !reflect.DeepEqual(replay, batch) {
		t.Fatal("candidate restart changed the durable unacknowledged batch")
	}
	if err := rt.AcknowledgeStats(batch.ReporterID, batch.Sequence); err != nil {
		t.Fatal("candidate Runtime did not accept the exact accounting receipt")
	}
	t.Log("runtime_stats_receipts_verified=true retained_batch_restart_verified=true")
	beforeDisable, err := api.ListClients(ctx)
	if err != nil {
		t.Fatal("cannot inspect original account identities")
	}
	if err := rt.RemoveUser(ctx, tag, email); err != nil {
		t.Fatalf("candidate temporary disable was not confirmed: %v", err)
	}
	fixture.Chunk(t, ctx)
	liveRuntimeDisabledWindow(t, ctx, fixture)
	if err := rt.AddUser(ctx, tag, email, account); err != nil {
		t.Fatalf("candidate re-enable was not confirmed: %v", err)
	}
	fixture.Chunk(t, ctx)
	fixture.Fresh(t, ctx, true)
	afterEnable, err := api.ListClients(ctx)
	if err != nil || len(beforeDisable) != len(afterEnable) {
		t.Fatal("temporary disable/re-enable lost an original account")
	}
	for i := range beforeDisable {
		if beforeDisable[i].ID != afterEnable[i].ID || beforeDisable[i].Email != afterEnable[i].Email {
			t.Fatal("temporary disable/re-enable replaced the original account identity")
		}
	}
	// Change a third credential while the two original response streams stay
	// open. HTTP probes are independent test clients, not process-management
	// commands; the runtime itself still uses only official user endpoints.
	rotationEmail, oldID, newID := prefix+"-rotation@credential", liveWriteUUID(t), liveWriteUUID(t)
	if err := rt.AddUser(ctx, tag, rotationEmail, liveWriteJSON(t, map[string]any{"id": oldID, "email": rotationEmail})); err != nil {
		t.Fatalf("candidate modification fixture creation failed: %v", err)
	}
	probe := func(id string, allowed bool) {
		t.Helper()
		before := fixture.controlFresh.Load()
		result, err := api.ProbeOutbound(ctx, liveHTTPProbeOutbound(t, prefix+"-rotation-probe", id, port), nil, probeURL)
		if allowed {
			if err != nil || result.HTTPStatus != 200 || fixture.controlFresh.Load() != before+1 {
				t.Fatalf("changed credential did not authenticate to the owned origin: %v", err)
			}
		} else if err == nil || fixture.controlFresh.Load() != before {
			t.Fatal("replaced credential still reached the owned origin")
		}
	}
	probe(oldID, true)
	if err := rt.AddUser(ctx, tag, rotationEmail, liveWriteJSON(t, map[string]any{"id": newID, "email": rotationEmail})); err != nil {
		t.Fatalf("candidate credential modification was not confirmed: %v", err)
	}
	fixture.Chunk(t, ctx)
	probe(newID, true)
	probe(oldID, false)
	if err := rt.RetireUser(ctx, tag, rotationEmail); err != nil {
		t.Fatalf("candidate modification fixture retirement failed: %v", err)
	}
	fixture.Chunk(t, ctx)
	t.Log("runtime_modification_verified=true")
	currentConfig, err := api.ReadAssembledConfig(ctx)
	if err != nil {
		t.Fatal("cannot read the current candidate revision for confirmation")
	}
	rt.SetProbe(liveHTTPProbeOutbound(t, prefix+"-confirmation", controlID, port), probeURL)
	if err := rt.Apply(7, currentConfig); err != nil || rt.ConfigVersion() != 7 {
		t.Fatalf("exact current candidate revision did not confirm with a real data probe: %v", err)
	}
	t.Log("runtime_exact_configuration_confirmation_verified=true")
	checkpoint, _, err := store.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Apply(1, []byte(`{"inbounds":[{"tag":"broken"}],"outbounds":[]}`)); !errors.Is(err, runtimeprovider.ErrOperationNotExecuted) {
		t.Fatal("different arbitrary config was not refused before mutation")
	}
	if err := rt.AddUser(ctx, tag, email, liveWriteJSON(t, map[string]any{"id": targetID, "email": email, "level": 7})); !errors.Is(err, runtimeprovider.ErrOperationNotExecuted) {
		t.Fatal("unsupported client level was silently discarded or written")
	}
	afterRefusal, _, err := store.Checkpoint()
	if err != nil || ownershipCheckpointDigest(afterRefusal) != ownershipCheckpointDigest(checkpoint) {
		t.Fatal("rejected candidate changed the retained desired checkpoint")
	}
	fixture.Chunk(t, ctx)
	fixture.Fresh(t, ctx, true)
	t.Log("runtime_configuration_refusal_verified=true existing_streams_survived=true")
	recentCtx, stopRecent := context.WithTimeout(ctx, 35*time.Second)
	defer stopRecent()
	for {
		recent, err := rt.RecentOnline(recentCtx)
		if err != nil || recent.RetrievedAt.IsZero() || len(recent.Users) != 2 {
			t.Fatalf("candidate recent-online read did not preserve the owned request set: %v", err)
		}
		found := false
		for _, user := range recent.Users {
			if user.Email != email && user.Email != controlEmail {
				t.Fatal("recent Runtime observation escaped owned credentials")
			}
			found = found || (user.Email == email && user.ObservedOnline)
		}
		if found {
			break
		}
		fixture.Chunk(t, recentCtx)
		liveTailPause(t, recentCtx)
	}
	if _, complete, err := rt.OnlineUsers(ctx); err == nil || complete {
		t.Fatal("recent observations were exposed as a current-device snapshot")
	}
	t.Log("runtime_recent_online_verified=true positive_ip_observation_verified=false")
	if err := rt.RetireUser(ctx, tag, email); err != nil {
		t.Fatalf("candidate permanent retirement was not confirmed: %v", err)
	}
	fixture.Chunk(t, ctx)
	fixture.Fresh(t, ctx, false)
	if err := rt.AddUser(ctx, tag, email, account); !errors.Is(err, runtimeprovider.ErrOperationNotExecuted) {
		t.Fatal("retired statistical identity was reused")
	}
	if rt.Snapshot().GetMode() != chiralv1.RuntimeMode_RUNTIME_MODE_CANDIDATE {
		t.Fatal("candidate Runtime advertised production ACTIVE")
	}
	liveHTTPProbeRequireIdentity(t, ctx, api, identity)
	t.Log("runtime_http_mutations_verified=true http_only_provider_control=true runtime_candidate_only=true final_settlement_verified=false core_integration_verified=false")
}

// A bounded disable window is distinct from permanent retirement. Do not
// reset the fixture's permanent revocation guard to permit reauthorization.
func liveRuntimeDisabledWindow(t *testing.T, ctx context.Context, fixture *liveTailFixture) {
	t.Helper()
	before := fixture.targetFresh.Load()
	status, body, err := fixture.freshRequest(ctx, fixture.target, 2*time.Second)
	if err == nil && (status != 0 || len(body) != 0) {
		t.Fatal("temporarily disabled credential completed a fresh proxy response")
	}
	fixture.requireFreshSuccess(t, ctx, fixture.control, 2*time.Second)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if fixture.targetFresh.Load() != before {
			t.Fatal("temporarily disabled fresh connection reached the origin during the rejection window")
		}
		liveTailPause(t, ctx)
	}
	t.Log("temporary_disable_new_connections_denied=true")
}
