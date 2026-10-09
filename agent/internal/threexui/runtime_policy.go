package threexui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// RuntimePolicy contains only settings that determine whether ordinary client
// operations can restart Xray. No other panel settings or secrets are exposed.
type RuntimePolicy struct {
	RestartXrayOnClientDisable bool
}

// ReadRuntimePolicy reads the existing setting through the official settings
// endpoint. It does not change the setting and is not a SHADOW observation.
func (c *Client) ReadRuntimePolicy(ctx context.Context) (RuntimePolicy, error) {
	u, err := c.endpointURL("panel/api/setting/all")
	if err != nil {
		return RuntimePolicy{}, err
	}
	var raw json.RawMessage
	if err := c.sensitiveEnvelope(ctx, http.MethodPost, u, nil, "", &raw); err != nil {
		return RuntimePolicy{}, err
	}
	return decodeRuntimePolicy(raw)
}

func decodeRuntimePolicy(raw json.RawMessage) (RuntimePolicy, error) {
	invalid := func() (RuntimePolicy, error) {
		return RuntimePolicy{}, &ContractError{Message: "3x-ui returned an invalid runtime policy"}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return invalid()
	}
	var policy RuntimePolicy
	seen := false
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return invalid()
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return invalid()
		}
		if key != "restartXrayOnClientDisable" {
			continue
		}
		if seen {
			return invalid()
		}
		seen = true
		switch string(bytes.TrimSpace(value)) {
		case "true":
			policy.RestartXrayOnClientDisable = true
		case "false":
			policy.RestartXrayOnClientDisable = false
		default:
			return invalid()
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || !seen {
		return invalid()
	}
	if _, err := decoder.Token(); err != io.EOF {
		return invalid()
	}
	return policy, nil
}
