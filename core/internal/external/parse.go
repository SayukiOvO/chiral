// Package external turns somebody else's subscription into proxies this panel
// can hand on.
//
// These are nodes no agent runs on: a provider gives you a link and that is
// all you get. They differ from the fleet in ways that matter and cannot be
// papered over — one credential shared by every subscriber, no per-user
// isolation, no traffic accounting, and disabling a user does not stop them
// using it. What the panel can do is carry them alongside its own nodes, and
// route them through one of its own on the way out.
package external

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Proxy is one node from an external source, kept in the shape clash-family
// clients want because that is the format every source can express and the one
// subscribers actually receive.
type Proxy struct {
	Name string
	Type string
	// Server and Port identify the endpoint. Kept separately from Config so a
	// refresh can recognise a proxy that was renamed — providers rename
	// constantly, usually to show expiry or remaining traffic in the label.
	Server string
	Port   int
	// Config is the whole proxy mapping, ready to emit as one YAML list item.
	Config map[string]any
}

// Parse reads a subscription body in whichever form the provider sent.
//
// Sources are inconsistent about this and none of them say which they are
// using, so the format is detected rather than configured: a Clash document
// has a proxies: key, and everything else is a list of share links, possibly
// base64-encoded as a whole.
// Parse returns the proxies it could read and a description of each one it
// could not, so a partly-readable source is served AND reported.
func Parse(body string) ([]Proxy, []string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, nil, fmt.Errorf("empty subscription")
	}
	if proxies, err := parseClash(body); err == nil && len(proxies) > 0 {
		return proxies, nil, nil
	}
	// A whole-body base64 blob is what v2rayN-style subscriptions serve.
	if decoded, ok := decodeBase64Body(body); ok {
		body = decoded
	}
	proxies, skipped, err := parseLinks(body)
	if err != nil {
		return nil, nil, err
	}
	if len(proxies) == 0 {
		return nil, skipped, fmt.Errorf("no proxies found; expected a Clash document or a list of share links")
	}
	return proxies, skipped, nil
}

func parseClash(body string) ([]Proxy, error) {
	var doc struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		return nil, err
	}
	out := make([]Proxy, 0, len(doc.Proxies))
	for _, m := range doc.Proxies {
		p, err := fromMap(m)
		if err != nil {
			// One malformed entry is not worth losing the rest: providers ship
			// proxy types this parser has never heard of, and the others still
			// work.
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func fromMap(m map[string]any) (Proxy, error) {
	name, _ := m["name"].(string)
	typ, _ := m["type"].(string)
	server, _ := m["server"].(string)
	if name == "" || typ == "" || server == "" {
		return Proxy{}, fmt.Errorf("proxy is missing name, type or server")
	}
	port, err := toInt(m["port"])
	if err != nil {
		return Proxy{}, fmt.Errorf("proxy %q: %w", name, err)
	}
	return Proxy{Name: name, Type: typ, Server: server, Port: port, Config: m}, nil
}

func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	case string:
		return strconv.Atoi(n)
	}
	return 0, fmt.Errorf("port %v is not a number", v)
}

// decodeBase64Body reports whether the whole body is one base64 blob, as
// v2rayN-style subscriptions serve. Both alphabets and both padding
// conventions are in the wild.
func decodeBase64Body(body string) (string, bool) {
	compact := strings.Join(strings.Fields(body), "")
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(compact); err == nil && strings.Contains(string(b), "://") {
			return string(b), true
		}
	}
	return "", false
}

// parseLinks reads one share link per line.
//
// Skipped links are counted and described rather than merely dropped. A source
// that quietly yields seven nodes out of ten looks exactly like a source with
// seven nodes, and the operator has no way to tell which they are looking at.
func parseLinks(body string) ([]Proxy, []string, error) {
	var out []Proxy
	var skipped []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p, err := ParseLink(line)
		if err != nil {
			// Same reasoning as the Clash path: skip what cannot be read
			// rather than refuse the whole subscription for it.
			skipped = append(skipped, describeSkip(line, err))
			continue
		}
		out = append(out, p)
	}
	return out, skipped, nil
}

// describeSkip names the link without quoting it — a share link is a working
// credential, and this string reaches the console and the logs.
func describeSkip(link string, err error) string {
	name := "?"
	if u, e := url.Parse(link); e == nil {
		if frag := strings.TrimSpace(u.Fragment); frag != "" {
			name = frag
		} else if u.Hostname() != "" {
			name = u.Hostname()
		}
	}
	return fmt.Sprintf("%s: %v", name, err)
}

// ParseLink converts one share link into a clash-shaped proxy.
func ParseLink(link string) (Proxy, error) {
	switch {
	case strings.HasPrefix(link, "vless://"):
		return parseVLESS(link)
	case strings.HasPrefix(link, "trojan://"):
		return parseTrojan(link)
	case strings.HasPrefix(link, "vmess://"):
		return parseVMess(link)
	case strings.HasPrefix(link, "ss://"):
		return parseShadowsocks(link)
	}
	return Proxy{}, fmt.Errorf("unsupported link scheme")
}

// name falls back to host:port so a nameless link still identifies itself.
func linkName(u *url.URL, host string, port int) string {
	if frag := strings.TrimSpace(u.Fragment); frag != "" {
		return frag
	}
	return fmt.Sprintf("%s:%d", host, port)
}

func hostPort(u *url.URL) (string, int, error) {
	host := u.Hostname()
	if host == "" {
		return "", 0, fmt.Errorf("no host")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 {
		return "", 0, fmt.Errorf("no port")
	}
	return host, port, nil
}

func parseVLESS(link string) (Proxy, error) {
	u, err := url.Parse(link)
	if err != nil {
		return Proxy{}, err
	}
	host, port, err := hostPort(u)
	if err != nil {
		return Proxy{}, err
	}
	q := u.Query()
	cfg := map[string]any{
		"name": linkName(u, host, port), "type": "vless",
		"server": host, "port": port, "uuid": u.User.Username(), "udp": true,
	}
	if flow := q.Get("flow"); flow != "" {
		cfg["flow"] = flow
	}
	switch q.Get("security") {
	case "tls":
		cfg["tls"] = true
	case "reality":
		cfg["tls"] = true
		reality := map[string]any{"public-key": q.Get("pbk")}
		if sid := q.Get("sid"); sid != "" {
			reality["short-id"] = sid
		}
		cfg["reality-opts"] = reality
	}
	if sni := q.Get("sni"); sni != "" {
		cfg["servername"] = sni
	}
	if fp := q.Get("fp"); fp != "" {
		cfg["client-fingerprint"] = fp
	}
	if err := applyTransport(cfg, q); err != nil {
		return Proxy{}, err
	}
	return fromMap(cfg)
}

func parseTrojan(link string) (Proxy, error) {
	u, err := url.Parse(link)
	if err != nil {
		return Proxy{}, err
	}
	host, port, err := hostPort(u)
	if err != nil {
		return Proxy{}, err
	}
	q := u.Query()
	cfg := map[string]any{
		"name": linkName(u, host, port), "type": "trojan",
		"server": host, "port": port, "password": u.User.Username(), "udp": true,
	}
	if sni := q.Get("sni"); sni != "" {
		cfg["sni"] = sni
	}
	if err := applyTransport(cfg, q); err != nil {
		return Proxy{}, err
	}
	return fromMap(cfg)
}

func parseVMess(link string) (Proxy, error) {
	raw := strings.TrimPrefix(link, "vmess://")
	// Not decodeBase64Body: that one requires "://" in the result, which is
	// how a base64 blob of share links is told apart from one that merely
	// decodes. A vmess payload is JSON and contains no such thing.
	decoded, ok := decodeBase64Raw(strings.Join(strings.Fields(raw), ""))
	if !ok {
		return Proxy{}, fmt.Errorf("vmess payload is not base64")
	}
	var v struct {
		PS   string `json:"ps"`
		Add  string `json:"add"`
		Port any    `json:"port"`
		ID   string `json:"id"`
		Aid  any    `json:"aid"`
		Net  string `json:"net"`
		Host string `json:"host"`
		Path string `json:"path"`
		TLS  string `json:"tls"`
		SNI  string `json:"sni"`
	}
	if err := json.Unmarshal([]byte(decoded), &v); err != nil {
		return Proxy{}, err
	}
	port, err := toInt(v.Port)
	if err != nil {
		return Proxy{}, err
	}
	aid, _ := toInt(v.Aid)
	name := v.PS
	if name == "" {
		name = fmt.Sprintf("%s:%d", v.Add, port)
	}
	cfg := map[string]any{
		"name": name, "type": "vmess", "server": v.Add, "port": port,
		"uuid": v.ID, "alterId": aid, "cipher": "auto", "udp": true,
	}
	if v.TLS == "tls" {
		cfg["tls"] = true
		if v.SNI != "" {
			cfg["servername"] = v.SNI
		} else if v.Host != "" {
			cfg["servername"] = v.Host
		}
	}
	if v.Net == "ws" {
		cfg["network"] = "ws"
		ws := map[string]any{}
		if v.Path != "" {
			ws["path"] = v.Path
		}
		if v.Host != "" {
			ws["headers"] = map[string]any{"Host": v.Host}
		}
		if len(ws) > 0 {
			cfg["ws-opts"] = ws
		}
	}
	return fromMap(cfg)
}

func parseShadowsocks(link string) (Proxy, error) {
	rest := strings.TrimPrefix(link, "ss://")
	frag := ""
	if i := strings.Index(rest, "#"); i >= 0 {
		frag, rest = rest[i+1:], rest[:i]
	}
	if i := strings.Index(rest, "?"); i >= 0 {
		rest = rest[:i]
	}
	// Either method:password is base64 and the host is plain, or the whole
	// userinfo@host:port is one blob. Both forms are served in the wild.
	var method, password, host string
	var port int
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		userinfo, hostport := rest[:at], rest[at+1:]
		if dec, ok := decodeBase64Pair(userinfo); ok {
			method, password = dec[0], dec[1]
		} else if m, p, found := strings.Cut(userinfo, ":"); found {
			method, password = m, p
		}
		h, ps, found := strings.Cut(hostport, ":")
		if !found {
			return Proxy{}, fmt.Errorf("no port")
		}
		host = h
		n, err := strconv.Atoi(ps)
		if err != nil {
			return Proxy{}, err
		}
		port = n
	} else {
		whole, ok := decodeBase64Raw(strings.Join(strings.Fields(rest), ""))
		if !ok {
			return Proxy{}, fmt.Errorf("not a shadowsocks link")
		}
		at := strings.LastIndex(whole, "@")
		if at < 0 {
			return Proxy{}, fmt.Errorf("not a shadowsocks link")
		}
		if m, p, found := strings.Cut(whole[:at], ":"); found {
			method, password = m, p
		}
		h, ps, found := strings.Cut(whole[at+1:], ":")
		if !found {
			return Proxy{}, fmt.Errorf("no port")
		}
		host = h
		n, err := strconv.Atoi(ps)
		if err != nil {
			return Proxy{}, err
		}
		port = n
	}
	if method == "" || host == "" {
		return Proxy{}, fmt.Errorf("incomplete shadowsocks link")
	}
	name := frag
	if unescaped, err := url.QueryUnescape(frag); err == nil {
		name = unescaped
	}
	if name == "" {
		name = fmt.Sprintf("%s:%d", host, port)
	}
	return fromMap(map[string]any{
		"name": name, "type": "ss", "server": host, "port": port,
		"cipher": method, "password": password, "udp": true,
	})
}

func decodeBase64Pair(s string) ([2]string, bool) {
	dec, ok := decodeBase64Raw(s)
	if !ok {
		return [2]string{}, false
	}
	m, p, found := strings.Cut(dec, ":")
	if !found {
		return [2]string{}, false
	}
	return [2]string{m, p}, true
}

func decodeBase64Raw(s string) (string, bool) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), true
		}
	}
	return "", false
}

// applyTransport maps the transport query parameters share links carry.
//
// An unrecognised transport is an ERROR, not something to leave out. This
// function used to handle ws and grpc and silently ignore everything else,
// which did not produce a node without a transport — it produced a node
// claiming to be plain TCP. A vless+REALITY entry over xhttp came out as a
// well-formed TCP proxy that every check passes: the panel lists it, mihomo
// loads the subscription, `xray -test` has nothing to say about it, and the
// subscriber gets a timeout on a node the console insists is fine. Failing here
// costs the operator one skipped node with a reason attached; the alternative
// costs them a support conversation with no evidence in it.
func applyTransport(cfg map[string]any, q url.Values) error {
	switch t := q.Get("type"); t {
	case "", "tcp", "raw":
		// The default. `headerType=http` disguises it as HTTP/1.1, which
		// mihomo spells as its own network rather than an option of tcp.
		if q.Get("headerType") == "http" {
			cfg["network"] = "http"
			opts := map[string]any{}
			if path := q.Get("path"); path != "" {
				opts["path"] = []string{path}
			}
			if host := q.Get("host"); host != "" {
				opts["headers"] = map[string]any{"Host": host}
			}
			if len(opts) > 0 {
				cfg["http-opts"] = opts
			}
		}
	case "ws":
		cfg["network"] = "ws"
		ws := map[string]any{}
		if path := q.Get("path"); path != "" {
			ws["path"] = path
		}
		if host := q.Get("host"); host != "" {
			ws["headers"] = map[string]any{"Host": host}
		}
		if len(ws) > 0 {
			cfg["ws-opts"] = ws
		}
	case "httpupgrade":
		// Not a network of its own in mihomo — a flag on the websocket one.
		cfg["network"] = "ws"
		ws := map[string]any{"v2ray-http-upgrade": true}
		if path := q.Get("path"); path != "" {
			ws["path"] = path
		}
		if host := q.Get("host"); host != "" {
			ws["headers"] = map[string]any{"Host": host}
		}
		cfg["ws-opts"] = ws
	case "grpc":
		cfg["network"] = "grpc"
		if svc := q.Get("serviceName"); svc != "" {
			cfg["grpc-opts"] = map[string]any{"grpc-service-name": svc}
		}
	case "http", "h2":
		cfg["network"] = "h2"
		opts := map[string]any{}
		if path := q.Get("path"); path != "" {
			opts["path"] = path
		}
		if host := q.Get("host"); host != "" {
			opts["host"] = strings.Split(host, ",")
		}
		if len(opts) > 0 {
			cfg["h2-opts"] = opts
		}
	case "xhttp", "splithttp":
		// The transport the panel was flattening. `extra` is a JSON blob of
		// tuning the provider chose (padding sizes, xmux limits); it is passed
		// through rather than interpreted, because mihomo understands more of
		// it than this panel needs to.
		cfg["network"] = "xhttp"
		opts := map[string]any{}
		if path := q.Get("path"); path != "" {
			opts["path"] = path
		}
		if host := q.Get("host"); host != "" {
			opts["host"] = host
		}
		if mode := q.Get("mode"); mode != "" {
			opts["mode"] = mode
		}
		if extra := q.Get("extra"); extra != "" {
			var v any
			if err := json.Unmarshal([]byte(extra), &v); err == nil {
				opts["extra"] = v
			}
		}
		if len(opts) > 0 {
			cfg["xhttp-opts"] = opts
		}
	default:
		return fmt.Errorf("transport %q is not supported", t)
	}
	return nil
}

// RenderYAML emits a proxy as one item of a Clash proxies list.
//
// dialerProxy, when set, is the name of another proxy this one is reached
// through — mihomo's dialer-proxy. That is the chaining: the external node is
// dialled from a node of this fleet rather than from the subscriber.
//
// Emitted by hand rather than through yaml.Marshal, for the same reason the
// rest of this panel writes its YAML by hand. yaml.v3 escapes astral-plane
// characters into \U form, and every provider on earth puts flag emoji in node
// names — which produced a document whose proxy was named with an escape and
// whose proxy-group member was the literal backslashes, so the client reported
// the proxy as not found and refused the whole configuration.
func RenderYAML(p Proxy, dialerProxy string) (string, error) {
	cfg := make(map[string]any, len(p.Config)+1)
	for k, v := range p.Config {
		cfg[k] = v
	}
	if dialerProxy != "" {
		cfg["dialer-proxy"] = dialerProxy
	}
	var b strings.Builder
	if err := writeMapping(&b, cfg, 0); err != nil {
		return "", err
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// writeMapping emits a mapping with keys in sorted order, so the same proxy
// renders identically every time. A subscription whose byte order shifts on
// each fetch looks like a change to every client that diffs it.
func writeMapping(b *strings.Builder, m map[string]any, depth int) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pad := strings.Repeat("  ", depth)
	for _, k := range keys {
		switch v := m[k].(type) {
		case map[string]any:
			fmt.Fprintf(b, "%s%s:\n", pad, k)
			if err := writeMapping(b, v, depth+1); err != nil {
				return err
			}
		case map[any]any:
			conv := make(map[string]any, len(v))
			for kk, vv := range v {
				conv[fmt.Sprint(kk)] = vv
			}
			fmt.Fprintf(b, "%s%s:\n", pad, k)
			if err := writeMapping(b, conv, depth+1); err != nil {
				return err
			}
		case []any:
			fmt.Fprintf(b, "%s%s:\n", pad, k)
			for _, item := range v {
				fmt.Fprintf(b, "%s  - %s\n", pad, scalar(item))
			}
		default:
			fmt.Fprintf(b, "%s%s: %s\n", pad, k, scalar(v))
		}
	}
	return nil
}

// scalar renders one value, quoting only when YAML would otherwise read it as
// something else. Never escapes UTF-8: the characters go out as themselves.
func scalar(v any) string {
	switch t := v.(type) {
	case nil:
		return `""`
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case string:
		return quoteScalar(t)
	}
	return quoteScalar(fmt.Sprint(v))
}

// quoteScalar wraps a string when leaving it bare would change its meaning.
//
// Single quotes, because YAML processes no escapes inside them beyond the
// doubled quote — so a name full of emoji, colons and backslashes goes out
// exactly as it came in.
func quoteScalar(s string) string {
	if s == "" {
		return `""`
	}
	needsQuote := strings.ContainsAny(s, ":#{}[],&*?|<>=!%@`\"'\\\n\t") ||
		strings.TrimSpace(s) != s
	if !needsQuote {
		switch strings.ToLower(s) {
		case "true", "false", "null", "yes", "no", "on", "off", "~":
			needsQuote = true
		}
		if !needsQuote {
			if _, err := strconv.ParseFloat(s, 64); err == nil {
				needsQuote = true
			}
		}
	}
	if !needsQuote {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
