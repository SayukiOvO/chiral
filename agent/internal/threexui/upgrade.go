package threexui

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

const (
	xrayVersionsEndpoint = "panel/api/server/getXrayVersion"
	xrayInstallEndpoint  = "panel/api/server/installXray/"
	maxXrayVersions      = 1024
	maxXrayVersionBytes  = 64
)

// XrayInstallOutcome describes what the HTTP exchange proves about a failed
// update request. It never describes dataplane health or successful rollback.
type XrayInstallOutcome string

const (
	// XrayInstallNotStarted means local validation or an already-cancelled
	// context prevented this method from dispatching any HTTP request.
	XrayInstallNotStarted XrayInstallOutcome = "not_started"
	// XrayInstallFailed means a complete success:false envelope was received.
	// Upstream's handler returned, but may already have stopped Xray or
	// replaced its binary. This does not mean the previous runtime is intact.
	XrayInstallFailed XrayInstallOutcome = "failed"
	// XrayInstallUnknown means there was no interpretable terminal envelope.
	// In particular, cancellation or timeout does not cancel upstream work.
	XrayInstallUnknown XrayInstallOutcome = "unknown"
)

// XrayInstallError retains only sanitized client errors. Outcome is suitable
// for a durable orchestrator's recovery decision, not an automatic retry.
type XrayInstallError struct {
	Outcome XrayInstallOutcome
	cause   error
}

func (e *XrayInstallError) Error() string {
	switch e.Outcome {
	case XrayInstallNotStarted:
		return "3x-ui Xray update was not requested"
	case XrayInstallFailed:
		return "3x-ui Xray update returned failure; the runtime may be partially changed"
	default:
		return "3x-ui Xray update outcome is unknown; the provider may still be executing it"
	}
}

// Unwrap exposes only the already-sanitized error, preserving timeout and
// cancellation sentinel checks without retaining a provider body in diagnostics.
func (e *XrayInstallError) Unwrap() error { return e.cause }

// XrayVersions returns upstream's available release tags in their original
// order, not the installed or running version (those belong to Status).
// The official v3.8.5 endpoint can serve a cached list; availability is not a
// guarantee that a later installation will be accepted or downloadable.
func (c *Client) XrayVersions(ctx context.Context) ([]string, error) {
	u, err := c.endpointURL(xrayVersionsEndpoint)
	if err != nil {
		return nil, err
	}
	var versions []string
	if err := c.sensitiveEnvelope(ctx, http.MethodGet, u, nil, "", &versions); err != nil {
		return nil, err
	}
	if len(versions) > maxXrayVersions {
		return nil, &ContractError{Message: "3x-ui returned too many Xray release tags"}
	}
	for _, version := range versions {
		if !validXrayVersion(version) {
			return nil, &ContractError{Message: "3x-ui returned an invalid Xray release tag"}
		}
	}
	return versions, nil
}

// InstallXray makes exactly one ordinary update request for an exact release
// tag. It neither preinstalls nor preserves a local rollback binary: upstream
// stops the current process, downloads/replaces its binary, and restarts it.
// The caller must select the exact tag from XrayVersions and journal its intent
// before invoking this method. No availability lookup or retry is implicit.
//
// A nil error means only that upstream returned success:true. The caller must
// still verify the reported running version and the real dataplane. A failed
// request may already have changed the runtime. An unknown outcome must not be
// replayed automatically: upstream can keep working after this request's
// bounded HTTP timeout or context cancellation, including after this returns.
// Config.Timeout and ctx bound the HTTP exchange, not upstream execution.
func (c *Client) InstallXray(ctx context.Context, version string) error {
	if !validXrayVersion(version) {
		return &XrayInstallError{Outcome: XrayInstallNotStarted,
			cause: errors.New("invalid 3x-ui Xray release tag")}
	}
	if err := ctx.Err(); err != nil {
		return &XrayInstallError{Outcome: XrayInstallNotStarted, cause: err}
	}
	u, err := c.endpointURL(xrayInstallEndpoint + version)
	if err != nil {
		return &XrayInstallError{Outcome: XrayInstallNotStarted, cause: err}
	}
	if err := c.sensitiveEnvelope(ctx, http.MethodPost, u, nil, "", nil); err != nil {
		outcome := XrayInstallUnknown
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			outcome = XrayInstallFailed
		}
		return &XrayInstallError{Outcome: outcome, cause: err}
	}
	return nil
}

// validXrayVersion follows the numeric release tags exposed by official 3.8.5,
// with or without one lowercase v. Do not interpret aliases or arbitrary Git
// references, trim input, or normalize a tag before upstream membership checks.
func validXrayVersion(version string) bool {
	if len(version) == 0 || len(version) > maxXrayVersionBytes {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}
