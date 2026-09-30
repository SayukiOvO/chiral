package online

import (
	"sort"
	"time"
)

// RecentObservation is one user's approximate provider observation on a node.
// It may come from a cache or history endpoint and therefore never establishes
// that an absent user is offline.
type RecentObservation struct {
	UserID         string
	ObservedOnline bool
	IPs            []string
}

// RecentAddress is an in-memory provider address annotated with its reporting
// node and retrieval time. It is not a durable last-seen record.
type RecentAddress struct {
	IP          string `json:"ip"`
	NodeID      string `json:"node_id"`
	RetrievedAt int64  `json:"retrieved_at"`
}

// Estimate is the approximate counterpart to an exact online Status. False
// ObservedOnline or zero RecentAddressCount never means confirmed offline.
type Estimate struct {
	ObservedOnline     bool  `json:"observed_online"`
	RecentAddressCount int   `json:"recent_address_count"`
	RetrievedAt        int64 `json:"retrieved_at"`
	NodesReporting     int   `json:"nodes_reporting"`
	ReadOK             bool  `json:"read_ok"`
}

type recentNodeState struct {
	at     time.Time
	readOK bool
	users  map[string]recentUserState
}

type recentUserState struct {
	observedOnline bool
	ips            map[string]struct{}
}

// ReplaceRecent replaces one node's approximate provider sample. at is the
// Core receive clock minus the provider-reported retrieval age; it is not an
// IP last-seen timestamp.
func (r *Registry) ReplaceRecent(nodeID string, obs []RecentObservation, readOK bool, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// A node cannot contribute an exact Xray snapshot and an approximate
	// provider sample at once. Remove only this node's exact contribution.
	delete(r.nodes, nodeID)
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

	state := recentNodeState{at: at, readOK: readOK}
	if readOK {
		state.users = make(map[string]recentUserState, len(obs))
		for _, o := range obs {
			if o.UserID == "" {
				continue
			}
			u := state.users[o.UserID]
			u.observedOnline = u.observedOnline || o.ObservedOnline
			if u.ips == nil {
				u.ips = make(map[string]struct{}, len(o.IPs))
			}
			for _, ip := range o.IPs {
				if ip != "" {
					u.ips[ip] = struct{}{}
				}
			}
			state.users[o.UserID] = u
		}
	}
	r.recent[nodeID] = state
}

// RecentAddresses returns current approximate addresses for one user. The
// same address observed on two nodes remains two entries so callers do not
// lose the provenance needed for node isolation.
func (r *Registry) RecentAddresses(userID string, now time.Time) []RecentAddress {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]RecentAddress, 0)
	for nodeID, state := range r.recent {
		if !state.readOK || now.Sub(state.at) > r.ttl {
			continue
		}
		for ip := range state.users[userID].ips {
			out = append(out, RecentAddress{
				IP:          ip,
				NodeID:      nodeID,
				RetrievedAt: state.at.Unix(),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NodeID != out[j].NodeID {
			return out[i].NodeID < out[j].NodeID
		}
		return out[i].IP < out[j].IP
	})
	return out
}

func (r *Registry) estimateLocked(userID string, now time.Time) *Estimate {
	if len(r.recent) == 0 {
		return nil
	}

	estimate := &Estimate{ReadOK: true}
	addresses := make(map[string]struct{})
	for _, state := range r.recent {
		if state.at.Unix() > estimate.RetrievedAt {
			estimate.RetrievedAt = state.at.Unix()
		}
		if now.Sub(state.at) > r.ttl {
			estimate.ReadOK = false
			continue
		}
		estimate.NodesReporting++
		if !state.readOK {
			estimate.ReadOK = false
			continue
		}
		u := state.users[userID]
		estimate.ObservedOnline = estimate.ObservedOnline || u.observedOnline
		for ip := range u.ips {
			addresses[ip] = struct{}{}
		}
	}
	if estimate.NodesReporting == 0 {
		estimate.ReadOK = false
	}
	estimate.RecentAddressCount = len(addresses)
	return estimate
}

func (r *Registry) pruneRecentLocked(now time.Time) {
	for nodeID, state := range r.recent {
		if now.Sub(state.at) <= r.ttl {
			continue
		}
		// Retain a lightweight provider-mode marker until the node disconnects.
		// Otherwise expiry would turn an unknown approximate fleet into the
		// exact-looking Count=0, Partial=false state.
		state.readOK = false
		state.users = nil
		r.recent[nodeID] = state
	}
}
