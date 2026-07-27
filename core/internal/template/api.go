package template

import (
	"encoding/json"
	"fmt"
)

// The management plane Chiral needs on every node: Xray's own gRPC API, which
// is how the agent adds and removes users online and reads per-user traffic.
//
// Without it those features do not fail loudly — they simply never work — so
// Core injects the block rather than expecting operators to remember the
// incantation. A skeleton that already declares "api" is left entirely alone,
// on the assumption that an operator who wrote one meant it.
//
// RoutingService is in the service list even though nothing calls it yet. It is
// what `xray api sib` needs to block a source address, and adding it later
// would mean a second config version and a second Xray restart across the whole
// fleet — every live connection on every node dropped, twice, for one line of
// JSON. Measured on 26.3.27: without it, sib fails with
// "Unimplemented: unknown service xray.app.router.command.RoutingService".
const (
	// APIInboundTag is the dokodemo-door inbound that carries API calls. It is
	// distinct from APIHandlerTag so it cannot collide with the implicit
	// outbound handler Xray creates for the API service.
	APIInboundTag = "chiral-api"
	// APIHandlerTag names the API service itself.
	APIHandlerTag = "api"
	// APIPort is the conventional Xray API port. The agent does not assume it:
	// it reads the port back out of the config it applied, so an operator's
	// own api inbound on a different port keeps working.
	APIPort = 10085
	// APIListen is loopback-only on purpose: this endpoint has no
	// authentication of its own, so it must never be reachable off-host.
	APIListen = "127.0.0.1"
)

// EnsureAPI makes sure a node config carries everything user management needs:
// Xray's gRPC API, the counters, and the per-user policy that turns those
// counters on. Reports whether it changed anything.
//
// Each piece is checked independently. An operator who wrote their own "api"
// block keeps it — but their config must still get per-user stats, or quota
// enforcement silently measures nothing and every quota becomes infinite.
// Likewise a hand-written "policy" is merged into rather than skipped.
func EnsureAPI(cfg map[string]json.RawMessage) (bool, error) {
	changed := false

	if _, present := cfg["api"]; !present {
		cfg["api"] = json.RawMessage(fmt.Sprintf(
			`{"tag":%q,"services":[%q,%q,%q]}`,
			APIHandlerTag, "HandlerService", "StatsService", "RoutingService"))
		if err := ensureAPIInbound(cfg); err != nil {
			return false, err
		}
		if err := ensureAPIRoute(cfg); err != nil {
			return false, err
		}
		changed = true
	}

	// An empty stats object is what turns counters on at all.
	if _, present := cfg["stats"]; !present {
		cfg["stats"] = json.RawMessage(`{}`)
		changed = true
	}

	statsChanged, err := ensureStatsPolicy(cfg)
	if err != nil {
		return false, err
	}
	return changed || statsChanged, nil
}

// ensureStatsPolicy turns on per-user counters for policy level 0, which is
// what an inbound's clients get unless told otherwise, without disturbing any
// other policy the operator set.
//
// statsUserOnline is here alongside the traffic counters, and its absence is
// the nastiest silent failure in this file. Measured on Xray 26.3.27: without
// it, per-user traffic still works, but `xray api statsonline` and
// `statsonlineiplist` return NotFound while `statsgetallonlineusers` prints
// `{}` and exits 0 — identical to nobody being connected. `xray -test` accepts
// the config either way. So a panel missing this line would show every account
// as using zero addresses, forever, with nothing anywhere reporting a problem.
func ensureStatsPolicy(cfg map[string]json.RawMessage) (bool, error) {
	var policy map[string]json.RawMessage
	if raw, ok := cfg["policy"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &policy); err != nil {
			return false, fmt.Errorf(`"policy" is not an object: %w`, err)
		}
	}
	if policy == nil {
		policy = map[string]json.RawMessage{}
	}

	var levels map[string]json.RawMessage
	if raw, ok := policy["levels"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &levels); err != nil {
			return false, fmt.Errorf(`"policy.levels" is not an object: %w`, err)
		}
	}
	if levels == nil {
		levels = map[string]json.RawMessage{}
	}

	var level0 map[string]json.RawMessage
	if raw, ok := levels["0"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &level0); err != nil {
			return false, fmt.Errorf(`"policy.levels.0" is not an object: %w`, err)
		}
	}
	if level0 == nil {
		level0 = map[string]json.RawMessage{}
	}

	changed := false
	for _, key := range []string{"statsUserUplink", "statsUserDownlink", "statsUserOnline"} {
		// Only fill in what is missing: an operator who deliberately set one
		// to false has said something, even if it costs them enforcement.
		if _, present := level0[key]; !present {
			level0[key] = json.RawMessage(`true`)
			changed = true
		}
	}
	if !changed {
		return false, nil
	}

	l0, err := json.Marshal(level0)
	if err != nil {
		return false, err
	}
	levels["0"] = l0
	lv, err := json.Marshal(levels)
	if err != nil {
		return false, err
	}
	policy["levels"] = lv
	out, err := json.Marshal(policy)
	if err != nil {
		return false, err
	}
	cfg["policy"] = out
	return true, nil
}

func ensureAPIInbound(cfg map[string]json.RawMessage) error {
	var inbounds []json.RawMessage
	if raw, ok := cfg["inbounds"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &inbounds); err != nil {
			return fmt.Errorf(`"inbounds" is not an array: %w`, err)
		}
	}
	inbound := json.RawMessage(fmt.Sprintf(
		`{"tag":%q,"listen":%q,"port":%d,"protocol":"dokodemo-door","settings":{"address":%q}}`,
		APIInboundTag, APIListen, APIPort, APIListen))
	// Prepend: the API inbound is infrastructure, and keeping it first makes an
	// assembled config easier to read.
	inbounds = append([]json.RawMessage{inbound}, inbounds...)

	merged, err := json.Marshal(inbounds)
	if err != nil {
		return err
	}
	cfg["inbounds"] = merged
	return nil
}

func ensureAPIRoute(cfg map[string]json.RawMessage) error {
	var routing map[string]json.RawMessage
	if raw, ok := cfg["routing"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &routing); err != nil {
			return fmt.Errorf(`"routing" is not an object: %w`, err)
		}
	}
	if routing == nil {
		routing = map[string]json.RawMessage{}
	}
	var rules []json.RawMessage
	if raw, ok := routing["rules"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &rules); err != nil {
			return fmt.Errorf(`"routing.rules" is not an array: %w`, err)
		}
	}
	rule := json.RawMessage(fmt.Sprintf(
		`{"type":"field","inboundTag":[%q],"outboundTag":%q}`, APIInboundTag, APIHandlerTag))
	// First: routing is first-match, and an operator's catch-all rule would
	// otherwise swallow API traffic and break user management.
	rules = append([]json.RawMessage{rule}, rules...)

	merged, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	routing["rules"] = merged
	out, err := json.Marshal(routing)
	if err != nil {
		return err
	}
	cfg["routing"] = out
	return nil
}

// APIAddress reports the host:port of a config's API inbound, or "" if the
// config has none. Both Core and the agent locate the endpoint this way rather
// than assuming APIPort, so an operator-written api inbound keeps working.
func APIAddress(configJSON []byte) string {
	var cfg struct {
		API struct {
			Tag string `json:"tag"`
		} `json:"api"`
		Inbounds []struct {
			Tag      string `json:"tag"`
			Listen   string `json:"listen"`
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
		} `json:"inbounds"`
		Routing struct {
			Rules []struct {
				InboundTag  []string `json:"inboundTag"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return ""
	}
	if cfg.API.Tag == "" {
		return ""
	}
	// The API inbound is whichever one routing sends to the api handler.
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
