package threexui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	maxUpdateRecords = 128
	maxUpdateJournal = 128 << 10
)

// XrayUpdateRequest identifies one ordinary online installation, not a staged
// installation or an offline rollback. ID is an opaque, non-secret operation
// ID. The caller must retain and reuse it when delivery is retried. Version is
// an exact tag from XrayVersions, never "latest". Expected values prevent a
// queued request from silently targeting a replaced provider or changed kernel.
type XrayUpdateRequest struct {
	ID                     string
	Version                string
	ExpectedPanelGUID      string
	ExpectedPanelVersion   string
	ExpectedRunningVersion string
}

type XrayUpdateState string

const (
	XrayUpdateNotStarted   XrayUpdateState = "not_started"
	XrayUpdateUnknown      XrayUpdateState = "unknown"
	XrayUpdateFailed       XrayUpdateState = "failed"
	XrayUpdateInconclusive XrayUpdateState = "inconclusive"
	XrayUpdateVerified     XrayUpdateState = "verified"
)

// XrayUpdateResult contains only bounded, locally generated diagnostics. A
// successful HTTP response is not Verified: the reported running version and
// the real data path must both be checked. Unknown means a remote installation
// might still be running; polling a target version never resolves that state.
type XrayUpdateResult struct {
	State         XrayUpdateState `json:"state"`
	Reason        string          `json:"reason"`
	TargetVersion string          `json:"target_version"`
	// These optional fields are independent, point-in-time service evidence.
	// They never turn a failed/unknown installation into a successful one.
	ServiceState   XrayServiceState `json:"service_state,omitempty"`
	ServiceVersion string           `json:"service_version,omitempty"`
	ServiceReason  string           `json:"service_reason,omitempty"`
	// Local observation time in Unix milliseconds, not an upstream sample timestamp. A replayed
	// reachable result is historical evidence, never current health.
	ServiceObservedAt int64  `json:"service_observed_at,omitempty"`
	RecoveryAttempted bool   `json:"recovery_attempted,omitempty"`
	RecoveryOutcome   string `json:"recovery_outcome,omitempty"`
}

// XrayUpdateFailure is returned for every retained non-Verified result. It
// deliberately contains no provider response, token, URL, or probe error.
type XrayUpdateFailure struct{ Result XrayUpdateResult }

func (e *XrayUpdateFailure) Error() string {
	return "3x-ui update did not verify successfully: " + e.Result.Reason
}

// XrayUpdateProbe must verify real bytes through the node's proxy data path
// using the intended credentials. A control-plane ping, process health check,
// or mere successful socket dial is not an acceptable implementation. The
// caller owns the credentials; the coordinator never stores them. A nil probe
// deliberately produces Inconclusive, not Verified. The callback must honor
// its context deadline; running it in an untracked background goroutine would
// break the coordinator's bounded recovery and single-writer guarantees.
type XrayUpdateProbe func(context.Context) error

type updateRecord struct {
	IDHash       string           `json:"id_hash"`
	RequestHash  string           `json:"request_hash"`
	ProviderHash string           `json:"provider_hash"`
	Result       XrayUpdateResult `json:"result"`
}

type updateJournal struct {
	Version      int            `json:"version"`
	EndpointHash string         `json:"endpoint_hash"`
	Records      []updateRecord `json:"records"`
}

// XrayUpdates is a candidate, isolated ordinary-update coordinator. It does
// not implement runtimeprovider.Upgrader or enable SHADOW mutations. One fixed,
// private, durable journal path must be owned by each provider for its whole
// lifetime, including container recreation. A lifetime OS lock excludes other
// coordinators using that path. It cannot exclude an administrator or another
// program using a different journal or directly calling the provider API.
//
// Results are retained, not evicted: after 128 requests it fails closed. Any
// result other than Verified or NotStarted closes the automatic update gate.
// Bounded Reconcile is available; manual settlement of unknown installations
// and safe history compaction are intentionally not exposed here. Deleting the
// journal or changing its path is not recovery and can re-enable unsafe retries.
type XrayUpdates struct {
	mu      sync.Mutex
	client  *Client
	path    string
	lock    *os.File
	journal updateJournal
	broken  bool
	write   func(string, []byte) error
	// Zero values select production bounds. Package tests may shorten them.
	recoveryPolicy updateRecoveryPolicy
}

// OpenXrayUpdates opens without dispatching any HTTP request. A crash after the
// durable dispatch barrier leaves Unknown on disk, so reopening cannot replay
// an installation. A missing, corrupt, unsafe, or incompatible existing journal
// is never silently reset. The caller must durably create the private parent
// directory first. Keep the lock file for the journal's entire lifetime. Give
// the Client an explicit bounded timeout suitable for downloading an update;
// its default 10-second request budget is usually too short for installation.
func OpenXrayUpdates(path string, client *Client) (*XrayUpdates, error) {
	if path == "" || client == nil {
		return nil, errors.New("3x-ui update journal path and client are required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("invalid 3x-ui update journal path")
	}
	dir := filepath.Dir(abs)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("3x-ui update journal directory must be private")
	}
	lock, freshLock, err := lockUpdateFile(abs + ".lock")
	if err != nil {
		return nil, err
	}
	u := &XrayUpdates{client: client, path: abs, lock: lock, write: writeUpdateFile}
	opened := false
	defer func() {
		if !opened {
			lock.Close()
		}
	}()
	info, err = os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		if !freshLock {
			return nil, errors.New("3x-ui update journal is missing beside an existing lock; recovery is required")
		}
		u.journal = updateJournal{Version: 1, EndpointHash: updateDigest(client.base.String()), Records: []updateRecord{}}
		if err := u.persist(); err != nil {
			return nil, err
		}
	} else {
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxUpdateJournal {
			return nil, errors.New("3x-ui update journal must be a private bounded regular file")
		}
		f, err := os.Open(abs)
		if err != nil {
			return nil, errors.New("cannot open 3x-ui update journal")
		}
		actual, statErr := f.Stat()
		if statErr != nil || !os.SameFile(info, actual) {
			f.Close()
			return nil, errors.New("3x-ui update journal changed while opening")
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxUpdateJournal+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || len(data) > maxUpdateJournal {
			return nil, errors.New("cannot read bounded 3x-ui update journal")
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		var journal updateJournal
		if decoder.Decode(&journal) != nil || decoder.Decode(new(any)) != io.EOF || !validUpdateJournal(journal) {
			return nil, errors.New("invalid 3x-ui update journal; recovery is required")
		}
		if journal.EndpointHash != updateDigest(client.base.String()) {
			return nil, errors.New("3x-ui update journal belongs to another endpoint")
		}
		u.journal = journal
	}
	opened = true
	return u, nil
}

// Close waits for a locally active request and releases the OS lock. It never
// cancels or assumes completion of an ambiguous remote installation.
func (u *XrayUpdates) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.lock == nil {
		return nil
	}
	err := u.lock.Close()
	u.lock = nil
	return err
}

// Run dispatches at most once per accepted request identity. Every mutation is
// preceded by a durable Unknown record. A timeout, transport error, crash, or
// malformed response preserves that barrier. A definite installation failure
// permits one bounded service-recovery restart only after confirming that the
// same provider is stopped. It never installs another version or claims rollback.
func (u *XrayUpdates) Run(ctx context.Context, request XrayUpdateRequest, probe XrayUpdateProbe) (XrayUpdateResult, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.lock == nil || u.broken {
		return XrayUpdateResult{}, errors.New("3x-ui update journal is closed or unavailable")
	}
	if !validUpdateRequest(request) {
		return XrayUpdateResult{}, errors.New("invalid 3x-ui update request")
	}
	idHash, requestHash := updateDigest(request.ID), updateDigestJSON(request)
	for _, record := range u.journal.Records {
		if record.IDHash == idHash {
			if record.RequestHash != requestHash {
				return XrayUpdateResult{}, errors.New("3x-ui update request identity was reused with a different payload")
			}
			return updateResult(record.Result)
		}
	}
	if len(u.journal.Records) >= maxUpdateRecords {
		return XrayUpdateResult{}, errors.New("3x-ui update journal is full; safe operator recovery is required")
	}
	if n := len(u.journal.Records); n > 0 && updateBlocksNext(u.journal.Records[n-1].Result.State) {
		return XrayUpdateResult{}, errors.New("3x-ui automatic updates are blocked by a retained unsuccessful or uncertain result")
	}
	record := updateRecord{IDHash: idHash, RequestHash: requestHash,
		ProviderHash: updateProviderDigest(request.ExpectedPanelGUID, request.ExpectedPanelVersion),
		Result:       XrayUpdateResult{State: XrayUpdateNotStarted, Reason: "preflight_incomplete", TargetVersion: request.Version}}
	u.journal.Records = append(u.journal.Records, record)
	index := len(u.journal.Records) - 1
	finish := func(state XrayUpdateState, reason string) (XrayUpdateResult, error) {
		u.journal.Records[index].Result.State = state
		u.journal.Records[index].Result.Reason = reason
		result := u.journal.Records[index].Result
		if err := u.persist(); err != nil {
			// The old durable dispatch barrier remains authoritative. A failed
			// write must not expose a local Verified state as durable success.
			return XrayUpdateResult{}, err
		}
		return updateResult(result)
	}
	if err := u.persist(); err != nil {
		return XrayUpdateResult{}, err
	}
	if ctx.Err() != nil {
		return finish(XrayUpdateNotStarted, "request_cancelled")
	}
	capabilities, err := u.client.DiscoverCapabilities(ctx)
	if err != nil || !capabilities.Supports(CapabilityStatus) || !capabilities.Supports(CapabilityXrayVersions) || !capabilities.Supports(CapabilityInstallXray) {
		return finish(XrayUpdateNotStarted, "capability_preflight_failed")
	}
	versions, err := u.client.XrayVersions(ctx)
	if err != nil {
		return finish(XrayUpdateNotStarted, "version_preflight_failed")
	}
	found := false
	for _, version := range versions {
		if version == request.Version {
			found = true
		}
	}
	if !found {
		return finish(XrayUpdateNotStarted, "target_version_not_offered")
	}
	status, err := u.client.Status(ctx)
	if err != nil {
		return finish(XrayUpdateNotStarted, "status_preflight_failed")
	}
	if status.PanelGUID != request.ExpectedPanelGUID || status.PanelVersion != request.ExpectedPanelVersion {
		return finish(XrayUpdateNotStarted, "provider_identity_changed")
	}
	if status.Xray.State != "running" || status.Xray.Version != request.ExpectedRunningVersion {
		return finish(XrayUpdateNotStarted, "running_version_changed")
	}
	if ctx.Err() != nil {
		return finish(XrayUpdateNotStarted, "request_cancelled")
	}
	// This fsynced record is the point of no automatic retry. Do not move it
	// after the HTTP call: the response can disappear while the server works.
	if _, err := finish(XrayUpdateUnknown, "installation_dispatched"); err != nil {
		var resultErr *XrayUpdateFailure
		if !errors.As(err, &resultErr) {
			return XrayUpdateResult{}, err
		}
	}
	if err := u.client.InstallXray(ctx, request.Version); err != nil {
		var installError *XrayInstallError
		if errors.As(err, &installError) {
			switch installError.Outcome {
			case XrayInstallNotStarted:
				return finish(XrayUpdateNotStarted, "request_not_dispatched")
			case XrayInstallFailed:
				if result, err := finish(XrayUpdateFailed, "installation_reported_failure"); u.broken {
					return result, err
				}
				return u.reconcileLocked(ctx, request, probe, index)
			}
		}
		if result, err := finish(XrayUpdateUnknown, "installation_outcome_unknown"); u.broken {
			return result, err
		}
		return u.reconcileLocked(ctx, request, probe, index)
	}
	// A definite successful response settles installation, not usability. Save
	// that distinction before making any read/probe that can itself fail.
	if _, err := finish(XrayUpdateInconclusive, "verification_incomplete"); err != nil {
		var resultErr *XrayUpdateFailure
		if !errors.As(err, &resultErr) {
			return XrayUpdateResult{}, err
		}
	}
	return u.verifyUpdate(ctx, request, probe, index)
}

// XrayUpdateObservation is a read-only point-in-time observation. Neither bool
// is evidence that an installation whose response was lost has terminated.
type XrayUpdateObservation struct {
	Recorded        XrayUpdateResult
	ProviderMatches bool
	RunningTarget   bool
}

// Observe reads status without changing any retained result or opening the
// update gate. In particular RunningTarget=true cannot clear Unknown.
func (u *XrayUpdates) Observe(ctx context.Context, requestID string) (XrayUpdateObservation, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.lock == nil || u.broken || !validUpdateID(requestID) {
		return XrayUpdateObservation{}, errors.New("3x-ui update observation is unavailable")
	}
	for _, record := range u.journal.Records {
		if record.IDHash != updateDigest(requestID) {
			continue
		}
		observation := XrayUpdateObservation{Recorded: record.Result}
		status, err := u.client.Status(ctx)
		if err != nil {
			return observation, errors.New("3x-ui update status could not be observed")
		}
		observation.ProviderMatches = updateProviderDigest(status.PanelGUID, status.PanelVersion) == record.ProviderHash
		observation.RunningTarget = observation.ProviderMatches && status.Xray.State == "running" && sameXrayRelease(status.Xray.Version, record.Result.TargetVersion)
		return observation, nil
	}
	return XrayUpdateObservation{}, errors.New("3x-ui update request was not found")
}

func updateResult(result XrayUpdateResult) (XrayUpdateResult, error) {
	if result.State == XrayUpdateVerified {
		return result, nil
	}
	return result, &XrayUpdateFailure{Result: result}
}

func updateBlocksNext(state XrayUpdateState) bool {
	return state != XrayUpdateVerified && state != XrayUpdateNotStarted
}

func validUpdateID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func validUpdateRequest(request XrayUpdateRequest) bool {
	return validUpdateID(request.ID) && validUpdateID(request.ExpectedPanelGUID) && validXrayVersion(request.Version) &&
		validXrayVersion(request.ExpectedPanelVersion) && validXrayVersion(request.ExpectedRunningVersion)
}

func sameXrayRelease(actual, tag string) bool {
	return validXrayVersion(actual) && validXrayVersion(tag) && strings.TrimPrefix(actual, "v") == strings.TrimPrefix(tag, "v")
}

func updateDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func updateDigestJSON(value any) string {
	data, _ := json.Marshal(value)
	return updateDigest(string(data))
}

func updateProviderDigest(guid, version string) string {
	return updateDigestJSON([2]string{guid, version})
}

func validUpdateJournal(journal updateJournal) bool {
	validHash := func(value string) bool {
		data, err := hex.DecodeString(value)
		return err == nil && len(data) == sha256.Size && value == strings.ToLower(value)
	}
	if journal.Version != 1 || !validHash(journal.EndpointHash) || journal.Records == nil || len(journal.Records) > maxUpdateRecords {
		return false
	}
	reasons := map[string]XrayUpdateState{
		"preflight_incomplete": XrayUpdateNotStarted, "request_cancelled": XrayUpdateNotStarted,
		"capability_preflight_failed": XrayUpdateNotStarted, "version_preflight_failed": XrayUpdateNotStarted,
		"target_version_not_offered": XrayUpdateNotStarted, "status_preflight_failed": XrayUpdateNotStarted,
		"provider_identity_changed": XrayUpdateNotStarted, "running_version_changed": XrayUpdateNotStarted,
		"request_not_dispatched":  XrayUpdateNotStarted,
		"installation_dispatched": XrayUpdateUnknown, "installation_outcome_unknown": XrayUpdateUnknown,
		"installation_reported_failure": XrayUpdateFailed, "verification_provider_changed": XrayUpdateFailed,
		"verification_running_target_failed": XrayUpdateFailed, "data_path_probe_failed": XrayUpdateFailed,
		"verification_incomplete": XrayUpdateInconclusive, "verification_status_unavailable": XrayUpdateInconclusive,
		"data_path_probe_missing": XrayUpdateInconclusive, "verification_cancelled": XrayUpdateInconclusive,
		"verified": XrayUpdateVerified,
	}
	seen := make(map[string]bool, len(journal.Records))
	for i, record := range journal.Records {
		if !validHash(record.IDHash) || !validHash(record.RequestHash) || !validHash(record.ProviderHash) || seen[record.IDHash] ||
			!validXrayVersion(record.Result.TargetVersion) || reasons[record.Result.Reason] != record.Result.State || record.Result.State == "" ||
			!validUpdateRecovery(record.Result) ||
			(i < len(journal.Records)-1 && updateBlocksNext(record.Result.State)) {
			return false
		}
		seen[record.IDHash] = true
	}
	return true
}

func (u *XrayUpdates) persist() error {
	data, err := json.Marshal(u.journal)
	if err != nil || !validUpdateJournal(u.journal) || len(data) > maxUpdateJournal {
		u.broken = true
		return errors.New("3x-ui update journal exceeds its validated bounds")
	}
	if err := u.write(u.path, data); err != nil {
		u.broken = true
		return errors.New("3x-ui update journal could not be durably saved; recovery is required")
	}
	return nil
}

func writeUpdateFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".xray-update-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(temporary, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync update directory: %w", err)
	}
	return nil
}
