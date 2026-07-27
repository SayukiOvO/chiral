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

// --- regressions from the M3 adversarial review ---

// A clash proxy entry routinely nests (reality-opts, ws-opts). Flattening
// every line to one depth reparents those keys onto the proxy itself: still
// valid YAML, but a different and broken config.
func TestNestedProxyOptionsKeepTheirStructure(t *testing.T) {
	fragment := strings.Join([]string{
		"name: tokyo-1",
		"type: vless",
		"server: 203.0.113.9",
		"port: 443",
		"reality-opts:",
		"  public-key: PUBKEY",
		"  short-id: a1fcb027",
		"client-fingerprint: chrome",
	}, "\n")
	body := assemble(ClientClash, []string{fragment}).Body

	// The nested keys must stay deeper than the key that introduces them.
	depth := func(needle string) int {
		for _, l := range strings.Split(body, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), needle) {
				return len(l) - len(strings.TrimLeft(l, " "))
			}
		}
		return -1
	}
	opts, pub := depth("reality-opts:"), depth("public-key:")
	if opts < 0 || pub < 0 {
		t.Fatalf("keys missing from output:\n%s", body)
	}
	if pub <= opts {
		t.Errorf("nested key was flattened to the parent's depth (reality-opts=%d public-key=%d):\n%s",
			opts, pub, body)
	}
	// A sibling of reality-opts must not be swallowed into it.
	if fp := depth("client-fingerprint:"); fp != opts {
		t.Errorf("sibling key ended up at the wrong depth (%d, want %d):\n%s", fp, opts, body)
	}
}

// Templates may be written with their own leading indentation; the entry
// should still be anchored correctly under the list item.
func TestIndentedFragmentIsReanchored(t *testing.T) {
	fragment := "    name: tokyo-1\n    type: vless\n    reality-opts:\n      public-key: K"
	body := assemble(ClientClash, []string{fragment}).Body
	if !strings.Contains(body, "  - name: tokyo-1\n") {
		t.Errorf("entry not anchored as a list item:\n%s", body)
	}
	if !strings.Contains(body, "    type: vless\n") {
		t.Errorf("sibling key at the wrong depth:\n%s", body)
	}
	if !strings.Contains(body, "      public-key: K\n") {
		t.Errorf("nested key at the wrong depth:\n%s", body)
	}
}

func TestBlankLinesInFragmentsAreDropped(t *testing.T) {
	body := assemble(ClientClash, []string{"name: a\n\ntype: vless\n"}).Body
	if strings.Contains(body, "\n\n") {
		t.Errorf("blank line survived into the document:\n%s", body)
	}
}

// Latency-based selection alongside the manual one, not instead of it.
//
// The manual group must stay: a subscriber who has worked out which node is
// good for them — often for reasons a latency probe cannot see, like which one
// their bank tolerates — must not have that quietly replaced.
func TestClashOffersBothManualAndAutomaticGroups(t *testing.T) {
	r := assemble(ClientClash, []string{
		"name: tokyo-1\ntype: vless\nserver: 203.0.113.9\nport: 443",
		"name: frankfurt-1\ntype: vless\nserver: 198.51.100.7\nport: 443",
	})

	if !strings.Contains(r.Body, "  - name: Chiral\n    type: select\n") {
		t.Errorf("the manual group is gone:\n%s", r.Body)
	}
	if !strings.Contains(r.Body, "type: url-test") {
		t.Errorf("no automatic group:\n%s", r.Body)
	}
	// Nested, so "auto" is a choice inside the one control the user already
	// knows about rather than a second control they have to discover.
	manual := r.Body[strings.Index(r.Body, "  - name: Chiral\n"):]
	if end := strings.Index(manual, "  - name: Chiral 自动"); end >= 0 {
		manual = manual[:end]
	}
	if !strings.Contains(manual, "      - Chiral 自动\n") {
		t.Errorf("the automatic group is not a member of the manual one:\n%s", manual)
	}
	// Every node belongs to both.
	for _, name := range []string{"tokyo-1", "frankfurt-1"} {
		if strings.Count(r.Body, "      - "+name+"\n") != 2 {
			t.Errorf("%s does not appear in both groups:\n%s", name, r.Body)
		}
	}
	// A url-test group with no probe URL silently never tests anything.
	if !strings.Contains(r.Body, "url: "+autoTestURL) {
		t.Errorf("the automatic group has no test URL:\n%s", r.Body)
	}
	if !strings.Contains(r.Body, "interval: 300") {
		t.Errorf("the automatic group has no interval:\n%s", r.Body)
	}
}
