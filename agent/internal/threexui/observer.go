package threexui

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	defaultCapabilitySuccessTTL = 5 * time.Minute
	defaultCapabilityFailureTTL = 30 * time.Second
)

// ObserverConfig controls how long an Observer trusts 3x-ui's live OpenAPI
// contract. Zero durations select the package defaults. Now exists so cache
// expiry can be tested without sleeping.
type ObserverConfig struct {
	SuccessTTL time.Duration
	FailureTTL time.Duration
	Now        func() time.Time
}

// Observation is the read-only runtime and contract snapshot exposed to the
// agent heartbeat. Capability slices follow RequiredCapabilities' stable order.
// A non-nil Error is safe to log or forward: Client has already redacted its
// bearer token from all remote and transport diagnostics.
type Observation struct {
	PanelVersion          string
	XrayState             string
	XrayVersion           string
	SupportedCapabilities []Capability
	MissingCapabilities   []Capability
	ContractDigest        string
	// ContractObservedAt is when the OpenAPI document was actually read. The
	// final-config permission was also verified for this panel version before
	// a successful snapshot is exposed. A status poll must not make an old
	// contract look freshly verified.
	ContractObservedAt time.Time
	Error              error
}

// Observer combines live status with a cached OpenAPI capability handshake.
// Status is fetched on every Observe call. Successful contract discoveries are
// cached longer than failures so an upgraded or temporarily unavailable panel
// self-recovers without receiving one OpenAPI request per heartbeat.
type Observer struct {
	client     *Client
	successTTL time.Duration
	failureTTL time.Duration
	now        func() time.Time

	mu              sync.Mutex
	capabilities    Capabilities
	hasCapabilities bool
	discoveryErr    error
	// discoveryPanelVersion is the status version associated with the most
	// recent discovery attempt (successful or failed). A panel upgrade must
	// invalidate the long success cache immediately; a failed re-discovery for
	// that same version still observes failureTTL instead of hammering it.
	discoveryPanelVersion string
	contractObservedAt    time.Time
	// A successful OpenAPI fetch already rechecks this credential on every
	// discovery. The much larger final config is read once per panel version
	// (and Agent process), avoiding a multi-megabyte transfer every five minutes.
	configReadPanelVersion string
	nextDiscovery          time.Time
	discoveryDone          chan struct{}
}

// NewObserver constructs a concurrency-safe, read-only 3x-ui observer.
func NewObserver(client *Client, cfg ObserverConfig) (*Observer, error) {
	if client == nil {
		return nil, fmt.Errorf("3x-ui observer client is required")
	}
	successTTL := cfg.SuccessTTL
	if successTTL == 0 {
		successTTL = defaultCapabilitySuccessTTL
	}
	if successTTL < 0 {
		return nil, fmt.Errorf("3x-ui capability success TTL must be positive")
	}
	failureTTL := cfg.FailureTTL
	if failureTTL == 0 {
		failureTTL = defaultCapabilityFailureTTL
	}
	if failureTTL < 0 {
		return nil, fmt.Errorf("3x-ui capability failure TTL must be positive")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Observer{
		client:     client,
		successTTL: successTTL,
		failureTTL: failureTTL,
		now:        now,
	}, nil
}

// Observe fetches current 3x-ui status and joins it with the latest capability
// handshake. It never invokes a mutating API operation.
func (o *Observer) Observe(ctx context.Context) Observation {
	status, statusErr := o.client.Status(ctx)
	capabilities, hasCapabilities, contractObservedAt, discoveryErr := o.observeCapabilities(ctx, status.PanelVersion)

	observation := Observation{}
	if statusErr == nil {
		observation.PanelVersion = status.PanelVersion
		observation.XrayState = status.Xray.State
		observation.XrayVersion = status.Xray.Version
	}
	if hasCapabilities {
		observation.ContractDigest = capabilities.DocumentSHA256
		observation.ContractObservedAt = contractObservedAt
		observation.MissingCapabilities = capabilities.Missing()
		for _, capability := range RequiredCapabilities() {
			if capabilities.Supports(capability) {
				observation.SupportedCapabilities = append(observation.SupportedCapabilities, capability)
			}
		}
	}

	var observationErrors []error
	if statusErr != nil {
		observationErrors = append(observationErrors, fmt.Errorf("reading 3x-ui status: %w", statusErr))
	}
	if discoveryErr != nil {
		observationErrors = append(observationErrors, fmt.Errorf("discovering 3x-ui capabilities: %w", discoveryErr))
	}
	observation.Error = errors.Join(observationErrors...)
	return observation
}

func (o *Observer) observeCapabilities(ctx context.Context, panelVersion string) (Capabilities, bool, time.Time, error) {
	for {
		o.mu.Lock()
		now := o.now()
		versionChanged := panelVersion != "" && panelVersion != o.discoveryPanelVersion
		if !versionChanged && !o.nextDiscovery.IsZero() && now.Before(o.nextDiscovery) {
			capabilities, hasCapabilities, observedAt, err := o.capabilitySnapshotLocked()
			o.mu.Unlock()
			return capabilities, hasCapabilities, observedAt, err
		}
		if done := o.discoveryDone; done != nil {
			o.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				o.mu.Lock()
				capabilities, hasCapabilities, observedAt, cachedErr := o.capabilitySnapshotLocked()
				o.mu.Unlock()
				waitErr := fmt.Errorf("waiting for 3x-ui capability discovery: %w", ctx.Err())
				return capabilities, hasCapabilities, observedAt, errors.Join(cachedErr, waitErr)
			}
		}

		done := make(chan struct{})
		o.discoveryDone = done
		needsConfigRead := panelVersion != "" && panelVersion != o.configReadPanelVersion
		o.mu.Unlock()

		capabilities, err := o.client.DiscoverCapabilities(ctx)
		configReadVerified := false
		if err == nil && capabilities.Supports(CapabilityGetConfig) && needsConfigRead {
			// OpenAPI describes routes independently of token scope. A safe read
			// of the assembled config proves this credential crosses the minimum
			// admin boundary Chiral's eventual reconciler requires, without
			// probing a mutating operation in SHADOW mode.
			_, err = o.client.ConfigJSON(ctx)
			configReadVerified = err == nil
		}

		o.mu.Lock()
		o.discoveryPanelVersion = panelVersion
		if err == nil {
			o.capabilities = capabilities
			o.hasCapabilities = true
			o.discoveryErr = nil
			o.contractObservedAt = o.now()
			if configReadVerified {
				o.configReadPanelVersion = panelVersion
			}
			o.nextDiscovery = o.now().Add(o.successTTL)
		} else {
			// Retain a previously successful snapshot while reporting that its
			// refresh failed. This avoids turning a transient panel failure into
			// a false claim that all capabilities disappeared.
			o.discoveryErr = err
			o.nextDiscovery = o.now().Add(o.failureTTL)
		}
		o.discoveryDone = nil
		close(done)
		capabilities, hasCapabilities, observedAt, snapshotErr := o.capabilitySnapshotLocked()
		o.mu.Unlock()
		return capabilities, hasCapabilities, observedAt, snapshotErr
	}
}

func (o *Observer) capabilitySnapshotLocked() (Capabilities, bool, time.Time, error) {
	return o.capabilities, o.hasCapabilities, o.contractObservedAt, o.discoveryErr
}
