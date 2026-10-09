package threexui

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const ownershipPrefix = "chiral-owned:"

// OwnershipScope binds candidate-only metadata to one persisted Chiral owner
// and one 3x-ui instance. It is secret-bearing state and must not be logged.
// This is an accidental-cross-management guard, not protection against an
// administrator who controls either process or can restore an entire database.
type OwnershipScope struct {
	panelGUID string
	ownerID   string
	key       [32]byte
}

// NewOwnershipScope restores an explicitly supplied identity. The caller must
// generate ownerID (16 random bytes, lowercase hex) and key (32 random bytes),
// persist them privately before any write, and never silently replace missing
// state. This primitive does not create a manifest, issue HTTP calls, or grant
// authority to mutate anything; the durable reconciler is not yet implemented.
func NewOwnershipScope(panelGUID, ownerID string, key []byte) (*OwnershipScope, error) {
	if !validOwnershipKey(panelGUID) || !ownershipHex(ownerID, 16) || len(key) != 32 {
		return nil, ownershipError("invalid_scope")
	}
	var nonzero byte
	for _, b := range key {
		nonzero |= b
	}
	if nonzero == 0 {
		return nil, ownershipError("invalid_scope")
	}
	scope := &OwnershipScope{panelGUID: panelGUID, ownerID: ownerID}
	copy(scope.key[:], key)
	return scope, nil
}

// InboundMarker belongs in Remark, never Tag: changing a tag would change
// routing and accounting semantics. This does not modify an inbound.
func (s *OwnershipScope) InboundMarker(tag string) (string, error) {
	return s.marker("inbound", tag)
}

// ClientMarker belongs in Comment, never Email or a proxy credential.
func (s *OwnershipScope) ClientMarker(email string) (string, error) {
	return s.marker("client", email)
}

func (s *OwnershipScope) valid() bool {
	if s == nil || !validOwnershipKey(s.panelGUID) || !ownershipHex(s.ownerID, 16) {
		return false
	}
	return s.key != [32]byte{}
}

func (s *OwnershipScope) marker(kind, externalKey string) (string, error) {
	if !s.valid() || !validOwnershipKey(externalKey) || (kind == "client" && validClientEmail(externalKey) != nil) {
		return "", ownershipError("invalid_identity")
	}
	mac := hmac.New(sha256.New, s.key[:])
	mac.Write([]byte("chiral-ownership-v1\x00"))
	for _, field := range []string{s.panelGUID, s.ownerID, kind, externalKey} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		mac.Write(size[:])
		mac.Write([]byte(field))
	}
	return ownershipPrefix + "v1:" + s.ownerID + ":" + kind + ":" + hex.EncodeToString(mac.Sum(nil)), nil
}

// OwnedKeys is the complete set already recorded by a future private manifest,
// not a desired-user list or an instruction to adopt same-named foreign rows.
// Missing or unexpected marked rows require reconciliation, not implicit repair.
type OwnedKeys struct {
	InboundTags []string
	Clients     []OwnedClientKey
}

// OwnedClientKey records the authorized binding as well as the external key.
// Moving a client to a different owned inbound is still an authorization change.
type OwnedClientKey struct {
	Email      string
	InboundTag string
}

// OwnedInventory contains freshly resolved numeric IDs. IDs are only caches;
// neither these maps nor a successful verification authorize a later write.
// The upstream API has no compare-and-swap operation, so it cannot exclude a
// foreign writer changing the rows immediately after this snapshot.
type OwnedInventory struct {
	InboundIDs map[string]int64
	ClientIDs  map[string]int64
}

// VerifyInventory is a pure, fail-closed candidate precondition. It checks
// stable identity, metadata bindings, exact manifest membership, unique keys
// and IDs, and attachments in both directions. Every owned credential must
// have exactly one owned inbound, preserving Chiral's credential granularity.
// It does not verify raw config fidelity, embedded clients, traffic settlement,
// runtime adoption, template drift, or permission to add/remove users. Those
// remain separate gates. An error returns no partial inventory or input data.
func (s *OwnershipScope) VerifyInventory(panelGUID string, expected OwnedKeys, inbounds []Inbound, clients []GlobalClient) (OwnedInventory, error) {
	fail := func(reason string) (OwnedInventory, error) { return OwnedInventory{}, ownershipError(reason) }
	if !s.valid() || panelGUID != s.panelGUID {
		return fail("provider_identity_changed")
	}
	wantInbounds, err := ownershipKeySet(expected.InboundTags)
	if err != nil {
		return OwnedInventory{}, err
	}
	wantClients := make(map[string]string, len(expected.Clients))
	for _, client := range expected.Clients {
		if !validOwnershipKey(client.Email) || validClientEmail(client.Email) != nil || wantClients[client.Email] != "" || !wantInbounds[client.InboundTag] {
			return fail("invalid_manifest_keys")
		}
		wantClients[client.Email] = client.InboundTag
	}
	result := OwnedInventory{InboundIDs: make(map[string]int64), ClientIDs: make(map[string]int64)}
	inboundOwners := make(map[int64]bool, len(inbounds))
	seenTags := make(map[string]bool, len(inbounds))
	for _, row := range inbounds {
		_, duplicateID := inboundOwners[row.ID]
		if row.ID <= 0 || !validOwnershipKey(row.Tag) || duplicateID || seenTags[row.Tag] {
			return fail("invalid_inbound_inventory")
		}
		seenTags[row.Tag] = true
		owned, err := s.owns("inbound", row.Tag, row.Remark)
		if err != nil {
			return OwnedInventory{}, err
		}
		if owned != wantInbounds[row.Tag] {
			return fail("inbound_ownership_conflict")
		}
		inboundOwners[row.ID] = owned
		if owned {
			result.InboundIDs[row.Tag] = row.ID
		}
	}
	seenClientIDs := make(map[int64]bool, len(clients))
	seenEmails := make(map[string]bool, len(clients))
	for _, row := range clients {
		if row.ID <= 0 || !validOwnershipKey(row.Email) || validClientEmail(row.Email) != nil || seenClientIDs[row.ID] || seenEmails[row.Email] {
			return fail("invalid_client_inventory")
		}
		seenClientIDs[row.ID], seenEmails[row.Email] = true, true
		owned, err := s.owns("client", row.Email, row.Comment)
		if err != nil {
			return OwnedInventory{}, err
		}
		if owned != (wantClients[row.Email] != "") {
			return fail("client_ownership_conflict")
		}
		if owned && len(row.InboundIDs) != 1 {
			return fail("client_attachment_conflict")
		}
		seenAttachments := make(map[int64]bool, len(row.InboundIDs))
		for _, id := range row.InboundIDs {
			inboundOwned, exists := inboundOwners[id]
			if id <= 0 || !exists || seenAttachments[id] || inboundOwned != owned || (owned && id != result.InboundIDs[wantClients[row.Email]]) {
				return fail("client_attachment_conflict")
			}
			seenAttachments[id] = true
		}
		if owned {
			result.ClientIDs[row.Email] = row.ID
		}
	}
	if len(result.InboundIDs) != len(wantInbounds) || len(result.ClientIDs) != len(wantClients) {
		return fail("owned_object_missing")
	}
	return result, nil
}

func (s *OwnershipScope) owns(kind, externalKey, marker string) (bool, error) {
	if !strings.HasPrefix(marker, ownershipPrefix) {
		return false, nil
	}
	parts := strings.Split(marker, ":")
	if len(parts) != 5 || parts[1] != "v1" || !ownershipHex(parts[2], 16) || parts[3] != kind || !ownershipHex(parts[4], 32) {
		return false, ownershipError("invalid_ownership_marker")
	}
	if parts[2] != s.ownerID {
		return false, nil
	}
	want, err := s.marker(kind, externalKey)
	if err != nil || !hmac.Equal([]byte(want), []byte(marker)) {
		return false, ownershipError("invalid_ownership_marker")
	}
	return true, nil
}

func ownershipKeySet(keys []string) (map[string]bool, error) {
	result := make(map[string]bool, len(keys))
	for _, key := range keys {
		if !validOwnershipKey(key) || result[key] {
			return nil, ownershipError("invalid_manifest_keys")
		}
		result[key] = true
	}
	return result, nil
}

func validOwnershipKey(value string) bool {
	if value == "" || len(value) > 512 || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}

func ownershipHex(value string, size int) bool {
	if len(value) != size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func ownershipError(reason string) error {
	// Every caller supplies a fixed label, never an object key or provider text.
	return fmt.Errorf("3x-ui ownership verification failed: %s", reason)
}
