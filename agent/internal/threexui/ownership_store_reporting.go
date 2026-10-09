package threexui

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
)

const runtimeAccountingFilename = "accounting.json"

// TrafficOwnership retains the original local HTTP row identities after an
// account is deleted. It contains no proxy secret and never authorizes access.
// An email or numeric row ID cannot be adopted again under a different binding.
type TrafficOwnership struct {
	PanelGUID  string `json:"panel_guid"`
	InboundTag string `json:"inbound_tag"`
	InboundID  int64  `json:"inbound_id"`
	ClientID   int64  `json:"client_id"`
	StatID     int64  `json:"stat_id"`
	Email      string `json:"email"`
	Retired    bool   `json:"retired"`
}

// This optional extension leaves pre-reporting ownership files byte-for-byte
// canonical under schema 1. Its absence permits reading an old file, but never
// automatically creates an accounting journal for previously managed objects.
type ownershipReportingState struct {
	Version int                `json:"version"`
	Owners  []TrafficOwnership `json:"owners"`
}

func cloneOwnershipReporting(state *ownershipReportingState) *ownershipReportingState {
	if state == nil {
		return nil
	}
	return &ownershipReportingState{Version: state.Version, Owners: append([]TrafficOwnership{}, state.Owners...)}
}

func validateOwnershipReporting(binding OwnershipStoreBinding, state *ownershipReportingState) error {
	if state == nil {
		return nil
	}
	if state.Version != 1 || state.Owners == nil || len(state.Owners) > maxOwnershipKeys {
		return ownershipStoreError("reporting_state_invalid")
	}
	emails, rows, clients := map[string]bool{}, map[int64]bool{}, map[int64]bool{}
	inboundTags, inboundIDs := map[string]int64{}, map[int64]string{}
	previous := ""
	for _, owner := range state.Owners {
		if owner.PanelGUID != binding.PanelGUID || !validOwnershipKey(owner.InboundTag) || validClientEmail(owner.Email) != nil ||
			!validOwnershipKey(owner.Email) || owner.InboundID <= 0 || owner.ClientID <= 0 || owner.StatID <= 0 ||
			emails[owner.Email] || rows[owner.StatID] || clients[owner.ClientID] || (previous != "" && owner.Email <= previous) {
			return ownershipStoreError("reporting_state_invalid")
		}
		if id, exists := inboundTags[owner.InboundTag]; exists && id != owner.InboundID {
			return ownershipStoreError("reporting_state_invalid")
		}
		if tag, exists := inboundIDs[owner.InboundID]; exists && tag != owner.InboundTag {
			return ownershipStoreError("reporting_state_invalid")
		}
		emails[owner.Email], rows[owner.StatID], clients[owner.ClientID] = true, true, true
		inboundTags[owner.InboundTag], inboundIDs[owner.InboundID], previous = owner.InboundID, owner.InboundTag, owner.Email
	}
	return nil
}

func (s *OwnershipStore) reportingSnapshot() (OwnershipStoreBinding, OwnershipCheckpoint, *ownershipReportingState, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.availableLocked(); err != nil {
		return OwnershipStoreBinding{}, OwnershipCheckpoint{}, nil, "", err
	}
	if s.path == "" {
		return OwnershipStoreBinding{}, OwnershipCheckpoint{}, nil, "", ownershipStoreError("reporting_directory_missing")
	}
	pathInfo, pathErr := os.Lstat(s.path)
	openedInfo, openedErr := s.dir.Stat()
	if pathErr != nil || openedErr != nil || !pathInfo.IsDir() || !os.SameFile(pathInfo, openedInfo) {
		return OwnershipStoreBinding{}, OwnershipCheckpoint{}, nil, "", ownershipStoreError("reporting_directory_changed")
	}
	return s.state.binding, cloneOwnershipCheckpoint(s.state.checkpoint), cloneOwnershipReporting(s.state.reporting),
		filepath.Join(s.path, runtimeAccountingFilename), nil
}

func (s *OwnershipStore) claimReporting() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.availableLocked(); err != nil {
		return err
	}
	if s.reportingActive {
		return ownershipStoreError("reporting_already_open")
	}
	s.reportingActive = true
	return nil
}

func (s *OwnershipStore) releaseReporting() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reportingActive = false
}

// Initialization is intentionally one-shot. The marker is persisted before the
// journal is created; a crash between these writes requires explicit recovery,
// instead of generating a new reporter ID and replaying historical usage.
func (s *OwnershipStore) initializeReporting() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.availableLocked(); err != nil {
		return err
	}
	if s.state.reporting != nil || s.state.pending != nil || s.state.generation != 0 ||
		len(s.state.checkpoint.Keys.InboundTags) != 0 || len(s.state.checkpoint.Keys.Clients) != 0 {
		return ownershipStoreError("reporting_initialization_refused")
	}
	next := cloneOwnershipState(s.state)
	next.reporting = &ownershipReportingState{Version: 1, Owners: []TrafficOwnership{}}
	return s.persistLocked(next)
}

// mergeTrafficOwnership adds only freshly verified live bindings. It cannot
// replace identities or resurrect a retired email, even if its marker matches.
func (s *OwnershipStore) mergeTrafficOwnership(keys OwnedKeys, observed []TrafficOwnership) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.availableLocked(); err != nil {
		return err
	}
	if s.state.reporting == nil || !s.reportingKeysAllowedLocked(keys) {
		return ownershipStoreError("reporting_manifest_changed")
	}
	if len(observed) != len(keys.Clients) {
		return ownershipStoreError("reporting_observation_incomplete")
	}
	want := map[string]string{}
	for _, key := range keys.Clients {
		want[key.Email] = key.InboundTag
	}
	next := cloneOwnershipState(s.state)
	index := map[string]int{}
	for i, owner := range next.reporting.Owners {
		index[owner.Email] = i
		if _, live := want[owner.Email]; !live && !owner.Retired {
			return ownershipStoreError("reporting_unretired_owner_missing")
		}
	}
	seen := map[string]bool{}
	for _, owner := range observed {
		if owner.PanelGUID != s.state.binding.PanelGUID || want[owner.Email] != owner.InboundTag || owner.Retired || seen[owner.Email] {
			return ownershipStoreError("reporting_observation_invalid")
		}
		seen[owner.Email] = true
		if i, exists := index[owner.Email]; exists {
			if next.reporting.Owners[i] != owner {
				return ownershipStoreError("reporting_owner_changed")
			}
		} else {
			index[owner.Email] = len(next.reporting.Owners)
			next.reporting.Owners = append(next.reporting.Owners, owner)
		}
	}
	sort.Slice(next.reporting.Owners, func(i, j int) bool { return next.reporting.Owners[i].Email < next.reporting.Owners[j].Email })
	if reflect.DeepEqual(next.reporting, s.state.reporting) {
		return nil
	}
	return s.persistLocked(next)
}

func (s *OwnershipStore) reportingKeysAllowed(keys OwnedKeys) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.availableLocked(); err != nil {
		return err
	}
	if !s.reportingKeysAllowedLocked(keys) {
		return ownershipStoreError("reporting_manifest_changed")
	}
	return nil
}

func (s *OwnershipStore) reportingKeysAllowedLocked(keys OwnedKeys) bool {
	return sameReportingKeys(keys, s.state.checkpoint.Keys) ||
		(s.state.pending != nil && sameReportingKeys(keys, s.state.pending.After.Keys))
}

func (s *OwnershipStore) retireTrafficOwnership(expected TrafficOwnership) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.availableLocked(); err != nil {
		return err
	}
	if s.state.reporting == nil {
		return ownershipStoreError("reporting_not_initialized")
	}
	next := cloneOwnershipState(s.state)
	for i, owner := range next.reporting.Owners {
		if owner.Email != expected.Email {
			continue
		}
		if owner != expected || owner.Retired {
			return ownershipStoreError("reporting_owner_changed")
		}
		next.reporting.Owners[i].Retired = true
		return s.persistLocked(next)
	}
	return ownershipStoreError("reporting_owner_missing")
}

func sameReportingKeys(left, right OwnedKeys) bool {
	if !validObservationKeys(left) || !validObservationKeys(right) {
		return false
	}
	// Reuse the checkpoint normalizer without making key order authoritative.
	digest := updateDigest("reporting-key-normalization")
	l, le := normalizeOwnershipCheckpoint(OwnershipCheckpoint{Keys: left, TemplateDigest: digest, ConfigDigest: digest})
	r, re := normalizeOwnershipCheckpoint(OwnershipCheckpoint{Keys: right, TemplateDigest: digest, ConfigDigest: digest})
	return le == nil && re == nil && reflect.DeepEqual(l.Keys, r.Keys)
}
