package threexui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// RuntimeReporting is the live provider's HTTP-only accounting and recent
// observation path. The caller owns the already-open OwnershipStore and keeps
// its directory lock for this component's entire lifetime. Close does not close
// that store. No method grants user access or modifies 3x-ui settings.
type RuntimeReporting struct {
	mu         sync.Mutex
	lifetime   sync.RWMutex
	client     *Client
	store      *OwnershipStore
	accounting *runtimeprovider.Accounting
	closed     bool
}

var _ runtimeprovider.AcknowledgedReporter = (*RuntimeReporting)(nil)
var _ runtimeprovider.RecentOnlineReporter = (*RuntimeReporting)(nil)

// NewRuntimeReporting initializes only an explicitly fresh, empty owner. On
// restore it requires both the retained reporting marker and the existing
// restricted journal. A missing/corrupt journal never receives a new reporter
// identity. initialize must be false for all previously managed resources.
func NewRuntimeReporting(client *Client, store *OwnershipStore, initialize bool) (*RuntimeReporting, error) {
	if client == nil || client.base == nil || store == nil {
		return nil, reportingError("invalid_configuration")
	}
	binding, _, state, path, err := store.reportingSnapshot()
	if err != nil || binding.EndpointHash != updateDigest(client.base.String()) {
		return nil, reportingError("ownership_unavailable")
	}
	if err := store.claimReporting(); err != nil {
		return nil, reportingError("already_open")
	}
	opened := false
	defer func() {
		if !opened {
			store.releaseReporting()
		}
	}()
	info, statErr := os.Lstat(path)
	if initialize {
		if state != nil || !errors.Is(statErr, os.ErrNotExist) {
			return nil, reportingError("initialization_refused")
		}
		if err := store.initializeReporting(); err != nil {
			return nil, reportingError("initialization_refused")
		}
	} else if state == nil || statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, reportingError("journal_unavailable")
	}
	identity := "3x-ui:" + binding.EndpointHash + ":" + binding.PanelGUID
	accounting, err := runtimeprovider.OpenAccounting(path, identity, runtimeprovider.BaselineOnly)
	if err != nil {
		return nil, reportingError("journal_unavailable")
	}
	retainedIdentity, err := accounting.BoundProviderIdentity()
	if err != nil || retainedIdentity != identity {
		return nil, reportingError("journal_binding_changed")
	}
	opened = true
	return &RuntimeReporting{client: client, store: store, accounting: accounting}, nil
}

// CheckClientKey is a pre-write guard, not authority to create an account.
// Core must issue a new statistical identity after an explicit reauthorization;
// a retained retired identity cannot be reused by a same-named new account.
func (r *RuntimeReporting) CheckClientKey(inboundTag, email string) error {
	r.lifetime.RLock()
	defer r.lifetime.RUnlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.available(); err != nil {
		return err
	}
	if !validOwnershipKey(inboundTag) || !validOwnershipKey(email) || validClientEmail(email) != nil {
		return reportingError("invalid_client_key")
	}
	_, _, state, _, err := r.store.reportingSnapshot()
	if err != nil || state == nil {
		return reportingError("ownership_unavailable")
	}
	for _, owner := range state.Owners {
		if owner.Email == email && (owner.Retired || owner.InboundTag != inboundTag) {
			return reportingError("retained_client_key_conflict")
		}
	}
	return nil
}

// VerifyOwnership is the Runtime's pre-write check for previously captured
// numeric bindings. A configuration digest alone cannot reveal a recreated
// client or statistics row with the same email and metadata marker. This read
// does not advance watermarks, produce a batch, or clear a pending write.
func (r *RuntimeReporting) VerifyOwnership(ctx context.Context) error {
	r.lifetime.RLock()
	defer r.lifetime.RUnlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.available(); err != nil {
		return err
	}
	binding, checkpoint, state, _, err := r.store.reportingSnapshot()
	if err != nil || state == nil {
		return reportingError("ownership_unavailable")
	}
	observation, err := r.observe(ctx, binding, checkpoint.Keys)
	if err != nil {
		return err
	}
	return validateReportingCounters(binding, checkpoint.Keys, state.Owners, observation, false)
}

// CaptureOwnership binds every live managed account to its exact original
// client and local-stat row identities under the current checkpoint or the
// sole pending intent's exact desired manifest. The Runtime calls this only
// after a complete successful write response and exact readback, then completes
// that intent. Capture never clears an intent or substitutes for its terminal
// response. A retained email cannot be silently reassigned after retirement.
func (r *RuntimeReporting) CaptureOwnership(ctx context.Context, keys OwnedKeys) error {
	r.lifetime.RLock()
	defer r.lifetime.RUnlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.available(); err != nil {
		return err
	}
	binding, _, state, _, err := r.store.reportingSnapshot()
	if err != nil || state == nil || r.store.reportingKeysAllowed(keys) != nil {
		return reportingError("manifest_changed")
	}
	observation, err := r.observe(ctx, binding, keys)
	if err != nil {
		return err
	}
	owners := make([]TrafficOwnership, 0, len(keys.Clients))
	for _, key := range keys.Clients {
		counter, exists := observation.clients[key.Email]
		if !exists || counter.InboundID != observation.inventory.InboundIDs[key.InboundTag] {
			return reportingError("live_counter_missing")
		}
		owners = append(owners, TrafficOwnership{PanelGUID: binding.PanelGUID, InboundTag: key.InboundTag,
			InboundID: counter.InboundID, ClientID: observation.inventory.ClientIDs[key.Email], StatID: counter.ID, Email: key.Email})
	}
	if err := validateReportingCounters(binding, keys, state.Owners, observation, true); err != nil {
		return err
	}
	return r.store.mergeTrafficOwnership(keys, owners)
}

// PrepareRetirement verifies and durably retains an account's original billing
// identity before DeleteClient(keepTraffic=true). It does not wait for a Core ACK
// and makes no claim of final settlement: an old response stream can continue
// producing bytes after deletion, and an existing pending batch prevents new
// watermarks from being committed until that batch has been acknowledged.
// On an uncertain deletion result this retired identity remains retained.
func (r *RuntimeReporting) PrepareRetirement(ctx context.Context, inboundTag, email string) error {
	r.lifetime.RLock()
	defer r.lifetime.RUnlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.available(); err != nil {
		return err
	}
	binding, checkpoint, state, _, err := r.store.reportingSnapshot()
	if err != nil || state == nil {
		return reportingError("ownership_unavailable")
	}
	var owner TrafficOwnership
	found := false
	for _, candidate := range state.Owners {
		if candidate.Email == email && candidate.InboundTag == inboundTag && !candidate.Retired {
			owner, found = candidate, true
		}
	}
	if !found {
		return reportingError("retirement_owner_missing")
	}
	observation, err := r.observe(ctx, binding, checkpoint.Keys)
	if err != nil {
		return err
	}
	if err := validateReportingCounters(binding, checkpoint.Keys, state.Owners, observation, false); err != nil {
		return err
	}
	if _, exists := observation.clients[email]; !exists {
		return reportingError("retirement_counter_missing")
	}
	if err := r.store.retireTrafficOwnership(owner); err != nil {
		return err
	}
	// This read is pre-delete evidence, not a flush. PendingBatch/Sample retains
	// the existing immutable delivery instead of pretending this is fresh data.
	_, err = r.accounting.Sample(binding.PanelGUID, reportingCumulativeCounters(state.Owners, observation))
	if err != nil {
		return reportingError("journal_write_failed")
	}
	return nil
}

// StatsBatch first replays the durable outbox without touching the provider.
// Only after exact ACKs drain it may another verified HTTP sample advance the
// retained watermarks. Retired owners continue using their original identities.
func (r *RuntimeReporting) StatsBatch(ctx context.Context) (*runtimeprovider.AccountingBatch, error) {
	r.lifetime.RLock()
	defer r.lifetime.RUnlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.available(); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, reportingError("cancelled")
	}
	pending, err := r.accounting.PendingBatch()
	if err != nil {
		return nil, reportingError("journal_unavailable")
	}
	if pending != nil {
		return pending, nil
	}
	binding, checkpoint, state, _, err := r.store.reportingSnapshot()
	if err != nil || state == nil {
		return nil, reportingError("ownership_unavailable")
	}
	observation, err := r.observe(ctx, binding, checkpoint.Keys)
	if err != nil {
		return nil, err
	}
	if err := validateReportingCounters(binding, checkpoint.Keys, state.Owners, observation, false); err != nil {
		return nil, err
	}
	batch, err := r.accounting.Sample(binding.PanelGUID, reportingCumulativeCounters(state.Owners, observation))
	if err != nil {
		return nil, reportingError("journal_write_failed")
	}
	return batch, nil
}

func (r *RuntimeReporting) AcknowledgeStats(reporterID string, sequence uint64) error {
	// Receipt persistence never waits behind a provider HTTP observation. The
	// lifetime lock only prevents Close; Accounting serializes its own state.
	r.lifetime.RLock()
	defer r.lifetime.RUnlock()
	if err := r.available(); err != nil {
		return err
	}
	if !chiralv1.ValidStatsIdentity(reporterID, sequence) {
		return reportingError("invalid_acknowledgement")
	}
	if err := r.accounting.Ack(reporterID + ":" + strconv.FormatUint(sequence, 10)); err != nil {
		return reportingError("acknowledgement_rejected")
	}
	return nil
}

// RecentOnline never reuses retired billing owners as authorized online users.
// The official cache supplies approximate observations; its HTTP read time is
// not a connection timestamp and does not produce a complete online snapshot.
func (r *RuntimeReporting) RecentOnline(ctx context.Context) (runtimeprovider.RecentOnlineSample, error) {
	r.lifetime.RLock()
	defer r.lifetime.RUnlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.available(); err != nil {
		return runtimeprovider.RecentOnlineSample{}, err
	}
	binding, checkpoint, state, _, err := r.store.reportingSnapshot()
	if err != nil || state == nil {
		return runtimeprovider.RecentOnlineSample{}, reportingError("ownership_unavailable")
	}
	observation, err := r.observe(ctx, binding, checkpoint.Keys)
	if err != nil {
		return runtimeprovider.RecentOnlineSample{}, err
	}
	if err := validateReportingCounters(binding, checkpoint.Keys, state.Owners, observation, false); err != nil {
		return runtimeprovider.RecentOnlineSample{}, err
	}
	emails := make([]string, 0, len(checkpoint.Keys.Clients))
	for _, owner := range state.Owners {
		if !owner.Retired {
			emails = append(emails, owner.Email)
		}
	}
	started := time.Now()
	users, err := r.client.ReadRecentOnline(ctx, binding.PanelGUID, emails)
	if err != nil {
		return runtimeprovider.RecentOnlineSample{}, err
	}
	return runtimeprovider.RecentOnlineSample{RetrievedAt: started, Users: users}, nil
}

func (r *RuntimeReporting) Close() error {
	if r == nil {
		return nil
	}
	r.lifetime.Lock()
	defer r.lifetime.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	r.store.releaseReporting()
	return nil
}

func (r *RuntimeReporting) available() error {
	if r == nil || r.closed || r.store == nil || r.accounting == nil {
		return reportingError("closed")
	}
	// Checking the owner state also detects a lost lock, deleted/replaced owner
	// file, or a poisoned persistence boundary before acknowledging a delivery.
	if _, _, state, _, err := r.store.reportingSnapshot(); err != nil || state == nil {
		return reportingError("ownership_unavailable")
	}
	if err := r.accounting.CheckDurableState(); err != nil {
		return reportingError("journal_unavailable")
	}
	return nil
}

type reportingObservation struct {
	inventory OwnedInventory
	inbounds  []Inbound
	global    []GlobalClient
	clients   map[string]ClientTraffic
}

func (r *RuntimeReporting) observe(ctx context.Context, binding OwnershipStoreBinding, keys OwnedKeys) (reportingObservation, error) {
	if ctx.Err() != nil {
		return reportingObservation{}, reportingError("cancelled")
	}
	scope, err := r.store.Scope()
	if err != nil {
		return reportingObservation{}, reportingError("ownership_unavailable")
	}
	before, err := r.client.Status(ctx)
	if err != nil || before.PanelGUID != binding.PanelGUID {
		return reportingObservation{}, reportingError("provider_identity_changed")
	}
	first, err := r.observeRound(ctx, scope, binding, keys)
	if err != nil {
		return reportingObservation{}, err
	}
	second, err := r.observeRound(ctx, scope, binding, keys)
	if err != nil {
		return reportingObservation{}, err
	}
	after, err := r.client.Status(ctx)
	if err != nil || after.PanelGUID != binding.PanelGUID || after.PanelVersion != before.PanelVersion ||
		!reflect.DeepEqual(first.inventory, second.inventory) ||
		observationInventoryDigest(first.inbounds, first.global) != observationInventoryDigest(second.inbounds, second.global) {
		return reportingObservation{}, reportingError("provider_drift")
	}
	// Counter values may advance between rounds, but local row identities may
	// not disappear, move, or be recycled during this read.
	if len(first.clients) != len(second.clients) {
		return reportingObservation{}, reportingError("counter_identity_changed")
	}
	for email, old := range first.clients {
		current, exists := second.clients[email]
		if !exists || old.ID != current.ID || old.InboundID != current.InboundID {
			return reportingObservation{}, reportingError("counter_identity_changed")
		}
	}
	return second, nil
}

func (r *RuntimeReporting) observeRound(ctx context.Context, scope *OwnershipScope, binding OwnershipStoreBinding, keys OwnedKeys) (reportingObservation, error) {
	inbounds, err := r.client.ListInbounds(ctx)
	if err != nil {
		return reportingObservation{}, reportingError("inventory_unavailable")
	}
	global, err := r.client.ListClients(ctx)
	if err != nil {
		return reportingObservation{}, reportingError("inventory_unavailable")
	}
	trafficRows := make([]inboundTrafficWire, 0, len(inbounds))
	for _, inbound := range inbounds {
		if _, err := decodeComparisonConfig(inbound.Raw, ""); err != nil {
			return reportingObservation{}, reportingError("inventory_ambiguous")
		}
		var row inboundTrafficWire
		if err := json.Unmarshal(inbound.Raw, &row); err != nil {
			return reportingObservation{}, reportingError("inventory_ambiguous")
		}
		trafficRows = append(trafficRows, row)
	}
	for _, client := range global {
		if _, err := decodeComparisonConfig(client.Raw, ""); err != nil {
			return reportingObservation{}, reportingError("inventory_ambiguous")
		}
	}
	inventory, err := scope.VerifyInventory(binding.PanelGUID, keys, inbounds, global)
	if err != nil {
		return reportingObservation{}, reportingError("ownership_conflict")
	}
	rows, _, err := decodeLocalInboundTrafficRows(trafficRows)
	if err != nil {
		return reportingObservation{}, reportingError("local_counters_unavailable")
	}
	observation := reportingObservation{inventory: inventory, inbounds: inbounds, global: global, clients: make(map[string]ClientTraffic, len(rows))}
	for _, row := range rows {
		observation.clients[row.Email] = row
	}
	return observation, nil
}

func validateReportingCounters(binding OwnershipStoreBinding, keys OwnedKeys, owners []TrafficOwnership, observation reportingObservation, allowNew bool) error {
	live := map[string]string{}
	for _, key := range keys.Clients {
		live[key.Email] = key.InboundTag
	}
	known := map[string]TrafficOwnership{}
	knownRows := map[int64]string{}
	ownedInboundIDs := map[int64]string{}
	for tag, id := range observation.inventory.InboundIDs {
		ownedInboundIDs[id] = tag
	}
	allInboundIDs := map[int64]Inbound{}
	allClientIDs := map[int64]GlobalClient{}
	allClientEmails := map[string]GlobalClient{}
	for _, inbound := range observation.inbounds {
		allInboundIDs[inbound.ID] = inbound
	}
	for _, client := range observation.global {
		allClientIDs[client.ID], allClientEmails[client.Email] = client, client
	}
	for _, owner := range owners {
		if owner.PanelGUID != binding.PanelGUID {
			return reportingError("provider_identity_changed")
		}
		known[owner.Email], knownRows[owner.StatID] = owner, owner.Email
		if tag, present := live[owner.Email]; present {
			if tag != owner.InboundTag || observation.inventory.InboundIDs[tag] != owner.InboundID || observation.inventory.ClientIDs[owner.Email] != owner.ClientID {
				return reportingError("live_binding_changed")
			}
			if _, exists := observation.clients[owner.Email]; !exists {
				return reportingError("live_counter_missing")
			}
		} else {
			if !owner.Retired {
				return reportingError("unretired_owner_missing")
			}
			// A tombstone is not authority to claim a newly created account that
			// happens to have the same email or database ID.
			if _, exists := allClientIDs[owner.ClientID]; exists {
				return reportingError("retired_client_reappeared")
			}
			if _, exists := allClientEmails[owner.Email]; exists {
				return reportingError("retired_client_reappeared")
			}
		}
		if inbound, exists := allInboundIDs[owner.InboundID]; exists {
			if inbound.Tag != owner.InboundTag || observation.inventory.InboundIDs[owner.InboundTag] != owner.InboundID {
				return reportingError("retired_inbound_changed")
			}
		} else if _, present := observation.clients[owner.Email]; present {
			return reportingError("counter_inbound_missing")
		}
		if row, present := observation.clients[owner.Email]; present && (row.ID != owner.StatID || row.InboundID != owner.InboundID) {
			return reportingError("counter_binding_changed")
		}
	}
	for email, row := range observation.clients {
		if row.Up > math.MaxInt64 || row.Down > math.MaxInt64-row.Up {
			return reportingError("invalid_local_counter")
		}
		if other, reused := knownRows[row.ID]; reused && other != email {
			return reportingError("counter_row_reused")
		}
		if _, owned := known[email]; owned {
			continue
		}
		if _, requested := live[email]; allowNew && requested {
			if row.InboundID != observation.inventory.InboundIDs[live[email]] {
				return reportingError("counter_binding_changed")
			}
			continue
		}
		if _, ownedInbound := ownedInboundIDs[row.InboundID]; ownedInbound {
			return reportingError("unknown_owned_counter")
		}
	}
	if !allowNew {
		for email := range live {
			if _, exists := known[email]; !exists {
				return reportingError("live_owner_missing")
			}
		}
	}
	return nil
}

func reportingCumulativeCounters(owners []TrafficOwnership, observation reportingObservation) []runtimeprovider.CumulativeCounter {
	counters := make([]runtimeprovider.CumulativeCounter, 0, len(owners))
	for _, owner := range owners {
		row, exists := observation.clients[owner.Email]
		if !exists {
			// Missing retired rows retain their watermark and tombstone. This
			// cannot prove settlement and must never trigger automatic cleanup.
			continue
		}
		counters = append(counters, runtimeprovider.CumulativeCounter{Key: "user>>>" + owner.Email, Up: row.Up, Down: row.Down})
	}
	return counters
}

func reportingError(reason string) error {
	return fmt.Errorf("3x-ui runtime reporting failed: %s", reason)
}
