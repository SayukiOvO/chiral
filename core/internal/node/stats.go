package node

import (
	"math"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/store"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

func (s *Service) recordAcknowledgedStats(nodeID string, sess *Session, report *chiralv1.StatsReport) {
	if s.traffic == nil {
		return
	}
	if !chiralv1.ValidStatsIdentity(report.GetReporterId(), report.GetSequence()) || len(report.GetEntries()) > chiralv1.MaxStatsBatchEntries {
		s.logger.Warn("acknowledged traffic batch rejected: invalid identity or size", "node", nodeID)
		return
	}
	if report.GetContinuityIndeterminate() && len(report.GetEntries()) != 0 {
		s.logger.Warn("acknowledged traffic batch rejected: continuity gap contains entries", "node", nodeID)
		return
	}
	entries := make([]store.TrafficDelta, 0, len(report.GetEntries()))
	for _, entry := range report.GetEntries() {
		var scope string
		switch entry.GetScope() {
		case chiralv1.StatScope_STAT_SCOPE_USER:
			scope = "user"
		case chiralv1.StatScope_STAT_SCOPE_INBOUND:
			scope = "inbound"
		case chiralv1.StatScope_STAT_SCOPE_OUTBOUND:
			scope = "outbound"
		default:
			s.logger.Warn("acknowledged traffic batch rejected: invalid scope", "node", nodeID)
			return
		}
		if entry.GetUplinkBytes() > math.MaxInt64 || entry.GetDownlinkBytes() > math.MaxInt64 {
			s.logger.Warn("acknowledged traffic batch rejected: invalid counter", "node", nodeID)
			return
		}
		entries = append(entries, store.TrafficDelta{Scope: scope, Name: entry.GetName(),
			Up: int64(entry.GetUplinkBytes()), Down: int64(entry.GetDownlinkBytes())})
	}
	if _, err := s.st.RecordStatsBatch(nodeID, report.GetReporterId(), report.GetSequence(), entries, report.GetContinuityIndeterminate(), time.Now()); err != nil {
		// Do not acknowledge partial or failed persistence. The Agent retains
		// and retries the same immutable batch, including across reconnects.
		s.logger.Error("persist acknowledged traffic failed", "node", nodeID, "err", err)
		return
	}
	if err := sess.enqueue(&chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_StatsAck{StatsAck: &chiralv1.StatsAck{
		ReporterId: report.GetReporterId(), Sequence: report.GetSequence(),
	}}}); err != nil {
		// The transaction already committed. A retry receives the same receipt
		// without billing or charting the bytes again.
		s.logger.Warn("traffic receipt could not be queued", "node", nodeID, "err", err)
	}
}
