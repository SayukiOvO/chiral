package client

import (
	"context"
	"math"
	"net/netip"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

type queuedRecentOnline struct {
	frame       *chiralv1.AgentFrame
	queuedAt    time.Time
	policyEpoch uint64
}

// Account for local writer backpressure and recheck permission at dispatch.
// Transport delay after Send cannot be measured by this per-stream protocol.
func (c *Client) prepareRecentOnlineForSend(ctx context.Context, queued queuedRecentOnline, now time.Time) *chiralv1.AgentFrame {
	allowed, epoch := c.recentOnlinePolicy()
	if ctx.Err() != nil || !allowed || epoch != queued.policyEpoch || queued.queuedAt.After(now) {
		return nil
	}
	report := queued.frame.GetRecentOnline()
	if report == nil {
		return nil
	}
	delay := now.Sub(queued.queuedAt)
	seconds := uint64(delay / time.Second)
	if delay%time.Second != 0 {
		seconds++
	}
	seconds += uint64(report.RetrievedAgeSeconds)
	if seconds > math.MaxUint32 {
		return nil
	}
	return &chiralv1.AgentFrame{Frame: &chiralv1.AgentFrame_RecentOnline{RecentOnline: &chiralv1.RecentOnlineReport{
		RetrievedAgeSeconds: uint32(seconds), ReadOk: report.ReadOk, Users: report.Users,
	}}}
}

func (c *Client) recentOnlinePolicy() (bool, uint64) {
	c.onlineMu.Lock()
	defer c.onlineMu.Unlock()
	return c.onlinePolicy.GetEnabled() && c.onlinePolicy.GetRecentObservations(), c.onlineEpoch
}

// An approximate-only runtime never falls back to the exact snapshot path.
// This is negotiated per stream, so an old Core cannot misread the report as
// current connections. Failed reads are explicit unknowns, not empty success.
func (c *Client) recentOnlineFrame(ctx context.Context, reporter runtimeprovider.RecentOnlineReporter) *chiralv1.AgentFrame {
	allowed, epoch := c.recentOnlinePolicy()
	if !allowed || ctx.Err() != nil {
		return nil
	}
	startedAt := time.Now()
	sample, err := reporter.RecentOnline(ctx)
	allowed, currentEpoch := c.recentOnlinePolicy()
	if !allowed || currentEpoch != epoch || ctx.Err() != nil {
		return nil
	}
	now := time.Now()
	// Even failed reads retain their local attempt age. Do not refresh an old
	// failure merely because the writer was blocked before it could dispatch.
	report := recentOnlineReport(runtimeprovider.RecentOnlineSample{RetrievedAt: startedAt}, now)
	report.ReadOk = false
	if err == nil {
		if candidate := recentOnlineReport(sample, now); candidate.ReadOk {
			report = candidate
		}
	}
	if !report.ReadOk {
		// Provider errors may contain credential keys or addresses.
		c.logger.Warn("recent online observation is unavailable")
	}
	return &chiralv1.AgentFrame{Frame: &chiralv1.AgentFrame_RecentOnline{RecentOnline: report}}
}

func recentOnlineReport(sample runtimeprovider.RecentOnlineSample, now time.Time) *chiralv1.RecentOnlineReport {
	failure := func() *chiralv1.RecentOnlineReport { return &chiralv1.RecentOnlineReport{} }
	if sample.RetrievedAt.IsZero() || sample.RetrievedAt.After(now) || len(sample.Users) > chiralv1.MaxRecentOnlineUsers {
		return failure()
	}
	age := now.Sub(sample.RetrievedAt)
	seconds := uint64(age / time.Second)
	if age%time.Second != 0 {
		seconds++ // Never round stale evidence into a newer interval.
	}
	if seconds > math.MaxUint32 {
		return failure()
	}
	users := make([]*chiralv1.RecentOnlineUser, 0, len(sample.Users))
	emails := make(map[string]bool, len(sample.Users))
	ipCount := 0
	for _, user := range sample.Users {
		if user.Email == "" || len(user.Email) > chiralv1.MaxRecentOnlineEmailBytes || !utf8.ValidString(user.Email) ||
			strings.TrimSpace(user.Email) != user.Email || strings.ContainsFunc(user.Email, unicode.IsControl) || emails[user.Email] {
			return failure()
		}
		emails[user.Email] = true
		if len(user.IPs) > chiralv1.MaxRecentOnlineIPs-ipCount {
			return failure()
		}
		ipCount += len(user.IPs)
		ips := make([]string, 0, len(user.IPs))
		seen := make(map[netip.Addr]bool, len(user.IPs))
		for _, raw := range user.IPs {
			addr, err := netip.ParseAddr(raw)
			if err != nil || addr.Zone() != "" {
				return failure()
			}
			addr = addr.Unmap()
			if !seen[addr] {
				ips = append(ips, addr.String())
				seen[addr] = true
			}
		}
		sort.Strings(ips)
		users = append(users, &chiralv1.RecentOnlineUser{Email: user.Email, ObservedOnline: user.ObservedOnline, Ips: ips})
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Email < users[j].Email })
	return &chiralv1.RecentOnlineReport{RetrievedAgeSeconds: uint32(seconds), ReadOk: true, Users: users}
}
