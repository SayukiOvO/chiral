package client

import (
	"context"
	"math"

	"github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

func (c *Client) acknowledgedStatsFrame(ctx context.Context, reporter runtimeprovider.AcknowledgedReporter) *chiralv1.AgentFrame {
	if ctx.Err() != nil {
		return nil
	}
	c.statsPollMu.Lock()
	defer c.statsPollMu.Unlock()
	// Check after acquiring the lock as well: an old stream may have been
	// cancelled while its ticker was waiting behind another provider read.
	if ctx.Err() != nil {
		return nil
	}
	c.statsMu.Lock()
	negotiated := c.acknowledgedStats
	c.statsMu.Unlock()
	if !negotiated {
		// An old Core ignores report identities and would charge every retry.
		// Do not read or advance provider counters until support is explicit.
		return nil
	}
	batch, err := reporter.StatsBatch(ctx)
	if ctx.Err() != nil {
		// A batch persisted just before cancellation remains in the provider's
		// outbox and is replayed on the next negotiated stream, not discarded.
		return nil
	}
	if err != nil {
		c.logger.Warn("reading acknowledged traffic failed", "err", err)
		return nil
	}
	if batch == nil {
		return nil
	}
	if !chiralv1.ValidStatsIdentity(batch.ReporterID, batch.Sequence) || len(batch.Entries) > chiralv1.MaxStatsBatchEntries {
		c.logger.Error("runtime produced an invalid acknowledged batch identity or size")
		return nil
	}
	if batch.Indeterminate {
		if len(batch.Entries) != 0 {
			c.logger.Error("runtime produced a continuity gap with traffic entries")
			return nil
		}
		c.logger.Warn("runtime accounting continuity is indeterminate", "reason", batch.Reason)
		// This best-effort event is only an immediate hint. The report below
		// carries the gap through the durable receipt protocol even if it drops.
		c.QueueEvent(chiralv1.EventKind_EVENT_KIND_ERROR, "runtime accounting continuity is indeterminate; operator review is required")
	}
	entries := make([]*chiralv1.StatEntry, 0, len(batch.Entries))
	seen := make(map[string]bool, len(batch.Entries))
	for _, delta := range batch.Entries {
		scopeName, name, ok := chiralv1.SplitStatsKey(delta.Key)
		if !ok || seen[delta.Key] || delta.Up > math.MaxInt64 || delta.Down > math.MaxInt64-delta.Up {
			c.logger.Error("runtime produced an invalid acknowledged counter")
			return nil
		}
		seen[delta.Key] = true
		var scope chiralv1.StatScope
		switch scopeName {
		case "user":
			scope = chiralv1.StatScope_STAT_SCOPE_USER
		case "inbound":
			scope = chiralv1.StatScope_STAT_SCOPE_INBOUND
		case "outbound":
			scope = chiralv1.StatScope_STAT_SCOPE_OUTBOUND
		default:
			c.logger.Error("runtime produced an unknown acknowledged counter scope")
			return nil
		}
		entries = append(entries, &chiralv1.StatEntry{Scope: scope, Name: name, UplinkBytes: delta.Up, DownlinkBytes: delta.Down})
	}
	return &chiralv1.AgentFrame{Frame: &chiralv1.AgentFrame_Stats{Stats: &chiralv1.StatsReport{
		Entries: entries, ReporterId: batch.ReporterID, Sequence: batch.Sequence, ContinuityIndeterminate: batch.Indeterminate,
	}}}
}
