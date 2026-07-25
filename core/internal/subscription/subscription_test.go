package subscription

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/template"
)

// --- client detection ---

func TestExplicitClientWins(t *testing.T) {
	r := httptest.NewRequest("GET", "/sub/tok?client=clash", nil)
	r.Header.Set("User-Agent", "v2rayN/6.0")
	if got := DetectClient(r); got != ClientClash {
		t.Errorf("?client= should win over the UA, got %q", got)
	}
}

func TestUnknownExplicitClientFallsBackToTheUA(t *testing.T) {
	r := httptest.NewRequest("GET", "/sub/tok?client=nonsense", nil)
	r.Header.Set("User-Agent", "Stash/2.0")
	if got := DetectClient(r); got != ClientStash {
		t.Errorf("got %q", got)
	}
}

func TestUserAgentDetection(t *testing.T) {
	cases := map[string]string{
		"clash-verge/1.5":       ClientClash,
		"mihomo/1.18":           ClientClash,
		"ClashMetaForAndroid":   ClientClash,
		"Stash/2.6.0 (like -)":  ClientStash,
		"v2rayN/6.31":           ClientVlessURI,
		"v2rayNG/1.8":           ClientVlessURI,
		"NekoBox/1.0":           ClientVlessURI,
		"Mozilla/5.0 (Firefox)": ClientXrayJSON,
		"":                      ClientXrayJSON,
	}
	for ua, want := range cases {
		if got := ClientForUserAgent(ua); got != want {
			t.Errorf("%q -> %q, want %q", ua, got, want)
		}
	}
}

// Stash reports a UA that also mentions clash in some builds, so the more
// specific match has to be tried first.
func TestStashBeatsClashWhenBothMatch(t *testing.T) {
	if got := ClientForUserAgent("Stash/2.0 clash-compatible"); got != ClientStash {
		t.Errorf("got %q, want stash", got)
	}
}

// --- assembly ---

func TestVlessURIsAreOnePerLine(t *testing.T) {
	r := assemble(ClientVlessURI, []string{"vless://a@h:443#one", "vless://b@h:443#two"})
	if r.Body != "vless://a@h:443#one\nvless://b@h:443#two" {
		t.Errorf("got %q", r.Body)
	}
	if !strings.HasPrefix(r.ContentType, "text/plain") {
		t.Errorf("content type %q", r.ContentType)
	}
}

func TestXrayJSONIsAValidDocument(t *testing.T) {
	r := assemble(ClientXrayJSON, []string{`{"tag":"a","protocol":"vless"}`, `{"tag":"b","protocol":"vless"}`})
	var parsed struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err := jsonUnmarshal(r.Body, &parsed); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, r.Body)
	}
	if len(parsed.Outbounds) != 2 || parsed.Outbounds[0].Tag != "a" {
		t.Errorf("got %+v", parsed.Outbounds)
	}
}

func TestXrayJSONWithOneFragmentHasNoTrailingComma(t *testing.T) {
	r := assemble(ClientXrayJSON, []string{`{"tag":"only"}`})
	var parsed map[string]any
	if err := jsonUnmarshal(r.Body, &parsed); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, r.Body)
	}
}

func TestEmptySubscriptionIsStillValid(t *testing.T) {
	// A user entitled to nothing must not receive a broken file.
	r := assemble(ClientXrayJSON, nil)
	var parsed map[string]any
	if err := jsonUnmarshal(r.Body, &parsed); err != nil {
		t.Errorf("empty xray subscription is not valid JSON: %v\n%s", err, r.Body)
	}
	if r.Fragments != 0 {
		t.Errorf("fragments = %d", r.Fragments)
	}
}

func TestClashDocumentHasProxiesAndAGroup(t *testing.T) {
	r := assemble(ClientClash, []string{
		"name: tokyo-1\ntype: vless\nserver: 203.0.113.9\nport: 443",
		"name: frankfurt-1\ntype: vless\nserver: 198.51.100.7\nport: 443",
	})
	if !strings.HasPrefix(r.Body, "proxies:\n") {
		t.Fatalf("missing proxies section:\n%s", r.Body)
	}
	// Each entry must be a list item, with its continuation lines aligned
	// under it, or the YAML is silently wrong.
	if !strings.Contains(r.Body, "  - name: tokyo-1\n    type: vless\n") {
		t.Errorf("proxy entry is not indented as a list item:\n%s", r.Body)
	}
	// Without a group, clash clients have nothing to select.
	if !strings.Contains(r.Body, "proxy-groups:") ||
		!strings.Contains(r.Body, "      - tokyo-1\n") ||
		!strings.Contains(r.Body, "      - frankfurt-1\n") {
		t.Errorf("proxy group missing or incomplete:\n%s", r.Body)
	}
}

func TestClashGroupIsOmittedWhenThereAreNoProxies(t *testing.T) {
	r := assemble(ClientClash, nil)
	if strings.Contains(r.Body, "proxy-groups:") {
		t.Errorf("an empty subscription should not declare an empty group:\n%s", r.Body)
	}
}

func TestYamlNameHandlesBothForms(t *testing.T) {
	if got := yamlName("name: tokyo-1\ntype: vless"); got != "tokyo-1" {
		t.Errorf("block form: %q", got)
	}
	if got := yamlName(`{name: tokyo-1, type: vless}`); got != "tokyo-1" {
		t.Errorf("inline form: %q", got)
	}
	if got := yamlName(`name: "quoted name"`); got != "quoted name" {
		t.Errorf("quoted: %q", got)
	}
	if got := yamlName("type: vless"); got != "" {
		t.Errorf("no name should yield empty, got %q", got)
	}
}

// The client context must not carry secrets — a template referencing one has
// to fail rather than render it into somebody's subscription.
func TestClientContextRefusesSecrets(t *testing.T) {
	ctx := template.NewContext(map[string]string{
		"reality.private": "PRIVATE-KEY",
		"reality.public":  "PUBLIC-KEY",
	}, []string{"reality.private"}).ForClient()

	if _, err := ctx.Render(`pbk: {{reality.private}}`); err == nil {
		t.Fatal("a client template referencing a private key must fail")
	}
	got, err := ctx.Render(`pbk: {{reality.public}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "pbk: PUBLIC-KEY" {
		t.Errorf("got %q", got)
	}
}

func jsonUnmarshal(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}
