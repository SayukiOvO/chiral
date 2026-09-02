package client

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/SayukiOvO/chiral/agent/internal/xray"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// The agent half of a runtime kernel upgrade: taking one install instruction at
// a time, reporting every phase, and answering nothing else while it runs.

// relayTimeout bounds the wait for one chunk from Core. Long enough for the
// panel to fetch the archive from GitHub on the first request, short enough
// that a silent peer becomes a reportable failure rather than a stall.
const relayTimeout = 3 * time.Minute

// directXrayUpgrade deliberately sits beside, rather than inside, the runtime
// provider contract. It is the legacy side-by-side binary installer and the
// Manager that owns activation/rollback. API-backed runtimes leave this nil.
type directXrayUpgrade struct {
	owner     *xray.Manager
	installer *xray.Installer
}

// installState tracks the one install allowed at a time, and the relay replies
// that belong to it.
type installState struct {
	mu sync.Mutex
	// running is the version currently being installed, "" when idle.
	running string
	// chunks carries XrayChunk frames from the reader goroutine to whichever
	// download is waiting for one. Buffered by one because exactly one request
	// is ever outstanding.
	chunks chan *chiralv1.XrayChunk
}

func newInstallState() *installState {
	return &installState{chunks: make(chan *chiralv1.XrayChunk, 1)}
}

// begin claims the install slot. Returns false when one is already running.
//
// Serialised deliberately. Two installs at once would race over the same
// version directories and, worse, over which binary the manager should be
// pointed at — and the second one's rollback target would be the first one's
// half-finished state.
func (s *installState) begin(version string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running != "" {
		return false
	}
	s.running = version
	// Discard any chunk left over from a previous transfer; it belongs to
	// nobody now and would be read as the first slice of the next archive.
	select {
	case <-s.chunks:
	default:
	}
	return true
}

func (s *installState) end() {
	s.mu.Lock()
	s.running = ""
	s.mu.Unlock()
}

func (s *installState) current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// deliver hands a chunk to the waiting download. Non-blocking: a chunk nobody
// asked for is dropped rather than left to be mistaken for a later reply.
func (s *installState) deliver(c *chiralv1.XrayChunk) {
	select {
	case s.chunks <- c:
	default:
	}
}

// streamRelay implements xray.Relay over the live stream.
type streamRelay struct {
	client *Client
	send   chan<- *chiralv1.AgentFrame
	state  *installState
}

func (r *streamRelay) Chunk(ctx context.Context, version string, offset int64) ([]byte, bool, error) {
	req := &chiralv1.AgentFrame{Frame: &chiralv1.AgentFrame_XrayRelayRequest{
		XrayRelayRequest: &chiralv1.XrayRelayRequest{Version: version, Offset: offset},
	}}
	if !trySend(ctx, r.send, req) {
		return nil, false, fmt.Errorf("the stream closed while relaying %s", version)
	}
	timer := time.NewTimer(relayTimeout)
	defer timer.Stop()
	for {
		select {
		case c := <-r.state.chunks:
			// A reply for a different version or offset is stale — a leftover
			// from a transfer that was abandoned. Keep waiting for ours rather
			// than writing someone else's bytes into this archive.
			if c.GetVersion() != version || c.GetOffset() != offset {
				continue
			}
			if e := c.GetError(); e != "" {
				return nil, false, fmt.Errorf("the panel cannot relay %s: %s", version, e)
			}
			return c.GetData(), c.GetLast(), nil
		case <-timer.C:
			return nil, false, fmt.Errorf("no reply from the panel for %s at offset %d", version, offset)
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
}

// startInstall runs one XrayInstall to completion on its own goroutine,
// reporting each phase back to Core.
func (c *Client) startInstall(ctx context.Context, sendCh chan<- *chiralv1.AgentFrame, req *chiralv1.XrayInstall) {
	version := req.GetVersion()
	if c.directUpgrade == nil {
		c.reportInstall(ctx, sendCh, version,
			chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED,
			"the configured runtime provider does not support direct Xray binary installs")
		return
	}
	if !c.installs.begin(version) {
		c.reportInstall(ctx, sendCh, version,
			chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED,
			"another install ("+c.installs.current()+") is already running")
		return
	}
	go func() {
		defer c.installs.end()
		relay := &streamRelay{client: c, send: sendCh, state: c.installs}
		progress := func(phase chiralv1.XrayInstallPhase, msg string) {
			c.reportInstall(ctx, sendCh, version, phase, msg)
		}
		phase, msg := c.directUpgrade.installer.Install(ctx, req, relay, progress)
		c.logger.Info("kernel install finished", "version", version, "phase", phase, "detail", msg)
		c.reportInstall(ctx, sendCh, version, phase, msg)

		// Keep the version now running and the one it replaced; drop the rest.
		// Each is ~66 MB unpacked, and a node that has followed prereleases for
		// a year would otherwise carry gigabytes nothing will ever start again.
		c.directUpgrade.installer.Prune(
			c.directUpgrade.owner.RunningVersion(),
			c.directUpgrade.owner.BinaryVersion(),
			version,
		)
	}()
}

func (c *Client) reportInstall(ctx context.Context, sendCh chan<- *chiralv1.AgentFrame, version string, phase chiralv1.XrayInstallPhase, msg string) {
	trySend(ctx, sendCh, &chiralv1.AgentFrame{Frame: &chiralv1.AgentFrame_XrayStatus{
		XrayStatus: &chiralv1.XrayStatus{
			Version: version,
			Phase:   phase,
			Message: msg,
			AtUnix:  time.Now().Unix(),
			// Carried with the verdict so Core need not wait for the next
			// heartbeat to believe it. The whole judgement hangs on these two.
			RunningVersion:   c.rt.RunningVersion(),
			InstalledVersion: c.rt.InstalledVersion(),
		},
	}})
}

var _ xray.Relay = (*streamRelay)(nil)
