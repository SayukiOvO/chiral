package threexui

import (
	"context"
	"errors"
	"time"
)

// XrayServiceState describes observed service reachability, not the outcome of
// the installation or whether a remote handler has finished running.
type XrayServiceState string

const (
	XrayServiceReachable   XrayServiceState = "reachable"
	XrayServiceUnreachable XrayServiceState = "unreachable"
	XrayServiceUnknown     XrayServiceState = "unknown"
)

type updateRecoveryPolicy struct {
	budget time.Duration
	poll   time.Duration
	probe  time.Duration
}

func (u *XrayUpdates) recoveryBounds() updateRecoveryPolicy {
	p := u.recoveryPolicy
	if p.budget <= 0 {
		p.budget = 30 * time.Second
	}
	if p.poll <= 0 {
		// Official 3x-ui caches status for two seconds. Two immediate identical
		// reads are not independent evidence that the service stayed stopped.
		p.poll = 2500 * time.Millisecond
	}
	if p.probe <= 0 {
		p.probe = 3 * time.Second
	}
	return p
}

// Reconcile resumes bounded service observation for only the latest blocked
// request. Its exact payload must
// match the retained request hash. An unequivocal installation success:false
// may receive one journaled restart of the currently installed binary, never a
// binary rollback. Unknown installations receive only GETs and data-path probes.
//
// Failed/unknown installation recovery is deliberately independent of caller
// cancellation, with a fresh 30-second maximum budget; once an installation has stopped the service,
// losing the original request context should not abandon the recovery attempt.
// A successful service probe cannot settle an ambiguous installation handler or
// release the installation gate. A retained Inconclusive result after a known
// successful response may resume target-version verification, which does honor
// the caller's cancellation. Run with the same
// ID remains a pure replay, including the original local observation timestamp.
func (u *XrayUpdates) Reconcile(ctx context.Context, request XrayUpdateRequest, probe XrayUpdateProbe) (XrayUpdateResult, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.lock == nil || u.broken || !validUpdateRequest(request) {
		return XrayUpdateResult{}, errors.New("3x-ui update reconciliation is unavailable")
	}
	index := len(u.journal.Records) - 1
	if index < 0 {
		return XrayUpdateResult{}, errors.New("3x-ui update reconciliation has no retained request")
	}
	record := u.journal.Records[index]
	if record.IDHash != updateDigest(request.ID) || record.RequestHash != updateDigestJSON(request) || !updateBlocksNext(record.Result.State) {
		return XrayUpdateResult{}, errors.New("3x-ui update reconciliation requires the exact latest blocked request")
	}
	if record.Result.State == XrayUpdateInconclusive {
		return u.verifyUpdate(ctx, request, probe, index)
	}
	return u.reconcileLocked(ctx, request, probe, index)
}

func (u *XrayUpdates) reconcileLocked(ctx context.Context, request XrayUpdateRequest, probe XrayUpdateProbe, index int) (XrayUpdateResult, error) {
	policy := u.recoveryBounds()
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), policy.budget)
	defer cancel()
	result := &u.journal.Records[index].Result
	allowRestart := result.State == XrayUpdateFailed && result.Reason == "installation_reported_failure" && !result.RecoveryAttempted
	if err := u.observeService(bounded, request, probe, index, false, allowRestart, policy); err != nil {
		return XrayUpdateResult{}, err
	}
	if err := u.persist(); err != nil {
		return XrayUpdateResult{}, err
	}
	return updateResult(*result)
}

func (u *XrayUpdates) verifyUpdate(ctx context.Context, request XrayUpdateRequest, probe XrayUpdateProbe, index int) (XrayUpdateResult, error) {
	policy := u.recoveryBounds()
	bounded, cancel := context.WithTimeout(ctx, policy.budget)
	defer cancel()
	if err := u.observeService(bounded, request, probe, index, true, false, policy); err != nil {
		return XrayUpdateResult{}, err
	}
	result := &u.journal.Records[index].Result
	switch {
	case ctx.Err() != nil:
		result.State, result.Reason = XrayUpdateInconclusive, "verification_cancelled"
	case result.ServiceState == XrayServiceReachable:
		result.State, result.Reason = XrayUpdateVerified, "verified"
	case result.ServiceReason == "provider_changed":
		result.State, result.Reason = XrayUpdateFailed, "verification_provider_changed"
	case result.ServiceReason == "probe_missing":
		result.State, result.Reason = XrayUpdateInconclusive, "data_path_probe_missing"
	case result.ServiceReason == "data_path_failed":
		result.State, result.Reason = XrayUpdateFailed, "data_path_probe_failed"
	case result.ServiceReason == "not_running" || result.ServiceReason == "target_not_observed":
		result.State, result.Reason = XrayUpdateFailed, "verification_running_target_failed"
	default:
		result.State, result.Reason = XrayUpdateInconclusive, "verification_status_unavailable"
	}
	if err := u.persist(); err != nil {
		return XrayUpdateResult{}, err
	}
	return updateResult(*result)
}

// observeService is always called with the single-writer mutex held. It only
// stores bounded enums and validated versions, never a raw provider/probe error.
func (u *XrayUpdates) observeService(ctx context.Context, request XrayUpdateRequest, probe XrayUpdateProbe, index int, targetOnly, allowRestart bool, policy updateRecoveryPolicy) error {
	result := &u.journal.Records[index].Result
	setService(result, XrayServiceUnknown, "", "service_not_observed")
	stoppedReads := 0
	probeConflict := false
	for ctx.Err() == nil {
		status, err := u.client.Status(ctx)
		if err != nil {
			if ctx.Err() != nil {
				// Reaching this round's deadline is not new evidence that
				// overwrites a confirmed wrong version or failed data path.
				return nil
			}
			setService(result, XrayServiceUnknown, "", "status_unavailable")
			stoppedReads = 0
		} else if !updateSameProvider(status, request) {
			setService(result, XrayServiceUnknown, "", "provider_changed")
			return nil
		} else if status.Xray.State == "running" {
			probeConflict = false
			// Do not interrupt a service currently observed running. A cached
			// running sample with a failed probe does not prohibit recovery if
			// subsequent, separated samples confirm the process is stopped.
			stoppedReads = 0
			if !validXrayVersion(status.Xray.Version) {
				setService(result, XrayServiceUnknown, "", "running_version_unavailable")
			} else if targetOnly && !sameXrayRelease(status.Xray.Version, request.Version) {
				setService(result, XrayServiceUnknown, status.Xray.Version, "target_not_observed")
			} else if probe == nil {
				setService(result, XrayServiceUnknown, status.Xray.Version, "probe_missing")
				return nil
			} else if err := runUpdateProbe(ctx, probe, policy.probe); err == nil && ctx.Err() == nil {
				setService(result, XrayServiceReachable, status.Xray.Version, "data_path_verified")
				return nil
			} else if ctx.Err() != nil {
				setService(result, XrayServiceUnknown, status.Xray.Version, "context_expired")
				return nil
			} else {
				setService(result, XrayServiceUnreachable, status.Xray.Version, "data_path_failed")
			}
		} else if status.Xray.State == "stop" || status.Xray.State == "error" {
			// A stopped process's version field is cached runtime metadata,
			// not evidence of which binary is currently on disk.
			if probeConflict {
				setService(result, XrayServiceUnknown, "", "status_probe_conflict")
			} else {
				setService(result, XrayServiceUnreachable, "", "not_running")
			}
			stoppedReads++
			if allowRestart && stoppedReads >= 2 && !result.RecoveryAttempted {
				capabilities, err := u.client.DiscoverCapabilities(ctx)
				if err != nil || !capabilities.Supports(CapabilityStatus) || !capabilities.Supports(CapabilityXrayRestart) {
					setService(result, XrayServiceUnknown, "", "recovery_capability_unavailable")
					return nil
				}
				if probe != nil {
					// Status has no upstream sample timestamp. Even separated
					// stopped samples must not trump actual flowing bytes.
					probeErr := runUpdateProbe(ctx, probe, min(policy.probe, 2*time.Second))
					if ctx.Err() != nil {
						setService(result, XrayServiceUnknown, "", "context_expired")
						return nil
					}
					if probeErr == nil {
						setService(result, XrayServiceUnknown, "", "status_probe_conflict")
						probeConflict = true
						allowRestart = false
						if !waitUpdatePoll(ctx, policy.poll) {
							return nil
						}
						continue
					}
				}
				// Recheck after both the handshake and the possibly slow probe.
				// A provider that changed or came back meanwhile must not receive
				// an unconditional restart. The official API has no CAS, so this
				// cannot coordinate an external administrator racing this call.
				latest, err := u.client.Status(ctx)
				if err != nil {
					setService(result, XrayServiceUnknown, "", "status_unavailable")
					stoppedReads = 0
				} else if !updateSameProvider(latest, request) {
					setService(result, XrayServiceUnknown, "", "provider_changed")
					return nil
				} else if latest.Xray.State == "stop" || latest.Xray.State == "error" {
					if ctx.Err() != nil {
						return nil
					}
					result.RecoveryAttempted, result.RecoveryOutcome = true, "unknown"
					// This durable barrier survives response loss and a process
					// crash. No subsequent call can issue a second recovery POST.
					if err := u.persist(); err != nil {
						return err
					}
					err = u.client.RestartXray(ctx)
					if err == nil {
						result.RecoveryOutcome = "succeeded"
					} else {
						var apiError *APIError
						if errors.As(err, &apiError) {
							result.RecoveryOutcome = "failed"
						}
					}
					if err := u.persist(); err != nil {
						return err
					}
					allowRestart = false
					stoppedReads = 0
				} else {
					stoppedReads = 0
				}
			}
		} else {
			setService(result, XrayServiceUnknown, "", "state_unrecognized")
			stoppedReads = 0
		}
		if !waitUpdatePoll(ctx, policy.poll) {
			return nil
		}
	}
	return nil
}

func runUpdateProbe(ctx context.Context, probe XrayUpdateProbe, budget time.Duration) error {
	bounded, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if err := probe(bounded); err != nil {
		return err
	}
	return bounded.Err()
}

func waitUpdatePoll(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func updateSameProvider(status Status, request XrayUpdateRequest) bool {
	return status.PanelGUID == request.ExpectedPanelGUID && status.PanelVersion == request.ExpectedPanelVersion
}

func setService(result *XrayUpdateResult, state XrayServiceState, version, reason string) {
	result.ServiceState, result.ServiceVersion, result.ServiceReason = state, version, reason
	result.ServiceObservedAt = time.Now().UnixMilli()
}

func validUpdateRecovery(result XrayUpdateResult) bool {
	if result.RecoveryAttempted {
		if result.State != XrayUpdateFailed || result.Reason != "installation_reported_failure" ||
			(result.RecoveryOutcome != "unknown" && result.RecoveryOutcome != "failed" && result.RecoveryOutcome != "succeeded") {
			return false
		}
	} else if result.RecoveryOutcome != "" {
		return false
	}
	if result.ServiceVersion != "" && !validXrayVersion(result.ServiceVersion) {
		return false
	}
	if result.ServiceState == "" {
		// v1 records written before recovery fields existed remain valid.
		return result.ServiceVersion == "" && result.ServiceReason == "" && result.ServiceObservedAt == 0 && !result.RecoveryAttempted
	}
	if result.State == XrayUpdateNotStarted || result.ServiceObservedAt <= 0 || result.ServiceObservedAt > 253402300799999 {
		return false
	}
	switch result.ServiceState {
	case XrayServiceReachable:
		return result.ServiceReason == "data_path_verified" && result.ServiceVersion != ""
	case XrayServiceUnreachable:
		return result.ServiceReason == "not_running" && result.ServiceVersion == "" ||
			result.ServiceReason == "data_path_failed" && result.ServiceVersion != ""
	case XrayServiceUnknown:
		switch result.ServiceReason {
		case "service_not_observed", "status_unavailable", "provider_changed", "running_version_unavailable", "recovery_capability_unavailable", "state_unrecognized", "status_probe_conflict":
			return result.ServiceVersion == ""
		case "target_not_observed", "probe_missing":
			return result.ServiceVersion != ""
		case "context_expired":
			return true
		}
	}
	return false
}
