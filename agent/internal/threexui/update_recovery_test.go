package threexui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func stopOnInstallFailure(f *updateFixture) {
	f.install = func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.status.Xray.State = "stop"
		f.mu.Unlock()
		fmt.Fprint(w, `{"success":false,"msg":"private-install-error"}`)
	}
}

func probeAfterRestart(f *updateFixture) XrayUpdateProbe {
	return func(context.Context) error {
		if f.restarts.Load() == 0 {
			return errors.New("private-probe-credentials")
		}
		return nil
	}
}

func TestXrayUpdateRecoveryDefaultsExceedOfficialStatusCache(t *testing.T) {
	policy := new(XrayUpdates).recoveryBounds()
	if policy.poll <= 2*time.Second || policy.budget != 30*time.Second {
		t.Fatalf("unsafe production recovery bounds: %+v", policy)
	}
}

func TestXrayUpdateRecoveryRestoresServiceButNeverReportsRollbackOrUpdateSuccess(t *testing.T) {
	for _, version := range []string{"26.8.3", "26.9.9"} {
		t.Run(version, func(t *testing.T) {
			f := newUpdateFixture(t)
			stopOnInstallFailure(f)
			path := filepath.Join(privateUpdateDir(t), "updates.json")
			f.restart = func(w http.ResponseWriter, _ *http.Request) {
				data, err := os.ReadFile(path)
				var journal updateJournal
				if err != nil || json.Unmarshal(data, &journal) != nil || len(journal.Records) != 1 ||
					!journal.Records[0].Result.RecoveryAttempted || journal.Records[0].Result.RecoveryOutcome != "unknown" {
					t.Error("restart arrived without a durable recovery barrier")
				}
				f.mu.Lock()
				f.status.Xray.State, f.status.Xray.Version = "running", version
				f.mu.Unlock()
				fmt.Fprint(w, `{"success":true}`)
			}
			u := openTestUpdates(t, path, f.client)
			result, err := u.Run(context.Background(), updateRequest(), probeAfterRestart(f))
			requireUpdateState(t, result, err, XrayUpdateFailed)
			if result.Reason != "installation_reported_failure" || !result.RecoveryAttempted || result.RecoveryOutcome != "succeeded" ||
				result.ServiceState != XrayServiceReachable || result.ServiceVersion != version || result.ServiceObservedAt <= 0 || f.restarts.Load() != 1 {
				t.Fatalf("unexpected recovery result: %+v, restarts=%d", result, f.restarts.Load())
			}
			request := updateRequest()
			request.ID = "another-install"
			if _, err := u.Run(context.Background(), request, nil); err == nil || f.calls.Load() != 1 {
				t.Fatal("service recovery reopened the installation gate")
			}
			data, _ := os.ReadFile(path)
			for _, secret := range []string{"private-install-error", "private-probe-credentials", testToken} {
				if strings.Contains(string(data), secret) || strings.Contains(err.Error(), secret) {
					t.Fatal("private recovery diagnostic escaped")
				}
			}
		})
	}
}

func TestXrayUpdateRecoveryDoesNotInterruptAlreadyRunningService(t *testing.T) {
	for _, version := range []string{"26.8.3", "26.9.9"} {
		t.Run(version, func(t *testing.T) {
			f := newUpdateFixture(t)
			f.install = func(w http.ResponseWriter, _ *http.Request) {
				f.setVersion(version)
				fmt.Fprint(w, `{"success":false}`)
			}
			u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
			result, err := u.Run(context.Background(), updateRequest(), func(context.Context) error { return nil })
			requireUpdateState(t, result, err, XrayUpdateFailed)
			if result.ServiceState != XrayServiceReachable || result.ServiceVersion != version || result.RecoveryAttempted || f.restarts.Load() != 0 {
				t.Fatalf("running service was disturbed: %+v", result)
			}
		})
	}
}

func TestXrayUpdateRecoveryWaitsPastCachedRunningSample(t *testing.T) {
	f := newUpdateFixture(t)
	stopOnInstallFailure(f)
	f.statusHook = func(count int32, status *Status) {
		if count == 2 { // First recovery read still contains the pre-update cache.
			status.Xray.State = "running"
		}
	}
	u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
	result, err := u.Run(context.Background(), updateRequest(), probeAfterRestart(f))
	requireUpdateState(t, result, err, XrayUpdateFailed)
	if result.ServiceState != XrayServiceReachable || f.restarts.Load() != 1 || f.statusReads.Load() < 6 {
		t.Fatalf("cached running sample prevented bounded recovery: %+v, reads=%d", result, f.statusReads.Load())
	}
}

func TestXrayUpdateRecoveryBoundedProbeLeavesTimeToRestoreService(t *testing.T) {
	f := newUpdateFixture(t)
	stopOnInstallFailure(f)
	f.statusHook = func(count int32, status *Status) {
		if count == 2 {
			status.Xray.State = "running"
		}
	}
	u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
	var failedProbes atomic.Int32
	probe := func(ctx context.Context) error {
		if f.restarts.Load() == 0 {
			failedProbes.Add(1)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	result, err := u.Run(context.Background(), updateRequest(), probe)
	requireUpdateState(t, result, err, XrayUpdateFailed)
	if result.ServiceState != XrayServiceReachable || f.restarts.Load() != 1 || failedProbes.Load() < 2 {
		t.Fatalf("context-honoring probes exhausted the recovery budget: %+v probes=%d", result, failedProbes.Load())
	}
}

func TestXrayUpdateRecoveryCachedStoppedStatusCannotOverrideFlowingBytes(t *testing.T) {
	f := newUpdateFixture(t)
	stopOnInstallFailure(f)
	u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
	result, err := u.Run(context.Background(), updateRequest(), func(context.Context) error { return nil })
	requireUpdateState(t, result, err, XrayUpdateFailed)
	if result.ServiceState != XrayServiceUnknown || result.ServiceReason != "status_probe_conflict" || result.RecoveryAttempted || f.restarts.Load() != 0 {
		t.Fatalf("stale status overrode a successful data-path probe: %+v", result)
	}
}

func TestXrayUpdateRecoveryChecksForServiceReturnAfterSlowProbe(t *testing.T) {
	f := newUpdateFixture(t)
	stopOnInstallFailure(f)
	u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
	var probes atomic.Int32
	probe := func(context.Context) error {
		if probes.Add(1) == 1 {
			// The service comes back just after this failed probe. Its status
			// must be read again before any unconditional restart is sent.
			f.mu.Lock()
			f.status.Xray.State = "running"
			f.mu.Unlock()
			return errors.New("connection ended while service returned")
		}
		return nil
	}
	result, err := u.Run(context.Background(), updateRequest(), probe)
	requireUpdateState(t, result, err, XrayUpdateFailed)
	if result.ServiceState != XrayServiceReachable || result.RecoveryAttempted || f.restarts.Load() != 0 {
		t.Fatalf("restart interrupted a service that came back during probing: %+v", result)
	}
}

func TestXrayUpdateRecoveryLostRestartResponseNeverRetriesAcrossReopen(t *testing.T) {
	f := newUpdateFixture(t)
	stopOnInstallFailure(f)
	f.restart = func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.status.Xray.State = "running"
		f.mu.Unlock()
		fmt.Fprint(w, `{"success":`) // Work finished but its response was lost.
	}
	path := filepath.Join(privateUpdateDir(t), "updates.json")
	u := openTestUpdates(t, path, f.client)
	result, err := u.Run(context.Background(), updateRequest(), probeAfterRestart(f))
	requireUpdateState(t, result, err, XrayUpdateFailed)
	if !result.RecoveryAttempted || result.RecoveryOutcome != "unknown" || result.ServiceState != XrayServiceReachable {
		t.Fatal(result)
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	u = openTestUpdates(t, path, f.client)
	before := f.statusReads.Load()
	replay, err := u.Run(context.Background(), updateRequest(), nil)
	requireUpdateState(t, replay, err, XrayUpdateFailed)
	if replay != result || f.statusReads.Load() != before {
		t.Fatal("same-ID Run stopped being a pure retained replay")
	}
	result, err = u.Reconcile(context.Background(), updateRequest(), probeAfterRestart(f))
	requireUpdateState(t, result, err, XrayUpdateFailed)
	if result.RecoveryOutcome != "unknown" || f.restarts.Load() != 1 || f.calls.Load() != 1 {
		t.Fatal("lost recovery response caused a repeated mutation")
	}
}

func TestXrayUpdateRecoveryCheckpointFailurePreventsRestart(t *testing.T) {
	f := newUpdateFixture(t)
	stopOnInstallFailure(f)
	u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
	u.write = func(path string, data []byte) error {
		var journal updateJournal
		if json.Unmarshal(data, &journal) == nil && len(journal.Records) > 0 && journal.Records[0].Result.RecoveryAttempted {
			return errors.New("injected checkpoint failure")
		}
		return writeUpdateFile(path, data)
	}
	if _, err := u.Run(context.Background(), updateRequest(), probeAfterRestart(f)); err == nil || f.restarts.Load() != 0 {
		t.Fatal("restart was sent without a durable checkpoint")
	}
}

func TestXrayUpdateRecoveryCrashAfterRestartDoesNotRepeat(t *testing.T) {
	f := newUpdateFixture(t)
	stopOnInstallFailure(f)
	path := filepath.Join(privateUpdateDir(t), "updates.json")
	u := openTestUpdates(t, path, f.client)
	u.write = func(path string, data []byte) error {
		var journal updateJournal
		if json.Unmarshal(data, &journal) == nil && len(journal.Records) > 0 && journal.Records[0].Result.RecoveryOutcome == "succeeded" {
			return errors.New("crash before terminal recovery checkpoint")
		}
		return writeUpdateFile(path, data)
	}
	if _, err := u.Run(context.Background(), updateRequest(), probeAfterRestart(f)); err == nil || f.restarts.Load() != 1 {
		t.Fatal("fixture did not simulate lost terminal recovery checkpoint")
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	u = openTestUpdates(t, path, f.client)
	result, err := u.Reconcile(context.Background(), updateRequest(), probeAfterRestart(f))
	requireUpdateState(t, result, err, XrayUpdateFailed)
	if result.RecoveryOutcome != "unknown" || result.ServiceState != XrayServiceReachable || f.restarts.Load() != 1 {
		t.Fatalf("crash recovery redispatched restart: %+v", result)
	}
}

func TestXrayUpdateRecoveryReconcilesRetainedFailureWithCancelledCaller(t *testing.T) {
	f := newUpdateFixture(t)
	stopOnInstallFailure(f)
	delete(f.paths, "/panel/api/server/restartXrayService")
	path := filepath.Join(privateUpdateDir(t), "updates.json")
	u := openTestUpdates(t, path, f.client)
	result, err := u.Run(context.Background(), updateRequest(), probeAfterRestart(f))
	requireUpdateState(t, result, err, XrayUpdateFailed)
	if result.RecoveryAttempted || result.ServiceReason != "recovery_capability_unavailable" {
		t.Fatal(result)
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	f.paths["/panel/api/server/restartXrayService"] = map[string]any{"post": map[string]any{}}
	u = openTestUpdates(t, path, f.client)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = u.Reconcile(ctx, updateRequest(), probeAfterRestart(f))
	requireUpdateState(t, result, err, XrayUpdateFailed)
	if result.ServiceState != XrayServiceReachable || f.restarts.Load() != 1 {
		t.Fatal("caller cancellation prevented independent bounded recovery")
	}
}

func TestXrayUpdateRecoveryUnknownInstallNeverRestartsStoppedService(t *testing.T) {
	f := newUpdateFixture(t)
	f.install = func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.status.Xray.State = "stop"
		f.mu.Unlock()
		fmt.Fprint(w, `{"success":`)
	}
	u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
	result, err := u.Run(context.Background(), updateRequest(), probeAfterRestart(f))
	requireUpdateState(t, result, err, XrayUpdateUnknown)
	result, err = u.Reconcile(context.Background(), updateRequest(), probeAfterRestart(f))
	requireUpdateState(t, result, err, XrayUpdateUnknown)
	if result.RecoveryAttempted || f.restarts.Load() != 0 || f.calls.Load() != 1 {
		t.Fatal("ambiguous install caused a second mutation")
	}
}

func TestXrayUpdateRecoveryRejectsProviderChangesAndUnknownStates(t *testing.T) {
	for _, changeAt := range []int32{2, 4} {
		t.Run(fmt.Sprintf("identity-before-read-%d", changeAt), func(t *testing.T) {
			f := newUpdateFixture(t)
			stopOnInstallFailure(f)
			f.statusHook = func(count int32, status *Status) {
				if count >= changeAt {
					status.PanelGUID = "private-replacement-guid"
				}
			}
			u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
			result, err := u.Run(context.Background(), updateRequest(), probeAfterRestart(f))
			requireUpdateState(t, result, err, XrayUpdateFailed)
			if result.ServiceState != XrayServiceUnknown || result.ServiceReason != "provider_changed" || f.restarts.Load() != 0 {
				t.Fatal(result)
			}
		})
	}
	f := newUpdateFixture(t)
	stopOnInstallFailure(f)
	f.statusHook = func(count int32, status *Status) {
		if count >= 2 {
			status.Xray.State = "initializing"
		}
	}
	u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
	result, err := u.Run(context.Background(), updateRequest(), nil)
	requireUpdateState(t, result, err, XrayUpdateFailed)
	if result.ServiceState != XrayServiceUnknown || result.ServiceReason != "state_unrecognized" || f.restarts.Load() != 0 {
		t.Fatal("unrecognized runtime state was interpreted as stopped")
	}
}

func TestXrayUpdateRecoveryWithoutProbeCannotClaimRecoveredService(t *testing.T) {
	f := newUpdateFixture(t)
	stopOnInstallFailure(f)
	u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
	result, err := u.Run(context.Background(), updateRequest(), nil)
	requireUpdateState(t, result, err, XrayUpdateFailed)
	if !result.RecoveryAttempted || f.restarts.Load() != 1 || result.ServiceState != XrayServiceUnknown || result.ServiceReason != "probe_missing" {
		t.Fatal(result)
	}
}

func TestXrayUpdateRecoverySuccessVerificationWaitsForStatusCache(t *testing.T) {
	f := newUpdateFixture(t)
	f.statusHook = func(count int32, status *Status) {
		if count <= 3 {
			status.Xray.Version = "26.8.3"
		}
	}
	u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
	var probes atomic.Int32
	result, err := u.Run(context.Background(), updateRequest(), func(context.Context) error { probes.Add(1); return nil })
	requireUpdateState(t, result, err, XrayUpdateVerified)
	if f.statusReads.Load() < 4 || probes.Load() != 1 || f.restarts.Load() != 0 {
		t.Fatal("verification did not wait for the running target version")
	}
}

func TestXrayUpdateRecoveryKnownSuccessfulInstallMayResumeVerification(t *testing.T) {
	f := newUpdateFixture(t)
	u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
	result, err := u.Run(context.Background(), updateRequest(), nil)
	requireUpdateState(t, result, err, XrayUpdateInconclusive)
	result, err = u.Reconcile(context.Background(), updateRequest(), func(context.Context) error { return nil })
	requireUpdateState(t, result, err, XrayUpdateVerified)
	if f.calls.Load() != 1 || f.restarts.Load() != 0 {
		t.Fatal("verification resumption unexpectedly mutated the provider")
	}
}

func TestXrayUpdateRecoveryRequiresExactLatestBlockedRequest(t *testing.T) {
	f := newUpdateFixture(t)
	u := openTestUpdates(t, filepath.Join(privateUpdateDir(t), "updates.json"), f.client)
	first := updateRequest()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = u.Run(ctx, first, nil)
	last := updateRequest()
	last.ID = "last-request"
	_, _ = u.Run(context.Background(), last, nil)
	for _, request := range []XrayUpdateRequest{first, func() XrayUpdateRequest { changed := last; changed.Version = "v26.8.3"; return changed }()} {
		if _, err := u.Reconcile(context.Background(), request, nil); err == nil {
			t.Fatal("reconciliation accepted an old operation or modified payload")
		}
	}
	if f.calls.Load() != 1 || f.restarts.Load() != 0 {
		t.Fatal("invalid reconciliation changed the runtime")
	}
}

func TestXrayUpdateRecoveryReadsV1JournalWithoutChangingRequestHash(t *testing.T) {
	f := newUpdateFixture(t)
	request := updateRequest()
	path := filepath.Join(privateUpdateDir(t), "updates.json")
	old := map[string]any{
		"version": 1, "endpoint_hash": updateDigest(f.client.base.String()),
		"records": []any{map[string]any{
			"id_hash": updateDigest(request.ID), "request_hash": updateDigestJSON(request),
			"provider_hash": updateProviderDigest(request.ExpectedPanelGUID, request.ExpectedPanelVersion),
			"result":        map[string]any{"state": "unknown", "reason": "installation_dispatched", "target_version": request.Version},
		}},
	}
	data, _ := json.Marshal(old)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	u := openTestUpdates(t, path, f.client)
	result, err := u.Reconcile(context.Background(), request, func(context.Context) error { return nil })
	requireUpdateState(t, result, err, XrayUpdateUnknown)
	if result.ServiceState != XrayServiceReachable || u.journal.Records[0].RequestHash != updateDigestJSON(request) || f.calls.Load() != 0 || f.restarts.Load() != 0 {
		t.Fatal("v1 recovery changed identity, install barrier, or provider")
	}
}

func TestXrayUpdateRecoveryJournalRejectsInvalidOptionalFields(t *testing.T) {
	base := XrayUpdateResult{State: XrayUpdateFailed, Reason: "installation_reported_failure", TargetVersion: "v26.9.9"}
	for _, mutate := range []func(*XrayUpdateResult){
		func(r *XrayUpdateResult) { r.RecoveryAttempted = true },
		func(r *XrayUpdateResult) { r.RecoveryOutcome = "unknown" },
		func(r *XrayUpdateResult) { setService(r, XrayServiceReachable, "", "data_path_verified") },
		func(r *XrayUpdateResult) { setService(r, XrayServiceReachable, "26.9.9", "arbitrary-provider-message") },
		func(r *XrayUpdateResult) {
			setService(r, XrayServiceReachable, "26.9.9", "data_path_verified")
			r.ServiceObservedAt = -1
		},
		func(r *XrayUpdateResult) {
			setService(r, XrayServiceReachable, "26.9.9", "data_path_verified")
			r.RecoveryAttempted, r.RecoveryOutcome, r.State = true, "unknown", XrayUpdateUnknown
		},
	} {
		result := base
		mutate(&result)
		if validUpdateRecovery(result) {
			t.Fatal("unsafe optional recovery fields were accepted")
		}
	}
}
