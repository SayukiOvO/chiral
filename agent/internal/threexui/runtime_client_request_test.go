package threexui

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"
)

func TestRuntimeClientRecordRequestConvertsOfficialWireTypesAndPreservesMetadata(t *testing.T) {
	const raw = `{"id":7,"uuid":"original-proxy-id","email":"owned@credential","enable":false,
		"password":"","flow":"xtls-rprx-vision","security":"auto","allowedIPs":" 192.0.2.1/32, , 2001:db8::1/128 ",
		"tgId":-9007199254740993,"group":"original-group","comment":"ownership-marker","subId":"original-subscription",
		"limitHwid":3,"resetDay":19,"resetWeekday":4,"resetMax":12,"trafficReset":"weekly","trafficResetDay":5,
		"createdAt":1700000000123,"updatedAt":1700000000456,"reverse":null,"inboundIds":[2],"futureEmpty":""}`
	request, err := runtimeClientRecordRequest(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	if request["id"] != "original-proxy-id" || !reflect.DeepEqual(request["allowedIPs"], []string{"192.0.2.1/32", "2001:db8::1/128"}) {
		t.Fatal("record identity or CSV address conversion changed")
	}
	for _, absent := range []string{"uuid", "inboundIds", "inbounds", "createdAt", "updatedAt"} {
		if _, ok := request[absent]; ok {
			t.Fatalf("record-only member %s leaked into the request", absent)
		}
	}
	for key, want := range map[string]any{
		"tgId": json.Number("-9007199254740993"), "group": "original-group", "subId": "original-subscription",
		"limitHwid": json.Number("3"), "resetDay": json.Number("19"), "resetWeekday": json.Number("4"),
		"resetMax": json.Number("12"), "trafficReset": "weekly", "trafficResetDay": json.Number("5"),
		"created_at": json.Number("1700000000123"), "updated_at": json.Number("1700000000456"), "futureEmpty": "",
	} {
		if !reflect.DeepEqual(request[key], want) {
			t.Fatalf("record metadata member %s was not preserved", key)
		}
	}
}

func TestRuntimeClientRecordRequestRejectsNonpreservableFieldsBeforeWrite(t *testing.T) {
	for name, field := range map[string]string{
		"unknown nonempty string":  `"future":"not-supported"`,
		"unknown nonzero number":   `"future":1`,
		"unknown true":             `"future":true`,
		"unknown nonempty array":   `"future":["member"]`,
		"unknown nonempty object":  `"future":{"member":true}`,
		"invalid CSV":              `"allowedIPs":"192.0.2.1/32,not-an-address"`,
		"wrong allowedIPs type":    `"allowedIPs":["192.0.2.1/32"]`,
		"wrong UUID type":          `"uuid":99`,
		"wrong limit type":         `"limitHwid":"3"`,
		"wrong traffic projection": `"traffic":"not-an-object"`,
		"ambiguous timestamp":      `"createdAt":17,"created_at":19`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := `{"id":7,"email":"owned@credential","enable":false,` + field + `}`
			request, err := runtimeClientRecordRequest(json.RawMessage(raw))
			if request != nil || !errors.Is(err, runtimeprovider.ErrOperationNotExecuted) {
				t.Fatalf("nonpreservable record did not fail before execution: %v", err)
			}
		})
	}
}

func TestRuntimeClientRecordRequestAcceptsOfficial39ListProjection(t *testing.T) {
	// v3.9.0 ClientWithAttachments embeds ClientRecord and appends inboundIds
	// and traffic; the latter is a read-only, potentially master-overlaid view.
	const raw = `{"id":7,"email":"owned@credential","subId":"upstream-subscription","uuid":"original-id",
		"password":"","auth":"","flow":"","security":"","reverse":null,"privateKey":"","publicKey":"",
		"allowedIPs":"","preSharedKey":"","keepAlive":0,"forwardedPorts":"","secret":"","adTag":"",
		"limitIp":0,"limitHwid":0,"totalGB":0,"expiryTime":0,"enable":true,"tgId":0,"group":"",
		"comment":"ownership-marker","reset":0,"resetDay":0,"resetWeekday":0,"resetMax":0,
		"trafficReset":"never","trafficResetDay":1,"createdAt":1700000000123,"updatedAt":1700000000456,
		"inboundIds":[2],"traffic":{"id":13,"inboundId":2,"email":"owned@credential","up":9007199254740993,"down":19,"total":0,"enable":true}}`
	request, err := runtimeClientRecordRequest(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"traffic", "inboundIds", "uuid", "createdAt", "updatedAt"} {
		if _, exists := request[field]; exists {
			t.Fatalf("read-only projection member %s reached the update payload", field)
		}
	}
	if request["id"] != "original-id" || request["enable"] != true || request["subId"] != "upstream-subscription" {
		t.Fatal("official list projection lost its account identity or metadata")
	}
	request["enable"] = false
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var officialRequestShape struct {
		ID         string   `json:"id"`
		AllowedIPs []string `json:"allowedIPs"`
		TgID       int64    `json:"tgId"`
		Enable     bool     `json:"enable"`
	}
	if err := json.Unmarshal(payload, &officialRequestShape); err != nil || officialRequestShape.Enable || officialRequestShape.ID != "original-id" {
		t.Fatal("complete v3.9.0 list projection could not bind to a disable request")
	}
}

func TestRuntimeClientRecordRequestAllowsNullTrafficProjection(t *testing.T) {
	request, err := runtimeClientRecordRequest(json.RawMessage(`{"id":7,"uuid":"original-id","email":"owned@credential","enable":true,"traffic":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := request["traffic"]; exists {
		t.Fatal("null read-only traffic projection reached the update payload")
	}
}

func TestRuntimeClientRecordRequestEmptyAllowedIPsIsArray(t *testing.T) {
	request, err := runtimeClientRecordRequest(json.RawMessage(`{"id":7,"uuid":"original-id","email":"owned@credential","enable":true,"allowedIPs":""}`))
	if err != nil {
		t.Fatal(err)
	}
	if addresses, ok := request["allowedIPs"].([]string); !ok || len(addresses) != 0 {
		t.Fatal("normal VLESS empty address field remains an incompatible string")
	}
	request["enable"] = false
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var officialRequestShape struct {
		ID         string   `json:"id"`
		AllowedIPs []string `json:"allowedIPs"`
		Enable     bool     `json:"enable"`
	}
	if err := json.Unmarshal(payload, &officialRequestShape); err != nil || officialRequestShape.Enable || officialRequestShape.ID != "original-id" {
		t.Fatal("disable payload cannot bind to the official request shape")
	}
}

func TestRuntimeMergeClientRequestReenablePreservesOwnedMetadata(t *testing.T) {
	existing, err := runtimeClientRecordRequest(json.RawMessage(`{"id":7,"uuid":"original-id","email":"owned@credential","enable":false,
		"allowedIPs":"","subId":"original-sub","tgId":99,"group":"original-group","limitHwid":3,
		"resetDay":19,"resetWeekday":4,"resetMax":12,"trafficReset":"weekly","trafficResetDay":5,
		"createdAt":1700000000123,"flow":"xtls-rprx-vision","security":"auto","totalGB":12,"expiryTime":100,"reset":30,"limitIp":4}`))
	if err != nil {
		t.Fatal(err)
	}
	account := map[string]any{"id": "original-id", "email": "owned@credential"}
	request := runtimeMergeClientRequest(existing, account, "ownership-marker")
	for _, key := range []string{"subId", "tgId", "group", "limitHwid", "resetDay", "resetWeekday", "resetMax", "trafficReset", "trafficResetDay", "created_at"} {
		if !reflect.DeepEqual(request[key], existing[key]) {
			t.Fatalf("reenable reset unrelated metadata member %s", key)
		}
	}
	if request["enable"] != true || request["id"] != "original-id" || request["comment"] != "ownership-marker" {
		t.Fatal("reenable changed the owned credential identity or enable state")
	}
	for _, key := range []string{"flow", "security", "password"} {
		if _, exists := request[key]; exists {
			t.Fatalf("the complete Core account omitted %s, but the old proxy member survived", key)
		}
	}
	for _, key := range []string{"totalGB", "expiryTime", "reset", "limitIp"} {
		if request[key] != 0 {
			t.Fatalf("Core-owned lifecycle member %s remains upstream-controlled", key)
		}
	}
	if existing["enable"] != false || account["comment"] != nil {
		t.Fatal("merge mutated its input objects")
	}
}

func TestRuntimeRestoreClientOrderReenableUsesOriginalIDWithoutOtherArrayChanges(t *testing.T) {
	const before = `{"inbounds":[{"tag":"other","settings":{"clients":[{"email":"untouched"}]}},
		{"tag":"owned","protocol":"vless","settings":{"clients":[{"id":"control-id","email":"control@credential"},{"id":"third-id","email":"third@credential"}]}}],
		"routing":{"rules":[{"domain":["second","first"]},{"domain":["last"]}]},"outbounds":[{"tag":"z"},{"tag":"a"}]}`
	account := map[string]any{"id": "target-id", "email": "target@credential"}
	want, _, existed, err := runtimeClientConfig([]byte(before), "owned", "target@credential", account)
	if err != nil || existed {
		t.Fatalf("disabled target fixture did not append the account: %v", err)
	}
	got, err := runtimeRestoreClientOrder(want, "owned", "target@credential", []GlobalClient{
		{ID: 9, Email: "third@credential"}, {ID: 7, Email: "target@credential"}, {ID: 8, Email: "control@credential"},
	})
	if err != nil {
		t.Fatal(err)
	}
	const expected = `{"inbounds":[{"tag":"other","settings":{"clients":[{"email":"untouched"}]}},
		{"tag":"owned","protocol":"vless","settings":{"clients":[{"id":"target-id","email":"target@credential"},{"id":"control-id","email":"control@credential"},{"id":"third-id","email":"third@credential"}]}}],
		"routing":{"rules":[{"domain":["second","first"]},{"domain":["last"]}]},"outbounds":[{"tag":"z"},{"tag":"a"}]}`
	comparison, err := CompareConfig([]byte(expected), got)
	if err != nil || !comparison.Equivalent {
		t.Fatal("reenable did not restore the target's ID position or changed an unrelated array")
	}
}

func TestRuntimeRestoreClientOrderRejectsAmbiguousNumericIdentity(t *testing.T) {
	const raw = `{"inbounds":[{"tag":"owned","settings":{"clients":[{"email":"control@credential"},{"email":"target@credential"}]}}]}`
	got, err := runtimeRestoreClientOrder([]byte(raw), "owned", "target@credential", []GlobalClient{
		{ID: 7, Email: "control@credential"}, {ID: 7, Email: "target@credential"},
	})
	if got != nil || !errors.Is(err, runtimeprovider.ErrOperationNotExecuted) {
		t.Fatal("ambiguous numeric identities produced a reordered configuration")
	}
}
