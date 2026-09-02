// Package node owns the node registry: the gRPC service agents talk to, the
// live session table, online determination, and config push.
package node

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// Session is one live Channel stream for a node. Frames destined for the
// agent go through send; the stream handler's writer goroutine owns
// stream.Send exclusively.
type Session struct {
	nodeID string
	send   chan *chiralv1.CoreFrame
	// done is closed when the session is replaced by a newer stream for the
	// same node; the handler must then return.
	done     chan struct{}
	doneOnce sync.Once

	mu       sync.Mutex
	lastSeen time.Time
	hello    *chiralv1.Hello
	lastHB   *chiralv1.Heartbeat
	lastHBAt time.Time
	// lastPushVersion/lastPushAt throttle heartbeat-driven config
	// reconciliation so a slow agent is not flooded with duplicate pushes.
	lastPushVersion int64
	lastPushAt      time.Time
}

func (s *Session) close() { s.doneOnce.Do(func() { close(s.done) }) }

func (s *Session) touch(hb *chiralv1.Heartbeat) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.lastSeen = now
	if hb != nil {
		s.lastHB = hb
		s.lastHBAt = now
	}
}

// Manager tracks live sessions. A node is online iff it has a session whose
// last activity is within the heartbeat timeout.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session

	hbTimeout time.Duration
	// probeURL is where agents fetch from to prove traffic flows. Set once at
	// startup, before any session exists, so it needs no lock.
	probeURL string
	logger   *slog.Logger
}

func NewManager(hbTimeout time.Duration, logger *slog.Logger) *Manager {
	return &Manager{
		sessions:  make(map[string]*Session),
		hbTimeout: hbTimeout,
		logger:    logger,
	}
}

// attach registers a new session, replacing (and closing) any previous one
// for the same node.
func (m *Manager) attach(nodeID string, s *Session) {
	m.mu.Lock()
	old := m.sessions[nodeID]
	m.sessions[nodeID] = s
	m.mu.Unlock()
	if old != nil {
		m.logger.Warn("replacing existing session", "node", nodeID)
		old.close()
	}
}

// detach removes the session unless a newer one has already replaced it.
func (m *Manager) detach(nodeID string, s *Session) {
	m.mu.Lock()
	if m.sessions[nodeID] == s {
		delete(m.sessions, nodeID)
	}
	m.mu.Unlock()
}

// NodeState is a point-in-time view of a node's liveness for the API.
type NodeState struct {
	Online    bool
	LastSeen  time.Time
	Hello     *chiralv1.Hello
	Heartbeat *chiralv1.Heartbeat
	// HeartbeatAt is distinct from LastSeen: stats, acks and events prove the
	// stream is alive but must not make an old runtime observation look fresh.
	HeartbeatAt time.Time
}

func (m *Manager) State(nodeID string) NodeState {
	m.mu.RLock()
	s := m.sessions[nodeID]
	m.mu.RUnlock()
	if s == nil {
		return NodeState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return NodeState{
		Online:      time.Since(s.lastSeen) < m.hbTimeout,
		LastSeen:    s.lastSeen,
		Hello:       s.hello,
		Heartbeat:   s.lastHB,
		HeartbeatAt: s.lastHBAt,
	}
}

// IsOnline reports whether a node currently has a live, recently-active
// session. Same rule the API's node list uses, so alerting and the UI can
// never disagree about who is up.
func (m *Manager) IsOnline(nodeID string) bool {
	return m.State(nodeID).Online
}

// PushConfig queues a ConfigPush to the node's live session. It fails when
// the node is offline or its send queue is full; the config stays persisted
// either way and is re-pushed on the next connect.
func (m *Manager) PushConfig(nodeID string, version int64, configJSON, probeOutbound []byte) error {
	return m.enqueue(nodeID, &chiralv1.CoreFrame{
		Frame: &chiralv1.CoreFrame_ConfigPush{ConfigPush: &chiralv1.ConfigPush{
			Version:           version,
			ConfigJson:        configJSON,
			ProbeOutboundJson: probeOutbound,
			ProbeUrl:          m.probeURL,
		}},
	})
}

// SetProbeURL sets where agents fetch from when checking that traffic flows.
// Empty leaves the agent's own default in place.
func (m *Manager) SetProbeURL(u string) { m.probeURL = u }

// SendUserOp queues an online user add/remove to the node's live session.
// Fails when the node is offline; the caller reconciles on reconnect.
func (m *Manager) SendUserOp(nodeID string, op *chiralv1.UserOp) error {
	return m.enqueue(nodeID, &chiralv1.CoreFrame{
		Frame: &chiralv1.CoreFrame_UserOp{UserOp: op},
	})
}

// SendCommand queues a Command frame to the node's live session.
func (m *Manager) SendCommand(nodeID string, cmd *chiralv1.Command) error {
	return m.enqueue(nodeID, &chiralv1.CoreFrame{
		Frame: &chiralv1.CoreFrame_Command{Command: cmd},
	})
}

// SendOnlinePolicy queues the address-polling policy to the node's live
// session. Agents start idle and keep no policy across reconnects, so this is
// sent on every connect rather than only on change.
func (m *Manager) SendOnlinePolicy(nodeID string, p *chiralv1.OnlinePolicy) error {
	return m.enqueue(nodeID, &chiralv1.CoreFrame{
		Frame: &chiralv1.CoreFrame_OnlinePolicy{OnlinePolicy: p},
	})
}

// SendXrayInstall queues a kernel install instruction.
func (m *Manager) SendXrayInstall(nodeID string, in *chiralv1.XrayInstall) error {
	return m.enqueue(nodeID, &chiralv1.CoreFrame{
		Frame: &chiralv1.CoreFrame_XrayInstall{XrayInstall: in},
	})
}

// SendXrayChunk queues one slice of a relayed archive.
//
// Uses the same bounded queue as everything else, and that is safe only because
// the agent asks for one chunk at a time: at most one relay frame is ever in
// flight per node, so a 21 MB transfer cannot crowd out a config push sharing
// the queue.
func (m *Manager) SendXrayChunk(nodeID string, c *chiralv1.XrayChunk) error {
	return m.enqueue(nodeID, &chiralv1.CoreFrame{
		Frame: &chiralv1.CoreFrame_XrayChunk{XrayChunk: c},
	})
}

// CloseSession force-closes a node's live session, e.g. after the node (and
// with it the credential) is deleted.
func (m *Manager) CloseSession(nodeID string) {
	m.mu.RLock()
	s := m.sessions[nodeID]
	m.mu.RUnlock()
	if s != nil {
		s.close()
	}
}

// CloseAll force-closes every live session. Called on shutdown so Channel
// handlers return and GracefulStop can complete.
func (m *Manager) CloseAll() {
	m.mu.RLock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.RUnlock()
	for _, s := range sessions {
		s.close()
	}
}

func (m *Manager) enqueue(nodeID string, f *chiralv1.CoreFrame) error {
	m.mu.RLock()
	s := m.sessions[nodeID]
	m.mu.RUnlock()
	if s == nil {
		return fmt.Errorf("node %s is offline", nodeID)
	}
	select {
	case s.send <- f:
		return nil
	case <-s.done:
		return fmt.Errorf("node %s is offline", nodeID)
	default:
		return fmt.Errorf("node %s send queue is full", nodeID)
	}
}
