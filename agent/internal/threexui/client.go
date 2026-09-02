package threexui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultTimeout          = 10 * time.Second
	defaultMaxResponseBytes = 4 << 20
	maxDiagnosticBytes      = 512
)

// Config describes one 3x-ui API endpoint.
type Config struct {
	// BaseURL is the panel origin plus its optional configured base path. It
	// must not include /panel/api, a query, or a fragment.
	BaseURL string
	// Token is a 3x-ui API token. It is sent only in the Authorization header.
	Token string
	// AllowPublic permits a non-loopback host, and only over HTTPS. It is
	// deliberately false by default because this client is intended to reach
	// the 3x-ui instance on the same node without exposing its admin token to a
	// network.
	AllowPublic bool
	// Timeout bounds a whole request. Zero selects the package default.
	Timeout time.Duration
	// MaxResponseBytes bounds both API envelopes and the OpenAPI document.
	// Zero selects the package default.
	MaxResponseBytes int64
	// HTTPClient optionally supplies a transport. The value is cloned: New
	// never changes the caller's client, timeout, or redirect policy.
	HTTPClient *http.Client
}

// Client is an authenticated, bounded 3x-ui API client.
type Client struct {
	base             *url.URL
	token            string
	http             *http.Client
	maxResponseBytes int64
}

// HTTPStatusError reports a response which did not have a 2xx status. The
// response body is intentionally excluded: proxies and error pages can echo
// request headers, including the full-admin API token.
type HTTPStatusError struct {
	StatusCode int
	Status     string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("3x-ui returned HTTP %s", e.Status)
}

// APIError reports a valid 3x-ui envelope whose success field is false.
type APIError struct {
	Message string
}

// ContractError reports a reachable provider whose response or advertised
// contract cannot be interpreted by this adapter. It intentionally retains no
// remote body or underlying decoder error.
type ContractError struct {
	Message string
}

func (e *ContractError) Error() string { return e.Message }

func (e *APIError) Error() string {
	if e.Message == "" {
		return "3x-ui reported an unsuccessful operation"
	}
	return "3x-ui reported an unsuccessful operation: " + e.Message
}

// ResponseTooLargeError reports a response that exceeded the configured
// memory bound.
type ResponseTooLargeError struct {
	Limit int64
}

func (e *ResponseTooLargeError) Error() string {
	return fmt.Sprintf("3x-ui response exceeds the %d-byte limit", e.Limit)
}

// New constructs a client after validating and canonicalising its endpoint.
func New(cfg Config) (*Client, error) {
	base, err := normaliseBaseURL(cfg.BaseURL, cfg.AllowPublic)
	if err != nil {
		return nil, err
	}
	if err := validateToken(cfg.Token); err != nil {
		return nil, err
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if timeout < 0 {
		return nil, fmt.Errorf("3x-ui timeout must be positive")
	}
	maxResponseBytes := cfg.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	if maxResponseBytes < 0 || maxResponseBytes == math.MaxInt64 {
		return nil, fmt.Errorf("3x-ui response limit must be positive")
	}

	httpClient := &http.Client{Transport: directTransport()}
	if cfg.HTTPClient != nil {
		clone := *cfg.HTTPClient
		httpClient = &clone
	}
	httpClient.Timeout = timeout
	// A redirect can move the Authorization header somewhere the operator did
	// not configure. Refusing all redirects also makes a reverse proxy with a
	// wrong base path fail loudly instead of silently reaching a login page.
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &Client{
		base:             base,
		token:            cfg.Token,
		http:             httpClient,
		maxResponseBytes: maxResponseBytes,
	}, nil
}

func directTransport() http.RoundTripper {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		clone := transport.Clone()
		// A node-local credential must not be handed to an ambient HTTP_PROXY
		// merely because the node-local 3x-ui address was not listed in NO_PROXY.
		clone.Proxy = nil
		return clone
	}
	return &http.Transport{}
}

func normaliseBaseURL(raw string, allowPublic bool) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("3x-ui base URL is required")
	}
	if strings.TrimSpace(raw) != raw {
		return nil, fmt.Errorf("3x-ui base URL must not contain surrounding whitespace")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("3x-ui base URL is invalid")
	}
	if u.Opaque != "" || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("3x-ui base URL must be an absolute HTTP URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("3x-ui base URL scheme must be http or https")
	}
	if u.User != nil {
		return nil, fmt.Errorf("3x-ui base URL must not contain user information")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return nil, fmt.Errorf("3x-ui base URL must not contain a query")
	}
	if u.Fragment != "" {
		return nil, fmt.Errorf("3x-ui base URL must not contain a fragment")
	}
	if u.RawPath != "" || strings.Contains(u.Path, "\\") {
		return nil, fmt.Errorf("3x-ui base URL contains an ambiguous path")
	}
	if strings.Contains(u.Path, "//") {
		return nil, fmt.Errorf("3x-ui base URL path must not contain empty segments")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, fmt.Errorf("3x-ui base URL path must not contain dot segments")
		}
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("3x-ui base URL must contain a host")
	}
	loopback := isLoopbackHost(u.Hostname())
	if !allowPublic && !loopback {
		return nil, fmt.Errorf("3x-ui base URL host is not loopback; set AllowPublic explicitly to permit it")
	}
	if !loopback && u.Scheme != "https" {
		return nil, fmt.Errorf("a non-loopback 3x-ui base URL must use https")
	}

	// Keep the origin and optional 3x-ui base path, but make endpoint joining
	// independent of whether the operator wrote a trailing slash.
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateToken(token string) error {
	if token == "" {
		return fmt.Errorf("3x-ui API token is required")
	}
	if strings.TrimSpace(token) != token {
		return fmt.Errorf("3x-ui API token must not contain surrounding whitespace")
	}
	for _, r := range token {
		// Header values cannot safely contain controls, whitespace, or non-ASCII
		// bytes. Do not include the rejected token in the diagnostic.
		if r <= 0x20 || r >= 0x7f {
			return fmt.Errorf("3x-ui API token contains an invalid character")
		}
	}
	return nil
}

func (c *Client) endpointURL(endpoint string) (*url.URL, error) {
	if endpoint == "" || strings.HasPrefix(endpoint, "/") || strings.Contains(endpoint, "?") || strings.Contains(endpoint, "#") {
		return nil, fmt.Errorf("invalid 3x-ui API endpoint")
	}
	for _, segment := range strings.Split(endpoint, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return nil, fmt.Errorf("invalid 3x-ui API endpoint")
		}
	}
	u := *c.base
	u.Path = c.base.Path + "/" + endpoint
	return &u, nil
}

func (c *Client) get(ctx context.Context, endpoint string) ([]byte, error) {
	u, err := c.endpointURL(endpoint)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, c.safeError("building a 3x-ui request", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, c.safeError("3x-ui request timed out", err)
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, c.safeError("3x-ui request timed out", err)
		}
		return nil, c.safeError("3x-ui request failed", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		status := strings.TrimSpace(fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode)))
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Status: status}
	}
	if resp.ContentLength > c.maxResponseBytes {
		return nil, &ResponseTooLargeError{Limit: c.maxResponseBytes}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
	if err != nil {
		return nil, c.safeError("reading the 3x-ui response", err)
	}
	if int64(len(body)) > c.maxResponseBytes {
		return nil, &ResponseTooLargeError{Limit: c.maxResponseBytes}
	}
	return body, nil
}

type envelope struct {
	Success *bool           `json:"success"`
	Message string          `json:"msg"`
	Object  json.RawMessage `json:"obj"`
}

func (c *Client) getEnvelope(ctx context.Context, endpoint string, dst any) error {
	body, err := c.get(ctx, endpoint)
	if err != nil {
		return err
	}
	var response envelope
	if err := json.Unmarshal(body, &response); err != nil {
		return &ContractError{Message: c.safeDiagnostic("decoding the 3x-ui response envelope: " + err.Error())}
	}
	if response.Success == nil {
		return &ContractError{Message: "3x-ui response envelope has no boolean success field"}
	}
	if !*response.Success {
		return &APIError{Message: c.safeDiagnostic(response.Message)}
	}
	if dst == nil {
		return nil
	}
	object := strings.TrimSpace(string(response.Object))
	if object == "" || object == "null" {
		return &ContractError{Message: "3x-ui response envelope has no object"}
	}
	if err := json.Unmarshal(response.Object, dst); err != nil {
		return &ContractError{Message: c.safeDiagnostic("decoding the 3x-ui response object: " + err.Error())}
	}
	return nil
}

type safeWrappedError struct {
	message string
	cause   error
}

func (e *safeWrappedError) Error() string { return e.message }

// Is preserves sentinel checks such as context.DeadlineExceeded without
// exposing the original error, whose text may have been produced by a custom
// transport that included the Authorization header.
func (e *safeWrappedError) Is(target error) bool { return errors.Is(e.cause, target) }

func (c *Client) safeError(prefix string, cause error) error {
	return &safeWrappedError{
		message: c.safeDiagnostic(prefix + ": " + cause.Error()),
		cause:   cause,
	}
}

func (c *Client) safeDiagnostic(message string) string {
	if c.token != "" {
		message = strings.ReplaceAll(message, c.token, "[REDACTED]")
	}
	var result strings.Builder
	for _, r := range message {
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		size := utf8.RuneLen(r)
		if size < 0 || result.Len()+size > maxDiagnosticBytes {
			break
		}
		result.WriteRune(r)
	}
	return strings.TrimSpace(result.String())
}
