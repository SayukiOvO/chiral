package threexui

import (
	"context"
	"encoding/json"
	"slices"
)

// ProvisionInbound creates the first explicitly requested loopback-only lab
// inbound. It is a candidate bootstrap operation, not an implementation of
// arbitrary ConfigPush. The official provider may persist a failed write;
// consequently this method is forbidden outside the isolated candidate and
// never retries a prepared intent. No users or access rights are inferred.
func (r *Runtime) ProvisionInbound(ctx context.Context, raw []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, r.config.RequestTimeout)
	defer cancel()
	inbound, err := candidateBootstrapInbound(raw)
	if err != nil {
		return err
	}
	checkpoint, generation, before, err := r.userPreflight(ctx)
	if err != nil {
		return err
	}
	if slices.Contains(checkpoint.Keys.InboundTags, inbound.Tag) {
		return runtimeRefusal("bootstrap_inbound_already_owned")
	}
	rows, err := r.api.ListInbounds(ctx)
	if err != nil {
		return runtimeRefusal("inventory_unavailable")
	}
	for _, row := range rows {
		if row.Tag == inbound.Tag || row.Port == inbound.Port {
			return runtimeRefusal("bootstrap_inbound_conflict")
		}
	}
	config, err := decodeComparisonConfig(before, "")
	if err != nil {
		return runtimeRefusal("configuration_invalid")
	}
	inbounds, ok := config["inbounds"].([]any)
	if !ok {
		return runtimeRefusal("configuration_invalid")
	}
	for _, row := range inbounds {
		object, ok := row.(map[string]any)
		if !ok || object["tag"] == inbound.Tag {
			return runtimeRefusal("bootstrap_inbound_conflict")
		}
	}
	object, _ := decodeComparisonConfig(raw, "") // already strictly validated
	config["inbounds"] = append(inbounds, object)
	want, err := json.Marshal(config)
	if err != nil {
		return runtimeRefusal("configuration_invalid")
	}
	comparison, err := CompareConfig(before, want)
	if err != nil {
		return runtimeRefusal("configuration_invalid")
	}
	inbound.Remark, err = r.scope.InboundMarker(inbound.Tag)
	if err != nil {
		return runtimeRefusal("bootstrap_inbound_invalid")
	}
	after := cloneOwnershipCheckpoint(checkpoint)
	after.Keys.InboundTags = append(after.Keys.InboundTags, inbound.Tag)
	after.ConfigDigest = comparison.ActualDigest
	opID, err := runtimeOperationID()
	if err != nil {
		return runtimeRefusal("operation_identity_unavailable")
	}
	if err := r.store.Prepare(opID, generation, updateDigest(string(raw)+after.ConfigDigest), after); err != nil {
		return runtimeRefusal("write_intent_unavailable")
	}
	if _, err := r.api.AddInbound(ctx, inbound); err != nil {
		return runtimeUnknown("bootstrap_write_result_unknown")
	}
	return r.completeWrite(ctx, opID, after)
}

// The bootstrap contract is intentionally narrow and rejects, rather than
// drops, every unsupported member. Complex configurations remain a separate
// unfinished apply gate. Empty clients prevent implicit credential adoption.
func candidateBootstrapInbound(raw []byte) (Inbound, error) {
	object, err := decodeComparisonConfig(raw, "")
	if err != nil || len(object) != 7 {
		return Inbound{}, runtimeRefusal("bootstrap_inbound_invalid")
	}
	for key := range object {
		if !slices.Contains([]string{"tag", "listen", "port", "protocol", "settings", "streamSettings", "sniffing"}, key) {
			return Inbound{}, runtimeRefusal("bootstrap_inbound_field_unsupported")
		}
	}
	var inbound Inbound
	if json.Unmarshal(raw, &inbound) != nil || !validOwnershipKey(inbound.Tag) || inbound.Tag == "api" ||
		inbound.Listen != "127.0.0.1" || inbound.Port < 1024 || inbound.Port > 65535 || inbound.Protocol != "vless" {
		return Inbound{}, runtimeRefusal("bootstrap_inbound_invalid")
	}
	settings, err := decodeComparisonConfig(inbound.Settings, "")
	if err != nil || len(settings) != 2 || settings["decryption"] != "none" {
		return Inbound{}, runtimeRefusal("bootstrap_settings_unsupported")
	}
	clients, ok := settings["clients"].([]any)
	if !ok || len(clients) != 0 {
		return Inbound{}, runtimeRefusal("bootstrap_clients_must_be_empty")
	}
	stream, err := decodeComparisonConfig(inbound.StreamSettings, "")
	if err != nil || len(stream) != 2 || stream["network"] != "tcp" || stream["security"] != "none" {
		return Inbound{}, runtimeRefusal("bootstrap_transport_unsupported")
	}
	sniffing, err := decodeComparisonConfig(inbound.Sniffing, "")
	if err != nil || len(sniffing) != 1 || sniffing["enabled"] != false {
		return Inbound{}, runtimeRefusal("bootstrap_sniffing_unsupported")
	}
	inbound.Enable = true
	return inbound, nil
}
