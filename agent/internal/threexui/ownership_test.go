package threexui

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func ownershipFixture(t *testing.T) (*OwnershipScope, OwnedKeys, []Inbound, []GlobalClient) {
	t.Helper()
	scope, err := NewOwnershipScope("fixture-panel", strings.Repeat("ab", 16), bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	inboundMarker, err := scope.InboundMarker("owned-inbound")
	if err != nil {
		t.Fatal(err)
	}
	clientMarker, err := scope.ClientMarker("owned@credential")
	if err != nil {
		t.Fatal(err)
	}
	expected := OwnedKeys{InboundTags: []string{"owned-inbound"}, Clients: []OwnedClientKey{{Email: "owned@credential", InboundTag: "owned-inbound"}}}
	inbounds := []Inbound{{ID: 7, Tag: "owned-inbound", Remark: inboundMarker}, {ID: 8, Tag: "foreign-inbound", Remark: "operator-owned"}}
	clients := []GlobalClient{{ID: 9, Email: "owned@credential", Comment: clientMarker, InboundIDs: []int64{7}},
		{ID: 10, Email: "foreign@credential", Comment: "operator-owned", InboundIDs: []int64{8}}}
	return scope, expected, inbounds, clients
}

func TestOwnershipResolvesStableKeysAfterDatabaseIDChanges(t *testing.T) {
	scope, expected, inbounds, clients := ownershipFixture(t)
	for _, shift := range []int64{0, 100} {
		inbounds[0].ID, inbounds[1].ID = 7+shift, 8+shift
		clients[0].ID, clients[1].ID = 9+shift, 10+shift
		clients[0].InboundIDs, clients[1].InboundIDs = []int64{7 + shift}, []int64{8 + shift}
		got, err := scope.VerifyInventory("fixture-panel", expected, inbounds, clients)
		if err != nil {
			t.Fatal(err)
		}
		want := OwnedInventory{InboundIDs: map[string]int64{"owned-inbound": 7 + shift}, ClientIDs: map[string]int64{"owned@credential": 9 + shift}}
		if !reflect.DeepEqual(got, want) {
			t.Fatal("stable ownership did not resolve the current database IDs")
		}
	}
	// Caller changes cannot rewrite the source rows or retain an internal cache.
	got, _ := scope.VerifyInventory("fixture-panel", expected, inbounds, clients)
	got.InboundIDs["owned-inbound"] = -1
	fresh, err := scope.VerifyInventory("fixture-panel", expected, inbounds, clients)
	if err != nil || fresh.InboundIDs["owned-inbound"] != 107 || inbounds[0].Tag != "owned-inbound" || clients[0].Email != "owned@credential" {
		t.Fatal("verification modified source identity or shared mutable state")
	}
}

func TestOwnershipRejectsConflictsWithoutPartialInventory(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*OwnershipScope, *OwnedKeys, *[]Inbound, *[]GlobalClient)
	}{
		{"changed_provider", func(s *OwnershipScope, _ *OwnedKeys, _ *[]Inbound, _ *[]GlobalClient) { s.panelGUID = "changed" }},
		{"missing_inbound", func(_ *OwnershipScope, _ *OwnedKeys, i *[]Inbound, _ *[]GlobalClient) { *i = (*i)[1:] }},
		{"missing_client", func(_ *OwnershipScope, _ *OwnedKeys, _ *[]Inbound, c *[]GlobalClient) { *c = (*c)[1:] }},
		{"foreign_inbound_collision", func(_ *OwnershipScope, _ *OwnedKeys, i *[]Inbound, _ *[]GlobalClient) { (*i)[0].Remark = "foreign" }},
		{"foreign_client_collision", func(_ *OwnershipScope, _ *OwnedKeys, _ *[]Inbound, c *[]GlobalClient) { (*c)[0].Comment = "foreign" }},
		{"unexpected_owned_inbound", func(_ *OwnershipScope, e *OwnedKeys, _ *[]Inbound, _ *[]GlobalClient) {
			e.InboundTags, e.Clients = nil, nil
		}},
		{"unexpected_owned_client", func(_ *OwnershipScope, e *OwnedKeys, _ *[]Inbound, _ *[]GlobalClient) { e.Clients = nil }},
		{"duplicate_inbound_tag", func(_ *OwnershipScope, _ *OwnedKeys, i *[]Inbound, _ *[]GlobalClient) { (*i)[1].Tag = (*i)[0].Tag }},
		{"duplicate_inbound_id", func(_ *OwnershipScope, _ *OwnedKeys, i *[]Inbound, _ *[]GlobalClient) { (*i)[1].ID = (*i)[0].ID }},
		{"invalid_inbound_id", func(_ *OwnershipScope, _ *OwnedKeys, i *[]Inbound, _ *[]GlobalClient) { (*i)[0].ID = 0 }},
		{"duplicate_client_email", func(_ *OwnershipScope, _ *OwnedKeys, _ *[]Inbound, c *[]GlobalClient) { (*c)[1].Email = (*c)[0].Email }},
		{"duplicate_client_id", func(_ *OwnershipScope, _ *OwnedKeys, _ *[]Inbound, c *[]GlobalClient) { (*c)[1].ID = (*c)[0].ID }},
		{"invalid_client_id", func(_ *OwnershipScope, _ *OwnedKeys, _ *[]Inbound, c *[]GlobalClient) { (*c)[0].ID = -1 }},
		{"owned_client_foreign_attachment", func(_ *OwnershipScope, _ *OwnedKeys, _ *[]Inbound, c *[]GlobalClient) {
			(*c)[0].InboundIDs = []int64{8}
		}},
		{"foreign_client_owned_attachment", func(_ *OwnershipScope, _ *OwnedKeys, _ *[]Inbound, c *[]GlobalClient) {
			(*c)[1].InboundIDs = []int64{7}
		}},
		{"owned_client_no_attachment", func(_ *OwnershipScope, _ *OwnedKeys, _ *[]Inbound, c *[]GlobalClient) { (*c)[0].InboundIDs = nil }},
		{"owned_client_multiple_attachments", func(_ *OwnershipScope, _ *OwnedKeys, _ *[]Inbound, c *[]GlobalClient) {
			(*c)[0].InboundIDs = []int64{7, 8}
		}},
		{"duplicate_foreign_attachment", func(_ *OwnershipScope, _ *OwnedKeys, _ *[]Inbound, c *[]GlobalClient) {
			(*c)[1].InboundIDs = []int64{8, 8}
		}},
		{"dangling_foreign_attachment", func(_ *OwnershipScope, _ *OwnedKeys, _ *[]Inbound, c *[]GlobalClient) {
			(*c)[1].InboundIDs = []int64{88}
		}},
		{"duplicate_manifest_inbound", func(_ *OwnershipScope, e *OwnedKeys, _ *[]Inbound, _ *[]GlobalClient) {
			e.InboundTags = append(e.InboundTags, e.InboundTags[0])
		}},
		{"duplicate_manifest_client", func(_ *OwnershipScope, e *OwnedKeys, _ *[]Inbound, _ *[]GlobalClient) {
			e.Clients = append(e.Clients, e.Clients[0])
		}},
		{"manifest_unknown_binding", func(_ *OwnershipScope, e *OwnedKeys, _ *[]Inbound, _ *[]GlobalClient) {
			e.Clients[0].InboundTag = "foreign-inbound"
		}},
		{"empty_manifest_key", func(_ *OwnershipScope, e *OwnedKeys, _ *[]Inbound, _ *[]GlobalClient) {
			e.InboundTags = append(e.InboundTags, "")
		}},
		{"invalid_client_key", func(_ *OwnershipScope, e *OwnedKeys, _ *[]Inbound, _ *[]GlobalClient) {
			e.Clients[0].Email = "invalid/credential"
		}},
		{"marker_copied_to_inbound", func(_ *OwnershipScope, _ *OwnedKeys, i *[]Inbound, _ *[]GlobalClient) {
			(*i)[1].Remark = (*i)[0].Remark
		}},
		{"marker_copied_to_client", func(_ *OwnershipScope, _ *OwnedKeys, _ *[]Inbound, c *[]GlobalClient) {
			(*c)[1].Comment = (*c)[0].Comment
		}},
		{"marker_moved_between_kinds", func(_ *OwnershipScope, _ *OwnedKeys, i *[]Inbound, c *[]GlobalClient) {
			(*c)[0].Comment = (*i)[0].Remark
		}},
		{"marker_unknown_version", func(_ *OwnershipScope, _ *OwnedKeys, i *[]Inbound, _ *[]GlobalClient) {
			(*i)[0].Remark = strings.Replace((*i)[0].Remark, ":v1:", ":v2:", 1)
		}},
		{"marker_trailing_text", func(_ *OwnershipScope, _ *OwnedKeys, _ *[]Inbound, c *[]GlobalClient) {
			(*c)[0].Comment += " description"
		}},
		{"marker_changed_mac", func(_ *OwnershipScope, _ *OwnedKeys, i *[]Inbound, _ *[]GlobalClient) {
			parts := strings.Split((*i)[0].Remark, ":")
			parts[4] = strings.Repeat("0", 64)
			(*i)[0].Remark = strings.Join(parts, ":")
		}},
		{"foreign_malformed_reserved_marker", func(_ *OwnershipScope, _ *OwnedKeys, i *[]Inbound, _ *[]GlobalClient) {
			(*i)[1].Remark = ownershipPrefix + "malformed"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope, expected, inbounds, clients := ownershipFixture(t)
			tc.edit(scope, &expected, &inbounds, &clients)
			got, err := scope.VerifyInventory("fixture-panel", expected, inbounds, clients)
			if err == nil || got.InboundIDs != nil || got.ClientIDs != nil {
				t.Fatal("conflicting inventory returned success or partial IDs")
			}
			for _, secret := range []string{"fixture-panel", "owned-inbound", "owned@credential", "foreign", strings.Repeat("ab", 16)} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("ownership diagnostic leaked an input value")
				}
			}
		})
	}
}

func TestOwnershipRejectsMoveBetweenTwoOwnedInbounds(t *testing.T) {
	scope, expected, inbounds, clients := ownershipFixture(t)
	marker, _ := scope.InboundMarker("second-owned")
	inbounds = append(inbounds, Inbound{ID: 99, Tag: "second-owned", Remark: marker})
	expected.InboundTags = append(expected.InboundTags, "second-owned")
	clients[0].InboundIDs = []int64{99}
	if _, err := scope.VerifyInventory("fixture-panel", expected, inbounds, clients); err == nil {
		t.Fatal("moving a credential to another owned inbound silently expanded authorization")
	}
}

func TestOwnershipScopeAndMarkersAreBoundToAllIdentityFields(t *testing.T) {
	scope, expected, inbounds, clients := ownershipFixture(t)
	for _, changed := range []struct {
		guid, owner string
		key         []byte
	}{
		{"another-panel", scope.ownerID, bytes.Repeat([]byte{7}, 32)},
		{scope.panelGUID, strings.Repeat("cd", 16), bytes.Repeat([]byte{7}, 32)},
		{scope.panelGUID, scope.ownerID, bytes.Repeat([]byte{8}, 32)},
	} {
		other, err := NewOwnershipScope(changed.guid, changed.owner, changed.key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := other.VerifyInventory(changed.guid, expected, inbounds, clients); err == nil {
			t.Fatal("changed scope adopted old metadata")
		}
	}
	key := bytes.Repeat([]byte{7}, 32)
	restored, _ := NewOwnershipScope(scope.panelGUID, scope.ownerID, key)
	key[0]++
	if _, err := restored.VerifyInventory(scope.panelGUID, expected, inbounds, clients); err != nil {
		t.Fatal("constructor retained a mutable caller key")
	}
	for _, invalid := range []string{"", " padded", "line\nsecret", string([]byte{0xff}), strings.Repeat("x", 513)} {
		if _, err := scope.InboundMarker(invalid); err == nil {
			t.Fatal("invalid stable identity accepted")
		}
		if _, err := NewOwnershipScope(invalid, scope.ownerID, key); err == nil {
			t.Fatal("invalid provider identity accepted")
		}
	}
	for _, invalid := range []string{"", "abcd", strings.Repeat("AB", 16), strings.Repeat("gg", 16)} {
		if _, err := NewOwnershipScope(scope.panelGUID, invalid, key); err == nil {
			t.Fatal("invalid owner identity accepted")
		}
	}
	for _, invalid := range [][]byte{nil, make([]byte, 32), make([]byte, 31), bytes.Repeat([]byte{1}, 33)} {
		if _, err := NewOwnershipScope(scope.panelGUID, scope.ownerID, invalid); err == nil {
			t.Fatal("missing or invalid marker key accepted")
		}
	}
	for _, invalid := range []*OwnershipScope{nil, {}} {
		if _, err := invalid.ClientMarker("valid@credential"); err == nil {
			t.Fatal("zero scope issued a marker")
		}
		if _, err := invalid.VerifyInventory(scope.panelGUID, expected, inbounds, clients); err == nil {
			t.Fatal("zero scope verified an inventory")
		}
	}
}

func TestOwnershipPreservesDisjointForeignObjects(t *testing.T) {
	scope, _, inbounds, clients := ownershipFixture(t)
	other, _ := NewOwnershipScope(scope.panelGUID, strings.Repeat("cd", 16), bytes.Repeat([]byte{9}, 32))
	inbounds[1].Remark, _ = other.InboundMarker(inbounds[1].Tag)
	clients[1].Comment, _ = other.ClientMarker(clients[1].Email)
	got, err := scope.VerifyInventory(scope.panelGUID, OwnedKeys{}, inbounds[1:], clients[1:])
	if err != nil || len(got.InboundIDs) != 0 || len(got.ClientIDs) != 0 {
		t.Fatal("foreign objects were adopted or rejected despite disjoint attachments")
	}
	// Empty manifests are valid only when no object claims this owner.
	if _, err := scope.VerifyInventory(scope.panelGUID, OwnedKeys{}, nil, nil); err != nil {
		t.Fatal(err)
	}
}
