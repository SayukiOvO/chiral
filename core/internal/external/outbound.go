package external

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// XrayOutbound converts a stored proxy into an Xray outbound.
//
// Every external proxy is kept in the clash shape, because that is what a
// subscription hands to a subscriber. Relaying one is the other direction:
// the node this panel runs has to DIAL it, and Xray takes its own vocabulary.
// So this is a translation, not a re-render, and it is the only place the two
// vocabularies meet.
//
// An unconvertible proxy is an error rather than a best effort. The failure a
// silent one produces is the same shape as the transport bug this package
// already carries a long comment about: a well-formed outbound that passes
// `xray -test`, starts cleanly, and cannot connect — except now it is the exit
// for a whole access configuration, so every subscriber on it goes dark at
// once with nothing naming the cause.
func XrayOutbound(clashYAML, tag string) (string, error) {
	var p map[string]any
	if err := yaml.Unmarshal([]byte(clashYAML), &p); err != nil {
		return "", fmt.Errorf("stored proxy is not YAML: %w", err)
	}
	server, _ := p["server"].(string)
	port := intOf(p["port"])
	if server == "" || port == 0 {
		return "", fmt.Errorf("proxy has no server:port")
	}

	ob := map[string]any{"tag": tag}
	switch typ, _ := p["type"].(string); typ {
	case "vless":
		user := map[string]any{"id": str(p["uuid"]), "encryption": "none"}
		if flow := str(p["flow"]); flow != "" {
			user["flow"] = flow
		}
		ob["protocol"] = "vless"
		ob["settings"] = map[string]any{"vnext": []any{map[string]any{
			"address": server, "port": port, "users": []any{user},
		}}}
	case "vmess":
		ob["protocol"] = "vmess"
		user := map[string]any{"id": str(p["uuid"]), "alterId": intOf(p["alterId"])}
		if c := str(p["cipher"]); c != "" {
			user["security"] = c
		}
		ob["settings"] = map[string]any{"vnext": []any{map[string]any{
			"address": server, "port": port, "users": []any{user},
		}}}
	case "trojan":
		ob["protocol"] = "trojan"
		ob["settings"] = map[string]any{"servers": []any{map[string]any{
			"address": server, "port": port, "password": str(p["password"]),
		}}}
	case "ss", "shadowsocks":
		ob["protocol"] = "shadowsocks"
		ob["settings"] = map[string]any{"servers": []any{map[string]any{
			"address": server, "port": port,
			"method": str(p["cipher"]), "password": str(p["password"]),
		}}}
	case "":
		return "", fmt.Errorf("proxy has no type")
	default:
		return "", fmt.Errorf("protocol %q cannot be used as a relay exit yet", typ)
	}

	// Clash leaves TLS implicit for trojan — the protocol has no plaintext
	// mode — while Xray wants it declared. Without this the outbound converts
	// cleanly and then speaks plaintext at a TLS listener.
	if typ, _ := p["type"].(string); typ == "trojan" {
		if _, reality := p["reality-opts"]; !reality {
			if _, ok := p["tls"]; !ok {
				p["tls"] = true
			}
		}
	}

	ss, err := streamSettings(p)
	if err != nil {
		return "", err
	}
	if len(ss) > 0 {
		ob["streamSettings"] = ss
	}
	out, err := json.MarshalIndent(ob, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// streamSettings translates the security and transport halves.
func streamSettings(p map[string]any) (map[string]any, error) {
	ss := map[string]any{}

	// Security. REALITY is recognised by its own options rather than by a
	// flag: clash writes `tls: true` for it too, so the presence of
	// reality-opts is the only thing that tells the two apart.
	sni := firstOf(str(p["servername"]), str(p["sni"]))
	fp := str(p["client-fingerprint"])
	if ro, ok := mapOf(p["reality-opts"]); ok {
		r := map[string]any{"serverName": sni, "publicKey": str(ro["public-key"])}
		if sid := str(ro["short-id"]); sid != "" {
			r["shortId"] = sid
		}
		if fp != "" {
			r["fingerprint"] = fp
		}
		ss["security"] = "reality"
		ss["realitySettings"] = r
	} else if b, _ := p["tls"].(bool); b {
		t := map[string]any{}
		if sni != "" {
			t["serverName"] = sni
		}
		if fp != "" {
			t["fingerprint"] = fp
		}
		if v, ok := p["skip-cert-verify"].(bool); ok && v {
			t["allowInsecure"] = true
		}
		if alpn, ok := listOf(p["alpn"]); ok {
			t["alpn"] = alpn
		}
		ss["security"] = "tls"
		ss["tlsSettings"] = t
	}

	// Transport. The names differ on both sides and not symmetrically — clash
	// `http` is HTTP/1.1 obfuscation over TCP while clash `h2` is HTTP/2, and
	// Xray spells those `tcp` with a header and `http` respectively.
	switch net := str(p["network"]); net {
	case "", "tcp":
		if o, ok := mapOf(p["http-opts"]); ok {
			hdr := map[string]any{"type": "http"}
			req := map[string]any{}
			if path, ok := listOf(o["path"]); ok {
				req["path"] = path
			}
			if h, ok := mapOf(o["headers"]); ok && len(h) > 0 {
				req["headers"] = h
			}
			if len(req) > 0 {
				hdr["request"] = req
			}
			ss["network"] = "tcp"
			ss["tcpSettings"] = map[string]any{"header": hdr}
		}
	case "ws":
		ss["network"] = "ws"
		w := map[string]any{}
		if o, ok := mapOf(p["ws-opts"]); ok {
			if path := str(o["path"]); path != "" {
				w["path"] = path
			}
			if h, ok := mapOf(o["headers"]); ok && len(h) > 0 {
				w["headers"] = h
			}
			// mihomo's flag for the HTTP-upgrade variant; Xray spells it as a
			// network of its own.
			if v, ok := o["v2ray-http-upgrade"].(bool); ok && v {
				ss["network"] = "httpupgrade"
				delete(w, "headers")
				ss["httpupgradeSettings"] = w
				return ss, nil
			}
		}
		if len(w) > 0 {
			ss["wsSettings"] = w
		}
	case "grpc":
		ss["network"] = "grpc"
		if o, ok := mapOf(p["grpc-opts"]); ok {
			if svc := str(o["grpc-service-name"]); svc != "" {
				ss["grpcSettings"] = map[string]any{"serviceName": svc}
			}
		}
	case "h2":
		// Xray 26.x removed the HTTP/2 transport outright — "migrated to XHTTP
		// stream-one H2 & H3" — so there is no client side left to dial an h2
		// server with. Rewriting it as xhttp would produce something that
		// builds and cannot speak to the provider, which is the failure this
		// whole file exists to refuse. Found by `xray -test`, not by reading.
		return nil, fmt.Errorf("h2 传输：Xray 26.x 已移除该传输，无法用它做中继出口")
	case "http":
		// Obfuscation over TCP, not HTTP/2 — see above.
		hdr := map[string]any{"type": "http"}
		if o, ok := mapOf(p["http-opts"]); ok {
			req := map[string]any{}
			if path, ok := listOf(o["path"]); ok {
				req["path"] = path
			}
			if h, ok := mapOf(o["headers"]); ok && len(h) > 0 {
				req["headers"] = h
			}
			if len(req) > 0 {
				hdr["request"] = req
			}
		}
		ss["network"] = "tcp"
		ss["tcpSettings"] = map[string]any{"header": hdr}
	case "xhttp", "splithttp":
		ss["network"] = "xhttp"
		x := map[string]any{}
		if o, ok := mapOf(p["xhttp-opts"]); ok {
			if path := str(o["path"]); path != "" {
				x["path"] = path
			}
			if host := str(o["host"]); host != "" {
				x["host"] = host
			}
			if mode := str(o["mode"]); mode != "" {
				x["mode"] = mode
			}
			if extra, ok := o["extra"]; ok && extra != nil {
				x["extra"] = normalise(extra)
			}
		}
		if len(x) > 0 {
			ss["xhttpSettings"] = x
		}
	default:
		return nil, fmt.Errorf("transport %q cannot be used as a relay exit yet", net)
	}
	return ss, nil
}

func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func intOf(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// mapOf accepts both shapes yaml.v3 produces depending on how a document was
// written, so a hand-pasted proxy converts the same as a fetched one.
func mapOf(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, vv := range m {
			out[fmt.Sprint(k)] = vv
		}
		return out, true
	}
	return nil, false
}

// listOf normalises the two ways clash writes a list-valued option: providers
// write `host: example.com` as often as `host: [example.com]`.
func listOf(v any) ([]any, bool) {
	switch l := v.(type) {
	case []any:
		return l, len(l) > 0
	case string:
		if l == "" {
			return nil, false
		}
		return []any{l}, true
	}
	return nil, false
}

// normalise makes a nested value JSON-encodable, since yaml.v3 can hand back
// map[any]any that encoding/json refuses.
func normalise(v any) any {
	switch t := v.(type) {
	case map[any]any, map[string]any:
		m, _ := mapOf(t)
		out := make(map[string]any, len(m))
		for k, vv := range m {
			out[k] = normalise(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = normalise(vv)
		}
		return out
	}
	return v
}
