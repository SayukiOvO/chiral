package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/online"
)

// Two routes with deliberately different guards.
//
// The COUNT answers every question an operator actually has — "why can this
// person not connect", "is this account being shared" — and sits at the same
// level as the rest of the read-only surface.
//
// The ADDRESSES answer a different question: where a specific person is. That
// is not an operations question, so it needs the highest role and leaves an
// audit trail. requireAdmin expands to auth.RoleViewer, which would put a list
// of every subscriber's home addresses behind the most widely handed-out role
// in the panel.

// onlineView is one user's current address count.
type onlineView struct {
	online.Status
	// Limit is the operator's expectation, 0 for none. Nothing enforces it —
	// no Xray API can terminate an established session — so it is here to be
	// displayed, not to be acted on.
	Limit int `json:"device_limit"`
	// Recording reports whether the feature is switched on at all, so the UI
	// can say "not recording" instead of showing a confident zero.
	Recording bool `json:"recording"`
}

func (s *Server) userOnline(w http.ResponseWriter, r *http.Request) {
	u, err := s.st.GetUser(r.PathValue("id"))
	if err != nil {
		s.notFoundOr(w, "load user", err, "no such user")
		return
	}
	view := onlineView{Limit: u.DeviceLimit, Recording: s.online != nil}
	if s.online != nil {
		view.Status = s.online.Status(u.ID, time.Now())
	}
	writeJSON(w, http.StatusOK, view)
}

// deviceView is one observed address.
type deviceView struct {
	IP        string `json:"ip"`
	NodeID    string `json:"node_id"`
	NodeName  string `json:"node_name"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
}

func (s *Server) userDevices(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, err := s.st.GetUser(id)
	if err != nil {
		s.notFoundOr(w, "load user", err, "no such user")
		return
	}
	devices, err := s.st.UserDevices(u.ID)
	if err != nil {
		s.internalErr(w, "load devices", err)
		return
	}

	names := map[string]string{}
	if nodes, err := s.st.ListNodes(); err == nil {
		for _, n := range nodes {
			names[n.ID] = n.Name
		}
	}
	out := make([]deviceView, 0, len(devices))
	for _, d := range devices {
		out = append(out, deviceView{
			IP: d.IP, NodeID: d.NodeID, NodeName: names[d.NodeID],
			FirstSeen: d.FirstSeen, LastSeen: d.LastSeen,
		})
	}

	// Audited on read, not on change. Looking up where someone lives is the
	// action worth recording here; there is nothing to change.
	s.audit(r, "user.devices_viewed", "user", u.ID, u.Name, strconv.Itoa(len(out)))
	writeJSON(w, http.StatusOK, map[string]any{"devices": out, "recording": s.online != nil})
}

// onlineCounts decorates a user list without a query per row.
func (s *Server) onlineCounts() map[string]int {
	if s.online == nil {
		return nil
	}
	return s.online.Counts(time.Now())
}

// OnlineSource is the subset of the registry the API reads. An interface so
// the API package does not depend on the registry's construction, and so a nil
// value cleanly means "not recording".
type OnlineSource interface {
	Status(userID string, now time.Time) online.Status
	Counts(now time.Time) map[string]int
}

var _ OnlineSource = (*online.Registry)(nil)
