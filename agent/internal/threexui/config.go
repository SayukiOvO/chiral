package threexui

import (
	"bytes"
	"context"
	"encoding/json"
)

const configEndpoint = "panel/api/server/getConfigJson"

// ConfigJSON reads the fully assembled Xray config. Despite its GET method,
// upstream may repair stored templates and perform client lifecycle maintenance
// while assembling it. It must not be used by the read-only SHADOW observer;
// callers need explicit authority over the target instance. Candidate workflows
// should prefer ReadAssembledConfig for its stricter sensitive diagnostics.
func (c *Client) ConfigJSON(ctx context.Context) (json.RawMessage, error) {
	var config json.RawMessage
	if err := c.getEnvelope(ctx, configEndpoint, &config); err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(config)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, &ContractError{Message: "3x-ui assembled config is not a JSON object"}
	}
	return append(json.RawMessage(nil), trimmed...), nil
}
