//go:build linux || darwin

package threexui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"
)

func TestRuntimeRejectsUnverifiedAccountValuesBeforeWriteIntent(t *testing.T) {
	const validID = "11111111-1111-4111-8111-111111111111"
	for _, protocol := range []string{"vless", "vmess"} {
		t.Run(protocol, func(t *testing.T) {
			rt, fixture, writes := newRuntimeValidationFixture(t, protocol, validID)
			// This exact no-op exercises the same real Runtime, policy, contract,
			// inventory, provenance and assembled-config preflight as the refusal
			// cases. A fixture failure must not masquerade as validation success.
			valid := map[string]any{"id": validID, "email": "owned@credential"}
			encoded, _ := json.Marshal(valid)
			if err := rt.AddUser(context.Background(), "owned-inbound", "owned@credential", encoded); err != nil {
				t.Fatalf("valid preflight fixture failed: %v", err)
			}
			before, generation, err := fixture.store.Checkpoint()
			if err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				name   string
				field  string
				value  any
				reason string
			}{
				{"non_uuid", "id", "uuid", "client_credential_not_verified"},
				{"unhyphenated", "id", "11111111111141118111111111111111", "client_credential_not_verified"},
				{"invalid_hex", "id", "g1111111-1111-4111-8111-111111111111", "client_credential_not_verified"},
				{"wrong_hyphen", "id", "1111111-11111-4111-8111-111111111111", "client_credential_not_verified"},
				{"trailing_text", "id", validID + "x", "client_credential_not_verified"},
				{"normalized_flow", "flow", "xtls-rprx-vision-udp443", "client_fields_not_preserved_by_official_hot_api"},
				{"unverified_flow", "flow", "xtls-rprx-vision", "client_fields_not_preserved_by_official_hot_api"},
				{"unverified_security", "security", "auto", "client_fields_not_preserved_by_official_hot_api"},
				{"empty_flow", "flow", "", "client_fields_not_preserved_by_official_hot_api"},
				{"typed_flow", "flow", true, "client_fields_not_preserved_by_official_hot_api"},
			} {
				t.Run(test.name, func(t *testing.T) {
					account := map[string]any{"id": validID, "email": "owned@credential", test.field: test.value}
					encoded, _ := json.Marshal(account)
					err := rt.AddUser(context.Background(), "owned-inbound", "owned@credential", encoded)
					if !errors.Is(err, runtimeprovider.ErrOperationNotExecuted) || !strings.Contains(err.Error(), test.reason) {
						t.Fatalf("unverified value was not refused at the account gate: %v", err)
					}
					after, afterGeneration, err := fixture.store.Checkpoint()
					if err != nil || generation != afterGeneration || !reflect.DeepEqual(before, after) {
						t.Fatal("pre-write refusal changed the expected checkpoint")
					}
					if pending, err := fixture.store.Pending(); err != nil || pending != nil {
						t.Fatal("pre-write refusal left an ambiguous write intent")
					}
					if *writes != 0 {
						t.Fatal("unverified account caused a mutating HTTP request")
					}
				})
			}
		})
	}
}

func TestRuntimeCredentialValidationUsesVerifiedDefaultShapes(t *testing.T) {
	for _, id := range []string{"11111111-1111-4111-8111-111111111111", "ABCDEF12-3456-7890-ABCD-ABCDEFABCDEF"} {
		for _, protocol := range []string{"vless", "vmess"} {
			if err := runtimeAccountCompatible(protocol, map[string]any{"id": id, "email": "owned@credential"}); err != nil {
				t.Fatal("canonical credential was refused")
			}
		}
	}
	if err := runtimeAccountCompatible("trojan", map[string]any{"password": "nonempty-credential", "email": "owned@credential"}); err != nil {
		t.Fatal("default Trojan credential was refused")
	}
	for _, value := range []any{"", " \t\n", nil, 123} {
		if err := runtimeAccountCompatible("trojan", map[string]any{"password": value, "email": "owned@credential"}); !errors.Is(err, runtimeprovider.ErrOperationNotExecuted) {
			t.Fatal("unusable Trojan credential was accepted")
		}
	}
}

// This fixture uses the actual Runtime and retained reporting/ownership state,
// not an alternate runtime implementation. Only the official HTTP responses
// are synthetic; unexpected routes are recorded and cannot report success.
func newRuntimeValidationFixture(t *testing.T, protocol, id string) (*Runtime, *runtimeReportingFixture, *int) {
	t.Helper()
	f := newRuntimeReportingFixture(t)
	if err := f.reporter.Close(); err != nil {
		t.Fatal(err)
	}
	f.inbounds = f.inbounds[:1]
	f.clients = f.clients[:1]
	f.counters = f.counters[:1]
	f.inbounds[0].Enable = true
	f.inbounds[0].Protocol = protocol
	config, _ := json.Marshal(map[string]any{"inbounds": []any{map[string]any{"tag": "owned-inbound", "protocol": protocol,
		"settings": map[string]any{"clients": []any{map[string]any{"id": id, "email": "owned@credential"}}}}}, "outbounds": []any{}})
	template := XrayTemplate{Config: json.RawMessage(`{"inbounds":[],"outbounds":[]}`), OutboundTestURL: "https://example.invalid/probe"}
	templateDigest, err := runtimeTemplateDigest(template)
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := CompareConfig(config, config)
	if err != nil {
		t.Fatal(err)
	}
	_, generation, _ := f.store.Checkpoint()
	checkpoint := OwnershipCheckpoint{Keys: f.keys, TemplateDigest: templateDigest, ConfigDigest: comparison.ActualDigest}
	if err := f.store.Prepare("seed-runtime-validation", generation, updateDigest("validation-fixture"), checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Complete("seed-runtime-validation", true, checkpoint); err != nil {
		t.Fatal(err)
	}
	paths := make(map[string]map[string]any)
	for _, requirement := range append(requiredOperations[:len(requiredOperations):len(requiredOperations)], optionalOperations...) {
		if paths[requirement.operation.path] == nil {
			paths[requirement.operation.path] = make(map[string]any)
		}
		paths[requirement.operation.path][requirement.operation.method] = map[string]any{}
	}
	document, _ := json.Marshal(map[string]any{"openapi": "3.0.3", "paths": paths})
	original := f.client.http.Transport
	writes := new(int)
	f.client.http.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		path := strings.TrimPrefix(request.URL.Path, "/base/panel/api/")
		var object any
		switch path {
		case "openapi.json":
			return fixtureReportingResponse(string(document)), nil
		case "setting/all":
			if request.Method != http.MethodPost {
				t.Error("runtime policy used an incorrect HTTP method")
			}
			object = map[string]any{"restartXrayOnClientDisable": false}
		case "xray/":
			encoded, _ := json.Marshal(map[string]any{"xraySetting": template.Config, "outboundTestUrl": template.OutboundTestURL})
			object = string(encoded)
		case "server/getConfigJson":
			object = json.RawMessage(config)
		case "clients/list":
			row := f.clients[0]
			object = []any{map[string]any{"id": row.ID, "uuid": id, "email": row.Email, "comment": row.Comment,
				"inboundIds": row.InboundIDs, "enable": true, "totalGB": 0, "expiryTime": 0, "reset": 0}}
		case "server/status", "inbounds/list", "clients/onlinesByGuid", "clients/clientIpsByGuid":
			return original.RoundTrip(request)
		default:
			*writes++
			return nil, fmt.Errorf("unexpected mutating route")
		}
		raw, _ := json.Marshal(map[string]any{"success": true, "obj": object})
		return fixtureReportingResponse(string(raw)), nil
	})
	rt, err := NewRuntime(context.Background(), f.client, f.store, RuntimeConfig{CandidateUserWrites: true, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt, f, writes
}
