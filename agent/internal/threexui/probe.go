package threexui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"unicode"
)

const (
	outboundProbeEndpoint = "panel/api/xray/testOutbound"
	maxProbeJSONBytes     = 1 << 20
	maxProbeOutbounds     = 128
	maxProbeURLBytes      = 4096
)

// OutboundProbeResult contains only bounded, non-sensitive response evidence.
// It proves a single HTTP response through the supplied proxy configuration,
// not the running server version, byte-for-byte body integrity, or future health.
type OutboundProbeResult struct {
	HTTPStatus  int
	DelayMillis int64
}

// ProbeOutbound asks official 3x-ui to make one cold HTTP request through its
// temporary client Xray, using the caller's real node outbound and credentials.
// It never calls Xray directly or starts a local binary. allOutboundsJSON is
// optional context for dialerProxy dependencies; it cannot override the tested
// outbound with another configuration sharing its tag.
//
// Official v3.8.5 takes the target URL from its settings, NOT this endpoint's
// form. expectedTestURL must explicitly match that setting before and after the
// probe; this method never changes it or silently selects the upstream default.
// The two reads are not an atomic compare-and-swap: the caller must exclusively
// control provider settings and select the intended node/credential. Template
// reads can heal legacy upstream storage, so this is not a SHADOW operation.
//
// Only mode=real is sent and accepted. No TCP/ping fallback or automatic retry
// is performed. A timeout/cancellation bounds the HTTP exchange, not upstream's
// temporary process; upstream can finish after this method returns. Callers
// must supply a suitable bounded Config.Timeout and context budget, and perform
// their own running-version/identity checks around this dataplane observation.
func (c *Client) ProbeOutbound(ctx context.Context, outboundJSON, allOutboundsJSON json.RawMessage, expectedTestURL string) (OutboundProbeResult, error) {
	var result OutboundProbeResult
	tag, err := validateOutboundProbe(outboundJSON, allOutboundsJSON, expectedTestURL)
	if err != nil {
		return result, err
	}
	form := url.Values{"outbound": {string(outboundJSON)}, "mode": {"real"}}
	if len(allOutboundsJSON) > 0 {
		form.Set("allOutbounds", string(allOutboundsJSON))
	}
	payload := []byte(form.Encode())
	if len(payload) > maxMutationRequestBytes {
		return result, &RequestTooLargeError{Limit: maxMutationRequestBytes}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	capabilities, err := c.DiscoverCapabilities(ctx)
	if err != nil {
		return result, &safeWrappedError{message: "3x-ui outbound probe capability discovery failed", cause: err}
	}
	if !capabilities.Supports(CapabilityOutboundProbe) {
		return result, &ContractError{Message: "3x-ui does not advertise the official outbound probe operation"}
	}
	if err := c.matchProbeTarget(ctx, expectedTestURL); err != nil {
		return result, err
	}
	u, err := c.endpointURL(outboundProbeEndpoint)
	if err != nil {
		return result, err
	}
	var wire struct {
		Tag        *string `json:"tag"`
		Success    *bool   `json:"success"`
		Mode       *string `json:"mode"`
		HTTPStatus *int    `json:"httpStatus"`
		Delay      *int64  `json:"delay"`
		Error      string  `json:"error"`
	}
	if err := c.sensitiveEnvelope(ctx, http.MethodPost, u, payload, "application/x-www-form-urlencoded", &wire); err != nil {
		return result, err
	}
	if wire.Success == nil || !*wire.Success || wire.Tag == nil || *wire.Tag != tag ||
		wire.Mode == nil || *wire.Mode != "real" || wire.HTTPStatus == nil ||
		*wire.HTTPStatus < 200 || *wire.HTTPStatus > 399 || wire.Delay == nil ||
		*wire.Delay < 0 || *wire.Delay > 24*60*60*1000 || wire.Error != "" {
		return result, &ContractError{Message: "3x-ui did not confirm a successful real outbound HTTP probe"}
	}
	if err := c.matchProbeTarget(ctx, expectedTestURL); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return OutboundProbeResult{HTTPStatus: *wire.HTTPStatus, DelayMillis: *wire.Delay}, nil
}

func (c *Client) matchProbeTarget(ctx context.Context, expected string) error {
	template, err := c.ReadXrayTemplate(ctx)
	if err != nil {
		return err
	}
	if template.OutboundTestURL != expected {
		return &ContractError{Message: "3x-ui configured outbound probe target does not match the explicit target"}
	}
	return nil
}

func validateOutboundProbe(outbound, allOutbounds json.RawMessage, target string) (string, error) {
	if len(outbound) > maxProbeJSONBytes || len(allOutbounds) > maxProbeJSONBytes {
		return "", &RequestTooLargeError{Limit: maxProbeJSONBytes}
	}
	if !validProbeTarget(target) {
		return "", errors.New("3x-ui outbound probe requires an explicit HTTP(S) target without user information or fragment")
	}
	primary, tag, protocol, ok := decodeProbeOutbound(outbound)
	if !ok || tag == "blocked" || !proxyProbeProtocol(protocol) {
		return "", errors.New("3x-ui outbound probe requires a tagged proxy outbound")
	}
	if len(allOutbounds) == 0 {
		return tag, nil
	}
	var rows []json.RawMessage
	if raw := bytes.TrimSpace(allOutbounds); len(raw) == 0 || raw[0] != '[' ||
		json.Unmarshal(raw, &rows) != nil || len(rows) > maxProbeOutbounds {
		return "", errors.New("3x-ui outbound probe context must be a bounded JSON array")
	}
	tags := make(map[string]bool, len(rows))
	for _, row := range rows {
		entry, entryTag, _, valid := decodeProbeOutbound(row)
		if !valid || tags[entryTag] {
			return "", errors.New("3x-ui outbound probe context requires valid uniquely tagged outbounds")
		}
		tags[entryTag] = true
		// Official 3x-ui gives allOutbounds precedence on a tag collision. A
		// different entry could otherwise turn a proxy probe into direct access.
		if entryTag == tag && !reflect.DeepEqual(entry, primary) {
			return "", errors.New("3x-ui outbound probe context overrides the tested outbound")
		}
	}
	return tag, nil
}

func decodeProbeOutbound(raw json.RawMessage) (map[string]any, string, string, bool) {
	// Share the fidelity parser: duplicate decoded keys, invalid UTF-8,
	// unpaired escapes, excess depth and additional documents must not be
	// interpreted differently by validation and upstream's JSON parser.
	entry, err := decodeComparisonConfig(raw, "expected")
	if err != nil {
		return nil, "", "", false
	}
	tag, tagOK := entry["tag"].(string)
	protocol, protocolOK := entry["protocol"].(string)
	if !tagOK || tag == "" || len(tag) > 256 || strings.TrimSpace(tag) != tag ||
		strings.ContainsFunc(tag, unicode.IsControl) || !protocolOK || protocol == "" || len(protocol) > 64 ||
		strings.TrimSpace(protocol) != protocol || strings.ContainsFunc(protocol, unicode.IsControl) {
		return nil, "", "", false
	}
	return entry, tag, protocol, true
}

func proxyProbeProtocol(protocol string) bool {
	switch protocol {
	case "vless", "vmess", "trojan", "shadowsocks", "socks", "http", "wireguard", "hysteria", "hysteria2", "tuic":
		return true
	default:
		return false
	}
}

func validProbeTarget(raw string) bool {
	if raw == "" || len(raw) > maxProbeURLBytes || strings.TrimSpace(raw) != raw ||
		strings.ContainsAny(raw, "#\\") || strings.ContainsFunc(raw, unicode.IsControl) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n <= 0 || n > 65535 {
			return false
		}
	}
	return true
}
