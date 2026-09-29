package runtimeprovider

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

const (
	accountingVersion    = 1
	maxAccountingKeys    = 100000
	maxAccountingBytes   = 64 << 20
	maxAccountingKeySize = 1024
)

// BootstrapMode explicitly decides whether usage predating this journal is
// excluded or billed. Never silently choose CountFromZero when adopting a node.
type BootstrapMode uint8

const (
	BaselineOnly BootstrapMode = iota
	CountFromZero
)

// CumulativeCounter is a provider-owned, durable byte counter. Key must be a
// stable identity, not a display name that an operator can rename or reuse.
type CumulativeCounter struct {
	Key  string `json:"key"`
	Up   uint64 `json:"up"`
	Down uint64 `json:"down"`
}

type CounterDelta struct {
	Key   string `json:"key"`
	Up    uint64 `json:"up"`
	Down  uint64 `json:"down"`
	Reset bool   `json:"reset,omitempty"`
}

// AccountingBatch must be delivered with ReporterID and Sequence and retained
// until Core has durably acknowledged that exact pair. A successful gRPC Send
// is not an acknowledgement. ID is a convenience for Ack and local diagnostics.
type AccountingBatch struct {
	ID            string         `json:"id"`
	ReporterID    string         `json:"reporter_id"`
	Sequence      uint64         `json:"sequence"`
	Epoch         string         `json:"epoch"`
	Entries       []CounterDelta `json:"entries"`
	Indeterminate bool           `json:"indeterminate,omitempty"`
	Reason        string         `json:"reason,omitempty"`
}

type accountingCounter struct {
	Up    uint64 `json:"up"`
	Down  uint64 `json:"down"`
	Epoch uint64 `json:"epoch"`
}

type accountingState struct {
	Version          int                          `json:"version"`
	ProviderIdentity string                       `json:"provider_identity"`
	SnapshotEpoch    string                       `json:"snapshot_epoch"`
	ReporterID       string                       `json:"reporter_id"`
	Sequence         uint64                       `json:"sequence"`
	Initialized      bool                         `json:"initialized"`
	Counters         map[string]accountingCounter `json:"counters"`
	Pending          *AccountingBatch             `json:"pending,omitempty"`
	Queued           []*AccountingBatch           `json:"queued,omitempty"`
}

// Accounting is a crash-recoverable ordered batch journal. Its owner
// must hold the Agent state-directory lock; independent processes must not open
// the same journal concurrently. Removed keys are retained, never forgotten on
// an incomplete provider listing. Hitting the bounded retention limit fails
// closed instead of discarding a watermark and billing historical bytes again.
// Only Pending is exposed for delivery. Queued holds the remaining immutable
// fragments of the same snapshot; Sequence is the highest allocated sequence.
type Accounting struct {
	mu       sync.Mutex
	path     string
	identity string
	mode     BootstrapMode
	state    accountingState
	failure  error
	write    func(string, []byte) error
}

// OpenAccounting never resets a damaged journal. Corrupt or unsupported data
// requires explicit operator recovery. Provider identity changes do not discard
// a pending report: it is replayed first, then the next Sample takes a baseline
// and produces an Indeterminate event. The reporting sequence remains stable.
func OpenAccounting(path, providerIdentity string, mode BootstrapMode) (*Accounting, error) {
	if path == "" || !validAccountingName(providerIdentity) || (mode != BaselineOnly && mode != CountFromZero) {
		return nil, fmt.Errorf("invalid accounting journal configuration")
	}
	a := &Accounting{path: path, identity: providerIdentity, mode: mode, write: writeAccountingFile}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create accounting journal directory: %w", err)
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, fmt.Errorf("create accounting reporter identity: %w", err)
		}
		a.state = accountingState{Version: accountingVersion, ProviderIdentity: providerIdentity,
			ReporterID: hex.EncodeToString(random[:]), Counters: make(map[string]accountingCounter)}
		if err := a.persist(a.state); err != nil {
			return nil, err
		}
		return a, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect accounting journal: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxAccountingBytes {
		return nil, fmt.Errorf("accounting journal must be a restricted regular file within the size limit")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open accounting journal: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxAccountingBytes+1))
	if err != nil || len(raw) > maxAccountingBytes {
		return nil, fmt.Errorf("cannot read accounting journal within the size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&a.state); err != nil {
		return nil, fmt.Errorf("invalid accounting journal encoding")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("accounting journal contains trailing data")
	}
	if !validAccountingState(a.state) {
		return nil, fmt.Errorf("invalid accounting journal state")
	}
	return a, nil
}

// Sample returns the already-durable pending batch until Ack, ignoring newer
// snapshots until the entire queue is acknowledged. Otherwise it atomically
// persists new watermarks and every bounded fragment of their deltas before
// exposing the first batch. A first BaselineOnly sample or unchanged sample
// returns nil and allocates no delivery sequence.
//
// snapshotEpoch must change after a known provider restore/replacement, but not
// on an ordinary Agent or Xray restart. An epoch/identity change is ambiguous:
// the new values establish a baseline and an empty Indeterminate batch reports
// the gap. An undetectable restore under the same identity/epoch cannot be made
// lossless from cumulative counters alone.
//
// If either direction decreases, this key enters a new counter epoch; both
// current counters count from zero. This preserves observed post-reset bytes,
// but cannot recover bytes erased between polls by an external counter reset.
func (a *Accounting) Sample(snapshotEpoch string, counters []CumulativeCounter) (*AccountingBatch, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failure != nil {
		return nil, a.failure
	}
	if a.state.Pending != nil {
		return cloneAccountingBatch(a.state.Pending), nil
	}
	if !validAccountingName(snapshotEpoch) || len(counters) > maxAccountingKeys {
		return nil, fmt.Errorf("invalid accounting snapshot")
	}
	seen := make(map[string]bool, len(counters))
	for _, counter := range counters {
		if !validAccountingKey(counter.Key) || !validAccountingBytes(counter.Up, counter.Down) || seen[counter.Key] {
			return nil, fmt.Errorf("invalid or duplicate accounting counter")
		}
		seen[counter.Key] = true
	}
	state := cloneAccountingState(a.state)
	changedIdentity := state.ProviderIdentity != a.identity
	changedEpoch := state.Initialized && state.SnapshotEpoch != snapshotEpoch
	baseline := (!state.Initialized && a.mode == BaselineOnly) || changedIdentity || changedEpoch
	if changedIdentity || changedEpoch {
		// Values from different backing stores cannot share a watermark. Retain
		// the reporting identity/sequence so Core can still reject replayed IDs.
		state.Counters = make(map[string]accountingCounter)
	}
	entries := make([]CounterDelta, 0)
	for _, counter := range counters {
		last, exists := state.Counters[counter.Key]
		next := accountingCounter{Up: counter.Up, Down: counter.Down, Epoch: last.Epoch}
		reset := exists && (counter.Up < last.Up || counter.Down < last.Down)
		if reset && !baseline {
			if next.Epoch == math.MaxInt64 {
				return nil, fmt.Errorf("accounting counter epoch exhausted")
			}
			next.Epoch++
		}
		state.Counters[counter.Key] = next
		if baseline {
			continue
		}
		up, down := counter.Up, counter.Down
		if exists && !reset {
			up, down = counter.Up-last.Up, counter.Down-last.Down
		}
		if up != 0 || down != 0 {
			entries = append(entries, CounterDelta{Key: counter.Key, Up: up, Down: down, Reset: reset})
		}
	}
	if len(state.Counters) > maxAccountingKeys {
		return nil, fmt.Errorf("accounting watermark retention limit reached")
	}
	state.ProviderIdentity, state.SnapshotEpoch, state.Initialized = a.identity, snapshotEpoch, true
	if len(entries) != 0 || changedIdentity || changedEpoch {
		fragmentCount := (len(entries) + chiralv1.MaxStatsBatchEntries - 1) / chiralv1.MaxStatsBatchEntries
		if fragmentCount == 0 {
			fragmentCount = 1 // An identity change still needs its empty gap report.
		}
		if uint64(fragmentCount) > math.MaxInt64-state.Sequence {
			return nil, fmt.Errorf("accounting delivery sequence exhausted")
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
		for i := 0; i < fragmentCount; i++ {
			begin := i * chiralv1.MaxStatsBatchEntries
			end := min(begin+chiralv1.MaxStatsBatchEntries, len(entries))
			state.Sequence++
			batch := &AccountingBatch{ID: accountingBatchID(state.ReporterID, state.Sequence),
				ReporterID: state.ReporterID, Sequence: state.Sequence, Epoch: snapshotEpoch, Entries: entries[begin:end],
				Indeterminate: changedIdentity || changedEpoch}
			if batch.Indeterminate {
				batch.Reason = "provider identity or snapshot epoch changed; new baseline excludes unverified historical usage"
			}
			if i == 0 {
				state.Pending = batch
			} else {
				state.Queued = append(state.Queued, batch)
			}
		}
	}
	if err := a.persist(state); err != nil {
		return nil, err
	}
	return cloneAccountingBatch(a.state.Pending), nil
}

// Ack clears only the exact pending batch, after Core's durable receipt, and
// atomically exposes the next already-persisted fragment. A duplicate
// acknowledgement of the most recently acknowledged ID is harmless.
func (a *Accounting) Ack(deliveryID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failure != nil {
		return a.failure
	}
	if a.state.Pending == nil {
		if a.state.Sequence != 0 && deliveryID == accountingBatchID(a.state.ReporterID, a.state.Sequence) {
			return nil
		}
		return fmt.Errorf("accounting acknowledgement does not match the pending delivery")
	}
	if sequence := a.state.Pending.Sequence; sequence > 1 && deliveryID == accountingBatchID(a.state.ReporterID, sequence-1) {
		return nil
	}
	if deliveryID != a.state.Pending.ID {
		return fmt.Errorf("accounting acknowledgement does not match the pending delivery")
	}
	state := cloneAccountingState(a.state)
	state.Pending = nil
	if len(state.Queued) > 0 {
		state.Pending = state.Queued[0]
		state.Queued = state.Queued[1:]
	}
	return a.persist(state)
}

func (a *Accounting) persist(state accountingState) error {
	raw, err := json.Marshal(state)
	if err == nil && len(raw) > maxAccountingBytes {
		err = fmt.Errorf("accounting journal exceeds the size limit")
	}
	if err == nil {
		err = a.write(a.path, raw)
	}
	if err != nil {
		// Rename may have succeeded before a directory-sync failure. Never
		// continue with an uncertain disk/memory boundary and reuse the same
		// delivery ID for different bytes; require reopening the journal.
		a.failure = fmt.Errorf("accounting journal write failed; reopen required: %w", err)
		return a.failure
	}
	a.state = state
	return nil
}

func writeAccountingFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".accounting-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	defer f.Close()
	if _, err := f.Write(raw); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func validAccountingState(state accountingState) bool {
	if state.Version != accountingVersion || !chiralv1.ValidStatsIdentity(state.ReporterID, 1) ||
		!validAccountingName(state.ProviderIdentity) || state.Sequence > math.MaxInt64 || state.Counters == nil || len(state.Counters) > maxAccountingKeys {
		return false
	}
	if state.Initialized && !validAccountingName(state.SnapshotEpoch) {
		return false
	}
	if !state.Initialized && (state.Sequence != 0 || len(state.Counters) != 0 || state.Pending != nil || len(state.Queued) != 0 || state.SnapshotEpoch != "") {
		return false
	}
	for key, counter := range state.Counters {
		if !validAccountingKey(key) || !validAccountingBytes(counter.Up, counter.Down) || counter.Epoch > math.MaxInt64 {
			return false
		}
	}
	if state.Pending == nil {
		return len(state.Queued) == 0
	}
	maxFragments := (maxAccountingKeys + chiralv1.MaxStatsBatchEntries - 1) / chiralv1.MaxStatsBatchEntries
	if len(state.Queued) >= maxFragments || uint64(len(state.Queued)+1) > state.Sequence {
		return false
	}
	batches := append([]*AccountingBatch{state.Pending}, state.Queued...)
	firstSequence := state.Sequence - uint64(len(state.Queued))
	seen := make(map[string]bool)
	for i, pending := range batches {
		sequence := firstSequence + uint64(i)
		if pending == nil || pending.Sequence != sequence || pending.ReporterID != state.ReporterID ||
			pending.ID != accountingBatchID(state.ReporterID, sequence) || pending.Epoch != state.SnapshotEpoch ||
			len(pending.Entries) > chiralv1.MaxStatsBatchEntries || (!pending.Indeterminate && len(pending.Entries) == 0) || len(pending.Reason) > 256 {
			return false
		}
		if pending.Indeterminate && (len(batches) != 1 || len(pending.Entries) != 0 || pending.Reason == "") {
			return false
		}
		for _, delta := range pending.Entries {
			counter, exists := state.Counters[delta.Key]
			if !exists || seen[delta.Key] || !validAccountingBytes(delta.Up, delta.Down) ||
				(delta.Up == 0 && delta.Down == 0) || delta.Up > counter.Up || delta.Down > counter.Down {
				return false
			}
			seen[delta.Key] = true
		}
	}
	return true
}

func validAccountingName(name string) bool {
	if name == "" || len(name) > maxAccountingKeySize || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validAccountingKey(key string) bool {
	if strings.Contains(key, ">>>") {
		_, _, ok := chiralv1.SplitStatsKey(key)
		return ok
	}
	return chiralv1.ValidStatsName(key)
}

func validAccountingBytes(up, down uint64) bool {
	return up <= math.MaxInt64 && down <= math.MaxInt64 && up <= math.MaxInt64-down
}

func accountingBatchID(reporterID string, sequence uint64) string {
	return fmt.Sprintf("%s:%d", reporterID, sequence)
}

func cloneAccountingBatch(batch *AccountingBatch) *AccountingBatch {
	if batch == nil {
		return nil
	}
	copy := *batch
	copy.Entries = append([]CounterDelta(nil), batch.Entries...)
	return &copy
}

func cloneAccountingState(state accountingState) accountingState {
	copy := state
	copy.Counters = make(map[string]accountingCounter, len(state.Counters))
	for key, counter := range state.Counters {
		copy.Counters[key] = counter
	}
	copy.Pending = cloneAccountingBatch(state.Pending)
	if state.Queued != nil {
		copy.Queued = make([]*AccountingBatch, len(state.Queued))
		for i, batch := range state.Queued {
			copy.Queued[i] = cloneAccountingBatch(batch)
		}
	}
	return copy
}
