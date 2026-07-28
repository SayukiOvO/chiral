package node

import (
	"context"
	"log/slog"
	"math"
	"net"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/online"
	"github.com/SayukiOvO/chiral/core/internal/store"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// CredentialMetadataKey carries the node's long-term credential on Channel.
const CredentialMetadataKey = "x-chiral-credential"

const sendQueueSize = 16

// TrafficRecorder accumulates the deltas agents report. Declared as an
// interface so this package keeps depending only on what it uses; the store
// satisfies it.
type TrafficRecorder interface {
	AddCredentialTraffic(email string, up, down int64) error
}

// OnlineRegistry receives the fleet's current source addresses. An interface
// for the same reason as TrafficRecorder; core/internal/online satisfies it.
type OnlineRegistry interface {
	Replace(nodeID string, obs []online.Observation, complete bool, now time.Time)
	Forget(nodeID string)
}

// OnlineRecorder persists an observed address.
type OnlineRecorder interface {
	RecordDevice(userID, ip, nodeID string, at time.Time) error
}

// UpgradeHandler receives the frames belonging to a runtime kernel upgrade.
// An interface so this package does not depend on core/internal/upgrade, which
// depends on it.
type UpgradeHandler interface {
	HandleStatus(nodeID string, st *chiralv1.XrayStatus)
	HandleRelayRequest(ctx context.Context, nodeID string, req *chiralv1.XrayRelayRequest)
}

// Service implements chiral.v1.AgentService.
type Service struct {
	chiralv1.UnimplementedAgentServiceServer

	// upgrades is nil until wired; the frames are then acknowledged as
	// unsupported rather than silently dropped.
	upgrades UpgradeHandler

	st      *store.Store
	mgr     *Manager
	traffic TrafficRecorder
	// online and devices are nil when address recording is switched off, in
	// which case agents are never asked to poll and reports are ignored.
	online  OnlineRegistry
	devices OnlineRecorder
	// onlineInterval is the polling cadence pushed to agents.
	onlineInterval time.Duration
	logger         *slog.Logger
}

func NewService(st *store.Store, mgr *Manager, traffic TrafficRecorder, logger *slog.Logger) *Service {
	return &Service{st: st, mgr: mgr, traffic: traffic, logger: logger}
}

// EnableUpgrades wires the runtime kernel upgrade handler.
func (s *Service) EnableUpgrades(h UpgradeHandler) { s.upgrades = h }

// EnableOnlineTracking switches on source-address recording. Called at startup
// only when the operator asked for it; left alone, nothing polls and
// user_devices stays empty.
func (s *Service) EnableOnlineTracking(reg OnlineRegistry, rec OnlineRecorder, interval time.Duration) {
	s.online, s.devices, s.onlineInterval = reg, rec, interval
}

// Register exchanges a one-time join token for the node's long-term
// credential. The token is invalidated in the same DB update.
func (s *Service) Register(ctx context.Context, req *chiralv1.RegisterRequest) (*chiralv1.RegisterResponse, error) {
	if req.GetJoinToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "join_token is required")
	}
	credential, credentialHash := auth.NewSecret()
	n, err := s.st.RedeemJoinToken(auth.HashSecret(req.GetJoinToken()), credentialHash, req.GetHostname(), req.GetAgentVersion(), peerIP(ctx))
	if err != nil {
		if store.IsNotFound(err) {
			return nil, status.Error(codes.PermissionDenied, "invalid or already used join token")
		}
		s.logger.Error("register: redeem failed", "err", err)
		return nil, status.Error(codes.Internal, "internal error")
	}
	s.logger.Info("node registered", "node", n.ID, "name", n.Name, "hostname", req.GetHostname())
	return &chiralv1.RegisterResponse{NodeId: n.ID, Credential: credential}, nil
}

// Channel is the persistent bidirectional stream with one agent.
func (s *Service) Channel(stream chiralv1.AgentService_ChannelServer) error {
	ctx := stream.Context()
	n, err := s.authenticate(ctx)
	if err != nil {
		return err
	}

	// The first frame must be Hello; require it promptly so half-open
	// connections don't occupy the credential.
	first, err := recvWithTimeout(ctx, stream, 30*time.Second)
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.FailedPrecondition, "first frame must be Hello")
	}
	if hello.GetNodeId() != n.ID {
		return status.Error(codes.PermissionDenied, "hello node_id does not match credential")
	}
	if err := s.st.UpdateHello(n.ID, orElse(hello.GetPublicIp(), peerIP(ctx)), hello.GetAgentVersion(), hello.GetXrayVersion()); err != nil {
		s.logger.Error("update hello failed", "node", n.ID, "err", err)
	}
	if _, err := s.st.SetNodePlatform(n.ID, hello.GetPlatform()); err != nil {
		s.logger.Error("recording the node platform failed", "node", n.ID, "err", err)
	}

	sess := &Session{
		nodeID:   n.ID,
		send:     make(chan *chiralv1.CoreFrame, sendQueueSize),
		done:     make(chan struct{}),
		lastSeen: time.Now(),
		hello:    hello,
	}
	s.mgr.attach(n.ID, sess)
	defer func() {
		// Close done so pending/future enqueues fail fast instead of landing
		// in a buffer nobody drains.
		sess.close()
		s.mgr.detach(n.ID, sess)
		// A node that is gone is not observing. Drop its addresses now rather
		// than letting them age out, or a user stays "online" through a node
		// that plainly is not.
		if s.online != nil {
			s.online.Forget(n.ID)
		}
		if err := s.st.TouchLastSeen(n.ID, time.Now().Unix()); err != nil {
			s.logger.Error("touch last_seen failed", "node", n.ID, "err", err)
		}
		s.logger.Info("node disconnected", "node", n.ID)
	}()
	s.logger.Info("node connected", "node", n.ID, "agent", hello.GetAgentVersion(), "xray", hello.GetXrayVersion())

	// Writer goroutine: sole owner of stream.Send.
	go func() {
		for {
			select {
			case f := <-sess.send:
				if err := stream.Send(f); err != nil {
					return // stream broken; read side will notice too
				}
			case <-sess.done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	// Re-push the newest non-failed config so a reconnecting agent converges
	// without operator action (the agent skips the apply if it already runs
	// identical content). Configs rejected by `xray -test` are not re-pushed.
	s.reconcileConfig(n.ID, sess, -1)

	// Tell the agent whether to poll. Sent on every connect because the agent
	// keeps no policy across reconnects — it starts idle, which is the safe
	// default if this frame is ever lost.
	if s.online != nil {
		if err := s.mgr.SendOnlinePolicy(n.ID, &chiralv1.OnlinePolicy{
			Enabled:         true,
			IntervalSeconds: int32(s.onlineInterval.Seconds()),
		}); err != nil {
			s.logger.Warn("pushing online policy failed", "node", n.ID, "err", err)
		}
	}

	// Reader loop. Recv runs in its own goroutine so replacement via
	// sess.done can end the handler even while Recv is blocked.
	frames := make(chan *chiralv1.AgentFrame)
	errs := make(chan error, 1)
	go func() {
		for {
			f, err := stream.Recv()
			if err != nil {
				select {
				case errs <- err:
				case <-ctx.Done():
				}
				return
			}
			select {
			case frames <- f:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-sess.done:
			return status.Error(codes.Aborted, "session replaced by a newer connection")
		case err := <-errs:
			return err
		case f := <-frames:
			s.handleFrame(n.ID, sess, f)
		}
	}
}

// reconcileConfig pushes the newest non-failed config when the agent's
// reported version (or -1 for connect time, meaning "unknown") differs.
// Throttled per session so a slow agent is not flooded with duplicates.
func (s *Service) reconcileConfig(nodeID string, sess *Session, agentVersion int64) {
	cfg, err := s.st.LatestPushableConfig(nodeID)
	if err != nil {
		if !store.IsNotFound(err) {
			s.logger.Error("load pushable config failed", "node", nodeID, "err", err)
		}
		return
	}
	if cfg.Version == agentVersion {
		return
	}
	sess.mu.Lock()
	throttled := sess.lastPushVersion == cfg.Version && time.Since(sess.lastPushAt) < 30*time.Second
	if !throttled {
		sess.lastPushVersion = cfg.Version
		sess.lastPushAt = time.Now()
	}
	sess.mu.Unlock()
	if throttled {
		return
	}
	if err := s.mgr.PushConfig(nodeID, cfg.Version, []byte(cfg.Config), []byte(cfg.ProbeOutbound)); err != nil {
		s.logger.Warn("config reconcile push failed", "node", nodeID, "version", cfg.Version, "err", err)
	}
}

func (s *Service) handleFrame(nodeID string, sess *Session, f *chiralv1.AgentFrame) {
	switch fr := f.GetFrame().(type) {
	case *chiralv1.AgentFrame_Heartbeat:
		sess.touch(fr.Heartbeat)
		if err := s.st.TouchLastSeen(nodeID, time.Now().Unix()); err != nil {
			s.logger.Error("touch last_seen failed", "node", nodeID, "err", err)
		}
		s.recordSample(nodeID, fr.Heartbeat)
		s.recordXrayVersions(nodeID, fr.Heartbeat)
		// Heartbeats report the applied config version; reconcile drift so a
		// dropped or out-of-order push heals automatically.
		s.reconcileConfig(nodeID, sess, fr.Heartbeat.GetConfigVersion())
	case *chiralv1.AgentFrame_ConfigAck:
		ack := fr.ConfigAck
		sess.touch(nil)
		if err := s.st.SetConfigResult(nodeID, ack.GetVersion(), ack.GetApplied(), ack.GetError()); err != nil {
			s.logger.Error("persist config ack failed", "node", nodeID, "err", err)
		}
		s.logger.Info("config ack", "node", nodeID, "version", ack.GetVersion(), "applied", ack.GetApplied(), "error", ack.GetError())
	case *chiralv1.AgentFrame_Event:
		sess.touch(nil)
		s.logger.Warn("agent event", "node", nodeID, "kind", fr.Event.GetKind(), "message", fr.Event.GetMessage())
	case *chiralv1.AgentFrame_Stats:
		sess.touch(nil)
		s.recordStats(nodeID, fr.Stats)
	case *chiralv1.AgentFrame_Online:
		sess.touch(nil)
		s.recordOnline(nodeID, fr.Online)
	case *chiralv1.AgentFrame_XrayStatus:
		sess.touch(nil)
		if s.upgrades == nil {
			s.logger.Warn("an install status arrived but upgrades are not wired", "node", nodeID)
			return
		}
		s.upgrades.HandleStatus(nodeID, fr.XrayStatus)
	case *chiralv1.AgentFrame_XrayRelayRequest:
		sess.touch(nil)
		if s.upgrades == nil {
			s.logger.Warn("a relay request arrived but upgrades are not wired", "node", nodeID)
			return
		}
		// On its own goroutine: serving a chunk may have to fetch a 21 MB
		// archive from GitHub first, and this is the loop that reads every
		// other frame from this node.
		go s.upgrades.HandleRelayRequest(context.Background(), nodeID, fr.XrayRelayRequest)
	case *chiralv1.AgentFrame_Hello:
		s.logger.Warn("unexpected Hello after stream start", "node", nodeID)
	}
}

// recordXrayVersions keeps Core's picture of which kernel a node is running
// current between Hello frames.
//
// Hello alone was enough while the binary could not change under a live agent.
// A runtime upgrade does not drop the stream, so without this the recorded
// version would describe whatever was installed when the node last connected —
// and every judgement about whether an upgrade took would be made against a
// number that cannot have moved.
func (s *Service) recordXrayVersions(nodeID string, hb *chiralv1.Heartbeat) {
	changed, err := s.st.SetXrayVersions(nodeID, hb.GetXrayVersion(), hb.GetInstalledXrayVersion())
	if err != nil {
		s.logger.Error("recording xray versions failed", "node", nodeID, "err", err)
		return
	}
	if changed {
		s.logger.Info("xray version changed", "node", nodeID,
			"running", hb.GetXrayVersion(), "installed", hb.GetInstalledXrayVersion())
	}
}

// recordSample files a heartbeat into the node's resource history. Heartbeats
// arrive far more often than the sample interval; the store keeps the first of
// each interval and drops the rest.
func (s *Service) recordSample(nodeID string, hb *chiralv1.Heartbeat) {
	if err := s.st.PutNodeSample(nodeID, time.Now(), store.NodeSample{
		CPUPercent:     hb.GetCpuPercent(),
		MemUsedBytes:   clampInt64(hb.GetMemUsedBytes()),
		MemTotalBytes:  clampInt64(hb.GetMemTotalBytes()),
		DiskUsedBytes:  clampInt64(hb.GetDiskUsedBytes()),
		DiskTotalBytes: clampInt64(hb.GetDiskTotalBytes()),
		NetTxBps:       clampInt64(hb.GetNetTxBps()),
		NetRxBps:       clampInt64(hb.GetNetRxBps()),
	}); err != nil {
		s.logger.Error("recording node sample failed", "node", nodeID, "err", err)
	}
}

// clampInt64 keeps an implausible unsigned value from wrapping negative on the
// way into the database.
func clampInt64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// recordStats accumulates one agent report. Entries are deltas — the agent
// reads Xray's counters with reset — so they are added, never assigned.
//
// A single bad entry is skipped rather than failing the report: losing one
// credential's interval is much better than dropping every other user's.
func (s *Service) recordStats(nodeID string, report *chiralv1.StatsReport) {
	if s.traffic == nil {
		return
	}
	for _, e := range report.GetEntries() {
		if e.GetScope() != chiralv1.StatScope_STAT_SCOPE_USER || e.GetName() == "" {
			continue
		}
		up, down := e.GetUplinkBytes(), e.GetDownlinkBytes()
		// The wire type is unsigned; guard the conversion so a bogus report
		// cannot turn into a negative delta the store would reject.
		if up > math.MaxInt64 || down > math.MaxInt64 {
			s.logger.Warn("implausible traffic delta ignored", "node", nodeID, "email", e.GetName())
			continue
		}
		if err := s.traffic.AddCredentialTraffic(e.GetName(), int64(up), int64(down)); err != nil {
			s.logger.Error("recording traffic failed", "node", nodeID, "email", e.GetName(), "err", err)
		}
		s.recordTrafficHistory(nodeID, e.GetName(), int64(up), int64(down))
	}
}

// recordTrafficHistory files a delta into the charted series. The owner is
// resolved from the credential; traffic for one that has just been deleted is
// still counted against the node, since losing the fleet total would be worse
// than carrying it without an owner.
func (s *Service) recordTrafficHistory(nodeID, email string, up, down int64) {
	userID, credNodeID, err := s.st.CredentialOwner(email)
	if err != nil {
		if !store.IsNotFound(err) {
			s.logger.Error("resolving credential owner failed", "email", email, "err", err)
			return
		}
		userID, credNodeID = "", nodeID
	}
	if credNodeID == "" {
		credNodeID = nodeID
	}
	if err := s.st.AddTraffic(credNodeID, userID, time.Now(), up, down); err != nil {
		s.logger.Error("recording traffic history failed", "node", nodeID, "err", err)
	}
}

// recordOnline installs one node's view of who is connected, and files the
// addresses into the durable record.
//
// Every entry is checked against the credential it names. A node may only
// speak for credentials issued FOR it: without that check, one compromised
// VPS could invent two hundred addresses for any subscriber it likes, and
// those addresses would land in user_devices — the very table an operator
// later reads as evidence of account sharing. Fabricated evidence against an
// innocent user is a worse outcome than losing the feature.
func (s *Service) recordOnline(nodeID string, report *chiralv1.OnlineReport) {
	if s.online == nil {
		return // recording is off; the agent should not be sending these
	}
	now := time.Now()
	obs := make([]online.Observation, 0, len(report.GetUsers()))
	forged := 0

	for _, u := range report.GetUsers() {
		email := u.GetEmail()
		if email == "" {
			continue
		}
		userID, credNodeID, err := s.st.CredentialOwner(email)
		if err != nil {
			if !store.IsNotFound(err) {
				s.logger.Error("resolving credential owner failed", "email", email, "err", err)
			}
			// An unknown credential is ordinary right after a revocation, and
			// there is no user to attribute it to either way.
			continue
		}
		if credNodeID != nodeID {
			// This node is reporting on a credential that belongs to another
			// node. An agent has no legitimate way to learn such a name.
			forged++
			continue
		}
		for _, ip := range u.GetIps() {
			addr := ip.GetIp()
			if addr == "" {
				continue
			}
			at := time.Unix(ip.GetLastSeenUnix(), 0)
			// A timestamp from the future, or from before this panel existed,
			// is the agent's clock being wrong; the observation is still real,
			// so keep it and use our own clock.
			if ip.GetLastSeenUnix() <= 0 || at.After(now) {
				at = now
			}
			obs = append(obs, online.Observation{UserID: userID, IP: addr, At: at})
			if s.devices != nil {
				if err := s.devices.RecordDevice(userID, addr, nodeID, at); err != nil {
					s.logger.Error("recording device failed", "node", nodeID, "err", err)
				}
			}
		}
	}

	if forged > 0 {
		s.logger.Warn("agent reported addresses for credentials that are not its own",
			"node", nodeID, "entries", forged)
	}
	s.online.Replace(nodeID, obs, report.GetComplete(), now)
}

func (s *Service) authenticate(ctx context.Context) (store.Node, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get(CredentialMetadataKey)
	if len(vals) == 0 || vals[0] == "" {
		return store.Node{}, status.Error(codes.Unauthenticated, "missing credential")
	}
	n, err := s.st.FindNodeByCredentialHash(auth.HashSecret(vals[0]))
	if err != nil {
		if store.IsNotFound(err) {
			return store.Node{}, status.Error(codes.PermissionDenied, "unknown or revoked credential")
		}
		s.logger.Error("credential lookup failed", "err", err)
		return store.Node{}, status.Error(codes.Internal, "internal error")
	}
	return n, nil
}

func recvWithTimeout(ctx context.Context, stream chiralv1.AgentService_ChannelServer, d time.Duration) (*chiralv1.AgentFrame, error) {
	type result struct {
		f   *chiralv1.AgentFrame
		err error
	}
	ch := make(chan result, 1)
	go func() {
		f, err := stream.Recv()
		ch <- result{f, err}
	}()
	select {
	case r := <-ch:
		return r.f, r.err
	case <-time.After(d):
		return nil, status.Error(codes.DeadlineExceeded, "timed out waiting for Hello")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func peerIP(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(p.Addr.String())
	if err != nil {
		return p.Addr.String()
	}
	return host
}

func orElse(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
