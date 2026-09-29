package threexui

import (
	"bytes"
	"context"
	"encoding/json"
)

const configEndpoint = "panel/api/server/getConfigJson"

// ConfigJSON reads the fully assembled Xray config without mutating it. The
// shadow observer uses this as an additional safe authorization check. Current
// node-sync tokens do not cover the whole observation contract; an admin token
// can read both this endpoint and the live OpenAPI document.
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
