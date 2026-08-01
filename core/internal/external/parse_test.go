package external

import (
	"encoding/base64"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Providers do not say which format they serve, and the same provider changes
// it between plans. Detection is the whole job.
func TestDetectsClashDocuments(t *testing.T) {
	body := `
port: 7890
proxies:
  - name: "🇭🇰 香港 01 | 剩余 82%"
    type: vless
    server: hk1.example.com
    port: 443
    uuid: 11111111-2222-3333-4444-555555555555
    tls: true
    servername: hk1.example.com
  - {name: "JP 02", type: trojan, server: jp2.example.com, port: 8443, password: hunter2}
proxy-groups:
  - name: PROXY
    type: select
    proxies: ["🇭🇰 香港 01 | 剩余 82%"]
`
	got, _, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("proxies = %d, want 2", len(got))
	}
	if got[0].Name != "🇭🇰 香港 01 | 剩余 82%" || got[0].Server != "hk1.example.com" || got[0].Port != 443 {
		t.Errorf("first = %+v", got[0])
	}
	// Flow style is as common as block style in these files.
	if got[1].Type != "trojan" || got[1].Port != 8443 {
		t.Errorf("second = %+v", got[1])
	}
}

// A proxy type this parser has never heard of must not cost the operator the
// rest of the subscription.
func TestUnreadableEntriesAreSkippedNotFatal(t *testing.T) {
	body := "proxies:\n" +
		"  - {name: fine, type: vless, server: a.example.com, port: 443, uuid: x}\n" +
		"  - {name: broken, type: hysteria2}\n" +
		"  - {name: alsofine, type: ss, server: b.example.com, port: 8388, cipher: aes-128-gcm, password: p}\n"
	got, _, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("proxies = %d, want the two readable ones", len(got))
	}
}

func TestParsesShareLinks(t *testing.T) {
	body := strings.Join([]string{
		"vless://11111111-2222-3333-4444-555555555555@hk.example.com:443?encryption=none&security=reality&sni=www.microsoft.com&fp=chrome&pbk=PUBKEY&sid=abcd&type=tcp&flow=xtls-rprx-vision#HK%20Node",
		"trojan://hunter2@jp.example.com:8443?sni=jp.example.com#JP",
		"ss://YWVzLTEyOC1nY206cGFzc3dvcmQ@sg.example.com:8388#SG",
	}, "\n")
	got, _, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("proxies = %d, want 3", len(got))
	}

	v := got[0]
	if v.Type != "vless" || v.Server != "hk.example.com" || v.Port != 443 {
		t.Errorf("vless = %+v", v)
	}
	if v.Config["flow"] != "xtls-rprx-vision" || v.Config["servername"] != "www.microsoft.com" {
		t.Errorf("vless options lost: %+v", v.Config)
	}
	reality, ok := v.Config["reality-opts"].(map[string]any)
	if !ok || reality["public-key"] != "PUBKEY" || reality["short-id"] != "abcd" {
		t.Errorf("reality options lost: %+v", v.Config["reality-opts"])
	}
	// The fragment is the name and it is percent-encoded.
	if v.Name != "HK Node" {
		t.Errorf("name = %q", v.Name)
	}

	if got[2].Config["cipher"] != "aes-128-gcm" || got[2].Config["password"] != "password" {
		t.Errorf("ss = %+v", got[2].Config)
	}
}

// v2rayN-style subscriptions serve the whole list base64-encoded, with no
// indication that they have done so.
func TestWholeBodyBase64(t *testing.T) {
	plain := "vless://uuid@a.example.com:443#A\ntrojan://pw@b.example.com:443#B\n"
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawURLEncoding} {
		got, _, err := Parse(enc.EncodeToString([]byte(plain)))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("proxies = %d, want 2", len(got))
		}
	}
}

func TestVMess(t *testing.T) {
	payload := `{"ps":"VM","add":"vm.example.com","port":"443","id":"uuid-here","aid":"0","net":"ws","host":"vm.example.com","path":"/ray","tls":"tls"}`
	got, err := ParseLink("vmess://" + base64.StdEncoding.EncodeToString([]byte(payload)))
	if err != nil {
		t.Fatalf("ParseLink: %v", err)
	}
	if got.Name != "VM" || got.Port != 443 || got.Config["network"] != "ws" {
		t.Errorf("vmess = %+v", got.Config)
	}
	// port arrives as a string in most vmess payloads.
	if got.Config["port"] != 443 {
		t.Errorf("port = %v (%T), want the number 443", got.Config["port"], got.Config["port"])
	}
	ws, _ := got.Config["ws-opts"].(map[string]any)
	if ws["path"] != "/ray" {
		t.Errorf("ws options lost: %+v", got.Config["ws-opts"])
	}
}

func TestRejectsWhatIsNotASubscription(t *testing.T) {
	for _, body := range []string{"", "   ", "<html>404</html>", "{}"} {
		if _, _, err := Parse(body); err == nil {
			t.Errorf("accepted %q", body)
		}
	}
}

// The chain is the point of this feature: an external node reached from one of
// this fleet's own nodes rather than from the subscriber.
func TestRenderAddsTheDialerProxy(t *testing.T) {
	p, err := ParseLink("vless://uuid@a.example.com:443#A")
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderYAML(p, "日本 · 东京 01")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "dialer-proxy: 日本 · 东京 01") {
		t.Fatalf("no dialer-proxy in:\n%s", out)
	}
	plain, err := RenderYAML(p, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "dialer-proxy") {
		t.Fatalf("unchained proxy carries a dialer:\n%s", plain)
	}
}

// The same proxy must render identically every time. A subscription whose byte
// order shifts on each fetch looks like a change to every client that diffs it.
func TestRenderIsStable(t *testing.T) {
	p, err := ParseLink("vless://uuid@a.example.com:443?sni=x&fp=chrome#A")
	if err != nil {
		t.Fatal(err)
	}
	first, _ := RenderYAML(p, "")
	for i := 0; i < 20; i++ {
		again, _ := RenderYAML(p, "")
		if again != first {
			t.Fatalf("render %d differs:\n%s\n---\n%s", i, first, again)
		}
	}
}

// Every provider on earth puts flag emoji in node names. yaml.Marshal escapes
// astral-plane characters into \U form, which produced a document whose proxy
// was named with an escape and whose proxy-group member was the literal
// backslashes — the client reported the proxy as not found and refused the
// whole configuration. Caught by a real kernel, not by reading the output.
func TestNamesGoOutAsThemselves(t *testing.T) {
	p, err := ParseLink("vless://uuid@a.example.com:443#%F0%9F%87%AD%F0%9F%87%B0%20HK%2001")
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderYAML(p, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "🇭🇰 HK 01") {
		t.Fatalf("the name was not emitted verbatim:\n%s", out)
	}
	if strings.Contains(out, `\U`) || strings.Contains(out, `\u`) {
		t.Fatalf("the name was escaped:\n%s", out)
	}
}

// A name YAML would read as something else has to be quoted, and quoted in a
// form that processes no escapes — otherwise a backslash in a provider's label
// changes the string.
func TestNamesThatNeedQuoting(t *testing.T) {
	for _, name := range []string{"Tokyo: 01", "12", "no", "  padded  ", `back\slash`, "it's"} {
		p := Proxy{Name: name, Type: "vless", Server: "a", Port: 1,
			Config: map[string]any{"name": name, "type": "vless", "server": "a", "port": 1}}
		out, err := RenderYAML(p, "")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Name string `yaml:"name"`
		}
		if err := yaml.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("%q produced unparseable YAML: %v\n%s", name, err, out)
		}
		if got.Name != name {
			t.Errorf("%q round-tripped as %q", name, got.Name)
		}
	}
}

// Nested mappings are common — reality-opts, ws-opts — and a hand-written
// emitter that flattened them would silently drop the transport.
func TestNestedOptionsSurvive(t *testing.T) {
	p, err := ParseLink("vless://uuid@a.example.com:443?security=reality&pbk=KEY&sid=ab&type=ws&path=/x&host=h#N")
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderYAML(p, "")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := yaml.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unparseable: %v\n%s", err, out)
	}
	reality, _ := got["reality-opts"].(map[string]any)
	if reality["public-key"] != "KEY" || reality["short-id"] != "ab" {
		t.Errorf("reality options lost:\n%s", out)
	}
	ws, _ := got["ws-opts"].(map[string]any)
	if ws["path"] != "/x" {
		t.Errorf("ws options lost:\n%s", out)
	}
}

// The shape that was silently coming out as plain TCP.
//
// A vless+REALITY node over xhttp: the transport lives entirely in the query
// string, and a parser that knows only ws and grpc drops it without a word.
// What reaches the subscriber then is a well-formed proxy pointing at the right
// host and port with the right credential, which every check in this panel
// passes and which cannot connect — the server is listening for xhttp on a
// path. The console shows the node as fine; the customer sees a timeout.
func TestXhttpSurvivesTheRoundTrip(t *testing.T) {
	link := "vless://11111111-2222-3333-4444-555555555555@198.51.100.7:15700" +
		"?encryption=none&security=reality&sni=mirror.example.test&fp=chrome" +
		"&pbk=IRAEG4zZjsHZc4O5_A0VFPntX1ZctKpeCHaS-3AHDWo&sid=9a6f92e2d73090" +
		"&type=xhttp&path=%2F90e41aaa&mode=auto&spx=%2F" +
		"&extra=%7B%22xPaddingBytes%22%3A%22100-1000%22%7D#HK"

	got, skipped, err := Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped: %v", skipped)
	}
	if len(got) != 1 {
		t.Fatalf("proxies = %d", len(got))
	}
	out, err := RenderYAML(got[0], "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"network: xhttp",
		"xhttp-opts:",
		"path: /90e41aaa",
		"mode: auto",
		"xPaddingBytes: 100-1000",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// An unknown transport is reported, not flattened. The node is dropped with a
// reason the operator can act on, rather than served as a proxy that cannot
// work — the failure this panel had was not "a node is missing" but "a node is
// present and broken", and only one of those gets investigated.
func TestAnUnknownTransportIsSkippedWithAReason(t *testing.T) {
	link := "vless://11111111-2222-3333-4444-555555555555@198.51.100.7:443" +
		"?encryption=none&security=tls&type=quic-not-a-real-one#Odd"
	got, skipped, err := Parse(link)
	if err == nil && len(got) != 0 {
		t.Fatalf("an unreadable transport was carried anyway: %+v", got)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "Odd") {
		t.Fatalf("no usable reason recorded: %v", skipped)
	}
	// The reason must not quote the link itself: it is a working credential and
	// it goes to the console and the logs.
	if strings.Contains(skipped[0], "11111111-2222") {
		t.Errorf("the credential leaked into the reason: %q", skipped[0])
	}
}
