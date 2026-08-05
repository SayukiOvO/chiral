package external

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every one of these is a real share-link shape, taken through the same parser
// a subscriber's node goes through, so the test covers the join between the two
// halves rather than a hand-written clash document that nothing else produces.
var relayCases = []struct {
	name string
	link string
	want map[string]string // json path -> expected value, as a flat probe
}{
	{
		name: "vless reality over xhttp",
		link: "vless://11111111-2222-3333-4444-555555555555@198.51.100.7:15700" +
			"?encryption=none&security=reality&sni=mirror.example.test&fp=chrome" +
			"&pbk=IRAEG4zZjsHZc4O5_A0VFPntX1ZctKpeCHaS-3AHDWo&sid=9a6f92e2d73090" +
			"&type=xhttp&path=%2Fabc&mode=auto#HK",
		want: map[string]string{
			"protocol":                                  "vless",
			"streamSettings.network":                    "xhttp",
			"streamSettings.security":                   "reality",
			"streamSettings.realitySettings.publicKey":  "IRAEG4zZjsHZc4O5_A0VFPntX1ZctKpeCHaS-3AHDWo",
			"streamSettings.realitySettings.shortId":    "9a6f92e2d73090",
			"streamSettings.realitySettings.serverName": "mirror.example.test",
			"streamSettings.xhttpSettings.path":         "/abc",
		},
	},
	{
		name: "vless reality tcp with vision",
		link: "vless://11111111-2222-3333-4444-555555555555@198.51.100.8:443" +
			"?encryption=none&security=reality&sni=a.example&fp=chrome" +
			"&pbk=IRAEG4zZjsHZc4O5_A0VFPntX1ZctKpeCHaS-3AHDWo&sid=ab12&type=tcp&flow=xtls-rprx-vision#V",
		want: map[string]string{
			"protocol":                "vless",
			"streamSettings.security": "reality",
		},
	},
	{
		name: "vless tls over ws",
		link: "vless://11111111-2222-3333-4444-555555555555@198.51.100.9:443" +
			"?encryption=none&security=tls&sni=b.example&type=ws&path=%2Fws&host=b.example#W",
		want: map[string]string{
			"protocol":                       "vless",
			"streamSettings.network":         "ws",
			"streamSettings.security":        "tls",
			"streamSettings.wsSettings.path": "/ws",
		},
	},
	{
		name: "vless grpc",
		link: "vless://11111111-2222-3333-4444-555555555555@198.51.100.10:443" +
			"?encryption=none&security=tls&sni=c.example&type=grpc&serviceName=svc#G",
		want: map[string]string{
			"streamSettings.network":                  "grpc",
			"streamSettings.grpcSettings.serviceName": "svc",
		},
	},
	{
		name: "trojan",
		link: "trojan://s3cret@198.51.100.11:443?sni=d.example#T",
		want: map[string]string{
			"protocol":                "trojan",
			"streamSettings.security": "tls",
		},
	},
	{
		name: "shadowsocks",
		link: "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ=@198.51.100.12:8388#S",
		want: map[string]string{"protocol": "shadowsocks"},
	},
}

func probe(t *testing.T, blob, path string) string {
	t.Helper()
	var cur any
	if err := json.Unmarshal([]byte(blob), &cur); err != nil {
		t.Fatalf("outbound is not JSON: %v\n%s", err, blob)
	}
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[part]
	}
	s, _ := cur.(string)
	return s
}

func TestRelayOutboundsFromRealLinks(t *testing.T) {
	for _, tc := range relayCases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseLink(tc.link)
			if err != nil {
				t.Fatalf("ParseLink: %v", err)
			}
			clash, err := RenderYAML(p, "")
			if err != nil {
				t.Fatal(err)
			}
			ob, err := XrayOutbound(clash, "exit")
			if err != nil {
				t.Fatalf("XrayOutbound: %v\nclash:\n%s", err, clash)
			}
			if got := probe(t, ob, "tag"); got != "exit" {
				t.Errorf("tag = %q", got)
			}
			for path, want := range tc.want {
				if got := probe(t, ob, path); got != want {
					t.Errorf("%s = %q, want %q\n%s", path, got, want, ob)
				}
			}
		})
	}
}

// vmess arrives base64-encoded rather than as query parameters, so it does not
// share a code path with the others.
func TestVMessRelayOutbound(t *testing.T) {
	// {"v":"2","ps":"M","add":"198.51.100.13","port":"443","id":"1111...","aid":"0",
	//  "net":"ws","host":"e.example","path":"/p","tls":"tls"}
	link := "vmess://eyJ2IjoiMiIsInBzIjoiTSIsImFkZCI6IjE5OC41MS4xMDAuMTMiLCJwb3J0IjoiNDQzIiwiaWQiOiIxMTExMTExMS0yMjIyLTMzMzMtNDQ0NC01NTU1NTU1NTU1NTUiLCJhaWQiOiIwIiwibmV0Ijoid3MiLCJob3N0IjoiZS5leGFtcGxlIiwicGF0aCI6Ii9wIiwidGxzIjoidGxzIn0="
	p, err := ParseLink(link)
	if err != nil {
		t.Fatalf("ParseLink: %v", err)
	}
	clash, _ := RenderYAML(p, "")
	ob, err := XrayOutbound(clash, "exit")
	if err != nil {
		t.Fatalf("XrayOutbound: %v\nclash:\n%s", err, clash)
	}
	for path, want := range map[string]string{
		"protocol":                       "vmess",
		"streamSettings.network":         "ws",
		"streamSettings.security":        "tls",
		"streamSettings.wsSettings.path": "/p",
	} {
		if got := probe(t, ob, path); got != want {
			t.Errorf("%s = %q, want %q\n%s", path, got, want, ob)
		}
	}
}

// Xray 26.x removed the HTTP/2 transport, so an h2 provider cannot be relayed
// at all. Saying so is the whole job here: the alternative is an outbound that
// loads and then cannot reach the provider.
func TestH2CannotBeARelayExit(t *testing.T) {
	clash := "name: X\ntype: vless\nserver: a.example\nport: 443\nuuid: u\ntls: true\nnetwork: h2\n"
	_, err := XrayOutbound(clash, "exit")
	if err == nil {
		t.Fatal("an h2 provider was converted into an exit anyway")
	}
	if !strings.Contains(err.Error(), "h2") {
		t.Fatalf("the reason does not name the transport: %v", err)
	}
}

// A transport this converter does not understand must be an error. The quiet
// alternative is an outbound that validates, starts, and cannot connect — and
// as an exit it takes every subscriber on that access configuration with it.
func TestAnUnknownTransportWillNotBeGuessedAt(t *testing.T) {
	clash := "name: X\ntype: vless\nserver: a.example\nport: 443\nuuid: u\nnetwork: quic-not-real\n"
	if _, err := XrayOutbound(clash, "exit"); err == nil {
		t.Fatal("an unconvertible transport produced an outbound anyway")
	}
	clash = "name: X\ntype: hysteria2\nserver: a.example\nport: 443\n"
	if _, err := XrayOutbound(clash, "exit"); err == nil {
		t.Fatal("an unconvertible protocol produced an outbound anyway")
	}
}

// The only authority on whether these outbounds are valid is the binary that
// has to load them. Everything above checks that the translation says what was
// intended; this checks that Xray accepts what was said.
//
// Skipped when no binary is around, and loudly — a validation that silently
// does not run is worse than no validation, because it reads as a pass.
func TestXrayItselfAcceptsEveryConvertedOutbound(t *testing.T) {
	bin := findXray(t)
	if bin == "" {
		t.Skip("no xray binary on PATH or in the usual places; conversion not validated against the kernel")
	}
	var obs []json.RawMessage
	for _, tc := range relayCases {
		p, err := ParseLink(tc.link)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		clash, err := RenderYAML(p, "")
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		ob, err := XrayOutbound(clash, "exit-"+strings.ReplaceAll(tc.name, " ", "-"))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		obs = append(obs, json.RawMessage(ob))
	}

	cfg, err := json.MarshalIndent(map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag": "in", "listen": "127.0.0.1", "port": 11080,
			"protocol": "socks", "settings": map[string]any{"udp": true},
		}},
		"outbounds": obs,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "-test", "-config", path).CombinedOutput()
	if err != nil {
		t.Fatalf("xray -test rejected the converted outbounds: %v\n%s\n%s", err, out, cfg)
	}
	// Positive assertion: xray says so in as many words. Exit status alone has
	// been wrong before in this repo's history.
	if !strings.Contains(string(out), "Configuration OK") {
		t.Fatalf("xray did not confirm the configuration:\n%s", out)
	}
	t.Logf("xray accepted %d converted outbounds", len(obs))
}

func findXray(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("xray"); err == nil {
		return p
	}
	for _, p := range []string{"/usr/local/bin/xray", "/opt/homebrew/bin/xray"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
