package xray

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The agent drives Xray's management API through the `xray api` subcommands of
// the very binary it supervises, rather than speaking gRPC to it directly.
// That keeps the wire format guaranteed-compatible with the running kernel and
// avoids pulling xray-core in as a Go dependency.
//
// One measured hazard shapes everything below: `xray api` exit codes only
// reflect TRANSPORT failures. A malformed request prints "Added 0 user(s)" and
// exits 0; removing a user who is not there prints an rpc error and exits 0.
// Trusting the exit code would let Core believe a ban took effect when it did
// not, so every call parses the output and checks the count.
const apiTimeout = 10 * time.Second

var (
	addedRE   = regexp.MustCompile(`Added (\d+) user\(s\)`)
	removedRE = regexp.MustCompile(`Removed (\d+) user\(s\)`)
)

// APIAddress returns the host:port of the API endpoint in the config the agent
// last applied, or "" if it has none. Read from the config rather than assumed,
// so an operator-written api inbound on a different port still works.
func (m *Manager) APIAddress() string {
	raw, err := os.ReadFile(m.configPath)
	if err != nil {
		return ""
	}
	return apiAddressOf(raw)
}

// apiAddressOf mirrors the Core-side helper. The two components share only
// proto/, so this small parser is deliberately duplicated rather than imported
// across the boundary.
func apiAddressOf(configJSON []byte) string {
	var cfg struct {
		API struct {
			Tag string `json:"tag"`
		} `json:"api"`
		Inbounds []struct {
			Tag    string `json:"tag"`
			Listen string `json:"listen"`
			Port   int    `json:"port"`
		} `json:"inbounds"`
		Routing struct {
			Rules []struct {
				InboundTag  []string `json:"inboundTag"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil || cfg.API.Tag == "" {
		return ""
	}
	apiInbounds := map[string]struct{}{}
	for _, r := range cfg.Routing.Rules {
		if r.OutboundTag != cfg.API.Tag {
			continue
		}
		for _, t := range r.InboundTag {
			apiInbounds[t] = struct{}{}
		}
	}
	for _, in := range cfg.Inbounds {
		if _, ok := apiInbounds[in.Tag]; !ok || in.Port == 0 {
			continue
		}
		host := in.Listen
		if host == "" || host == "0.0.0.0" {
			host = "127.0.0.1"
		}
		return fmt.Sprintf("%s:%d", host, in.Port)
	}
	return ""
}

// AddUser installs one client on a live inbound without restarting the kernel.
// accountJSON is the rendered clients[] entry Core sends.
func (m *Manager) AddUser(ctx context.Context, inboundTag, email string, accountJSON []byte) error {
	applied, err := os.ReadFile(m.configPath)
	if err != nil {
		return fmt.Errorf("reading the applied config: %w", err)
	}
	addr := apiAddressOf(applied)
	if addr == "" {
		return fmt.Errorf("no Xray API endpoint in the applied config")
	}
	var account map[string]json.RawMessage
	if err := json.Unmarshal(accountJSON, &account); err != nil {
		return fmt.Errorf("account is not a JSON object: %w", err)
	}
	protocol := inboundProtocol(applied, inboundTag)
	if protocol == "" {
		return fmt.Errorf("inbound %q is not in the applied config", inboundTag)
	}

	// `xray api adu` takes config fragments, and it needs an inbound complete
	// enough to build — a bare {tag, users} is accepted and silently adds
	// nothing. The listen/port here are never bound; they only satisfy the
	// config parser.
	fragment, err := json.Marshal(map[string]any{
		"inbounds": []any{map[string]any{
			"tag":      inboundTag,
			"listen":   "127.0.0.1",
			"port":     1,
			"protocol": protocol,
			"settings": map[string]any{
				"clients":    []json.RawMessage{accountJSON},
				"decryption": "none",
			},
		}},
	})
	if err != nil {
		return err
	}

	dir, err := os.MkdirTemp("", "chiral-adu-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "user.json")
	if err := os.WriteFile(path, fragment, 0o600); err != nil {
		return err
	}

	out, err := m.runAPI(ctx, addr, "adu", path)
	if err != nil {
		return err
	}
	n, ok := countFrom(addedRE, out)
	if !ok {
		return fmt.Errorf("could not tell whether %s was added: %s", email, firstLines(out, 3))
	}
	if n != 1 {
		return fmt.Errorf("adding %s affected %d users: %s", email, n, firstLines(out, 3))
	}
	m.logger.Info("user added online", "inbound", inboundTag, "email", email)
	return nil
}

// RemoveUser takes a client off a live inbound. A user who is already gone is
// not an error: the desired state is "absent" either way, and Core re-sends
// removals when it cannot confirm one.
func (m *Manager) RemoveUser(ctx context.Context, inboundTag, email string) error {
	addr := m.APIAddress()
	if addr == "" {
		return fmt.Errorf("no Xray API endpoint in the applied config")
	}
	out, err := m.runAPI(ctx, addr, "rmu", "-tag="+inboundTag, email)
	if err != nil {
		return err
	}
	n, ok := countFrom(removedRE, out)
	if !ok {
		return fmt.Errorf("could not tell whether %s was removed: %s", email, firstLines(out, 3))
	}
	if n == 0 {
		if userAlreadyAbsent(out) {
			m.logger.Info("user already absent", "inbound", inboundTag, "email", email)
			return nil
		}
		return fmt.Errorf("removing %s from %s affected no users: %s",
			email, inboundTag, firstLines(out, 3))
	}
	m.logger.Info("user removed online", "inbound", inboundTag, "email", email)
	return nil
}

// Stats reads and RESETS Xray's counters, so each call returns the traffic
// since the previous one. Deltas by construction: a kernel restart yields a
// smaller delta instead of the negative jump that subtracting a remembered
// absolute value would produce.
func (m *Manager) Stats(ctx context.Context) ([]Stat, error) {
	addr := m.APIAddress()
	if addr == "" {
		return nil, nil // no API configured; nothing to report
	}
	out, err := m.runAPI(ctx, addr, "statsquery", "-reset")
	if err != nil {
		return nil, err
	}
	return parseStats(out)
}

// Stat is one counter Xray reported.
type Stat struct {
	// Name is Xray's raw key, e.g. "user>>>alice@p.node>>>traffic>>>uplink".
	Name  string
	Value int64
}

func parseStats(out string) ([]Stat, error) {
	// The response may be preceded by log lines; start at the JSON object.
	start := strings.Index(out, "{")
	if start < 0 {
		return nil, fmt.Errorf("no JSON in statsquery output: %s", firstLines(out, 3))
	}
	var parsed struct {
		Stat []struct {
			Name string `json:"name"`
			// A zero counter omits "value" entirely, so this must stay a
			// pointer-free int that simply defaults to 0.
			Value int64 `json:"value"`
		} `json:"stat"`
	}
	if err := json.Unmarshal([]byte(out[start:]), &parsed); err != nil {
		return nil, fmt.Errorf("statsquery output is not JSON: %w", err)
	}
	stats := make([]Stat, 0, len(parsed.Stat))
	for _, s := range parsed.Stat {
		if s.Name == "" {
			continue
		}
		stats = append(stats, Stat{Name: s.Name, Value: s.Value})
	}
	return stats, nil
}

// UserTraffic is one credential's traffic since the last read.
type UserTraffic struct {
	Email string
	Up    int64
	Down  int64
}

// UserTrafficFrom folds raw counters into per-user deltas, dropping the
// inbound/outbound ones Core does not attribute to a user.
func UserTrafficFrom(stats []Stat) []UserTraffic {
	byEmail := map[string]*UserTraffic{}
	order := []string{}
	for _, s := range stats {
		// user>>>{email}>>>traffic>>>{uplink|downlink}
		parts := strings.Split(s.Name, ">>>")
		if len(parts) != 4 || parts[0] != "user" || parts[2] != "traffic" {
			continue
		}
		email := parts[1]
		t, ok := byEmail[email]
		if !ok {
			t = &UserTraffic{Email: email}
			byEmail[email] = t
			order = append(order, email)
		}
		switch parts[3] {
		case "uplink":
			t.Up += s.Value
		case "downlink":
			t.Down += s.Value
		}
	}
	out := make([]UserTraffic, 0, len(order))
	for _, e := range order {
		t := byEmail[e]
		// A user with no traffic this interval is not worth a report.
		if t.Up == 0 && t.Down == 0 {
			continue
		}
		out = append(out, *t)
	}
	return out
}

// OnlineUser is one credential's currently-connected source addresses.
type OnlineUser struct {
	Email string
	IPs   map[string]int64 // address -> last-seen unix seconds
}

// OnlineUsers reports who is connected right now and from where.
//
// Two calls per round plus one per online user: `statsgetallonlineusers` for
// the roster, then `statsonlineiplist` for each name in it. The roster alone is
// not enough — it gives names, not addresses — and there is no bulk form.
//
// The boolean reports whether the enumeration finished. A partial answer must
// never be read as a complete one: it would look exactly like several devices
// disconnecting at once. Everything measured here is against Xray 26.3.27:
//
//   - Without policy.levels."0".statsUserOnline, every one of these commands
//     fails silently. statsonline and statsonlineiplist return NotFound with a
//     non-zero exit, but statsgetallonlineusers prints `{}` and exits 0 —
//     indistinguishable from nobody being connected. `xray -test` accepts the
//     config either way, so the kernel is the only oracle for this.
//
//   - The count is of distinct source addresses, not sessions. Three
//     concurrent connections from one address report as one address.
//
//   - Loopback sources are excluded from the accounting altogether: a
//     connection from 127.0.0.1 leaves the count at zero and the map empty.
//     That only bites in local testing, but it makes a loopback test look
//     exactly like a broken feature.
func (m *Manager) OnlineUsers(ctx context.Context) ([]OnlineUser, bool, error) {
	addr := m.APIAddress()
	if addr == "" {
		return nil, false, nil // no API configured; nothing to report
	}
	out, err := m.runAPI(ctx, addr, "statsgetallonlineusers")
	if err != nil {
		return nil, false, err
	}
	emails, err := parseOnlineRoster(out)
	if err != nil {
		return nil, false, err
	}

	users := make([]OnlineUser, 0, len(emails))
	for _, email := range emails {
		ipOut, err := m.runAPI(ctx, addr, "statsonlineiplist", "-email="+email)
		if err != nil {
			// The roster is already stale by the time we walk it, so a user who
			// disconnected mid-round is ordinary. Report what we have and say
			// the enumeration was short, so Core marks the count a floor rather
			// than treating a truncated list as a total.
			m.logger.Warn("reading online addresses failed", "email", email, "err", err)
			return users, false, nil
		}
		ips, err := parseOnlineIPs(ipOut)
		if err != nil {
			m.logger.Warn("parsing online addresses failed", "email", email, "err", err)
			return users, false, nil
		}
		if len(ips) == 0 {
			continue
		}
		users = append(users, OnlineUser{Email: email, IPs: ips})
	}
	return users, true, nil
}

// parseOnlineRoster pulls the credential names out of statsgetallonlineusers.
//
// The names arrive as full stat keys ("user>>>alice.u1@p1.n1>>>online"), which
// are not what statsonlineiplist wants; it takes the bare email.
func parseOnlineRoster(out string) ([]string, error) {
	start := strings.Index(out, "{")
	if start < 0 {
		return nil, fmt.Errorf("no JSON in statsgetallonlineusers output: %s", firstLines(out, 3))
	}
	var parsed struct {
		Users []string `json:"users"`
	}
	if err := json.Unmarshal([]byte(out[start:]), &parsed); err != nil {
		return nil, fmt.Errorf("statsgetallonlineusers output is not JSON: %w", err)
	}
	emails := make([]string, 0, len(parsed.Users))
	for _, name := range parsed.Users {
		parts := strings.Split(name, ">>>")
		if len(parts) != 3 || parts[0] != "user" || parts[2] != "online" {
			continue
		}
		if parts[1] != "" {
			emails = append(emails, parts[1])
		}
	}
	return emails, nil
}

// parseOnlineIPs reads the {address: last-seen} map from statsonlineiplist.
func parseOnlineIPs(out string) (map[string]int64, error) {
	start := strings.Index(out, "{")
	if start < 0 {
		return nil, fmt.Errorf("no JSON in statsonlineiplist output: %s", firstLines(out, 3))
	}
	var parsed struct {
		IPs map[string]int64 `json:"ips"`
	}
	if err := json.Unmarshal([]byte(out[start:]), &parsed); err != nil {
		return nil, fmt.Errorf("statsonlineiplist output is not JSON: %w", err)
	}
	return parsed.IPs, nil
}

func (m *Manager) runAPI(ctx context.Context, addr, sub string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	full := append([]string{"api", sub, "--server=" + addr}, args...)
	cmd := exec.CommandContext(ctx, m.bin, full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("xray api %s did not finish: %w", sub, ctx.Err())
		}
		// A non-zero exit means the API was unreachable — the one failure the
		// exit code is honest about.
		return "", fmt.Errorf("xray api %s failed: %s", sub, firstLines(string(out), 3))
	}
	return string(out), nil
}

// userAlreadyAbsent distinguishes the two "not found" replies Xray gives, which
// look alike but mean opposite things:
//
//	proxy/vless: User ghost@x not found.                      -> already absent, fine
//	app/proxyman/inbound: handler not found: no-such-inbound  -> the tag is wrong
//
// Treating the second as success would let a typo'd inbound tag masquerade as a
// completed ban, so it must stay an error.
func userAlreadyAbsent(out string) bool {
	if strings.Contains(out, "handler not found") {
		return false
	}
	return strings.Contains(out, "User ") && strings.Contains(out, "not found")
}

func countFrom(re *regexp.Regexp, out string) (int, bool) {
	m := re.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// inboundProtocol reports the protocol of an inbound in the applied config.
//
// `xray api adu` needs a protocol to build its config fragment, and guessing
// it from the account's shape is not safe: shadowsocks and trojan accounts
// both carry a "password", so a guess would hand the wrong protocol to the
// running kernel. The applied config is authoritative — it is what the kernel
// was started from.
func inboundProtocol(configJSON []byte, tag string) string {
	var cfg struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return ""
	}
	for _, in := range cfg.Inbounds {
		if in.Tag == tag {
			return in.Protocol
		}
	}
	return ""
}
