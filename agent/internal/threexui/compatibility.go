package threexui

import "encoding/json"

// CompatibilityReviewSource identifies evidence, not an allowed deployment
// version. A newer release still needs an independent runtime assessment.
const CompatibilityReviewSource = "7ef22f94c950ff09f0870e2295fa65ad5968742c"

const maxCompatibilityFindings = 64

// ConfigCompatibility is an offline inventory of known client-field hazards.
// Neither a clean inventory nor a recognized field is a runtime approval.
// All strings in the result are fixed labels, never input keys or values.
type ConfigCompatibility struct {
	ReviewSource              string                 `json:"review_source"`
	Assessment                string                 `json:"assessment"`
	TakeoverPermitted         bool                   `json:"takeover_permitted"`
	RuntimeValidationRequired bool                   `json:"runtime_validation_required"`
	UnverifiedGuarantees      []string               `json:"unverified_guarantees"`
	Inbounds                  int                    `json:"inbounds"`
	Clients                   int                    `json:"clients"`
	Findings                  []CompatibilityFinding `json:"findings"`
	Truncated                 bool                   `json:"truncated"`
}

// CompatibilityFinding uses array indices to locate a private source record.
// Client is -1 for an inbound-level finding.
// It deliberately omits tags, emails, credentials, and unknown field names.
type CompatibilityFinding struct {
	Inbound int    `json:"inbound_index"`
	Client  int    `json:"client_index"`
	Code    string `json:"code"`
	Path    string `json:"application_path"`
}

type compatibilityShapeError struct{}

func (*compatibilityShapeError) Error() string {
	return "configuration inventory requires object inbounds, settings and clients with array collections"
}

// AssessConfigCompatibility inspects a complete local configuration without
// modifying it, opening files, or contacting a provider. The strict comparison
// parser enforces the same UTF-8, duplicate-key, depth and 4 MiB limits.
//
// Detailed client-field review covers VLESS, VMess and Trojan; known managed
// Level loss also covers Shadowsocks and Hysteria. Shared tag/reverse hazards
// are checked independently. Other fields/protocols need additional review.
// The reviewed upstream typed client model omits level; its AddUser path does
// not set protocol.User.Level. No field is removed or rewritten here.
//
// This flags static prerequisites for transitions but does not compare an old
// and new config, validate Xray syntax, prove unchanged
// connections, or prove that a rejected apply leaves the previous runtime
// untouched. TakeoverPermitted is always false, including for an empty config.
func AssessConfigCompatibility(raw []byte) (ConfigCompatibility, error) {
	config, err := decodeComparisonConfig(raw, "")
	if err != nil {
		return ConfigCompatibility{}, err
	}
	result := ConfigCompatibility{
		ReviewSource:              CompatibilityReviewSource,
		Assessment:                "runtime_validation_required",
		RuntimeValidationRequired: true,
		UnverifiedGuarantees: []string{
			"complete_configuration_fidelity",
			"daily_user_change_connection_continuity",
			"rejected_config_preserves_running_revision",
		},
		Findings: []CompatibilityFinding{},
	}
	value, present := config["inbounds"]
	if !present {
		return result, nil
	}
	inbounds, ok := value.([]any)
	if !ok {
		return ConfigCompatibility{}, &compatibilityShapeError{}
	}
	result.Inbounds = len(inbounds)
	tags := make(map[string]bool)
	for inboundIndex, value := range inbounds {
		inbound, ok := value.(map[string]any)
		if !ok {
			return ConfigCompatibility{}, &compatibilityShapeError{}
		}
		tag, _ := inbound["tag"].(string)
		if tag == "" || tags[tag] {
			result.addFinding(inboundIndex, -1, "inbound_tag_not_unique_nonempty", "raw_template_hot_update", true)
		}
		tags[tag] = true
		protocol, _ := inbound["protocol"].(string)
		userDiffable := protocol == "vless" || protocol == "vmess" || protocol == "trojan"
		value, present := inbound["settings"]
		if !present {
			if userDiffable {
				result.addFinding(inboundIndex, -1, "hot_client_array_missing", "raw_client_hot_update", true)
			}
			continue
		}
		settings, ok := value.(map[string]any)
		if !ok {
			return ConfigCompatibility{}, &compatibilityShapeError{}
		}
		value, present = settings["clients"]
		if !present {
			if userDiffable {
				result.addFinding(inboundIndex, -1, "hot_client_array_missing", "raw_client_hot_update", true)
			}
			continue
		}
		clients, ok := value.([]any)
		if !ok {
			return ConfigCompatibility{}, &compatibilityShapeError{}
		}
		emails := make(map[string]bool)
		for clientIndex, value := range clients {
			client, ok := value.(map[string]any)
			if !ok {
				return ConfigCompatibility{}, &compatibilityShapeError{}
			}
			result.Clients++
			add := func(code, path string, knownIssue bool) {
				result.addFinding(inboundIndex, clientIndex, code, path, knownIssue)
			}
			if userDiffable {
				email, _ := client["email"].(string)
				if email == "" || emails[email] {
					add("hot_client_email_not_unique_nonempty", "raw_client_hot_update", true)
				}
				emails[email] = true
			}
			// Upstream checks for any non-null reverse payload before trying
			// a client-only diff, irrespective of the inbound protocol.
			if reverse, present := client["reverse"]; present && reverse != nil {
				add("reverse_inbound_change_requires_restart", "raw_template_hot_update", true)
			}
			if level, present := client["level"]; present && (userDiffable || protocol == "shadowsocks" || protocol == "hysteria") {
				// Even explicit zero is dropped from the managed model. Do not
				// silently normalize missing and zero to claim field fidelity.
				add("managed_client_level_not_preserved", "managed_client", true)
				if number, ok := level.(json.Number); userDiffable && (!ok || string(number) != "0") {
					// Noncanonical zero and invalid types are deliberately left
					// for review; this check is not a Xray number validator.
					if ok && isPositiveInteger(number) {
						add("hot_client_level_not_preserved", "raw_client_hot_update", true)
					} else {
						add("client_level_value_requires_review", "raw_client_hot_update", false)
					}
				}
			}
			if !userDiffable {
				add("client_protocol_not_reviewed", "unassessed", false)
				continue
			}
			for key, value := range client {
				if key == "level" {
					continue
				}
				if !reviewedClientField(protocol, key, value) {
					add("client_fields_require_review", "unassessed", false)
					break
				}
			}
		}
	}
	return result, nil
}

func (result *ConfigCompatibility) addFinding(inbound, client int, code, path string, knownIssue bool) {
	if knownIssue {
		result.Assessment = "known_path_incompatibility"
	} else if result.Assessment != "known_path_incompatibility" {
		result.Assessment = "additional_review_required"
	}
	if len(result.Findings) == maxCompatibilityFindings {
		result.Truncated = true
		return
	}
	result.Findings = append(result.Findings, CompatibilityFinding{
		Inbound: inbound, Client: client, Code: code, Path: path,
	})
}

func isPositiveInteger(number json.Number) bool {
	value, err := number.Int64()
	return err == nil && value > 0
}

// A recognized shape only avoids an additional-review finding. It is not a
// promise of preservation, account validity, or safe mutation by upstream.
func reviewedClientField(protocol, key string, value any) bool {
	if key == "email" || (key == "id" && (protocol == "vless" || protocol == "vmess")) ||
		(key == "password" && protocol == "trojan") || (key == "flow" && protocol == "vless") {
		_, ok := value.(string)
		return ok
	}
	if protocol == "vless" && key == "reverse" {
		reverse, ok := value.(map[string]any)
		if !ok || len(reverse) != 1 {
			return false
		}
		_, ok = reverse["tag"].(string)
		return ok
	}
	return false
}
