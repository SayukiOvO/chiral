package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/store"
)

// Series endpoints back the charts. The window is given as a duration in
// seconds so a client can ask for "the last 6 hours" without doing timezone
// arithmetic, and is clamped to what the retention policy actually keeps —
// asking for a year would otherwise return a mostly-empty chart that looks
// like an outage.
const (
	defaultWindow = 6 * time.Hour
	minWindow     = 5 * time.Minute
)

// window parses ?window=<seconds>, clamped to [minWindow, max].
func window(r *http.Request, fallback, max time.Duration) (from, to time.Time) {
	d := fallback
	if raw := r.URL.Query().Get("window"); raw != "" {
		if secs, err := strconv.ParseInt(raw, 10, 64); err == nil && secs > 0 {
			d = time.Duration(secs) * time.Second
		}
	}
	if d < minWindow {
		d = minWindow
	}
	if d > max {
		d = max
	}
	to = time.Now()
	return to.Add(-d), to
}

// nodeSamples serves a node's resource history.
func (s *Server) nodeSamples(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.st.GetNode(id); err != nil {
		s.notFoundOr(w, "load node", err, "no such node")
		return
	}
	from, to := window(r, defaultWindow, store.NodeSampleRetention)
	samples, err := s.st.NodeSamples(id, from, to)
	if err != nil {
		s.internalErr(w, "load node samples", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from":            from.Unix(),
		"to":              to.Unix(),
		"interval":        int64(store.NodeSampleInterval.Seconds()),
		"samples":         samples,
		"retention_hours": int64(store.NodeSampleRetention.Hours()),
	})
}

// trafficSeries serves accumulated traffic, optionally narrowed to one user
// or node. With neither, it is the whole fleet.
func (s *Server) trafficSeries(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	nodeID := r.URL.Query().Get("node_id")
	// A 404 for an unknown id beats an empty chart that looks like no traffic.
	if userID != "" {
		if _, err := s.st.GetUser(userID); err != nil {
			s.notFoundOr(w, "load user", err, "no such user")
			return
		}
	}
	if nodeID != "" {
		if _, err := s.st.GetNode(nodeID); err != nil {
			s.notFoundOr(w, "load node", err, "no such node")
			return
		}
	}

	from, to := window(r, 24*time.Hour, store.TrafficRetention)
	points, err := s.st.TrafficSeries(userID, nodeID, from, to)
	if err != nil {
		s.internalErr(w, "load traffic series", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from":     from.Unix(),
		"to":       to.Unix(),
		"interval": int64(store.TrafficBucket.Seconds()),
		"points":   points,
	})
}
