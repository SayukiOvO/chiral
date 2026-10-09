package threexui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCompatibilityNeverApprovesTakeover(t *testing.T) {
	for _, input := range []string{
		`{}`, `{"inbounds":[]}`, `{"inbounds":[{"tag":"inbound","protocol":"vless","settings":{"clients":[{"id":"credential","email":"private","flow":"xtls-rprx-vision"}]}}]}`,
	} {
		result, err := AssessConfigCompatibility([]byte(input))
		if err != nil || result.Assessment != "runtime_validation_required" || result.TakeoverPermitted || !result.RuntimeValidationRequired || result.Findings == nil || len(result.UnverifiedGuarantees) != 3 {
			t.Fatalf("invalid non-approval result: %+v, %v", result, err)
		}
	}
}

func TestCompatibilityKnownLevelPaths(t *testing.T) {
	for _, test := range []struct {
		level string
		codes []string
	}{
		{"0", []string{"managed_client_level_not_preserved"}},
		{"3", []string{"managed_client_level_not_preserved", "hot_client_level_not_preserved"}},
		{"3.0", []string{"managed_client_level_not_preserved", "client_level_value_requires_review"}},
		{"null", []string{"managed_client_level_not_preserved", "client_level_value_requires_review"}},
	} {
		input := []byte(`{"inbounds":[{"tag":"inbound","protocol":"vless","settings":{"clients":[{"id":"private-credential","email":"private-email","level":` + test.level + `}]}}]}`)
		before := bytes.Clone(input)
		result, err := AssessConfigCompatibility(input)
		if err != nil || result.Assessment != "known_path_incompatibility" || result.Clients != 1 || result.Inbounds != 1 || result.TakeoverPermitted {
			t.Fatalf("level %s: %+v, %v", test.level, result, err)
		}
		var codes []string
		for _, finding := range result.Findings {
			codes = append(codes, finding.Code)
			if finding.Inbound != 0 || finding.Client != 0 {
				t.Fatal("incorrect inventory position")
			}
		}
		if !reflect.DeepEqual(codes, test.codes) || !bytes.Equal(input, before) {
			t.Fatalf("level %s: unexpected codes or modified input: %v", test.level, codes)
		}
	}
}

func TestCompatibilityUnreviewedFieldsDoNotLeakOrClaimUnsupported(t *testing.T) {
	input := []byte(`{"inbounds":[{"tag":"secret-tag","protocol":"vless","settings":{"clients":[{"id":"secret-id","email":"secret-email","secret-key-name":"secret-value"}]}}]}`)
	result, err := AssessConfigCompatibility(input)
	if err != nil || result.Assessment != "additional_review_required" || len(result.Findings) != 1 || result.Findings[0].Code != "client_fields_require_review" {
		t.Fatalf("unexpected unreviewed-field result: %+v, %v", result, err)
	}
	encoded, err := json.Marshal(result)
	if err != nil || bytes.Contains(encoded, []byte("secret")) {
		t.Fatal("assessment disclosed source content")
	}
	for range 20 {
		again, err := AssessConfigCompatibility(input)
		if err != nil || !reflect.DeepEqual(result, again) {
			t.Fatal("assessment is not deterministic")
		}
	}
}

func TestCompatibilityOtherProtocolsRequireReview(t *testing.T) {
	for _, protocol := range []string{"shadowsocks", "wireguard", "private-protocol", ""} {
		result, err := AssessConfigCompatibility([]byte(`{"inbounds":[{"tag":"inbound","protocol":"` + protocol + `","settings":{"clients":[{}]}}]}`))
		if err != nil || result.Assessment != "additional_review_required" || len(result.Findings) != 1 || result.Findings[0].Code != "client_protocol_not_reviewed" {
			t.Fatalf("unexpected protocol review: %+v, %v", result, err)
		}
	}
}

func TestCompatibilityRejectsAmbiguousAndMalformedInput(t *testing.T) {
	for _, input := range []string{
		`{"inbounds":[],"inbounds":[]}`, `{"inbounds":null}`, `{"inbounds":[null]}`,
		`{"inbounds":[{"settings":null}]}`, `{"inbounds":[{"settings":{"clients":null}}]}`,
		`{"inbounds":[{"settings":{"clients":[null]}}]}`,
		`{"secret-key":"\ud800"}`, `{} {}`, `[]`, string([]byte{0xff}),
		`{}` + strings.Repeat(" ", maxConfigComparisonBytes),
	} {
		result, err := AssessConfigCompatibility([]byte(input))
		if err == nil || !reflect.DeepEqual(result, ConfigCompatibility{}) || strings.Contains(err.Error(), "secret-key") {
			t.Fatalf("expected fixed diagnostic and zero evidence, got %+v, %v", result, err)
		}
	}
}

func TestCompatibilityBoundsFindingsButFinishesInventory(t *testing.T) {
	var clients []string
	for i := 0; i <= maxCompatibilityFindings; i++ {
		clients = append(clients, fmt.Sprintf(`{"email":"private-%d","level":3}`, i))
	}
	input := []byte(`{"inbounds":[{"tag":"inbound","protocol":"vless","settings":{"clients":[` + strings.Join(clients, ",") + `]}}]}`)
	result, err := AssessConfigCompatibility(input)
	if err != nil || !result.Truncated || result.Clients != maxCompatibilityFindings+1 || len(result.Findings) != maxCompatibilityFindings || result.TakeoverPermitted {
		t.Fatalf("unexpected bounded inventory: %+v, %v", result, err)
	}
	// Do not stop structural validation after reaching the finding limit.
	invalid := bytes.Replace(input, []byte(clients[len(clients)-1]), []byte(`null`), 1)
	if result, err := AssessConfigCompatibility(invalid); err == nil || !reflect.DeepEqual(result, ConfigCompatibility{}) {
		t.Fatal("truncation hid invalid later input")
	}
}

func TestCompatibilityFindsStaticHotUpdateHazards(t *testing.T) {
	for _, test := range []struct {
		inbounds string
		code     string
	}{
		{`[{"protocol":"vless","settings":{"clients":[]}}]`, "inbound_tag_not_unique_nonempty"},
		{`[{"tag":"duplicate"},{"tag":"duplicate"}]`, "inbound_tag_not_unique_nonempty"},
		{`[{"tag":"inbound","protocol":"vless"}]`, "hot_client_array_missing"},
		{`[{"tag":"inbound","protocol":"vless","settings":{}}]`, "hot_client_array_missing"},
		{`[{"tag":"inbound","protocol":"vless","settings":{"clients":[{}]}}]`, "hot_client_email_not_unique_nonempty"},
		{`[{"tag":"inbound","protocol":"vless","settings":{"clients":[{"email":""}]}}]`, "hot_client_email_not_unique_nonempty"},
		{`[{"tag":"inbound","protocol":"vless","settings":{"clients":[{"email":42}]}}]`, "hot_client_email_not_unique_nonempty"},
		{`[{"tag":"inbound","protocol":"vless","settings":{"clients":[{"email":"duplicate"},{"email":"duplicate"}]}}]`, "hot_client_email_not_unique_nonempty"},
		{`[{"tag":"inbound","protocol":"vless","settings":{"clients":[{"email":"private-email","reverse":{"tag":"private-reverse"}}]}}]`, "reverse_inbound_change_requires_restart"},
		{`[{"tag":"inbound","protocol":"shadowsocks","settings":{"clients":[{"level":3}]}}]`, "managed_client_level_not_preserved"},
		{`[{"tag":"inbound","protocol":"hysteria","settings":{"clients":[{"level":0}]}}]`, "managed_client_level_not_preserved"},
	} {
		result, err := AssessConfigCompatibility([]byte(`{"inbounds":` + test.inbounds + `}`))
		if err != nil || result.Assessment != "known_path_incompatibility" || result.TakeoverPermitted {
			t.Fatalf("missing known hazard %s: %+v, %v", test.code, result, err)
		}
		found := false
		for _, finding := range result.Findings {
			found = found || finding.Code == test.code
		}
		if !found {
			t.Fatalf("missing finding %s", test.code)
		}
		encoded, _ := json.Marshal(result)
		if bytes.Contains(encoded, []byte("private-")) || bytes.Contains(encoded, []byte("duplicate")) {
			t.Fatal("hot-update hazard disclosed source content")
		}
	}
}
