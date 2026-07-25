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

// EnsureAPI adds the api / stats / policy block and its routing rule to a node
// config unless the config already declares "api". Reports whether it changed
// anything.
func EnsureAPI(cfg map[string]json.RawMessage) (bool, error) {
	if _, present := cfg["api"]; present {
		return false, nil
	}

	cfg["api"] = json.RawMessage(fmt.Sprintf(
		`{"tag":%q,"services":["HandlerService","StatsService"]}`, APIHandlerTag))

	// An empty stats object is what turns counters on at all.
	if _, present := cfg["stats"]; !present {
		cfg["stats"] = json.RawMessage(`{}`)
	}

	// Per-user counters only exist if the user's policy level asks for them;
	// level 0 is what an inbound's clients get unless told otherwise.
	if _, present := cfg["policy"]; !present {
		cfg["policy"] = json.RawMessage(`{
			"levels": {"0": {"statsUserUplink": true, "statsUserDownlink": true}},
			"system": {"statsInboundUplink": true, "statsInboundDownlink": true}
		}`)
	}

	if err := ensureAPIInbound(cfg); err != nil {
		return false, err
	}
	if err := ensureAPIRoute(cfg); err != nil {
		return false, err
	}
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
