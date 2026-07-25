// Package client maintains the agent's connection to Core: one-time
// registration, the persistent Channel stream with exponential-backoff
// reconnect, heartbeats, and frame dispatch.
package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/SayukiOvO/chiral/agent/internal/collector"
	"github.com/SayukiOvO/chiral/agent/internal/xray"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// CredentialMetadataKey mirrors the constant on the Core side; duplicated
// because the components share only proto/.
const CredentialMetadataKey = "x-chiral-credential"

const (
	backoffMin = time.Second
	backoffMax = time.Minute
)

type Config struct {
	PanelAddr         string // host:port of Core's gRPC endpoint
	JoinToken         string // one-time; only needed until first registration
	StateDir          string
	Insecure          bool // plaintext gRPC, dev only
	AgentVersion      string
	HeartbeatInterval time.Duration
	// StatsInterval is how often traffic is read and reported. Each read
	// resets the kernel's counters, so this is also the accounting
	// granularity — and the window of traffic lost if the agent dies.
	StatsInterval time.Duration
}

type state struct {
	NodeID     string `json:"node_id"`
	Credential string `json:"credential"`
}

type Client struct {
	cfg    Config
	xr     *xray.Manager
	col    *collector.Collector
	logger *slog.Logger

	st state
	// events buffers async xray events for the active stream; bounded and
	// lossy (an overwhelmed queue drops, Core learns state via heartbeats).
	events chan *chiralv1.Event
}

func New(cfg Config, xr *xray.Manager, col *collector.Collector, logger *slog.Logger) *Client {
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 10 * time.Second
	}
	if cfg.StatsInterval <= 0 {
		cfg.StatsInterval = 60 * time.Second
	}
	return &Client{cfg: cfg, xr: xr, col: col, logger: logger, events: make(chan *chiralv1.Event, 32)}
}

// QueueEvent enqueues an event for delivery to Core; safe from any goroutine
// and never blocks.
func (c *Client) QueueEvent(kind chiralv1.EventKind, message string) {
	e := &chiralv1.Event{Kind: kind, Message: message, AtUnix: time.Now().Unix()}
	select {
	case c.events <- e:
	default:
		c.logger.Warn("event queue full, dropping event", "kind", kind)
	}
}

// Run blocks until ctx is cancelled, keeping the node registered and
// connected with exponential backoff in between attempts.
func (c *Client) Run(ctx context.Context) error {
	conn, err := c.dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	svc := chiralv1.NewAgentServiceClient(conn)

	if err := c.ensureRegistered(ctx, svc); err != nil {
		return err
	}

	backoff := backoffMin
	for {
		started := time.Now()
		err := c.runStream(ctx, svc)
		if ctx.Err() != nil {
			return nil
		}
		// A rejected credential will not heal by retrying: the node was
		// deleted or the credential revoked. Exit loudly so the operator
		// (or the container restart policy) surfaces it.
		switch status.Code(err) {
		case codes.PermissionDenied, codes.Unauthenticated:
			return fmt.Errorf("credential rejected by core (node deleted or credential revoked; re-register with a fresh join token): %w", err)
		}
		if err != nil {
			c.logger.Warn("stream ended", "err", err)
		}
		// A stream that survived a while means the problem is fresh; start
		// the backoff ladder over.
		if time.Since(started) > time.Minute {
			backoff = backoffMin
		}
		delay := jitter(backoff)
		c.logger.Info("reconnecting", "in", delay.Round(time.Millisecond))
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil
		}
		if backoff *= 2; backoff > backoffMax {
			backoff = backoffMax
		}
	}
}

func (c *Client) dial() (*grpc.ClientConn, error) {
	var creds credentials.TransportCredentials
	if c.cfg.Insecure {
		c.logger.Warn("connecting over PLAINTEXT gRPC (--insecure); dev only")
		creds = insecure.NewCredentials()
	} else {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	return grpc.NewClient(c.cfg.PanelAddr,
		grpc.WithTransportCredentials(creds),
		// Detect dead connections in ~40s instead of the TCP default;
		// matches the server's keepalive enforcement floor.
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)
}

// ensureRegistered loads persisted identity, or performs the one-time join
// token exchange (retrying while Core is unreachable) and persists it.
func (c *Client) ensureRegistered(ctx context.Context, svc chiralv1.AgentServiceClient) error {
	path := c.statePath()
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &c.st); err == nil && c.st.NodeID != "" && c.st.Credential != "" {
			c.logger.Info("loaded node identity", "node", c.st.NodeID)
			return nil
		}
		c.logger.Warn("state file unreadable, re-registering", "path", path)
	}
	if c.cfg.JoinToken == "" {
		return errors.New("not registered and JOIN_TOKEN is empty")
	}
	// Prove the state dir is writable BEFORE spending the one-time join
	// token; failing after registration would strand the node.
	if err := c.probeStateDir(); err != nil {
		return fmt.Errorf("state dir %s not writable (refusing to spend the join token): %w", c.cfg.StateDir, err)
	}

	hostname, _ := os.Hostname()
	backoff := backoffMin
	for {
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		resp, err := svc.Register(callCtx, &chiralv1.RegisterRequest{
			JoinToken:    c.cfg.JoinToken,
			AgentVersion: c.cfg.AgentVersion,
			Hostname:     hostname,
		})
		cancel()
		if err == nil {
			c.st = state{NodeID: resp.GetNodeId(), Credential: resp.GetCredential()}
			if err := c.saveState(); err != nil {
				return fmt.Errorf("persist node identity: %w", err)
			}
			c.logger.Info("registered", "node", c.st.NodeID)
			return nil
		}
		// A definitive rejection (bad/used token) will not heal by retrying.
		if !registerRetryable(err) {
			return fmt.Errorf("registration rejected: %w", err)
		}
		c.logger.Warn("registration attempt failed", "err", err, "retry_in", backoff)
		select {
		case <-time.After(jitter(backoff)):
		case <-ctx.Done():
			return ctx.Err()
		}
		if backoff *= 2; backoff > backoffMax {
			backoff = backoffMax
		}
	}
}

func (c *Client) statePath() string { return filepath.Join(c.cfg.StateDir, "state.json") }

func (c *Client) probeStateDir() error {
	if err := os.MkdirAll(c.cfg.StateDir, 0o755); err != nil {
		return err
	}
	probe := filepath.Join(c.cfg.StateDir, ".write-probe")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		return err
	}
	return os.Remove(probe)
}

func (c *Client) saveState() error {
	if err := os.MkdirAll(c.cfg.StateDir, 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(c.st)
	if err != nil {
		return err
	}
	return os.WriteFile(c.statePath(), raw, 0o600)
}

// runStream runs one Channel stream until it breaks or ctx ends.
func (c *Client) runStream(ctx context.Context, svc chiralv1.AgentServiceClient) error {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	streamCtx = metadata.AppendToOutgoingContext(streamCtx, CredentialMetadataKey, c.st.Credential)

	stream, err := svc.Channel(streamCtx)
	if err != nil {
		return err
	}

	// The protocol requires Hello as the very first frame: send it
	// synchronously, before the writer goroutine can interleave anything
	// (e.g. an xray crash event buffered while we were disconnected).
	hello := &chiralv1.AgentFrame{Frame: &chiralv1.AgentFrame_Hello{Hello: &chiralv1.Hello{
		NodeId:       c.st.NodeID,
		AgentVersion: c.cfg.AgentVersion,
		XrayVersion:  c.xr.BinaryVersion(),
		// PublicIp left empty: Core records the connection's peer address.
	}}}
	if err := stream.Send(hello); err != nil {
		return err
	}
	c.logger.Info("stream established")

	// All further sends flow through one writer goroutine via sendCh (grpc
	// streams forbid concurrent Send).
	sendCh := make(chan *chiralv1.AgentFrame, 16)
	writerDone := make(chan error, 1)
	go func() {
		for {
			select {
			case f := <-sendCh:
				if err := stream.Send(f); err != nil {
					writerDone <- err
					return
				}
			case e := <-c.events:
				if err := stream.Send(&chiralv1.AgentFrame{Frame: &chiralv1.AgentFrame_Event{Event: e}}); err != nil {
					writerDone <- err
					return
				}
			case <-streamCtx.Done():
				writerDone <- streamCtx.Err()
				return
			}
		}
	}()

	// Heartbeat ticker.
	go func() {
		t := time.NewTicker(c.cfg.HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				trySend(streamCtx, sendCh, c.heartbeatFrame())
			case <-streamCtx.Done():
				return
			}
		}
	}()

	// Traffic reporting. Deliberately a separate, slower cadence than the
	// heartbeat: each read RESETS Xray's counters, so the interval defines the
	// accounting granularity, and reading too often multiplies subprocess
	// spawns for no benefit.
	go func() {
		t := time.NewTicker(c.cfg.StatsInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if f := c.statsFrame(streamCtx); f != nil {
					trySend(streamCtx, sendCh, f)
				}
			case <-streamCtx.Done():
				return
			}
		}
	}()

	// Reader loop: dispatch frames from Core.
	for {
		frame, err := stream.Recv()
		if err != nil {
			select {
			case werr := <-writerDone:
				if werr != nil && !errors.Is(werr, context.Canceled) {
					return werr
				}
			default:
			}
			return err
		}
		c.handleFrame(streamCtx, sendCh, frame)
	}
}

func (c *Client) handleFrame(ctx context.Context, sendCh chan<- *chiralv1.AgentFrame, f *chiralv1.CoreFrame) {
	switch fr := f.GetFrame().(type) {
	case *chiralv1.CoreFrame_ConfigPush:
		push := fr.ConfigPush
		c.logger.Info("config push received", "version", push.GetVersion())
		ack := &chiralv1.ConfigAck{Version: push.GetVersion(), Applied: true}
		if err := c.xr.Apply(push.GetVersion(), push.GetConfigJson()); err != nil {
			ack.Applied = false
			ack.Error = err.Error()
			c.logger.Error("config apply failed", "version", push.GetVersion(), "err", err)
		}
		trySend(ctx, sendCh, &chiralv1.AgentFrame{Frame: &chiralv1.AgentFrame_ConfigAck{ConfigAck: ack}})
	case *chiralv1.CoreFrame_Command:
		switch fr.Command.GetCmd().(type) {
		case *chiralv1.Command_RestartXray:
			c.logger.Info("restart command received")
			if err := c.xr.Restart(); err != nil {
				c.QueueEvent(chiralv1.EventKind_EVENT_KIND_ERROR, fmt.Sprintf("restart failed: %v", err))
			} else {
				c.QueueEvent(chiralv1.EventKind_EVENT_KIND_XRAY_RESTARTED, "restarted on command")
			}
		case *chiralv1.Command_ReportNow:
			trySend(ctx, sendCh, c.heartbeatFrame())
		}
	case *chiralv1.CoreFrame_UserOp:
		// Applied against the live kernel so a ban or a new subscriber takes
		// effect without dropping everyone else's connections.
		op := fr.UserOp
		var err error
		switch op.GetKind() {
		case chiralv1.UserOpKind_USER_OP_KIND_ADD:
			err = c.xr.AddUser(ctx, op.GetInboundTag(), op.GetEmail(), op.GetAccountJson())
		case chiralv1.UserOpKind_USER_OP_KIND_REMOVE:
			err = c.xr.RemoveUser(ctx, op.GetInboundTag(), op.GetEmail())
		default:
			err = fmt.Errorf("unknown user op kind %v", op.GetKind())
		}
		if err != nil {
			// Core must hear about this: it believes the operation landed.
			c.logger.Error("user op failed", "kind", op.GetKind(), "email", op.GetEmail(), "err", err)
			c.QueueEvent(chiralv1.EventKind_EVENT_KIND_ERROR,
				fmt.Sprintf("user op %v for %s failed: %v", op.GetKind(), op.GetEmail(), err))
		}
	}
}

// statsFrame reads and resets the kernel's counters, returning a report of
// what flowed since the previous read, or nil when there is nothing to say.
//
// A failed read is logged but not retried: the counters have already been
// reset by a successful call or not consumed at all by a failed one, and the
// next tick covers the same ground.
func (c *Client) statsFrame(ctx context.Context) *chiralv1.AgentFrame {
	stats, err := c.xr.Stats(ctx)
	if err != nil {
		c.logger.Warn("reading xray stats failed", "err", err)
		return nil
	}
	traffic := xray.UserTrafficFrom(stats)
	if len(traffic) == 0 {
		return nil
	}
	entries := make([]*chiralv1.StatEntry, 0, len(traffic))
	for _, t := range traffic {
		entries = append(entries, &chiralv1.StatEntry{
			Scope:         chiralv1.StatScope_STAT_SCOPE_USER,
			Name:          t.Email,
			UplinkBytes:   uint64(t.Up),
			DownlinkBytes: uint64(t.Down),
		})
	}
	return &chiralv1.AgentFrame{Frame: &chiralv1.AgentFrame_Stats{
		Stats: &chiralv1.StatsReport{Entries: entries},
	}}
}

func (c *Client) heartbeatFrame() *chiralv1.AgentFrame {
	hb := c.col.Sample()
	hb.XrayState = c.xr.State()
	hb.ConfigVersion = c.xr.ConfigVersion()
	return &chiralv1.AgentFrame{Frame: &chiralv1.AgentFrame_Heartbeat{Heartbeat: hb}}
}

// trySend enqueues to the writer unless the stream is gone.
func trySend(ctx context.Context, sendCh chan<- *chiralv1.AgentFrame, f *chiralv1.AgentFrame) bool {
	select {
	case sendCh <- f:
		return true
	case <-ctx.Done():
		return false
	}
}

func jitter(d time.Duration) time.Duration {
	// ±20% keeps a fleet of agents from reconnecting in lockstep.
	f := 0.8 + 0.4*rand.Float64()
	return time.Duration(float64(d) * f)
}

// registerRetryable reports whether a Register error may heal by retrying.
// Explicit rejections (invalid or used token) are terminal; transport errors
// and server hiccups are transient.
func registerRetryable(err error) bool {
	switch status.Code(err) {
	case codes.PermissionDenied, codes.InvalidArgument, codes.Unauthenticated:
		return false
	}
	return true
}
