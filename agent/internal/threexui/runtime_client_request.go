package threexui

import (
	"encoding/json"
	"net/netip"
	"strings"
)

// runtimeClientRecordRequest converts the official list record into its
// distinct update schema. Preserve metadata; unsupported nonempty fields fail
// before a write intent instead of being silently dropped by upstream binding.
func runtimeClientRecordRequest(raw json.RawMessage) (map[string]any, error) {
	account, err := decodeComparisonConfig(raw, "")
	if err != nil {
		return nil, runtimeRefusal("client_record_invalid")
	}
	for key, value := range account {
		switch key {
		case "id", "limitIp", "limitHwid", "totalGB", "expiryTime", "reset", "resetDay", "resetWeekday", "resetMax", "trafficResetDay", "keepAlive", "createdAt", "updatedAt":
			number, ok := value.(json.Number)
			if !ok {
				return nil, runtimeRefusal("client_record_invalid")
			}
			if n, err := number.Int64(); err != nil || n < 0 || (key == "id" && n == 0) {
				return nil, runtimeRefusal("client_record_invalid")
			}
		case "uuid", "email", "password", "auth", "flow", "security", "privateKey", "publicKey", "preSharedKey", "forwardedPorts", "secret", "adTag", "subId", "group", "comment", "trafficReset", "allowedIPs":
			if _, ok := value.(string); !ok {
				return nil, runtimeRefusal("client_record_invalid")
			}
		case "tgId":
			// Telegram group/chat identifiers may be negative.
			number, ok := value.(json.Number)
			if !ok {
				return nil, runtimeRefusal("client_record_invalid")
			}
			if _, err := number.Int64(); err != nil {
				return nil, runtimeRefusal("client_record_invalid")
			}
		case "enable":
			if _, ok := value.(bool); !ok {
				return nil, runtimeRefusal("client_record_invalid")
			}
		case "reverse":
			if value != nil {
				if _, ok := value.(map[string]any); !ok {
					return nil, runtimeRefusal("client_record_invalid")
				}
			}
		case "inboundIds", "inbounds":
			// Attachment records are verified separately, never written by a
			// client update or mistaken for protocol account members.
		case "traffic":
			// clients/list returns ClientWithAttachments, which adds a
			// read-only traffic projection to ClientRecord. It may contain
			// master-overlaid values, so never send it back or use it for billing.
			if value != nil {
				if _, ok := value.(map[string]any); !ok {
					return nil, runtimeRefusal("client_record_invalid")
				}
			}
		default:
			if !runtimeEmptyRecordField(value) {
				return nil, runtimeRefusal("client_record_fields_not_preserved_by_official_api")
			}
		}
	}
	if _, ok := account["id"]; !ok {
		return nil, runtimeRefusal("client_record_invalid")
	}
	if email, ok := account["email"].(string); !ok || validClientEmail(email) != nil {
		return nil, runtimeRefusal("client_record_invalid")
	}
	if _, ok := account["enable"].(bool); !ok {
		return nil, runtimeRefusal("client_record_invalid")
	}
	delete(account, "id") // Database integer; the update id is the proxy UUID.
	if uuid, ok := account["uuid"].(string); ok {
		account["id"] = uuid
	}
	delete(account, "uuid")
	delete(account, "inboundIds")
	delete(account, "inbounds")
	delete(account, "traffic")
	for _, timestamp := range []struct{ from, to string }{{"createdAt", "created_at"}, {"updatedAt", "updated_at"}} {
		if value, ok := account[timestamp.from]; ok {
			if _, duplicate := account[timestamp.to]; duplicate {
				return nil, runtimeRefusal("client_record_invalid")
			}
			account[timestamp.to] = value
			delete(account, timestamp.from)
		}
	}
	if csv, ok := account["allowedIPs"].(string); ok {
		addresses := make([]string, 0)
		for _, part := range strings.Split(csv, ",") {
			address := strings.TrimSpace(part)
			if address == "" {
				continue
			}
			if _, err := netip.ParsePrefix(address); err != nil {
				if _, err := netip.ParseAddr(address); err != nil {
					return nil, runtimeRefusal("client_record_invalid")
				}
			}
			addresses = append(addresses, address)
		}
		account["allowedIPs"] = addresses
	}
	return account, nil
}

func runtimeEmptyRecordField(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case string:
		return value == ""
	case bool:
		return !value
	case json.Number:
		n, err := value.Int64()
		return err == nil && n == 0
	case []any:
		return len(value) == 0
	case map[string]any:
		return len(value) == 0
	default:
		return false
	}
}

// Core supplies a complete proxy account, not a metadata patch. Clear the
// protocol members that it omitted while preserving the existing record's
// unrelated metadata and stable upstream subscription identity.
func runtimeMergeClientRequest(existing, account map[string]any, marker string) map[string]any {
	request := make(map[string]any, len(existing)+len(account)+7)
	for key, value := range existing {
		request[key] = value
	}
	for _, key := range []string{"id", "password", "flow", "security"} {
		delete(request, key)
	}
	for key, value := range account {
		request[key] = value
	}
	request["comment"], request["enable"] = marker, true
	request["totalGB"], request["expiryTime"], request["reset"] = 0, 0, 0
	request["limitIp"] = 0
	if existing == nil {
		request["subId"] = ""
	}
	return request
}

// Restore only a re-enabled target to the position of its existing database
// ID. Upstream assembles enabled clients in ascending global record ID order;
// other clients, inbounds and every unrelated array retain their exact order.
func runtimeRestoreClientOrder(raw []byte, tag, email string, records []GlobalClient) ([]byte, error) {
	ids := make(map[string]int64, len(records))
	seenIDs := make(map[int64]bool, len(records))
	for _, record := range records {
		if record.ID <= 0 || ids[record.Email] != 0 || seenIDs[record.ID] {
			return nil, runtimeRefusal("client_record_invalid")
		}
		ids[record.Email], seenIDs[record.ID] = record.ID, true
	}
	targetID := ids[email]
	if targetID <= 0 {
		return nil, runtimeRefusal("owned_client_missing")
	}
	config, err := decodeComparisonConfig(raw, "")
	if err != nil {
		return nil, runtimeRefusal("configuration_invalid")
	}
	inbounds, ok := config["inbounds"].([]any)
	if !ok {
		return nil, runtimeRefusal("configuration_invalid")
	}
	for _, item := range inbounds {
		inbound, ok := item.(map[string]any)
		if !ok {
			return nil, runtimeRefusal("configuration_invalid")
		}
		if inbound["tag"] != tag {
			continue
		}
		settings, ok := inbound["settings"].(map[string]any)
		if !ok {
			return nil, runtimeRefusal("configuration_invalid")
		}
		clients, ok := settings["clients"].([]any)
		if !ok {
			return nil, runtimeRefusal("configuration_invalid")
		}
		var target map[string]any
		remaining := make([]any, 0, len(clients))
		position := -1
		for _, entry := range clients {
			client, ok := entry.(map[string]any)
			if !ok {
				return nil, runtimeRefusal("configuration_invalid")
			}
			identity, ok := client["email"].(string)
			if !ok || ids[identity] <= 0 {
				return nil, runtimeRefusal("client_record_invalid")
			}
			if identity == email {
				if target != nil {
					return nil, runtimeRefusal("client_identity_ambiguous")
				}
				target = client
				continue
			}
			if position == -1 && ids[identity] > targetID {
				position = len(remaining)
			}
			remaining = append(remaining, entry)
		}
		if target == nil {
			return nil, runtimeRefusal("owned_client_missing")
		}
		if position == -1 {
			position = len(remaining)
		}
		result := make([]any, 0, len(clients))
		result = append(result, remaining[:position]...)
		result = append(result, target)
		result = append(result, remaining[position:]...)
		settings["clients"] = result
		return json.Marshal(config)
	}
	return nil, runtimeRefusal("owned_inbound_missing")
}
