package threexui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// OwnershipObservationRequest names an already reviewed provider contract and
// the complete existing ownership manifest. Discovery does not approve a new
// OpenAPI document automatically. Keys describe existing authorized bindings,
// not a desired update or permission to adopt foreign objects.
type OwnershipObservationRequest struct {
	Binding               OwnershipStoreBinding
	ExpectedPanelVersion  string
	ExpectedOpenAPIDigest string
	Keys                  OwnedKeys
}

// OwnershipObservation is candidate readback evidence, not ConfigAck or proof
// of runtime adoption. It contains credential emails and must not be logged.
// Numeric IDs are fresh lookup results, never durable object identities.
type OwnershipObservation struct {
	Checkpoint OwnershipCheckpoint
	Inventory  OwnedInventory
}

// ObserveOwnership reads the official HTTP inventory, template and assembled
// config twice, bracketed by identity and exact OpenAPI checks. It refuses a
// changed contract, mismatched ownership or observed drift and never returns
// partial evidence. The second read detects some concurrent changes but is not
// a transactional snapshot or compare-and-swap guard against a later writer.
//
// This method is candidate-only, NOT a SHADOW observer: upstream's template and
// assembled-config reads can perform storage and client lifecycle maintenance.
// Call only in an explicitly authorized isolated or takeover workflow. It does
// not create/update/delete clients, restart Xray, probe the data path, or claim
// that the assembled configuration was accepted by the running process.
func (c *Client) ObserveOwnership(ctx context.Context, scope *OwnershipScope, request OwnershipObservationRequest) (OwnershipObservation, error) {
	fail := func(reason string) (OwnershipObservation, error) {
		return OwnershipObservation{}, ownershipObservationError(reason)
	}
	if c == nil || c.base == nil || !scope.valid() || request.Binding.EndpointHash != updateDigest(c.base.String()) ||
		request.Binding.PanelGUID != scope.panelGUID || !validXrayVersion(request.ExpectedPanelVersion) ||
		!ownershipHex(request.ExpectedOpenAPIDigest, 32) {
		return fail("invalid_request")
	}
	// Clone caller slices before any blocking request; the caller must still
	// avoid concurrently mutating input while entering this method.
	request.Keys = observationKeys(request.Keys)
	if !validObservationKeys(request.Keys) {
		return fail("invalid_manifest")
	}
	before, err := c.ownershipObservationIdentity(ctx, request)
	if err != nil {
		return OwnershipObservation{}, err
	}
	first, firstIdentity, err := c.ownershipObservationRound(ctx, scope, request)
	if err != nil {
		return OwnershipObservation{}, err
	}
	second, secondIdentity, err := c.ownershipObservationRound(ctx, scope, request)
	if err != nil {
		return OwnershipObservation{}, err
	}
	after, err := c.ownershipObservationIdentity(ctx, request)
	if err != nil {
		return OwnershipObservation{}, err
	}
	if before.Xray.State != after.Xray.State || before.Xray.Version != after.Xray.Version ||
		firstIdentity != secondIdentity || first.Checkpoint.TemplateDigest != second.Checkpoint.TemplateDigest ||
		first.Checkpoint.ConfigDigest != second.Checkpoint.ConfigDigest {
		return fail("provider_drift")
	}
	return second, nil
}

func (c *Client) ownershipObservationIdentity(ctx context.Context, request OwnershipObservationRequest) (Status, error) {
	if ctx.Err() != nil {
		return Status{}, ownershipObservationError("cancelled")
	}
	capabilities, err := c.DiscoverCapabilities(ctx)
	if err != nil || capabilities.DocumentSHA256 != request.ExpectedOpenAPIDigest {
		return Status{}, ownershipObservationError("contract_changed")
	}
	for _, capability := range []Capability{CapabilityStatus, CapabilityInboundsList, CapabilityClientsList, CapabilityXrayTemplateRead, CapabilityGetConfig} {
		if !capabilities.Supports(capability) {
			return Status{}, ownershipObservationError("capability_missing")
		}
	}
	status, err := c.Status(ctx)
	if err != nil || status.PanelGUID != request.Binding.PanelGUID || status.PanelVersion != request.ExpectedPanelVersion {
		return Status{}, ownershipObservationError("provider_identity_changed")
	}
	return status, nil
}

func (c *Client) ownershipObservationRound(ctx context.Context, scope *OwnershipScope, request OwnershipObservationRequest) (OwnershipObservation, string, error) {
	fail := func(reason string) (OwnershipObservation, string, error) {
		return OwnershipObservation{}, "", ownershipObservationError(reason)
	}
	inbounds, err := c.ListInbounds(ctx)
	if err != nil {
		return fail("inventory_unavailable")
	}
	clients, err := c.ListClients(ctx)
	if err != nil {
		return fail("inventory_unavailable")
	}
	inventory, err := scope.VerifyInventory(request.Binding.PanelGUID, request.Keys, inbounds, clients)
	if err != nil {
		return fail("ownership_conflict")
	}
	// Reject ownership conflicts before reads that can maintain upstream state.
	template, err := c.ReadXrayTemplate(ctx)
	if err != nil {
		return fail("template_unavailable")
	}
	config, err := c.ReadAssembledConfig(ctx)
	if err != nil {
		return fail("configuration_unavailable")
	}
	templateJSON, err := json.Marshal(struct {
		Config json.RawMessage `json:"config"`
		URL    string          `json:"outbound_test_url"`
	}{Config: template.Config, URL: template.OutboundTestURL})
	if err != nil {
		return fail("template_invalid")
	}
	// The strict comparison decoder rejects duplicate keys, invalid Unicode
	// and ambiguous input. Include the test URL in the template digest because
	// saving a template without preserving that setting changes upstream state.
	templateComparison, err := CompareConfig(templateJSON, templateJSON)
	if err != nil {
		return fail("template_invalid")
	}
	configComparison, err := CompareConfig(config, config)
	if err != nil {
		return fail("configuration_invalid")
	}
	return OwnershipObservation{
		Checkpoint: OwnershipCheckpoint{Keys: observationKeys(request.Keys),
			TemplateDigest: templateComparison.ActualDigest, ConfigDigest: configComparison.ActualDigest},
		Inventory: inventory,
	}, observationInventoryDigest(inbounds, clients), nil
}

// Fingerprint every row's identity and attachments, including foreign rows.
// Ignore changing byte counters and list order, not object identity. Config
// fidelity is checked separately; this is not a digest of all database fields.
func observationInventoryDigest(inbounds []Inbound, clients []GlobalClient) string {
	type inboundIdentity struct {
		ID     int64  `json:"id"`
		Tag    string `json:"tag"`
		Marker string `json:"marker"`
	}
	type clientIdentity struct {
		ID          int64   `json:"id"`
		Email       string  `json:"email"`
		Marker      string  `json:"marker"`
		Attachments []int64 `json:"attachments"`
	}
	rows := struct {
		Inbounds []inboundIdentity `json:"inbounds"`
		Clients  []clientIdentity  `json:"clients"`
	}{Inbounds: make([]inboundIdentity, 0, len(inbounds)), Clients: make([]clientIdentity, 0, len(clients))}
	for _, row := range inbounds {
		rows.Inbounds = append(rows.Inbounds, inboundIdentity{row.ID, row.Tag, row.Remark})
	}
	for _, row := range clients {
		ids := append([]int64{}, row.InboundIDs...)
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		rows.Clients = append(rows.Clients, clientIdentity{row.ID, row.Email, row.Comment, ids})
	}
	sort.Slice(rows.Inbounds, func(i, j int) bool { return rows.Inbounds[i].ID < rows.Inbounds[j].ID })
	sort.Slice(rows.Clients, func(i, j int) bool { return rows.Clients[i].ID < rows.Clients[j].ID })
	data, _ := json.Marshal(rows)
	return comparisonDigest(data)
}

func observationKeys(keys OwnedKeys) OwnedKeys {
	return OwnedKeys{InboundTags: append([]string{}, keys.InboundTags...), Clients: append([]OwnedClientKey{}, keys.Clients...)}
}

func validObservationKeys(keys OwnedKeys) bool {
	inbounds, err := ownershipKeySet(keys.InboundTags)
	if err != nil {
		return false
	}
	clients := make(map[string]bool, len(keys.Clients))
	for _, client := range keys.Clients {
		if !validOwnershipKey(client.Email) || validClientEmail(client.Email) != nil || clients[client.Email] || !inbounds[client.InboundTag] {
			return false
		}
		clients[client.Email] = true
	}
	return true
}

func ownershipObservationError(reason string) error {
	return fmt.Errorf("3x-ui candidate ownership observation failed: %s", reason)
}
