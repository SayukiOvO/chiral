// Package online tracks which source addresses each user is currently
// connected from, across the whole fleet.
//
// Everything here lives in memory. "Who is online right now" is a question
// about the present, answered every polling round by every node; persisting it
// would mean a write per user per node per round against a database handle
// pinned to a single connection, to store something that is wrong seconds
// later. The durable record — which addresses a user has ever connected from —
// is a different question and lives in store.user_devices.
package online

import (
	"sync"
	"time"
)

// Registry holds the fleet's current view, keyed by user.
type Registry struct {
	mu sync.RWMutex
	// users[userID][ip][nodeID] is the most recent sighting of that address
	// through that node. The extra node dimension preserves another node's
	// contribution when one node disconnects or changes provider mode.
	users map[string]map[string]map[string]sighting
	// nodes records which nodes have reported, and whether their last round
	// was complete, so a partial view can be reported as partial.
	nodes map[string]nodeState
	// recent holds provider cache/history observations. They are deliberately
	// separate from users: their absence never establishes that a user is
	// offline, and they never contribute to the exact Count.
	recent map[string]recentNodeState
	// ttl is how long a sighting counts without being refreshed.
	ttl time.Duration
}

type sighting struct {
	at time.Time
}

type nodeState struct {
	at       time.Time
	complete bool
}

// New builds a registry. ttl should be comfortably longer than the polling
// interval: a sighting that expires between rounds would make a user flicker
// offline and back.
func New(ttl time.Duration) *Registry {
	if ttl <= 0 {
		ttl = 3 * time.Minute
	}
	return &Registry{
		users:  make(map[string]map[string]map[string]sighting),
		nodes:  make(map[string]nodeState),
		recent: make(map[string]recentNodeState),
		ttl:    ttl,
	}
}

// Observation is one user's addresses as seen by one node.
type Observation struct {
	UserID string
	IP     string
	At     time.Time
}

// Replace installs one node's view of the fleet.
//
// The report is an absolute snapshot, so this replaces that node's
// contribution rather than adding to it: an address the node no longer reports
// is an address that disconnected. Sightings from OTHER nodes are untouched —
// a user connected to Tokyo and Frankfurt is online at both, and a report from
// one must not clear the other.
func (r *Registry) Replace(nodeID string, obs []Observation, complete bool, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Receiving an exact snapshot means this node is no longer operating in
	// approximate-provider mode. Do not leave its old estimate beside the new
	// exact contribution.
	delete(r.recent, nodeID)
	r.nodes[nodeID] = nodeState{at: now, complete: complete}

	// Drop this node's previous sightings before installing the new ones.
	for userID, ips := range r.users {
		for ip, sightings := range ips {
			delete(sightings, nodeID)
			if len(sightings) == 0 {
				delete(ips, ip)
			}
		}
		if len(ips) == 0 {
			delete(r.users, userID)
		}
	}

	for _, o := range obs {
		if o.UserID == "" || o.IP == "" {
			continue
		}
		ips, ok := r.users[o.UserID]
		if !ok {
			ips = make(map[string]map[string]sighting)
			r.users[o.UserID] = ips
		}
		sightings, ok := ips[o.IP]
		if !ok {
			sightings = make(map[string]sighting)
			ips[o.IP] = sightings
		}
		at := o.At
		if at.IsZero() {
			at = now
		}
		// A user reachable through two nodes from the same address is one
		// address, not two. Keep the most recent sighting.
		if prev, ok := sightings[nodeID]; !ok || at.After(prev.at) {
			sightings[nodeID] = sighting{at: at}
		}
	}
}

// Forget drops a node's contribution entirely, for when it disconnects.
//
// Its addresses stop counting immediately rather than aging out: a
// disconnected agent is not reporting, and holding its last view for a further
// TTL would keep a user "online" through a node that is plainly gone.
func (r *Registry) Forget(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.nodes, nodeID)
	delete(r.recent, nodeID)
	for userID, ips := range r.users {
		for ip, sightings := range ips {
			delete(sightings, nodeID)
			if len(sightings) == 0 {
				delete(ips, ip)
			}
		}
		if len(ips) == 0 {
			delete(r.users, userID)
		}
	}
}

// Status is what the panel shows for one user.
type Status struct {
	// Count is distinct source addresses across the fleet — a union, not a
	// sum. One device reaching two nodes is one address.
	Count int `json:"count"`
	// Partial is true when the exact view is absent or incomplete, or when any
	// node contributes only approximate observations. Count is then a floor,
	// not a fleet-wide offline assertion.
	Partial bool `json:"partial"`
	// NodesReporting counts the nodes whose view is current.
	NodesReporting int `json:"nodes_reporting"`
	// Estimate is a separate provider-cache/history signal. It never changes
	// Count, because a missing approximate observation is not an offline fact.
	Estimate *Estimate `json:"estimate,omitempty"`
}

// Status reports one user's current address count.
func (r *Registry) Status(userID string, now time.Time) Status {
	r.mu.RLock()
	defer r.mu.RUnlock()

	st := Status{}
	for _, s := range r.nodes {
		if now.Sub(s.at) <= r.ttl {
			st.NodesReporting++
			if !s.complete {
				st.Partial = true
			}
		}
	}
	if st.NodesReporting == 0 {
		// No current exact snapshot means Count is an empty observation, not
		// evidence that the fleet is offline. This also covers a new registry,
		// the last node disconnecting, and Prune removing the last stale node.
		st.Partial = true
	}
	for _, sightings := range r.users[userID] {
		for _, s := range sightings {
			if now.Sub(s.at) <= r.ttl {
				st.Count++
				break
			}
		}
	}
	st.Estimate = r.estimateLocked(userID, now)
	if st.Estimate != nil {
		// A fleet containing an approximate-only node cannot be presented as a
		// complete exact snapshot, including when its last sample has expired.
		st.Partial = true
	}
	return st
}

// Counts reports every user's current address count, for list views that would
// otherwise ask per row.
func (r *Registry) Counts(now time.Time) map[string]int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make(map[string]int, len(r.users))
	for userID, ips := range r.users {
		n := 0
		for _, sightings := range ips {
			for _, s := range sightings {
				if now.Sub(s.at) <= r.ttl {
					n++
					break
				}
			}
		}
		if n > 0 {
			out[userID] = n
		}
	}
	return out
}

// Prune drops sightings and node states past their TTL, so a node that stops
// reporting without disconnecting does not hold a stale view forever.
func (r *Registry) Prune(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for nodeID, s := range r.nodes {
		if now.Sub(s.at) > r.ttl {
			delete(r.nodes, nodeID)
		}
	}
	for userID, ips := range r.users {
		for ip, sightings := range ips {
			for nodeID, s := range sightings {
				if now.Sub(s.at) > r.ttl {
					delete(sightings, nodeID)
				}
			}
			if len(sightings) == 0 {
				delete(ips, ip)
			}
		}
		if len(ips) == 0 {
			delete(r.users, userID)
		}
	}
	r.pruneRecentLocked(now)
}
