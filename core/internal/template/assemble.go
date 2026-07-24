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
