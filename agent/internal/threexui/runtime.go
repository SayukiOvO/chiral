package threexui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
	"google.golang.org/protobuf/proto"
)

// RuntimeConfig permits HTTP execution only in a separately provisioned
// candidate instance. No setting enables production ACTIVE. Configuration
// changes requiring upstream's non-atomic apply are rejected before writing.
type RuntimeConfig struct {
	InitializeReporting bool
	CandidateUserWrites bool
	RequestTimeout      time.Duration
}

// Runtime connects the real HTTP provider to the Agent's existing stream,
// durable accounting and recent-observation protocols. It owns no Xray
// process, management API, binary or configuration history.
type Runtime struct {
	mu        sync.Mutex
	api       *Client
	store     *OwnershipStore
	scope     *OwnershipScope
	reporting *RuntimeReporting
	request   OwnershipObservationRequest
	config    RuntimeConfig
	probe     json.RawMessage
	probeURL  string

	statusMu sync.RWMutex
	status   *chiralv1.RuntimeStatus
	version  int64
}

var _ runtimeprovider.Runtime = (*Runtime)(nil)
var _ runtimeprovider.AcknowledgedReporter = (*Runtime)(nil)
var _ runtimeprovider.RecentOnlineReporter = (*Runtime)(nil)

func RuntimeStoreBinding(api *Client, panelGUID string) (OwnershipStoreBinding, error) {
	if api == nil || api.base == nil || !validOwnershipKey(panelGUID) {
		return OwnershipStoreBinding{}, runtimeRefusal("provider_identity_invalid")
	}
	return OwnershipStoreBinding{EndpointHash: updateDigest(api.base.String()), PanelGUID: panelGUID}, nil
}

func NewRuntime(ctx context.Context, api *Client, store *OwnershipStore, cfg RuntimeConfig) (*Runtime, error) {
	if api == nil || store == nil {
		return nil, runtimeRefusal("provider_state_missing")
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 15 * time.Second
	}
	binding, err := store.Binding()
	if err != nil || binding.EndpointHash != updateDigest(api.base.String()) {
		return nil, runtimeRefusal("provider_binding_changed")
	}
	scope, err := store.Scope()
	if err != nil {
		return nil, runtimeRefusal("ownership_state_unavailable")
	}
	status, err := api.Status(ctx)
	if err != nil || status.PanelGUID != binding.PanelGUID {
		return nil, runtimeRefusal("provider_identity_changed")
	}
	capabilities, err := api.DiscoverCapabilities(ctx)
	if err != nil || len(capabilities.Missing()) != 0 {
		return nil, runtimeRefusal("provider_contract_incompatible")
	}
	reporter, err := NewRuntimeReporting(api, store, cfg.InitializeReporting)
	if err != nil {
		return nil, err
	}
	rt := &Runtime{api: api, store: store, scope: scope, reporting: reporter, config: cfg,
		request: OwnershipObservationRequest{Binding: binding, ExpectedPanelVersion: status.PanelVersion,
			ExpectedOpenAPIDigest: capabilities.DocumentSHA256}}
	rt.cacheStatus(status, nil)
	return rt, nil
}

// InitializeRuntimeStore records only an empty, explicitly isolated provider.
// Existing rows are never adopted. The caller holds the Agent directory lock.
func InitializeRuntimeStore(ctx context.Context, api *Client, dir string) (*OwnershipStore, error) {
	if api == nil {
		return nil, runtimeRefusal("provider_missing")
	}
	status, err := api.Status(ctx)
	if err != nil || status.PanelGUID == "" {
		return nil, runtimeRefusal("provider_identity_unavailable")
	}
	capabilities, err := api.DiscoverCapabilities(ctx)
	if err != nil || len(capabilities.Missing()) != 0 {
		return nil, runtimeRefusal("provider_contract_incompatible")
	}
	inbounds, err := api.ListInbounds(ctx)
	if err != nil || len(inbounds) != 0 {
		return nil, runtimeRefusal("initial_provider_not_empty")
	}
	clients, err := api.ListClients(ctx)
	if err != nil || len(clients) != 0 {
		return nil, runtimeRefusal("initial_provider_not_empty")
	}
	template, err := api.ReadXrayTemplate(ctx)
	if err != nil {
		return nil, runtimeRefusal("template_unavailable")
	}
	assembled, err := api.ReadAssembledConfig(ctx)
	if err != nil {
		return nil, runtimeRefusal("configuration_unavailable")
	}
	for _, raw := range []json.RawMessage{template.Config, assembled} {
		config, err := decodeComparisonConfig(raw, "")
		if err != nil {
			return nil, runtimeRefusal("configuration_invalid")
		}
		rows, ok := config["inbounds"].([]any)
		if config["inbounds"] == nil {
			continue // Upstream may store an omitted/null empty template array.
		}
		if !ok {
			return nil, runtimeRefusal("initial_provider_not_empty")
		}
		for _, row := range rows {
			inbound, ok := row.(map[string]any)
			if !ok || inbound["tag"] != "api" ||
				(inbound["protocol"] != "dokodemo-door" && inbound["protocol"] != "tunnel") ||
				(inbound["listen"] != "127.0.0.1" && inbound["listen"] != "::1") {
				return nil, runtimeRefusal("initial_provider_not_empty")
			}
		}
	}
	templateDigest, err := runtimeTemplateDigest(template)
	if err != nil {
		return nil, err
	}
	comparison, err := CompareConfig(assembled, assembled)
	if err != nil {
		return nil, runtimeRefusal("configuration_invalid")
	}
	after, err := api.Status(ctx)
	if err != nil || after.PanelGUID != status.PanelGUID || after.PanelVersion != status.PanelVersion {
		return nil, runtimeRefusal("provider_identity_changed")
	}
	return CreateOwnershipStore(dir, OwnershipStoreBinding{EndpointHash: updateDigest(api.base.String()), PanelGUID: status.PanelGUID},
		OwnershipCheckpoint{Keys: OwnedKeys{InboundTags: []string{}, Clients: []OwnedClientKey{}},
			TemplateDigest: templateDigest, ConfigDigest: comparison.ActualDigest})
}

// Apply acknowledges only an exact, already assembled revision with a real
// data-path probe. Official HTTP currently cannot stage/atomically commit an
// arbitrary different config while protecting the old instance. Refusing such
// changes is essential; compensation after a failed write is not that guarantee.
func (r *Runtime) Apply(version int64, configJSON []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if version <= 0 {
		return runtimeRefusal("configuration_version_invalid")
	}
	if _, err := CompareConfig(configJSON, configJSON); err != nil {
		return runtimeRefusal("configuration_invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.config.RequestTimeout)
	defer cancel()
	checkpoint, _, observed, err := r.observe(ctx)
	if err != nil {
		return err
	}
	comparison, err := CompareConfig(configJSON, observed)
	if err != nil || !comparison.Equivalent || comparison.ActualDigest != checkpoint.ConfigDigest {
		return runtimeRefusal("atomic_configuration_apply_unavailable")
	}
	if len(r.probe) == 0 || r.probeURL == "" {
		return runtimeRefusal("data_path_probe_missing")
	}
	if _, err := r.api.ProbeOutbound(ctx, r.probe, nil, r.probeURL); err != nil {
		return runtimeRefusal("data_path_probe_failed")
	}
	if _, _, _, err := r.observe(ctx); err != nil {
		return err
	}
	r.statusMu.Lock()
	r.version = version
	r.statusMu.Unlock()
	return nil
}

func (r *Runtime) SetProbe(outbound []byte, target string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.probe = append(json.RawMessage(nil), outbound...)
	r.probeURL = target
}

func (r *Runtime) Restart() error {
	// Restarts belong to a confirmed command/update workflow. They are never
	// a fallback for rejected configuration or client fields.
	return runtimeRefusal("candidate_restart_not_enabled")
}

func (r *Runtime) AddUser(ctx context.Context, tag, email string, accountJSON []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, r.config.RequestTimeout)
	defer cancel()
	checkpoint, generation, before, err := r.userPreflight(ctx)
	if err != nil {
		return err
	}
	account, err := decodeComparisonConfig(accountJSON, "")
	if err != nil || account["email"] != email || validClientEmail(email) != nil {
		return runtimeRefusal("client_account_invalid")
	}
	want, protocol, existed, err := runtimeClientConfig(before, tag, email, account)
	if err != nil {
		return err
	}
	if err := runtimeAccountCompatible(protocol, account); err != nil {
		return err
	}
	owned := false
	for _, client := range checkpoint.Keys.Clients {
		if client.Email == email {
			if client.InboundTag != tag {
				return runtimeRefusal("client_attachment_changed")
			}
			owned = true
		}
	}
	if !slices.Contains(checkpoint.Keys.InboundTags, tag) || (existed && !owned) {
		return runtimeRefusal("client_ownership_conflict")
	}
	if err := r.reporting.CheckClientKey(tag, email); err != nil {
		return runtimeRefusal("client_accounting_identity_conflict")
	}
	clients, err := r.api.ListClients(ctx)
	if err != nil {
		return runtimeRefusal("inventory_unavailable")
	}
	var existingRequest map[string]any
	for _, row := range clients {
		if row.Email == email {
			if !owned || existingRequest != nil {
				return runtimeRefusal("client_ownership_conflict")
			}
			existingRequest, err = runtimeClientRecordRequest(row.Raw)
			if err != nil {
				return err
			}
			if !existed {
				if enabled, ok := existingRequest["enable"].(bool); !ok || enabled {
					return runtimeRefusal("client_runtime_state_conflict")
				}
			}
		}
	}
	if owned && existingRequest == nil {
		return runtimeRefusal("owned_client_missing")
	}
	if owned && !existed {
		want, err = runtimeRestoreClientOrder(want, tag, email, clients)
		if err != nil {
			return err
		}
	}
	comparison, err := CompareConfig(before, want)
	if err != nil {
		return runtimeRefusal("configuration_invalid")
	}
	if comparison.Equivalent {
		return r.reporting.CaptureOwnership(ctx, checkpoint.Keys)
	}
	rows, err := r.api.ListInbounds(ctx)
	if err != nil {
		return runtimeRefusal("inventory_unavailable")
	}
	var inboundID int64
	for _, row := range rows {
		if row.Tag == tag {
			inboundID = row.ID
		}
	}
	if inboundID <= 0 {
		return runtimeRefusal("owned_inbound_missing")
	}
	marker, err := r.scope.ClientMarker(email)
	if err != nil {
		return runtimeRefusal("client_account_invalid")
	}
	// Lifecycle and quotas remain owned by Core. Do not let upstream's
	// getConfigJson maintenance renew or disable an independently owned user.
	request := runtimeMergeClientRequest(existingRequest, account, marker)
	payload, err := json.Marshal(request)
	if err != nil {
		return runtimeRefusal("client_account_invalid")
	}
	after := cloneOwnershipCheckpoint(checkpoint)
	if !owned {
		after.Keys.Clients = append(after.Keys.Clients, OwnedClientKey{Email: email, InboundTag: tag})
	}
	after.ConfigDigest = comparison.ActualDigest
	opID, err := runtimeOperationID()
	if err != nil {
		return runtimeRefusal("operation_identity_unavailable")
	}
	if err := r.store.Prepare(opID, generation, updateDigest(string(payload)+after.ConfigDigest), after); err != nil {
		return runtimeRefusal("write_intent_unavailable")
	}
	// From this point every error is potentially a committed/partial write.
	// Leave the persistent intent in place; never retry or report NOT_EXECUTED.
	if owned {
		err = r.api.UpdateClient(ctx, email, payload)
	} else {
		err = r.api.AddClient(ctx, payload, []int64{inboundID})
	}
	if err != nil {
		return runtimeUnknown("client_write_result_unknown")
	}
	return r.completeWrite(ctx, opID, after)
}

// RemoveUser temporarily disables an account. The original credential and
// retained counter row are reused only for this exact verified binding.
func (r *Runtime) RemoveUser(ctx context.Context, tag, email string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, r.config.RequestTimeout)
	defer cancel()
	checkpoint, generation, before, err := r.userPreflight(ctx)
	if err != nil {
		return err
	}
	owned := false
	for _, key := range checkpoint.Keys.Clients {
		if key.Email == email {
			if key.InboundTag != tag {
				return runtimeRefusal("client_attachment_changed")
			}
			owned = true
		}
	}
	if !owned {
		return runtimeRefusal("owned_client_missing")
	}
	want, _, existed, err := runtimeClientConfig(before, tag, email, nil)
	if err != nil {
		return err
	}
	if !existed {
		return nil
	}
	clients, err := r.api.ListClients(ctx)
	if err != nil {
		return runtimeRefusal("inventory_unavailable")
	}
	var payload []byte
	for _, row := range clients {
		if row.Email != email {
			continue
		}
		if payload != nil {
			return runtimeRefusal("client_ownership_conflict")
		}
		account, err := runtimeClientRecordRequest(row.Raw)
		if err != nil {
			return err
		}
		account["enable"] = false
		payload, err = json.Marshal(account)
		if err != nil {
			return runtimeRefusal("client_account_invalid")
		}
	}
	if len(payload) == 0 {
		return runtimeRefusal("owned_client_missing")
	}
	comparison, err := CompareConfig(before, want)
	if err != nil {
		return runtimeRefusal("configuration_invalid")
	}
	after := cloneOwnershipCheckpoint(checkpoint)
	after.ConfigDigest = comparison.ActualDigest
	opID, err := runtimeOperationID()
	if err != nil {
		return runtimeRefusal("operation_identity_unavailable")
	}
	if err := r.store.Prepare(opID, generation, updateDigest(string(payload)+after.ConfigDigest), after); err != nil {
		return runtimeRefusal("write_intent_unavailable")
	}
	if err := r.api.UpdateClient(ctx, email, payload); err != nil {
		return runtimeUnknown("client_write_result_unknown")
	}
	return r.completeWrite(ctx, opID, after)
}

// RetireUser permanently deletes a credential but retains its local billing
// row and provenance. Core must request this explicitly before dropping rows.
func (r *Runtime) RetireUser(ctx context.Context, tag, email string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, r.config.RequestTimeout)
	defer cancel()
	checkpoint, generation, before, err := r.userPreflight(ctx)
	if err != nil {
		return err
	}
	owned := false
	after := cloneOwnershipCheckpoint(checkpoint)
	after.Keys.Clients = nil
	for _, client := range checkpoint.Keys.Clients {
		if client.Email == email {
			if client.InboundTag != tag {
				return runtimeRefusal("client_attachment_changed")
			}
			owned = true
		} else {
			after.Keys.Clients = append(after.Keys.Clients, client)
		}
	}
	if !slices.Contains(checkpoint.Keys.InboundTags, tag) || validClientEmail(email) != nil {
		return runtimeRefusal("client_ownership_conflict")
	}
	want, _, existed, err := runtimeClientConfig(before, tag, email, nil)
	if err != nil || (existed && !owned) {
		return runtimeRefusal("client_ownership_conflict")
	}
	if !owned {
		return nil // Already absent under this exact validated manifest.
	}
	comparison, err := CompareConfig(before, want)
	if err != nil {
		return runtimeRefusal("configuration_invalid")
	}
	after.ConfigDigest = comparison.ActualDigest
	opID, err := runtimeOperationID()
	if err != nil {
		return runtimeRefusal("operation_identity_unavailable")
	}
	if err := r.store.Prepare(opID, generation, updateDigest("remove\x00"+tag+"\x00"+email+after.ConfigDigest), after); err != nil {
		return runtimeRefusal("write_intent_unavailable")
	}
	// Bind the retained statistics row BEFORE the upstream account disappears.
	// This is ongoing sampling, never a claim of final settlement.
	if err := r.reporting.PrepareRetirement(ctx, tag, email); err != nil {
		return runtimeUnknown("traffic_retirement_unavailable")
	}
	if err := r.api.DeleteClient(ctx, email, true); err != nil {
		return runtimeUnknown("client_write_result_unknown")
	}
	return r.completeWrite(ctx, opID, after)
}

func (r *Runtime) completeWrite(ctx context.Context, opID string, after OwnershipCheckpoint) error {
	if err := r.verifyCandidateInventory(ctx, after.Keys); err != nil {
		return runtimeUnknown("client_write_inventory_changed")
	}
	request := r.request
	request.Keys = after.Keys
	observation, err := r.api.ObserveOwnership(ctx, r.scope, request)
	if err != nil || ownershipCheckpointDigest(observation.Checkpoint) != ownershipCheckpointDigest(after) {
		return runtimeUnknown("client_write_readback_failed")
	}
	policy, err := r.api.ReadRuntimePolicy(ctx)
	if err != nil || policy.RestartXrayOnClientDisable {
		return runtimeUnknown("client_runtime_policy_changed")
	}
	if err := r.reporting.CaptureOwnership(ctx, after.Keys); err != nil {
		return runtimeUnknown("client_accounting_binding_failed")
	}
	if err := r.store.Complete(opID, true, observation.Checkpoint); err != nil {
		return runtimeUnknown("client_write_receipt_failed")
	}
	return nil
}

func (r *Runtime) userPreflight(ctx context.Context) (OwnershipCheckpoint, uint64, json.RawMessage, error) {
	if !r.config.CandidateUserWrites {
		return OwnershipCheckpoint{}, 0, nil, runtimeRefusal("candidate_user_writes_disabled")
	}
	policy, err := r.api.ReadRuntimePolicy(ctx)
	if err != nil || policy.RestartXrayOnClientDisable {
		return OwnershipCheckpoint{}, 0, nil, runtimeRefusal("client_restart_policy_unsafe")
	}
	checkpoint, generation, raw, err := r.observe(ctx)
	if err != nil {
		return OwnershipCheckpoint{}, 0, nil, err
	}
	if err := r.reporting.VerifyOwnership(ctx); err != nil {
		return OwnershipCheckpoint{}, 0, nil, runtimeRefusal("provider_numeric_identity_changed")
	}
	return checkpoint, generation, raw, nil
}

func (r *Runtime) observe(ctx context.Context) (OwnershipCheckpoint, uint64, json.RawMessage, error) {
	checkpoint, generation, err := r.store.Checkpoint()
	if err != nil {
		return OwnershipCheckpoint{}, 0, nil, runtimeRefusal("ownership_state_unavailable")
	}
	pending, err := r.store.Pending()
	if err != nil || pending != nil {
		return OwnershipCheckpoint{}, 0, nil, runtimeRefusal("unresolved_write_intent")
	}
	if err := r.verifyCandidateInventory(ctx, checkpoint.Keys); err != nil {
		return OwnershipCheckpoint{}, 0, nil, err
	}
	request := r.request
	request.Keys = checkpoint.Keys
	observation, err := r.api.ObserveOwnership(ctx, r.scope, request)
	if err != nil || ownershipCheckpointDigest(observation.Checkpoint) != ownershipCheckpointDigest(checkpoint) {
		return OwnershipCheckpoint{}, 0, nil, runtimeRefusal("provider_configuration_drift")
	}
	raw, err := r.api.ReadAssembledConfig(ctx)
	if err != nil {
		return OwnershipCheckpoint{}, 0, nil, runtimeRefusal("configuration_unavailable")
	}
	comparison, err := CompareConfig(raw, raw)
	if err != nil || comparison.ActualDigest != checkpoint.ConfigDigest {
		return OwnershipCheckpoint{}, 0, nil, runtimeRefusal("provider_configuration_drift")
	}
	return checkpoint, generation, raw, nil
}

// Candidate ownership starts empty and remains exclusive. Refuse unexpected
// objects or independent lifecycle settings BEFORE lifecycle-bearing config
// reads, rather than maintaining another administrator's accounts as a side
// effect of discovering drift. This is not a CAS against an external writer.
func (r *Runtime) verifyCandidateInventory(ctx context.Context, keys OwnedKeys) error {
	inbounds, err := r.api.ListInbounds(ctx)
	if err != nil || len(inbounds) != len(keys.InboundTags) {
		return runtimeRefusal("candidate_inventory_not_exclusive")
	}
	clients, err := r.api.ListClients(ctx)
	if err != nil || len(clients) != len(keys.Clients) {
		return runtimeRefusal("candidate_inventory_not_exclusive")
	}
	if _, err := r.scope.VerifyInventory(r.request.Binding.PanelGUID, keys, inbounds, clients); err != nil {
		return runtimeRefusal("candidate_ownership_changed")
	}
	for _, row := range inbounds {
		inbound, err := decodeComparisonConfig(row.Raw, "")
		if err != nil || inbound["enable"] != true {
			return runtimeRefusal("candidate_lifecycle_not_core_owned")
		}
		for _, key := range []string{"total", "expiryTime"} {
			number, ok := inbound[key].(json.Number)
			if !ok {
				return runtimeRefusal("candidate_lifecycle_not_core_owned")
			}
			if value, err := number.Int64(); err != nil || value != 0 {
				return runtimeRefusal("candidate_lifecycle_not_core_owned")
			}
		}
	}
	for _, row := range clients {
		account, err := decodeComparisonConfig(row.Raw, "")
		if err != nil {
			return runtimeRefusal("client_record_invalid")
		}
		for _, key := range []string{"totalGB", "expiryTime", "reset"} {
			number, ok := account[key].(json.Number)
			if !ok {
				return runtimeRefusal("candidate_lifecycle_not_core_owned")
			}
			if value, err := number.Int64(); err != nil || value != 0 {
				return runtimeRefusal("candidate_lifecycle_not_core_owned")
			}
		}
	}
	return nil
}

func (r *Runtime) StatsBatch(ctx context.Context) (*runtimeprovider.AccountingBatch, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reporting.StatsBatch(ctx)
}

func (r *Runtime) AcknowledgeStats(reporterID string, sequence uint64) error {
	// ACK reception must remain independent of a serialized mutation/read.
	return r.reporting.AcknowledgeStats(reporterID, sequence)
}

func (r *Runtime) RecentOnline(ctx context.Context) (runtimeprovider.RecentOnlineSample, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reporting.RecentOnline(ctx)
}

func (r *Runtime) Stats(context.Context) ([]runtimeprovider.Stat, error) {
	return nil, runtimeRefusal("acknowledged_accounting_required")
}

func (r *Runtime) OnlineUsers(context.Context) ([]runtimeprovider.OnlineUser, bool, error) {
	return nil, false, runtimeRefusal("recent_observations_required")
}

func (r *Runtime) Run(ctx context.Context) {
	r.refresh(ctx)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.refresh(ctx)
		}
	}
}

func (r *Runtime) refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, r.config.RequestTimeout)
	defer cancel()
	status, err := r.api.Status(ctx)
	if err == nil && (status.PanelGUID != r.request.Binding.PanelGUID || status.PanelVersion != r.request.ExpectedPanelVersion) {
		err = runtimeRefusal("provider_identity_changed")
	}
	r.cacheStatus(status, err)
}

func (r *Runtime) cacheStatus(status Status, err error) {
	view := &chiralv1.RuntimeStatus{Provider: "3x-ui", Mode: chiralv1.RuntimeMode_RUNTIME_MODE_CANDIDATE,
		Version: r.request.ExpectedPanelVersion, ContractDigest: r.request.ExpectedOpenAPIDigest,
		ObservedAtUnix: time.Now().Unix(), Health: chiralv1.RuntimeHealth_RUNTIME_HEALTH_INCOMPATIBLE,
		Error: "isolated candidate; arbitrary atomic configuration apply remains unavailable"}
	if err != nil {
		view.Health = chiralv1.RuntimeHealth_RUNTIME_HEALTH_UNREACHABLE
		view.Error = "candidate provider observation failed"
	} else {
		view.XrayState = runtimeXrayState(status.Xray.State)
		view.XrayVersion = status.Xray.Version
	}
	r.statusMu.Lock()
	r.status = view
	r.statusMu.Unlock()
}

func (r *Runtime) Snapshot() *chiralv1.RuntimeStatus {
	r.statusMu.RLock()
	defer r.statusMu.RUnlock()
	if r.status == nil {
		return nil
	}
	copy := proto.Clone(r.status).(*chiralv1.RuntimeStatus)
	age := time.Now().Unix() - copy.ObservedAtUnix
	if age > 0 && age < 1<<32 {
		copy.ObservedAgeSeconds = uint32(age)
	}
	return copy
}

func (r *Runtime) State() chiralv1.XrayState {
	r.statusMu.RLock()
	defer r.statusMu.RUnlock()
	return r.status.GetXrayState()
}

func (r *Runtime) ConfigVersion() int64 {
	r.statusMu.RLock()
	defer r.statusMu.RUnlock()
	return r.version
}

func (r *Runtime) RunningVersion() string {
	r.statusMu.RLock()
	defer r.statusMu.RUnlock()
	if r.status.GetXrayState() != chiralv1.XrayState_XRAY_STATE_RUNNING {
		return ""
	}
	return strings.TrimPrefix(r.status.GetXrayVersion(), "v")
}

func (r *Runtime) InstalledVersion() string {
	// status reports the running binary, not the next-start on-disk binary.
	return ""
}

func (r *Runtime) Close() error { return r.reporting.Close() }

func runtimeXrayState(value string) chiralv1.XrayState {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "running":
		return chiralv1.XrayState_XRAY_STATE_RUNNING
	case "stop", "stopped":
		return chiralv1.XrayState_XRAY_STATE_STOPPED
	case "error":
		return chiralv1.XrayState_XRAY_STATE_ERROR
	default:
		return chiralv1.XrayState_XRAY_STATE_UNSPECIFIED
	}
}

func runtimeTemplateDigest(template XrayTemplate) (string, error) {
	raw, err := json.Marshal(struct {
		Config json.RawMessage `json:"config"`
		URL    string          `json:"outbound_test_url"`
	}{template.Config, template.OutboundTestURL})
	if err != nil {
		return "", runtimeRefusal("template_invalid")
	}
	comparison, err := CompareConfig(raw, raw)
	if err != nil {
		return "", runtimeRefusal("template_invalid")
	}
	return comparison.ActualDigest, nil
}

func runtimeOperationID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func runtimeRefusal(reason string) error {
	return fmt.Errorf("3x-ui operation refused: %s: %w", reason, runtimeprovider.ErrOperationNotExecuted)
}

func runtimeUnknown(reason string) error {
	return fmt.Errorf("3x-ui operation requires reconciliation: %s", reason)
}

func runtimeAccountCompatible(protocol string, account map[string]any) error {
	for key, value := range account {
		if key == "email" {
			continue
		}
		text, ok := value.(string)
		// Candidate hot writes accept only the narrow default account shape.
		// A stored/readback flow or security value does not prove that the hot
		// User account carries it. In particular upstream normalizes vision-
		// udp443, so accepting it here would create a write/readback conflict.
		allowed := (key == "id" && (protocol == "vless" || protocol == "vmess")) ||
			(key == "password" && protocol == "trojan")
		if !ok || text == "" || !allowed {
			return runtimeRefusal("client_fields_not_preserved_by_official_hot_api")
		}
	}
	credential := "id"
	if protocol == "trojan" {
		credential = "password"
	} else if protocol != "vless" && protocol != "vmess" {
		return runtimeRefusal("client_protocol_not_verified")
	}
	if value, ok := account[credential].(string); !ok || value == "" {
		return runtimeRefusal("client_account_invalid")
	}
	if credential == "id" && !runtimeCanonicalUUID(account[credential].(string)) {
		return runtimeRefusal("client_credential_not_verified")
	}
	if credential == "password" && strings.TrimSpace(account[credential].(string)) == "" {
		return runtimeRefusal("client_account_invalid")
	}
	return nil
}

func runtimeCanonicalUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil && len(decoded) == 16
}

// Modify exactly one client array in a complete config. Every other member,
// including unknown fields, participates in the expected readback digest.
func runtimeClientConfig(raw []byte, tag, email string, account map[string]any) ([]byte, string, bool, error) {
	config, err := decodeComparisonConfig(raw, "")
	if err != nil {
		return nil, "", false, runtimeRefusal("configuration_invalid")
	}
	inbounds, ok := config["inbounds"].([]any)
	if !ok {
		return nil, "", false, runtimeRefusal("configuration_invalid")
	}
	var matched map[string]any
	for _, item := range inbounds {
		inbound, ok := item.(map[string]any)
		if !ok {
			return nil, "", false, runtimeRefusal("configuration_invalid")
		}
		if inbound["tag"] == tag {
			if matched != nil {
				return nil, "", false, runtimeRefusal("inbound_tag_ambiguous")
			}
			matched = inbound
		}
	}
	if matched == nil {
		return nil, "", false, runtimeRefusal("owned_inbound_missing")
	}
	protocol, _ := matched["protocol"].(string)
	settings, ok := matched["settings"].(map[string]any)
	if !ok {
		return nil, "", false, runtimeRefusal("configuration_invalid")
	}
	clients, ok := settings["clients"].([]any)
	if !ok {
		return nil, "", false, runtimeRefusal("configuration_invalid")
	}
	result := make([]any, 0, len(clients)+1)
	existed := false
	for _, item := range clients {
		client, ok := item.(map[string]any)
		if !ok {
			return nil, "", false, runtimeRefusal("configuration_invalid")
		}
		if client["email"] != email {
			result = append(result, item)
			continue
		}
		if existed {
			return nil, "", false, runtimeRefusal("client_identity_ambiguous")
		}
		existed = true
		if account != nil {
			result = append(result, account)
		}
	}
	if !existed && account != nil {
		result = append(result, account)
	}
	settings["clients"] = result
	want, err := json.Marshal(config)
	if err != nil {
		return nil, "", false, runtimeRefusal("configuration_invalid")
	}
	return want, protocol, existed, nil
}
