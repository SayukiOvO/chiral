package threexui

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
)

const (
	ownershipStoreSchema   = 1
	ownershipStoreFilename = "ownership-state.json"
	maxOwnershipHistory    = 128
	maxOwnershipKeys       = 100_000
	maxOwnershipStateBytes = maxConfigComparisonBytes
)

// OwnershipStoreBinding pins private ownership state to one already verified
// 3x-ui endpoint and panel identity. EndpointHash is a lowercase SHA-256
// digest; no endpoint URL is stored in this file.
type OwnershipStoreBinding struct {
	EndpointHash string
	PanelGUID    string
}

// OwnershipCheckpoint is durable reconciliation evidence. It records only
// ownership keys and canonical template/configuration digests. It is not proof
// that Xray accepted the configuration or that the data path works.
type OwnershipCheckpoint struct {
	Keys           OwnedKeys
	TemplateDigest string
	ConfigDigest   string
}

// OwnershipIntent is the single durable write barrier. OperationIDHash never
// contains the caller's raw operation identifier. After is the exact readback
// checkpoint required before the intent may be completed.
type OwnershipIntent struct {
	OperationIDHash    string
	RequestHash        string
	ExpectedGeneration uint64
	After              OwnershipCheckpoint
}

type ownershipHistoryRecord struct {
	OperationIDHash string
	RequestHash     string
	AfterDigest     string
	Generation      uint64
}

type ownershipStoreState struct {
	binding    OwnershipStoreBinding
	generation uint64
	ownerID    string
	macKey     [32]byte
	checkpoint OwnershipCheckpoint
	pending    *OwnershipIntent
	history    []ownershipHistoryRecord
	reporting  *ownershipReportingState
}

// OwnershipStore persists one candidate provider's ownership checkpoint and
// at most one unresolved intent. Its lifetime lock serializes only users of
// this component and directory; it does not exclude the update journal,
// external 3x-ui writers, or any future provider implementation.
type OwnershipStore struct {
	mu              sync.Mutex
	dir             *os.File
	path            string
	identity        ownershipFileIdentity
	fileHash        string
	state           ownershipStoreState
	writeFile       func(*os.File, ownershipFileIdentity, string, []byte) (ownershipFileIdentity, error)
	poisoned        bool
	closed          bool
	reportingActive bool
}

type ownershipFileIdentity struct {
	Device uint64
	Inode  uint64
	Valid  bool
}

type ownershipBindingDisk struct {
	EndpointHash string `json:"endpoint_hash"`
	PanelGUID    string `json:"panel_guid"`
}

type ownershipClientKeyDisk struct {
	Email      string `json:"email"`
	InboundTag string `json:"inbound_tag"`
}

type ownershipKeysDisk struct {
	InboundTags []string                 `json:"inbound_tags"`
	Clients     []ownershipClientKeyDisk `json:"clients"`
}

type ownershipCheckpointDisk struct {
	Keys           ownershipKeysDisk `json:"keys"`
	TemplateDigest string            `json:"template_digest"`
	ConfigDigest   string            `json:"config_digest"`
}

type ownershipIntentDisk struct {
	OperationIDHash    string                  `json:"operation_id_hash"`
	RequestHash        string                  `json:"request_hash"`
	ExpectedGeneration uint64                  `json:"expected_generation"`
	After              ownershipCheckpointDisk `json:"after"`
}

type ownershipHistoryDisk struct {
	OperationIDHash string `json:"operation_id_hash"`
	RequestHash     string `json:"request_hash"`
	AfterDigest     string `json:"after_digest"`
	Generation      uint64 `json:"generation"`
}

type ownershipStateDisk struct {
	Schema     int                      `json:"schema"`
	Binding    ownershipBindingDisk     `json:"binding"`
	Generation uint64                   `json:"generation"`
	OwnerID    string                   `json:"owner_id"`
	MACKey     string                   `json:"mac_key"`
	Checkpoint ownershipCheckpointDisk  `json:"checkpoint"`
	Pending    *ownershipIntentDisk     `json:"pending,omitempty"`
	History    []ownershipHistoryDisk   `json:"history"`
	Reporting  *ownershipReportingState `json:"reporting,omitempty"`
}

// CreateOwnershipStore creates the first private state file. Initial ownership
// must be empty: adopting existing provider rows requires an explicit later
// reconciliation workflow. A missing state file is never recreated by Open.
func CreateOwnershipStore(dir string, binding OwnershipStoreBinding, initial OwnershipCheckpoint) (*OwnershipStore, error) {
	if !validOwnershipBinding(binding) {
		return nil, ownershipStoreError("invalid_binding")
	}
	checkpoint, err := normalizeOwnershipCheckpoint(initial)
	if err != nil || len(checkpoint.Keys.InboundTags) != 0 || len(checkpoint.Keys.Clients) != 0 {
		return nil, ownershipStoreError("invalid_initial_checkpoint")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, ownershipStoreError("directory_unavailable")
	}
	directory, err := openOwnershipStoreDirectory(absolute, true)
	if err != nil {
		return nil, ownershipStoreError("directory_unavailable")
	}
	store := &OwnershipStore{dir: directory, path: absolute, writeFile: writeOwnershipStateFile}
	store.state.binding = binding
	store.state.checkpoint = checkpoint
	store.state.history = make([]ownershipHistoryRecord, 0)
	owner := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, owner); err != nil {
		_ = store.Close()
		return nil, ownershipStoreError("randomness_unavailable")
	}
	store.state.ownerID = hex.EncodeToString(owner)
	if _, err := io.ReadFull(rand.Reader, store.state.macKey[:]); err != nil || store.state.macKey == [32]byte{} {
		_ = store.Close()
		return nil, ownershipStoreError("randomness_unavailable")
	}
	encoded, err := encodeOwnershipState(store.state)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	identity, err := store.writeFile(store.dir, ownershipFileIdentity{}, "", encoded)
	if err != nil {
		_ = store.Close()
		return nil, ownershipStoreError("persistence_failed")
	}
	store.identity = identity
	store.fileHash = updateDigest(string(encoded))
	return store, nil
}

// OpenOwnershipStore opens existing state and holds its directory lock until
// Close. Missing, corrupt, internally duplicated receipt, or differently bound
// state fails closed. This cannot identify restoration of an entire older,
// internally valid directory backup between process lifetimes.
func OpenOwnershipStore(dir string, binding OwnershipStoreBinding) (*OwnershipStore, error) {
	if !validOwnershipBinding(binding) {
		return nil, ownershipStoreError("invalid_binding")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, ownershipStoreError("directory_unavailable")
	}
	directory, err := openOwnershipStoreDirectory(absolute, false)
	if err != nil {
		return nil, ownershipStoreError("directory_unavailable")
	}
	raw, identity, err := readOwnershipStateFile(directory)
	if err != nil {
		_ = directory.Close()
		return nil, ownershipStoreError("state_unavailable")
	}
	state, err := decodeOwnershipState(raw)
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	if state.binding != binding {
		zeroOwnershipState(&state)
		_ = directory.Close()
		return nil, ownershipStoreError("binding_changed")
	}
	return &OwnershipStore{dir: directory, path: absolute, identity: identity, fileHash: updateDigest(string(raw)), state: state, writeFile: writeOwnershipStateFile}, nil
}

// Binding returns a value copy suitable for binding an explicit candidate
// readback to this store. It does not authorize a provider mutation.
func (s *OwnershipStore) Binding() (OwnershipStoreBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.availableLocked(); err != nil {
		return OwnershipStoreBinding{}, err
	}
	return s.state.binding, nil
}

// Scope returns an independent secret-bearing marker scope. Callers must never
// log or format the returned value.
func (s *OwnershipStore) Scope() (*OwnershipScope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.availableLocked(); err != nil {
		return nil, err
	}
	return NewOwnershipScope(s.state.binding.PanelGUID, s.state.ownerID, s.state.macKey[:])
}

// Checkpoint is available while an intent is pending for read-only diagnosis.
func (s *OwnershipStore) Checkpoint() (OwnershipCheckpoint, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.availableLocked(); err != nil {
		return OwnershipCheckpoint{}, 0, err
	}
	return cloneOwnershipCheckpoint(s.state.checkpoint), s.state.generation, nil
}

// Pending returns an independent copy. A nil result means no write barrier is
// retained. No API exists to clear, retry, force, or compensate an intent.
func (s *OwnershipStore) Pending() (*OwnershipIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.availableLocked(); err != nil {
		return nil, err
	}
	return cloneOwnershipIntent(s.state.pending), nil
}

// Prepare durably records the sole pending intent before any external write.
// Every unresolved intent blocks every subsequent Prepare, including replay of
// the same operation. requestHash must be the caller's canonical request hash.
func (s *OwnershipStore) Prepare(operationID string, expectedGeneration uint64, requestHash string, after OwnershipCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.availableLocked(); err != nil {
		return err
	}
	if s.state.pending != nil {
		return ownershipStoreError("pending_intent")
	}
	if expectedGeneration != s.state.generation {
		return ownershipStoreError("generation_changed")
	}
	if len(s.state.history) >= maxOwnershipHistory {
		return ownershipStoreError("history_full")
	}
	if !validOwnershipKey(operationID) || !ownershipHex(requestHash, 32) {
		return ownershipStoreError("invalid_intent")
	}
	checkpoint, err := normalizeOwnershipCheckpoint(after)
	if err != nil {
		return err
	}
	operationHash := updateDigest(operationID)
	for _, record := range s.state.history {
		if record.OperationIDHash == operationHash {
			return ownershipStoreError("operation_replayed")
		}
	}
	next := cloneOwnershipState(s.state)
	next.pending = &OwnershipIntent{OperationIDHash: operationHash, RequestHash: requestHash,
		ExpectedGeneration: expectedGeneration, After: checkpoint}
	return s.persistLocked(next)
}

// Complete advances the checkpoint only after the caller confirms it received
// a complete successful HTTP envelope and supplies an exact readback. The
// acknowledged bit is caller-provided HTTP terminal evidence only: it does not
// prove runtime adoption, data-path health, authorization, or a global
// transaction. A missing response always preserves the pending write barrier,
// even when a later read happens to equal After.
func (s *OwnershipStore) Complete(operationID string, acknowledged bool, observed OwnershipCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.availableLocked(); err != nil {
		return err
	}
	if s.state.pending == nil {
		return ownershipStoreError("pending_missing")
	}
	if !validOwnershipKey(operationID) || updateDigest(operationID) != s.state.pending.OperationIDHash ||
		s.state.pending.ExpectedGeneration != s.state.generation {
		return ownershipStoreError("intent_changed")
	}
	if !acknowledged {
		return ownershipStoreError("acknowledgement_missing")
	}
	checkpoint, err := normalizeOwnershipCheckpoint(observed)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(checkpoint, s.state.pending.After) {
		return ownershipStoreError("readback_changed")
	}
	if len(s.state.history) >= maxOwnershipHistory {
		return ownershipStoreError("history_full")
	}
	next := cloneOwnershipState(s.state)
	next.generation++
	next.checkpoint = checkpoint
	next.history = append(next.history, ownershipHistoryRecord{
		OperationIDHash: next.pending.OperationIDHash,
		RequestHash:     next.pending.RequestHash,
		AfterDigest:     ownershipCheckpointDigest(checkpoint),
		Generation:      next.generation,
	})
	next.pending = nil
	return s.persistLocked(next)
}

// Close is idempotent. It erases the in-memory MAC key and releases the
// lifetime directory lock. A closed store cannot be reopened in place.
func (s *OwnershipStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	zeroOwnershipState(&s.state)
	directory := s.dir
	s.dir = nil
	if directory != nil && directory.Close() != nil {
		return ownershipStoreError("close_failed")
	}
	return nil
}

func (s *OwnershipStore) availableLocked() error {
	if s == nil || s.closed {
		return ownershipStoreError("closed")
	}
	if s.poisoned {
		return ownershipStoreError("poisoned")
	}
	if s.dir == nil || !s.identity.Valid || !ownershipHex(s.fileHash, 32) || s.writeFile == nil {
		s.poisoned = true
		return ownershipStoreError("uninitialized")
	}
	if err := verifyOwnershipStateSnapshot(s.dir, s.identity, s.fileHash); err != nil {
		s.poisoned = true
		return ownershipStoreError("state_changed")
	}
	return nil
}

func (s *OwnershipStore) persistLocked(next ownershipStoreState) error {
	encoded, err := encodeOwnershipState(next)
	if err != nil {
		return err
	}
	identity, err := s.writeFile(s.dir, s.identity, s.fileHash, encoded)
	if err != nil {
		s.poisoned = true
		return ownershipStoreError("persistence_failed")
	}
	s.identity = identity
	s.fileHash = updateDigest(string(encoded))
	s.state = next
	return nil
}

func validOwnershipBinding(binding OwnershipStoreBinding) bool {
	return ownershipHex(binding.EndpointHash, 32) && validOwnershipKey(binding.PanelGUID)
}

func normalizeOwnershipCheckpoint(checkpoint OwnershipCheckpoint) (OwnershipCheckpoint, error) {
	if !ownershipHex(checkpoint.TemplateDigest, 32) || !ownershipHex(checkpoint.ConfigDigest, 32) ||
		len(checkpoint.Keys.InboundTags) > maxOwnershipKeys || len(checkpoint.Keys.Clients) > maxOwnershipKeys {
		return OwnershipCheckpoint{}, ownershipStoreError("invalid_checkpoint")
	}
	tags, err := ownershipKeySet(checkpoint.Keys.InboundTags)
	if err != nil {
		return OwnershipCheckpoint{}, ownershipStoreError("invalid_checkpoint")
	}
	normalized := OwnershipCheckpoint{TemplateDigest: checkpoint.TemplateDigest, ConfigDigest: checkpoint.ConfigDigest}
	normalized.Keys.InboundTags = make([]string, 0, len(tags))
	for tag := range tags {
		normalized.Keys.InboundTags = append(normalized.Keys.InboundTags, tag)
	}
	sort.Strings(normalized.Keys.InboundTags)
	seenClients := make(map[string]bool, len(checkpoint.Keys.Clients))
	normalized.Keys.Clients = make([]OwnedClientKey, 0, len(checkpoint.Keys.Clients))
	for _, client := range checkpoint.Keys.Clients {
		if !validOwnershipKey(client.Email) || validClientEmail(client.Email) != nil || !tags[client.InboundTag] || seenClients[client.Email] {
			return OwnershipCheckpoint{}, ownershipStoreError("invalid_checkpoint")
		}
		seenClients[client.Email] = true
		normalized.Keys.Clients = append(normalized.Keys.Clients, client)
	}
	sort.Slice(normalized.Keys.Clients, func(i, j int) bool {
		if normalized.Keys.Clients[i].Email == normalized.Keys.Clients[j].Email {
			return normalized.Keys.Clients[i].InboundTag < normalized.Keys.Clients[j].InboundTag
		}
		return normalized.Keys.Clients[i].Email < normalized.Keys.Clients[j].Email
	})
	return normalized, nil
}

func cloneOwnershipCheckpoint(checkpoint OwnershipCheckpoint) OwnershipCheckpoint {
	return OwnershipCheckpoint{
		Keys: OwnedKeys{
			InboundTags: append([]string{}, checkpoint.Keys.InboundTags...),
			Clients:     append([]OwnedClientKey{}, checkpoint.Keys.Clients...),
		},
		TemplateDigest: checkpoint.TemplateDigest,
		ConfigDigest:   checkpoint.ConfigDigest,
	}
}

func cloneOwnershipIntent(intent *OwnershipIntent) *OwnershipIntent {
	if intent == nil {
		return nil
	}
	return &OwnershipIntent{OperationIDHash: intent.OperationIDHash, RequestHash: intent.RequestHash,
		ExpectedGeneration: intent.ExpectedGeneration, After: cloneOwnershipCheckpoint(intent.After)}
}

func cloneOwnershipState(state ownershipStoreState) ownershipStoreState {
	result := state
	result.checkpoint = cloneOwnershipCheckpoint(state.checkpoint)
	result.pending = cloneOwnershipIntent(state.pending)
	result.history = append([]ownershipHistoryRecord{}, state.history...)
	result.reporting = cloneOwnershipReporting(state.reporting)
	return result
}

func ownershipCheckpointDigest(checkpoint OwnershipCheckpoint) string {
	data, _ := json.Marshal(checkpointToDisk(checkpoint))
	return updateDigest(string(data))
}

func encodeOwnershipState(state ownershipStoreState) ([]byte, error) {
	if err := validateOwnershipState(state); err != nil {
		return nil, err
	}
	disk := stateToDisk(state)
	data, err := json.Marshal(disk)
	if err != nil || len(data) > maxOwnershipStateBytes {
		return nil, ownershipStoreError("state_too_large")
	}
	return data, nil
}

func decodeOwnershipState(data []byte) (ownershipStoreState, error) {
	if len(data) == 0 || len(data) > maxOwnershipStateBytes {
		return ownershipStoreState{}, ownershipStoreError("state_invalid")
	}
	if _, err := decodeComparisonConfig(data, "expected"); err != nil {
		return ownershipStoreState{}, ownershipStoreError("state_invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var disk ownershipStateDisk
	if err := decoder.Decode(&disk); err != nil {
		return ownershipStoreState{}, ownershipStoreError("state_invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ownershipStoreState{}, ownershipStoreError("state_invalid")
	}
	state, err := stateFromDisk(disk)
	if err != nil {
		return ownershipStoreState{}, err
	}
	if err := validateOwnershipState(state); err != nil {
		zeroOwnershipState(&state)
		return ownershipStoreState{}, err
	}
	// Compare the parsed state to a complete canonical encoding. This rejects
	// case aliases and missing zero-valued fields while allowing insignificant
	// whitespace and object-key order.
	canonical, marshalErr := json.Marshal(stateToDisk(state))
	comparison, compareErr := CompareConfig(data, canonical)
	if marshalErr != nil || compareErr != nil || !comparison.Equivalent {
		zeroOwnershipState(&state)
		return ownershipStoreState{}, ownershipStoreError("state_noncanonical")
	}
	return state, nil
}

func validateOwnershipState(state ownershipStoreState) error {
	if !validOwnershipBinding(state.binding) || !ownershipHex(state.ownerID, 16) || state.macKey == [32]byte{} ||
		len(state.history) > maxOwnershipHistory || uint64(len(state.history)) != state.generation {
		return ownershipStoreError("state_invalid")
	}
	checkpoint, err := normalizeOwnershipCheckpoint(state.checkpoint)
	if err != nil || !reflect.DeepEqual(checkpoint, state.checkpoint) {
		return ownershipStoreError("state_invalid")
	}
	seen := make(map[string]bool, len(state.history))
	for index, record := range state.history {
		if !ownershipHex(record.OperationIDHash, 32) || !ownershipHex(record.RequestHash, 32) ||
			!ownershipHex(record.AfterDigest, 32) || record.Generation != uint64(index+1) || seen[record.OperationIDHash] {
			return ownershipStoreError("state_invalid")
		}
		seen[record.OperationIDHash] = true
	}
	if len(state.history) > 0 && state.history[len(state.history)-1].AfterDigest != ownershipCheckpointDigest(state.checkpoint) {
		return ownershipStoreError("state_invalid")
	}
	if state.pending != nil {
		after, err := normalizeOwnershipCheckpoint(state.pending.After)
		if err != nil || !reflect.DeepEqual(after, state.pending.After) || !ownershipHex(state.pending.OperationIDHash, 32) ||
			!ownershipHex(state.pending.RequestHash, 32) || state.pending.ExpectedGeneration != state.generation || seen[state.pending.OperationIDHash] {
			return ownershipStoreError("state_invalid")
		}
	}
	if err := validateOwnershipReporting(state.binding, state.reporting); err != nil {
		return err
	}
	return nil
}

func stateToDisk(state ownershipStoreState) ownershipStateDisk {
	disk := ownershipStateDisk{
		Schema:     ownershipStoreSchema,
		Binding:    ownershipBindingDisk{EndpointHash: state.binding.EndpointHash, PanelGUID: state.binding.PanelGUID},
		Generation: state.generation,
		OwnerID:    state.ownerID,
		MACKey:     hex.EncodeToString(state.macKey[:]),
		Checkpoint: checkpointToDisk(state.checkpoint),
		History:    make([]ownershipHistoryDisk, 0, len(state.history)),
		Reporting:  cloneOwnershipReporting(state.reporting),
	}
	if state.pending != nil {
		disk.Pending = &ownershipIntentDisk{OperationIDHash: state.pending.OperationIDHash, RequestHash: state.pending.RequestHash,
			ExpectedGeneration: state.pending.ExpectedGeneration, After: checkpointToDisk(state.pending.After)}
	}
	for _, record := range state.history {
		disk.History = append(disk.History, ownershipHistoryDisk(record))
	}
	return disk
}

func stateFromDisk(disk ownershipStateDisk) (ownershipStoreState, error) {
	if disk.Schema != ownershipStoreSchema || !ownershipHex(disk.MACKey, 32) {
		return ownershipStoreState{}, ownershipStoreError("state_invalid")
	}
	key, _ := hex.DecodeString(disk.MACKey)
	state := ownershipStoreState{
		binding:    OwnershipStoreBinding{EndpointHash: disk.Binding.EndpointHash, PanelGUID: disk.Binding.PanelGUID},
		generation: disk.Generation,
		ownerID:    disk.OwnerID,
		checkpoint: checkpointFromDisk(disk.Checkpoint),
		history:    make([]ownershipHistoryRecord, 0, len(disk.History)),
		reporting:  cloneOwnershipReporting(disk.Reporting),
	}
	copy(state.macKey[:], key)
	if disk.Pending != nil {
		state.pending = &OwnershipIntent{OperationIDHash: disk.Pending.OperationIDHash, RequestHash: disk.Pending.RequestHash,
			ExpectedGeneration: disk.Pending.ExpectedGeneration, After: checkpointFromDisk(disk.Pending.After)}
	}
	for _, record := range disk.History {
		state.history = append(state.history, ownershipHistoryRecord(record))
	}
	return state, nil
}

func checkpointToDisk(checkpoint OwnershipCheckpoint) ownershipCheckpointDisk {
	keys := ownershipKeysDisk{
		InboundTags: append([]string{}, checkpoint.Keys.InboundTags...),
		Clients:     make([]ownershipClientKeyDisk, 0, len(checkpoint.Keys.Clients)),
	}
	for _, client := range checkpoint.Keys.Clients {
		keys.Clients = append(keys.Clients, ownershipClientKeyDisk{Email: client.Email, InboundTag: client.InboundTag})
	}
	return ownershipCheckpointDisk{Keys: keys, TemplateDigest: checkpoint.TemplateDigest, ConfigDigest: checkpoint.ConfigDigest}
}

func checkpointFromDisk(checkpoint ownershipCheckpointDisk) OwnershipCheckpoint {
	keys := OwnedKeys{
		InboundTags: append([]string{}, checkpoint.Keys.InboundTags...),
		Clients:     make([]OwnedClientKey, 0, len(checkpoint.Keys.Clients)),
	}
	for _, client := range checkpoint.Keys.Clients {
		keys.Clients = append(keys.Clients, OwnedClientKey{Email: client.Email, InboundTag: client.InboundTag})
	}
	return OwnershipCheckpoint{Keys: keys, TemplateDigest: checkpoint.TemplateDigest, ConfigDigest: checkpoint.ConfigDigest}
}

func zeroOwnershipState(state *ownershipStoreState) {
	if state == nil {
		return
	}
	for index := range state.macKey {
		state.macKey[index] = 0
	}
	state.ownerID = ""
	state.pending = nil
	state.history = nil
	state.checkpoint = OwnershipCheckpoint{}
	state.reporting = nil
}

func ownershipStoreError(reason string) error {
	return fmt.Errorf("3x-ui ownership state failed: %s", reason)
}
