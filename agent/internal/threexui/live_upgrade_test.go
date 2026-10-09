package threexui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	realUpgradeBaseline = "v26.9.8"
	realUpgradeTarget   = "v26.9.9"
	realUpgradeInbound  = 39088
	realUpgradeOrigin   = "127.0.0.1:39089"
	realUpgradeMaxState = 16 << 20
)

// Each stage is a separate process against the same private fixture volumes.
// Only the host runner recreates containers. No stage receives Docker access,
// starts a local Xray binary, calls Xray's API or substitutes upstream responses.
// A failed stage remains started on disk: rerunning it is not reconciliation.
type realUpgradeFixture struct {
	Version          int               `json:"version"`
	Stage            string            `json:"stage"`
	Verified         bool              `json:"verified"`
	EndpointHash     string            `json:"endpoint_hash"`
	Prefix           string            `json:"prefix"`
	Credential       string            `json:"credential"`
	PanelGUID        string            `json:"panel_guid"`
	PanelVersion     string            `json:"panel_version"`
	ImageXrayVersion string            `json:"image_xray_version"`
	OriginalTemplate XrayTemplate      `json:"original_template"`
	OriginalConfig   json.RawMessage   `json:"original_config"`
	Candidate        json.RawMessage   `json:"candidate"`
	BaselineRequest  XrayUpdateRequest `json:"baseline_request"`
	TargetRequest    XrayUpdateRequest `json:"target_request"`
	LastResult       XrayUpdateResult  `json:"last_result"`
}

// TestLive3XUIRealUpgrade requires the dedicated restricted-egress runner:
// CHIRAL_3XUI_TEST_WRITE=isolated-empty-instance
// CHIRAL_3XUI_TEST_UPGRADE_STAGE=prepare|baseline-rebuilt|upgrade|target-rebuilt
// CHIRAL_3XUI_TEST_STATE_DIR=<same private writable directory across stages>
// CHIRAL_3XUI_TEST_URL and CHIRAL_3XUI_TEST_TOKEN_FILE identify the fixture only.
// The official provider must fetch both exact releases and their checksums.
func TestLive3XUIRealUpgrade(t *testing.T) {
	stage := os.Getenv("CHIRAL_3XUI_TEST_UPGRADE_STAGE")
	if stage == "" {
		t.Skip("requires an explicit real-upgrade stage in the restricted-egress laboratory")
	}
	if !realUpgradeKnownStage(stage) || os.Getenv("CHIRAL_3XUI_TEST_WRITE") != "isolated-empty-instance" {
		t.Fatal("real upgrade requires an exact stage and isolated empty-instance write authorization")
	}
	realUpgradeRequireNamespace(t)
	baseURL := os.Getenv("CHIRAL_3XUI_TEST_URL")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Hostname() != "127.0.0.1" || parsed.Scheme != "http" {
		t.Fatal("real upgrade requires a same-namespace loopback HTTP provider")
	}
	directory := os.Getenv("CHIRAL_3XUI_TEST_STATE_DIR")
	if !realUpgradePrivateDirectory(directory) {
		t.Fatal("real upgrade requires an existing private absolute state directory without symlink aliases")
	}
	lock, fresh, err := lockUpdateFile(filepath.Join(directory, "fixture.lock"))
	if err != nil {
		t.Fatal("cannot acquire the exclusive fixture-state lock")
	}
	defer lock.Close()
	statePath := filepath.Join(directory, "state.json")
	var state realUpgradeFixture
	if stage == "prepare" {
		entries, err := os.ReadDir(directory)
		if err != nil || !fresh || len(entries) != 1 || entries[0].Name() != "fixture.lock" {
			t.Fatal("prepare requires a new empty fixture directory; previous attempts must not be replayed")
		}
	} else {
		if fresh {
			t.Fatal("fixture ownership lock is missing; automatic continuation is forbidden")
		}
		state, err = realUpgradeReadFixture(statePath)
		if err != nil || !realUpgradeCanAdvance(state, stage) {
			t.Fatal("fixture state is incomplete, invalid or not ready for this exact next stage")
		}
	}
	transport := &realUpgradeTransport{next: directTransport()}
	client, err := New(Config{BaseURL: baseURL, Token: readLive3XUIToken(t, os.Getenv("CHIRAL_3XUI_TEST_TOKEN_FILE")),
		Timeout: 150 * time.Second, HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal("cannot construct the real-upgrade HTTP client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	identity, err := liveUpdateWaitState(ctx, client, "running")
	if err != nil {
		t.Fatal("real upgrade requires a complete running provider identity")
	}
	capabilities, err := client.DiscoverCapabilities(ctx)
	if err != nil || !capabilities.Supports(CapabilityOutboundProbe) || !capabilities.Supports(CapabilityXrayUpdate) ||
		!capabilities.Supports(CapabilityInstallXray) || !capabilities.Supports(CapabilityXrayVersions) {
		t.Fatal("official provider lacks a required real-upgrade HTTP operation")
	}
	liveHTTPProbeRequireEmpty(t, ctx, client)
	if stage == "prepare" {
		if !sameXrayRelease(identity.Xray.Version, realUpgradeTarget) {
			t.Fatal("initial image kernel must differ from the baseline and match the recorded image target")
		}
		before, err := client.ReadAssembledConfig(ctx)
		if err != nil {
			t.Fatal("cannot read the initial assembled configuration")
		}
		liveWriteRequireAPIOnly(t, before)
		snapshot, err := client.ReadXrayTemplate(ctx)
		if err != nil || snapshot.OutboundTestURL == "" {
			t.Fatal("cannot read a restorable initial template")
		}
		liveWriteRequireAPIOnly(t, snapshot.Config)
		state = realUpgradeFixture{Version: 1, Stage: stage, EndpointHash: updateDigest(client.base.String()),
			Prefix: "chiral-real-upgrade-" + liveWriteRandomID(t), Credential: liveWriteUUID(t),
			PanelGUID: identity.PanelGUID, PanelVersion: identity.PanelVersion, ImageXrayVersion: identity.Xray.Version,
			OriginalTemplate: snapshot, OriginalConfig: before}
		state.Candidate = liveHTTPProbeConfig(t, before, state.Prefix, state.Credential, realUpgradeInbound, realUpgradeOrigin)
		state.BaselineRequest = XrayUpdateRequest{ID: liveWriteRandomID(t), Version: realUpgradeBaseline,
			ExpectedPanelGUID: identity.PanelGUID, ExpectedPanelVersion: identity.PanelVersion, ExpectedRunningVersion: identity.Xray.Version}
		state.TargetRequest = XrayUpdateRequest{ID: liveWriteRandomID(t), Version: realUpgradeTarget,
			ExpectedPanelGUID: identity.PanelGUID, ExpectedPanelVersion: identity.PanelVersion, ExpectedRunningVersion: strings.TrimPrefix(realUpgradeBaseline, "v")}
	} else {
		if state.EndpointHash != updateDigest(client.base.String()) || identity.PanelGUID != state.PanelGUID || identity.PanelVersion != state.PanelVersion {
			t.Fatal("fixture endpoint or provider identity changed between stages")
		}
		want := realUpgradeBaseline
		if stage == "target-rebuilt" {
			want = realUpgradeTarget
		}
		if !sameXrayRelease(identity.Xray.Version, want) {
			t.Fatal("recreated provider did not retain the required runtime kernel")
		}
		liveProjectionRequireStoredAndAssembled(t, ctx, client, state.Candidate, state.targetURL(), "persisted real-upgrade fixture")
	}
	state.Stage, state.Verified = stage, false
	realUpgradeSaveFixture(t, statePath, state) // Durable stage barrier precedes writes.
	origin := realUpgradeStartOrigin(t, state.Prefix)
	defer origin.close(t)
	outbound := liveHTTPProbeOutbound(t, state.Prefix+"-client", state.Credential, realUpgradeInbound)
	probe := func(ctx context.Context) error {
		hits := origin.hits.Load()
		result, err := client.ProbeOutbound(ctx, outbound, nil, state.targetURL())
		if err != nil || result.HTTPStatus != http.StatusNoContent || origin.hits.Load() != hits+1 || origin.invalid.Load() != 0 {
			return errors.New("official real-upgrade probe did not verify the loopback origin")
		}
		return nil
	}
	if stage == "prepare" {
		reservation, err := net.Listen("tcp4", "127.0.0.1:39088")
		if err != nil {
			t.Fatal("owned fixture inbound port is already occupied")
		}
		if reservation.Close() != nil {
			t.Fatal("cannot release the owned fixture inbound reservation")
		}
		if err := client.UpdateXrayTemplate(ctx, XrayTemplate{Config: state.Candidate, OutboundTestURL: state.targetURL()}); err != nil {
			t.Fatal("cannot install the owned real-upgrade proxy fixture")
		}
		liveProjectionRequireStoredAndAssembled(t, ctx, client, state.Candidate, state.targetURL(), "initial real-upgrade fixture")
	}
	if err := probe(ctx); err != nil {
		t.Fatal("pre-stage official HTTP proxy-path verification failed")
	}
	t.Logf("real_upgrade_preflight_verified=%s http_probe=reachable", stage)
	journalPath := filepath.Join(directory, "updates.json")
	if stage != "prepare" {
		for _, path := range []string{journalPath, journalPath + ".lock"} {
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
				t.Fatal("retained update journal or ownership lock is missing or unsafe")
			}
		}
	}
	updates, err := OpenXrayUpdates(journalPath, client)
	if err != nil {
		t.Fatal("cannot open the retained real-upgrade operation journal")
	}
	defer updates.Close()
	switch stage {
	case "prepare", "upgrade":
		versions, err := client.XrayVersions(ctx)
		if err != nil {
			t.Fatal("cannot obtain the official release directory; this stage has not dispatched an installation")
		}
		available := realUpgradeVersionsAvailable(versions)
		t.Logf("official_release_directory_verified=true offered_versions=%d exact_versions_available=%t", len(versions), available)
		if !available {
			t.Fatal("official release directory does not contain both exact laboratory versions")
		}
		request := state.BaselineRequest
		if stage == "upgrade" {
			realUpgradeRequireRecorded(t, ctx, updates, state.BaselineRequest)
			request = state.TargetRequest
		}
		installs := transport.installs.Load()
		result, runErr := updates.Run(ctx, request, probe)
		state.LastResult = result
		realUpgradeSaveFixture(t, statePath, state)
		if runErr != nil || result.State != XrayUpdateVerified || result.ServiceState != XrayServiceReachable || !sameXrayRelease(result.ServiceVersion, request.Version) {
			t.Logf("real_upgrade_result=%s service_state=%s failure_reason=%s", result.State, result.ServiceState, result.Reason)
			t.Fatal("real installation did not verify; retained state forbids automatic stage replay")
		}
		if transport.installs.Load() != installs+1 || transport.restarts.Load() != 0 {
			t.Fatal("successful real upgrade did not use exactly one installation and zero recovery restarts")
		}
		beforeReplay := transport.requests.Load()
		replayed, err := updates.Run(ctx, request, probe)
		if err != nil || !reflect.DeepEqual(replayed, result) || transport.requests.Load() != beforeReplay {
			t.Fatal("same operation replay dispatched HTTP or changed its retained result")
		}
		if err := updates.Close(); err != nil {
			t.Fatal("cannot close the verified update journal")
		}
		updates, err = OpenXrayUpdates(journalPath, client)
		if err != nil {
			t.Fatal("cannot reopen the verified update journal")
		}
		defer updates.Close()
		replayed, err = updates.Run(ctx, request, probe)
		if err != nil || !reflect.DeepEqual(replayed, result) || transport.requests.Load() != beforeReplay {
			t.Fatal("reopened operation replay dispatched HTTP or changed its retained result")
		}
		realUpgradeRequireRecorded(t, ctx, updates, request)
		t.Logf("real_install_verified=true target=%s install_dispatches=1 replay_dispatches=0 download_response_injected=false", request.Version)
	case "baseline-rebuilt":
		realUpgradeRequireRecorded(t, ctx, updates, state.BaselineRequest)
		if sameXrayRelease(identity.Xray.Version, state.ImageXrayVersion) {
			t.Fatal("baseline rebuild cannot prove persistence when it matches the original image kernel")
		}
		t.Log("baseline_differs_from_image=true persisted_baseline_verified=true")
	case "target-rebuilt":
		realUpgradeRequireRecorded(t, ctx, updates, state.TargetRequest)
	}
	if err := probe(ctx); err != nil {
		t.Fatal("post-stage official HTTP proxy-path verification failed")
	}
	liveProjectionRequireStoredAndAssembled(t, ctx, client, state.Candidate, state.targetURL(), "post-stage real-upgrade fixture")
	liveHTTPProbeRequireEmpty(t, ctx, client)
	if stage == "target-rebuilt" {
		liveHTTPProbeRestore(t, client, identity, state.OriginalTemplate, state.OriginalConfig, state.Candidate, state.targetURL())
		if t.Failed() {
			return
		}
	}
	state.Verified = true
	realUpgradeSaveFixture(t, statePath, state)
	t.Logf("real_upgrade_stage_verified=%s http_only_control=true no_local_xray_process=true upstream_unmodified=true", stage)
}

func (s realUpgradeFixture) targetURL() string { return "http://8.8.8.8/" + s.Prefix }

func realUpgradeKnownStage(stage string) bool {
	switch stage {
	case "prepare", "baseline-rebuilt", "upgrade", "target-rebuilt":
		return true
	default:
		return false
	}
}

func realUpgradeCanAdvance(s realUpgradeFixture, stage string) bool {
	previous := map[string]string{"baseline-rebuilt": "prepare", "upgrade": "baseline-rebuilt", "target-rebuilt": "upgrade"}
	return s.Verified && previous[stage] != "" && s.Stage == previous[stage]
}

func realUpgradeVersionsAvailable(versions []string) bool {
	baseline, target := false, false
	for _, version := range versions {
		baseline = baseline || version == realUpgradeBaseline
		target = target || version == realUpgradeTarget
	}
	return baseline && target
}

func realUpgradePrivateDirectory(directory string) bool {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return false
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return false
	}
	resolved, err := filepath.EvalSymlinks(directory)
	return err == nil && resolved == directory
}

func realUpgradeSaveFixture(t *testing.T, path string, s realUpgradeFixture) {
	t.Helper()
	body, err := json.Marshal(s)
	if err != nil || len(body) > realUpgradeMaxState || writeUpdateFile(path, body) != nil {
		t.Fatal("cannot durably retain private real-upgrade fixture state")
	}
}

func realUpgradeReadFixture(path string) (realUpgradeFixture, error) {
	invalid := errors.New("invalid private real-upgrade fixture")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > realUpgradeMaxState {
		return realUpgradeFixture{}, invalid
	}
	f, err := os.Open(path)
	if err != nil {
		return realUpgradeFixture{}, invalid
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return realUpgradeFixture{}, invalid
	}
	decoder := json.NewDecoder(io.LimitReader(f, realUpgradeMaxState+1))
	decoder.DisallowUnknownFields()
	var s realUpgradeFixture
	if decoder.Decode(&s) != nil || decoder.Decode(new(any)) != io.EOF || s.Version != 1 || !realUpgradeKnownStage(s.Stage) ||
		!validUpdateID(s.PanelGUID) || !validXrayVersion(s.PanelVersion) || !sameXrayRelease(s.ImageXrayVersion, realUpgradeTarget) ||
		len(s.EndpointHash) != 64 || !validUpdateID(s.Prefix) || !strings.HasPrefix(s.Prefix, "chiral-real-upgrade-") || len(s.Prefix) > 80 || len(s.Credential) != 36 ||
		!validUpdateRequest(s.BaselineRequest) || !validUpdateRequest(s.TargetRequest) || s.BaselineRequest.ID == s.TargetRequest.ID ||
		s.BaselineRequest.Version != realUpgradeBaseline || s.TargetRequest.Version != realUpgradeTarget ||
		s.BaselineRequest.ExpectedRunningVersion != s.ImageXrayVersion || !sameXrayRelease(s.TargetRequest.ExpectedRunningVersion, realUpgradeBaseline) ||
		s.BaselineRequest.ExpectedPanelGUID != s.PanelGUID || s.TargetRequest.ExpectedPanelGUID != s.PanelGUID ||
		s.BaselineRequest.ExpectedPanelVersion != s.PanelVersion || s.TargetRequest.ExpectedPanelVersion != s.PanelVersion ||
		len(s.OriginalConfig) == 0 || len(s.OriginalTemplate.Config) == 0 || len(s.Candidate) == 0 || s.OriginalTemplate.OutboundTestURL == "" {
		return realUpgradeFixture{}, invalid
	}
	if s.Verified {
		want := realUpgradeBaseline
		if s.Stage == "upgrade" || s.Stage == "target-rebuilt" {
			want = realUpgradeTarget
		}
		if s.LastResult.State != XrayUpdateVerified || s.LastResult.TargetVersion != want ||
			s.LastResult.ServiceState != XrayServiceReachable || !sameXrayRelease(s.LastResult.ServiceVersion, want) {
			return realUpgradeFixture{}, invalid
		}
	}
	return s, nil
}

func realUpgradeRequireRecorded(t *testing.T, ctx context.Context, updates *XrayUpdates, request XrayUpdateRequest) {
	t.Helper()
	observation, err := updates.Observe(ctx, request.ID)
	if err != nil || observation.Recorded.State != XrayUpdateVerified || observation.Recorded.TargetVersion != request.Version ||
		!observation.ProviderMatches || !observation.RunningTarget {
		t.Fatal("retained installation and current official runtime do not agree")
	}
}

// This transport only counts requests before forwarding them unchanged. It
// neither constructs responses nor rewrites a request, release or download.
type realUpgradeTransport struct {
	next     http.RoundTripper
	requests atomic.Int64
	installs atomic.Int64
	restarts atomic.Int64
}

func (tr *realUpgradeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.requests.Add(1)
	if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/"+xrayInstallEndpoint) {
		tr.installs.Add(1)
	}
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/panel/api/server/restartXrayService") {
		tr.restarts.Add(1)
	}
	return tr.next.RoundTrip(r)
}

func realUpgradeStartOrigin(t *testing.T, prefix string) *liveHTTPProbeOrigin {
	t.Helper()
	listener, err := net.Listen("tcp4", realUpgradeOrigin)
	if err != nil {
		t.Fatal("cannot bind the same owned loopback origin after recreation")
	}
	origin := &liveHTTPProbeOrigin{address: realUpgradeOrigin, done: make(chan error, 1)}
	origin.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() || r.Method != http.MethodGet || r.Host != "8.8.8.8" || r.URL.Path != "/"+prefix || r.URL.RawQuery != "" {
			origin.invalid.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		origin.hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { origin.done <- origin.server.Serve(listener) }()
	return origin
}

// The host runner additionally verifies Docker's internal isolated gateway,
// exclusive network membership, mounts and restricted CONNECT proxy. Here we
// refuse an ordinary egress bridge, public addresses or extra live interfaces.
func realUpgradeRequireNamespace(t *testing.T) {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal("cannot inspect the restricted-egress fixture namespace")
	}
	privateInterfaces := 0
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		privateInterfaces++
		addresses, err := iface.Addrs()
		if err != nil || len(addresses) == 0 {
			t.Fatal("cannot establish private fixture interface addresses")
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || ip.To4() == nil || !ip.IsPrivate() {
				t.Fatal("real upgrade requires an IPv4-only private fixture interface")
			}
		}
	}
	if privateInterfaces != 1 {
		t.Fatal("real upgrade requires exactly one private restricted-egress fixture interface")
	}
	routes, err := os.ReadFile("/proc/net/route")
	if err != nil || !realUpgradeNoDefaultRoute(routes) {
		t.Fatal("real upgrade refuses a namespace with an IPv4 default route")
	}
}

func realUpgradeNoDefaultRoute(routes []byte) bool {
	lines := bytes.Split(bytes.TrimSpace(routes), []byte("\n"))
	if len(lines) < 2 || !bytes.Contains(lines[0], []byte("Destination")) {
		return false
	}
	for _, line := range lines[1:] {
		fields := bytes.Fields(line)
		if len(fields) < 8 || bytes.Equal(fields[1], []byte("00000000")) {
			return false
		}
	}
	return true
}

func TestRealUpgradeStageGates(t *testing.T) {
	for _, stage := range []string{"prepare", "baseline-rebuilt", "upgrade", "target-rebuilt"} {
		if !realUpgradeKnownStage(stage) {
			t.Fatal("known stage was rejected")
		}
		if realUpgradeCanAdvance(realUpgradeFixture{Stage: stage, Verified: true}, stage) ||
			realUpgradeCanAdvance(realUpgradeFixture{Stage: stage, Verified: false}, "upgrade") {
			t.Fatal("replayed or unfinished stage was accepted")
		}
	}
	for next, previous := range map[string]string{"baseline-rebuilt": "prepare", "upgrade": "baseline-rebuilt", "target-rebuilt": "upgrade"} {
		if !realUpgradeCanAdvance(realUpgradeFixture{Stage: previous, Verified: true}, next) {
			t.Fatal("verified predecessor was rejected")
		}
	}
	if realUpgradeKnownStage("latest") || realUpgradeVersionsAvailable([]string{"v26.9.9"}) ||
		realUpgradeVersionsAvailable([]string{"26.9.8", "26.9.9"}) || !realUpgradeVersionsAvailable([]string{realUpgradeTarget, realUpgradeBaseline}) {
		t.Fatal("exact stage or release selection guard failed")
	}
}

func TestRealUpgradeDefaultRouteGuard(t *testing.T) {
	header := "Iface Destination Gateway Flags RefCnt Use Metric Mask\n"
	if !realUpgradeNoDefaultRoute([]byte(header + "eth0 000013AC 00000000 0001 0 0 0 00FFFFFF\n")) {
		t.Fatal("isolated on-link route rejected")
	}
	for _, input := range []string{"", header, header + "eth0 00000000 010013AC 0003 0 0 0 00000000\n", header + "invalid\n"} {
		if realUpgradeNoDefaultRoute([]byte(input)) {
			t.Fatal("missing or default route evidence accepted")
		}
	}
}

func TestRealUpgradePrivateState(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil || os.Chmod(directory, 0700) != nil || !realUpgradePrivateDirectory(directory) {
		t.Fatal("private fixture directory was not accepted")
	}
	state := realUpgradeFixture{
		Version: 1, Stage: "prepare", Verified: true, EndpointHash: updateDigest("fixture"),
		Prefix: "chiral-real-upgrade-" + liveWriteRandomID(t), Credential: liveWriteUUID(t),
		PanelGUID: "fixture-provider", PanelVersion: "3.8.5", ImageXrayVersion: "26.9.9",
		OriginalTemplate: XrayTemplate{Config: json.RawMessage(`{}`), OutboundTestURL: "https://example.invalid"},
		OriginalConfig:   json.RawMessage(`{}`), Candidate: json.RawMessage(`{}`),
		BaselineRequest: XrayUpdateRequest{ID: "baseline-operation", Version: realUpgradeBaseline,
			ExpectedPanelGUID: "fixture-provider", ExpectedPanelVersion: "3.8.5", ExpectedRunningVersion: "26.9.9"},
		TargetRequest: XrayUpdateRequest{ID: "target-operation", Version: realUpgradeTarget,
			ExpectedPanelGUID: "fixture-provider", ExpectedPanelVersion: "3.8.5", ExpectedRunningVersion: "26.9.8"},
		LastResult: XrayUpdateResult{State: XrayUpdateVerified, TargetVersion: realUpgradeBaseline,
			ServiceState: XrayServiceReachable, ServiceVersion: "26.9.8"},
	}
	path := filepath.Join(directory, "state.json")
	realUpgradeSaveFixture(t, path, state)
	loaded, err := realUpgradeReadFixture(path)
	if err != nil || !reflect.DeepEqual(state, loaded) {
		t.Fatal("private fixture state did not round trip")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private fixture state has unsafe permissions")
	}
	bad := state
	bad.LastResult.State = XrayUpdateUnknown
	realUpgradeSaveFixture(t, path, bad)
	if _, err := realUpgradeReadFixture(path); err == nil {
		t.Fatal("verified stage accepted an unknown installation result")
	}
	realUpgradeSaveFixture(t, path, state)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal("cannot construct unsafe fixture-state permissions")
	}
	if _, err := realUpgradeReadFixture(path); err == nil {
		t.Fatal("public fixture state was accepted")
	}
	link := filepath.Join(directory, "state-link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal("cannot construct a fixture-state symlink")
	}
	if _, err := realUpgradeReadFixture(link); err == nil {
		t.Fatal("fixture-state symlink was accepted")
	}
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal("cannot construct unsafe fixture-directory permissions")
	}
	if realUpgradePrivateDirectory(directory) {
		t.Fatal("public fixture directory was accepted")
	}
}
