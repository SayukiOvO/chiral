package threexui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

const maxMutationRequestBytes = 4 << 20

// XrayTemplate is the stored 3x-ui template, not evidence of the configuration
// currently running. Preserve OutboundTestURL when changing Config: upstream
// resets that setting to its default if the form field is absent or empty.
type XrayTemplate struct {
	Config          json.RawMessage
	OutboundTestURL string
}

// Inbound contains the local managed-inbound fields accepted by 3x-ui v3.
// Raw JSON preserves nested protocol/transport fields in our request, but
// upstream may normalise them or rebuild clients through a narrower schema.
// Successful mutation requires a subsequent full configuration comparison at
// the provider layer; these methods do not claim lossless projection or apply.
// Node assignment is intentionally absent: this adapter operates locally.
type Inbound struct {
	ID              int64           `json:"id,omitempty"`
	Remark          string          `json:"remark"`
	Enable          bool            `json:"enable"`
	Listen          string          `json:"listen"`
	Port            int             `json:"port"`
	Protocol        string          `json:"protocol"`
	Tag             string          `json:"tag"`
	Settings        json.RawMessage `json:"settings"`
	StreamSettings  json.RawMessage `json:"streamSettings,omitempty"`
	Sniffing        json.RawMessage `json:"sniffing,omitempty"`
	DisableFlow     bool            `json:"disableFlow"`
	Total           int64           `json:"total"`
	ExpiryTime      int64           `json:"expiryTime"`
	TrafficReset    string          `json:"trafficReset"`
	TrafficResetDay int             `json:"trafficResetDay,omitempty"`
	SubSortIndex    int             `json:"subSortIndex,omitempty"`
	// Raw is the complete API response object, including fields not modelled
	// above. It is readback evidence and is never implicitly sent as a write.
	Raw json.RawMessage `json:"-"`
}

// GlobalClient is the readback shape of the non-paginated /clients/list route.
// Raw retains every field. In particular, the numeric database ID and the
// uuid field in a listed record differ from the id credential accepted by
// /clients/add and /clients/update; Raw must not be blindly posted back.
type GlobalClient struct {
	ID         int64           `json:"id"`
	Email      string          `json:"email"`
	InboundIDs []int64         `json:"inboundIds"`
	Raw        json.RawMessage `json:"-"`
}

// RequestTooLargeError means a sensitive write was rejected before any HTTP
// request. The payload itself is never retained in the diagnostic.
type RequestTooLargeError struct{ Limit int }

func (e *RequestTooLargeError) Error() string {
	return fmt.Sprintf("3x-ui request exceeds the %d-byte limit", e.Limit)
}

// ReadXrayTemplate uses the exact trailing-slash POST read route. Its obj is a
// JSON-encoded string containing xraySetting and outboundTestUrl in v3.8.5.
// Upstream may heal legacy wrapped templates on this endpoint; it is not used
// by the read-only SHADOW observer.
func (c *Client) ReadXrayTemplate(ctx context.Context) (XrayTemplate, error) {
	u, err := c.endpointURL("panel/api/xray")
	if err != nil {
		return XrayTemplate{}, err
	}
	u.Path += "/"
	var encoded string
	if err := c.sensitiveEnvelope(ctx, http.MethodPost, u, nil, "", &encoded); err != nil {
		return XrayTemplate{}, err
	}
	var wire struct {
		Config          json.RawMessage `json:"xraySetting"`
		OutboundTestURL string          `json:"outboundTestUrl"`
	}
	if json.Unmarshal([]byte(encoded), &wire) != nil || !isJSONObject(wire.Config) {
		return XrayTemplate{}, &ContractError{Message: "3x-ui returned an invalid Xray template"}
	}
	return XrayTemplate{Config: append(json.RawMessage(nil), wire.Config...), OutboundTestURL: wire.OutboundTestURL}, nil
}

// UpdateXrayTemplate saves the template and asks upstream to reconcile a running
// Xray. Upstream can partially apply or persist a candidate even when it returns
// an error. The provider must preserve a rollback snapshot and verify runtime
// success; this HTTP acknowledgement alone is not an application acknowledgement.
func (c *Client) UpdateXrayTemplate(ctx context.Context, template XrayTemplate) error {
	if len(template.Config) > maxMutationRequestBytes {
		return &RequestTooLargeError{Limit: maxMutationRequestBytes}
	}
	if !isJSONObject(template.Config) {
		return fmt.Errorf("3x-ui Xray template must be a JSON object")
	}
	if template.OutboundTestURL == "" {
		return fmt.Errorf("3x-ui outbound test URL must be preserved explicitly")
	}
	form := url.Values{"xraySetting": {string(template.Config)}, "outboundTestUrl": {template.OutboundTestURL}}
	u, err := c.endpointURL("panel/api/xray/update")
	if err != nil {
		return err
	}
	return c.sensitiveEnvelope(ctx, http.MethodPost, u, []byte(form.Encode()), "application/x-www-form-urlencoded", nil)
}

// ReadAssembledConfig returns the configuration generated from upstream's
// database with the stricter diagnostics needed by a mutating reconciler. It
// does not assert that the running process accepted it: upstream can retain an
// older running configuration after a failed or partially applied update.
// ConfigJSON remains the existing read-only SHADOW observer entrypoint.
func (c *Client) ReadAssembledConfig(ctx context.Context) (json.RawMessage, error) {
	u, err := c.endpointURL(configEndpoint)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := c.sensitiveEnvelope(ctx, http.MethodGet, u, nil, "", &raw); err != nil {
		return nil, err
	}
	if !isJSONObject(raw) {
		return nil, &ContractError{Message: "3x-ui assembled configuration is not a JSON object"}
	}
	return append(json.RawMessage(nil), raw...), nil
}

func (c *Client) ListInbounds(ctx context.Context) ([]Inbound, error) {
	u, err := c.endpointURL("panel/api/inbounds/list")
	if err != nil {
		return nil, err
	}
	var rows []json.RawMessage
	if err := c.sensitiveEnvelope(ctx, http.MethodGet, u, nil, "", &rows); err != nil {
		return nil, err
	}
	inbounds := make([]Inbound, 0, len(rows))
	for _, raw := range rows {
		inbound, err := decodeInbound(raw)
		if err != nil {
			return nil, err
		}
		inbounds = append(inbounds, inbound)
	}
	return inbounds, nil
}

func (c *Client) AddInbound(ctx context.Context, inbound Inbound) (Inbound, error) {
	if inbound.ID != 0 {
		return Inbound{}, fmt.Errorf("new 3x-ui inbound must not have an ID")
	}
	return c.writeInbound(ctx, "panel/api/inbounds/add", inbound)
}

func (c *Client) UpdateInbound(ctx context.Context, id int64, inbound Inbound) (Inbound, error) {
	if id <= 0 || (inbound.ID != 0 && inbound.ID != id) {
		return Inbound{}, fmt.Errorf("invalid 3x-ui inbound ID")
	}
	inbound.ID = id
	return c.writeInbound(ctx, "panel/api/inbounds/update/"+strconv.FormatInt(id, 10), inbound)
}

func (c *Client) DeleteInbound(ctx context.Context, id int64) error {
	if id <= 0 {
		return fmt.Errorf("invalid 3x-ui inbound ID")
	}
	u, err := c.endpointURL("panel/api/inbounds/del/" + strconv.FormatInt(id, 10))
	if err != nil {
		return err
	}
	return c.sensitiveEnvelope(ctx, http.MethodPost, u, nil, "", nil)
}

func (c *Client) writeInbound(ctx context.Context, endpoint string, inbound Inbound) (Inbound, error) {
	if inbound.Port < 0 || inbound.Port > 65535 || strings.TrimSpace(inbound.Tag) == "" || strings.TrimSpace(inbound.Protocol) == "" {
		return Inbound{}, fmt.Errorf("3x-ui inbound requires a valid port, tag, and protocol")
	}
	if !isJSONObject(inbound.Settings) || (len(inbound.StreamSettings) != 0 && !isJSONObject(inbound.StreamSettings)) || (len(inbound.Sniffing) != 0 && !isJSONObject(inbound.Sniffing)) {
		return Inbound{}, fmt.Errorf("3x-ui inbound settings must be JSON objects")
	}
	if inbound.Total < 0 || inbound.ExpiryTime < 0 {
		return Inbound{}, fmt.Errorf("invalid 3x-ui inbound quota or expiry")
	}
	if inbound.TrafficReset == "" {
		inbound.TrafficReset = "never"
	}
	body, err := marshalSensitive(inbound)
	if err != nil {
		return Inbound{}, err
	}
	u, err := c.endpointURL(endpoint)
	if err != nil {
		return Inbound{}, err
	}
	var raw json.RawMessage
	if err := c.sensitiveEnvelope(ctx, http.MethodPost, u, body, "application/json", &raw); err != nil {
		return Inbound{}, err
	}
	return decodeInbound(raw)
}

func decodeInbound(raw json.RawMessage) (Inbound, error) {
	var inbound Inbound
	if json.Unmarshal(raw, &inbound) != nil || inbound.ID <= 0 || inbound.Tag == "" {
		return Inbound{}, &ContractError{Message: "3x-ui returned an invalid inbound record"}
	}
	inbound.Raw = append(json.RawMessage(nil), raw...)
	return inbound, nil
}

func (c *Client) ListClients(ctx context.Context) ([]GlobalClient, error) {
	u, err := c.endpointURL("panel/api/clients/list")
	if err != nil {
		return nil, err
	}
	var rows []json.RawMessage
	if err := c.sensitiveEnvelope(ctx, http.MethodGet, u, nil, "", &rows); err != nil {
		return nil, err
	}
	clients := make([]GlobalClient, 0, len(rows))
	for _, raw := range rows {
		var client GlobalClient
		if json.Unmarshal(raw, &client) != nil || client.ID <= 0 || validClientEmail(client.Email) != nil {
			return nil, &ContractError{Message: "3x-ui returned an invalid client record"}
		}
		client.Raw = append(json.RawMessage(nil), raw...)
		clients = append(clients, client)
	}
	return clients, nil
}

// AddClient accepts the upstream client request object, not a listed database
// record. Arbitrary JSON members are preserved in transit only: 3x-ui uses a
// typed client model and may discard unsupported fields. The caller must read
// the resulting config to check credential fidelity and attachments.
func (c *Client) AddClient(ctx context.Context, account json.RawMessage, inboundIDs []int64) error {
	if _, err := clientEmail(account); err != nil {
		return err
	}
	if len(inboundIDs) == 0 {
		return fmt.Errorf("3x-ui client requires at least one inbound")
	}
	seen := make(map[int64]bool, len(inboundIDs))
	for _, id := range inboundIDs {
		if id <= 0 || seen[id] {
			return fmt.Errorf("invalid or duplicate 3x-ui client inbound ID")
		}
		seen[id] = true
	}
	payload := struct {
		Client     json.RawMessage `json:"client"`
		InboundIDs []int64         `json:"inboundIds"`
	}{account, inboundIDs}
	body, err := marshalSensitive(payload)
	if err != nil {
		return err
	}
	u, err := c.endpointURL("panel/api/clients/add")
	if err != nil {
		return err
	}
	return c.sensitiveEnvelope(ctx, http.MethodPost, u, body, "application/json", nil)
}

// UpdateClient applies the full client object to every attachment for email.
// Renaming through this method is refused: ownership remains keyed by the
// existing Chiral credential email and must not expand to another identity.
func (c *Client) UpdateClient(ctx context.Context, email string, account json.RawMessage) error {
	accountEmail, err := clientEmail(account)
	if err != nil {
		return err
	}
	if accountEmail != email {
		return fmt.Errorf("3x-ui client email must not change during update")
	}
	u, err := c.clientEndpoint("panel/api/clients/update", email)
	if err != nil {
		return err
	}
	return c.sensitiveEnvelope(ctx, http.MethodPost, u, account, "application/json", nil)
}

// DeleteClient removes all attachments. keepTraffic must be chosen explicitly
// by the caller; false also deletes upstream accounting state.
func (c *Client) DeleteClient(ctx context.Context, email string, keepTraffic bool) error {
	u, err := c.clientEndpoint("panel/api/clients/del", email)
	if err != nil {
		return err
	}
	keep := "0"
	if keepTraffic {
		keep = "1"
	}
	u.RawQuery = url.Values{"keepTraffic": {keep}}.Encode()
	return c.sensitiveEnvelope(ctx, http.MethodPost, u, nil, "", nil)
}

// RestartXray requests an unconditional upstream restart. It may interrupt
// live sessions; a successful envelope does not prove dataplane health.
func (c *Client) RestartXray(ctx context.Context) error {
	u, err := c.endpointURL("panel/api/server/restartXrayService")
	if err != nil {
		return err
	}
	return c.sensitiveEnvelope(ctx, http.MethodPost, u, nil, "", nil)
}

func (c *Client) clientEndpoint(endpoint, email string) (*url.URL, error) {
	if err := validClientEmail(email); err != nil {
		return nil, err
	}
	u, err := c.endpointURL(endpoint)
	if err != nil {
		return nil, err
	}
	escaped := u.EscapedPath() + "/" + url.PathEscape(email)
	u.Path += "/" + email
	u.RawPath = escaped
	return u, nil
}

func validClientEmail(email string) error {
	if email == "" || email == "." || email == ".." || len(email) > 512 {
		return fmt.Errorf("invalid 3x-ui client email")
	}
	for _, r := range email {
		if r == '/' || r == '\\' || unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("invalid 3x-ui client email")
		}
	}
	return nil
}

func clientEmail(account json.RawMessage) (string, error) {
	if len(account) > maxMutationRequestBytes {
		return "", &RequestTooLargeError{Limit: maxMutationRequestBytes}
	}
	if !isJSONObject(account) {
		return "", fmt.Errorf("3x-ui client must be a JSON object")
	}
	var identity struct {
		Email  string `json:"email"`
		Enable *bool  `json:"enable"`
	}
	if json.Unmarshal(account, &identity) != nil {
		return "", fmt.Errorf("invalid 3x-ui client identity")
	}
	if identity.Enable == nil {
		return "", fmt.Errorf("3x-ui client enable state must be explicit")
	}
	if err := validClientEmail(identity.Email); err != nil {
		return "", err
	}
	return identity.Email, nil
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 1 && trimmed[0] == '{' && json.Valid(trimmed)
}

func marshalSensitive(value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("invalid 3x-ui request payload")
	}
	if len(body) > maxMutationRequestBytes {
		return nil, &RequestTooLargeError{Limit: maxMutationRequestBytes}
	}
	return body, nil
}

// sensitiveEnvelope omits provider msg and decoder details from every error.
// These responses contain secret material; redacting the bearer alone is not
// sufficient. HTTP status, timeout sentinels, and bounded sizes remain visible.
func (c *Client) sensitiveEnvelope(ctx context.Context, method string, u *url.URL, payload []byte, contentType string, dst any) error {
	if len(payload) > maxMutationRequestBytes {
		return &RequestTooLargeError{Limit: maxMutationRequestBytes}
	}
	body, err := c.request(ctx, method, u, bytes.NewReader(payload), contentType, true)
	if err != nil {
		return err
	}
	var response envelope
	if json.Unmarshal(body, &response) != nil {
		return &ContractError{Message: "3x-ui returned an invalid response envelope"}
	}
	if response.Success == nil {
		return &ContractError{Message: "3x-ui response envelope has no boolean success field"}
	}
	if !*response.Success {
		return &APIError{}
	}
	if dst == nil {
		return nil
	}
	object := bytes.TrimSpace(response.Object)
	if len(object) == 0 || bytes.Equal(object, []byte("null")) {
		return &ContractError{Message: "3x-ui response envelope has no object"}
	}
	if json.Unmarshal(object, dst) != nil {
		return &ContractError{Message: "3x-ui returned an invalid response object"}
	}
	return nil
}
