package template

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// DefaultSkeleton is the node config a freshly-added node starts from. The
// inbounds array is filled in from the profiles bound to that node.
const DefaultSkeleton = `{
  "log": { "loglevel": "warning" },
  "inbounds": [],
  "outbounds": [
    { "protocol": "freedom", "tag": "direct" }
  ]
}`

// InboundSource is one profile's server inbound, with the context to render
// it against (global + profile + node variables already merged).
type InboundSource struct {
	ProfileID   string
	ProfileName string
	Template    string
	Ctx         *Context
	// Clients are the already-rendered client-entry objects for the users
	// entitled to this profile on this node — one per credential. They are
	// spliced into the inbound's settings.clients.
	Clients []string
}

// AssembleNode renders each profile's inbound and splices the results into
// the node's config skeleton.
//
// Inbounds written by hand in the skeleton are preserved and the rendered
// ones are appended, so a node can carry both profile-managed access points
// and one-off manual ones.
func AssembleNode(skeleton string, sources []InboundSource) ([]byte, error) {
	if skeleton == "" {
		skeleton = DefaultSkeleton
	}
	// Decode into an ordered-agnostic map; Xray does not care about key order
	// and neither do we.
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal([]byte(skeleton), &cfg); err != nil {
		return nil, fmt.Errorf("node config skeleton is not a JSON object: %w", err)
	}
	// JSON `null` unmarshals into a nil map without error; assigning to it
	// later would panic. Reject it here with a message that names the cause.
	if cfg == nil {
		return nil, fmt.Errorf("node config skeleton must be a JSON object, got null")
	}

	var inbounds []json.RawMessage
	if raw, ok := cfg["inbounds"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &inbounds); err != nil {
			return nil, fmt.Errorf(`skeleton "inbounds" is not an array: %w`, err)
		}
	}

	for _, src := range sources {
		rendered, err := src.Ctx.Render(src.Template)
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", src.ProfileName, err)
		}
		// Parse before splicing: a template that renders to malformed or
		// non-object JSON must fail here with a pointed message, not produce
		// a corrupt config that fails later with an opaque one.
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(rendered), &obj); err != nil {
			return nil, fmt.Errorf("profile %q inbound is not a JSON object after rendering: %w", src.ProfileName, err)
		}
		if err := spliceClients(obj, src.Clients); err != nil {
			return nil, fmt.Errorf("profile %q: %w", src.ProfileName, err)
		}
		compact, err := json.Marshal(obj)
		if err != nil {
			return nil, err
		}
		inbounds = append(inbounds, compact)
	}

	merged, err := json.Marshal(inbounds)
	if err != nil {
		return nil, err
	}
	cfg["inbounds"] = merged

	// Add the management API last, so its inbound and routing rule sit ahead
	// of everything the profiles contributed.
	if _, err := EnsureAPI(cfg); err != nil {
		return nil, err
	}

	out, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	// Indent so an operator reading the stored version sees something legible.
	var buf bytes.Buffer
	if err := json.Indent(&buf, out, "", "  "); err != nil {
		return out, nil
	}
	return buf.Bytes(), nil
}

// spliceClients puts the rendered per-user entries into an inbound's
// settings.clients, keeping any the template author wrote by hand — the same
// courtesy manual inbounds get in the skeleton.
func spliceClients(inbound map[string]json.RawMessage, entries []string) error {
	if len(entries) == 0 {
		return nil
	}
	var settings map[string]json.RawMessage
	if raw, ok := inbound["settings"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf(`inbound "settings" is not an object: %w`, err)
		}
	}
	if settings == nil {
		settings = map[string]json.RawMessage{}
	}
	var clients []json.RawMessage
	if raw, ok := settings["clients"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &clients); err != nil {
			return fmt.Errorf(`inbound "settings.clients" is not an array: %w`, err)
		}
	}
	for _, e := range entries {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(e), &obj); err != nil {
			return fmt.Errorf("client entry is not a JSON object after rendering: %w", err)
		}
		compact, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		clients = append(clients, compact)
	}
	merged, err := json.Marshal(clients)
	if err != nil {
		return err
	}
	settings["clients"] = merged
	out, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	inbound["settings"] = out
	return nil
}

// InboundTags extracts the "tag" of each inbound in an assembled config, for
// showing an operator what a node is actually serving.
func InboundTags(configJSON []byte) ([]string, error) {
	var cfg struct {
		Inbounds []struct {
			Tag string `json:"tag"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(cfg.Inbounds))
	for _, in := range cfg.Inbounds {
		out = append(out, in.Tag)
	}
	return out, nil
}
