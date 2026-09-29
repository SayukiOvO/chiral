package threexui

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"

	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

const (
	inboundTrafficEndpoint  = "panel/api/inbounds/list"
	outboundTrafficEndpoint = "panel/api/xray/getOutboundsTraffic"
	maxTrafficRows          = 100000
)

// ClientTraffic contains local cumulative counters for one credential. Total
// in the upstream object is a quota, not a byte counter, and is not exposed.
type ClientTraffic struct {
	ID        int64
	InboundID int64
	Email     string
	Up        uint64
	Down      uint64
}

// TaggedTraffic contains local cumulative counters for an inbound or outbound.
type TaggedTraffic struct {
	ID   int64
	Tag  string
	Up   uint64
	Down uint64
}

// TrafficSnapshot is cumulative persisted usage, not deltas. Client and
// inbound counters are read together; outbound counters require a second HTTP
// request. Consequently this is not a transactionally consistent cross-scope
// snapshot and those scopes must never be summed to bill a user twice.
type TrafficSnapshot struct {
	Clients   []ClientTraffic
	Inbounds  []TaggedTraffic
	Outbounds []TaggedTraffic
}

type trafficCounters struct {
	ID   *int64 `json:"id"`
	Up   *int64 `json:"up"`
	Down *int64 `json:"down"`
}

type clientTrafficWire struct {
	trafficCounters
	InboundID *int64 `json:"inboundId"`
	Email     string `json:"email"`
}

type inboundTrafficWire struct {
	trafficCounters
	Tag         string              `json:"tag"`
	NodeID      *int64              `json:"nodeId"`
	ClientStats []clientTrafficWire `json:"clientStats"`
}

type outboundTrafficWire struct {
	trafficCounters
	Tag string `json:"tag"`
}

// TrafficSnapshot reads only the official provider's persisted counters. A
// failed response returns no partial snapshot, so a caller cannot advance its
// accounting watermarks on a truncated or failed round.
func (c *Client) TrafficSnapshot(ctx context.Context) (TrafficSnapshot, error) {
	clients, inbounds, err := c.localInboundTraffic(ctx)
	if err != nil {
		return TrafficSnapshot{}, err
	}
	outbounds, err := c.OutboundTraffics(ctx)
	if err != nil {
		return TrafficSnapshot{}, err
	}
	return TrafficSnapshot{Clients: clients, Inbounds: inbounds, Outbounds: outbounds}, nil
}

// ClientTraffics reads raw local clientStats from the full inbound list. The
// clients/list and clients/traffic endpoints can overlay a master's aggregate
// usage, and must not be used as this node's billing source. Statistics for a
// shared email may appear on several inbounds; they are returned exactly once.
// Detached/deleted clients are not necessarily returned by the upstream list;
// callers must sample before removing managed clients and retain watermarks.
func (c *Client) ClientTraffics(ctx context.Context) ([]ClientTraffic, error) {
	clients, _, err := c.localInboundTraffic(ctx)
	return clients, err
}

func (c *Client) InboundTraffics(ctx context.Context) ([]TaggedTraffic, error) {
	_, inbounds, err := c.localInboundTraffic(ctx)
	return inbounds, err
}

func (c *Client) localInboundTraffic(ctx context.Context) ([]ClientTraffic, []TaggedTraffic, error) {
	var rows []inboundTrafficWire
	if err := c.getTrafficArray(ctx, inboundTrafficEndpoint, &rows); err != nil {
		return nil, nil, err
	}
	if len(rows) > maxTrafficRows {
		return nil, nil, trafficContractError()
	}
	inbounds := make([]TaggedTraffic, 0, len(rows))
	clients := make(map[string]ClientTraffic)
	tags := make(map[string]bool)
	ids := make(map[int64]bool)
	clientIDs := make(map[int64]string)
	for _, row := range rows {
		if !validTrafficCounters(row.trafficCounters) || !validTrafficName(row.Tag) ||
			(row.NodeID != nil && *row.NodeID != 0) || tags[row.Tag] || ids[*row.ID] {
			return nil, nil, trafficContractError()
		}
		tags[row.Tag], ids[*row.ID] = true, true
		inbounds = append(inbounds, TaggedTraffic{ID: *row.ID, Tag: row.Tag, Up: uint64(*row.Up), Down: uint64(*row.Down)})
		for _, raw := range row.ClientStats {
			if !validTrafficCounters(raw.trafficCounters) || !validTrafficName(raw.Email) || raw.InboundID == nil || *raw.InboundID <= 0 {
				return nil, nil, trafficContractError()
			}
			entry := ClientTraffic{ID: *raw.ID, InboundID: *raw.InboundID, Email: raw.Email, Up: uint64(*raw.Up), Down: uint64(*raw.Down)}
			if prev, exists := clients[entry.Email]; exists && prev != entry {
				return nil, nil, trafficContractError()
			}
			if email, exists := clientIDs[entry.ID]; exists && email != entry.Email {
				return nil, nil, trafficContractError()
			}
			clients[entry.Email], clientIDs[entry.ID] = entry, entry.Email
			if len(clients) > maxTrafficRows {
				return nil, nil, trafficContractError()
			}
		}
	}
	out := make([]ClientTraffic, 0, len(clients))
	for _, entry := range clients {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	sort.Slice(inbounds, func(i, j int) bool { return inbounds[i].Tag < inbounds[j].Tag })
	return out, inbounds, nil
}

// OutboundTraffics reads cumulative provider-owned outbound counters. These
// are telemetry, not another contribution to subscriber billing.
func (c *Client) OutboundTraffics(ctx context.Context) ([]TaggedTraffic, error) {
	var rows []outboundTrafficWire
	if err := c.getTrafficArray(ctx, outboundTrafficEndpoint, &rows); err != nil {
		return nil, err
	}
	if len(rows) > maxTrafficRows {
		return nil, trafficContractError()
	}
	out := make([]TaggedTraffic, 0, len(rows))
	tags := make(map[string]bool)
	ids := make(map[int64]bool)
	for _, row := range rows {
		if !validTrafficCounters(row.trafficCounters) || !validTrafficName(row.Tag) || tags[row.Tag] || ids[*row.ID] {
			return nil, trafficContractError()
		}
		tags[row.Tag], ids[*row.ID] = true, true
		out = append(out, TaggedTraffic{ID: *row.ID, Tag: row.Tag, Up: uint64(*row.Up), Down: uint64(*row.Down)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tag < out[j].Tag })
	return out, nil
}

func (c *Client) getTrafficArray(ctx context.Context, endpoint string, dst any) error {
	u, err := c.endpointURL(endpoint)
	if err != nil {
		return err
	}
	// Inbound responses include credential settings. Even error responses
	// must not echo arbitrary upstream fields or messages into Agent logs.
	body, err := c.request(ctx, http.MethodGet, u, nil, "", true)
	if err != nil {
		return err
	}
	var response envelope
	if json.Unmarshal(body, &response) != nil || response.Success == nil {
		return trafficContractError()
	}
	if !*response.Success {
		return &APIError{}
	}
	raw := bytes.TrimSpace(response.Object)
	// Upstream serializes a nil slice as null when a new panel has no rows.
	// Missing obj is still invalid; only an explicit successful null is empty.
	if bytes.Equal(raw, []byte("null")) {
		raw = []byte("[]")
	}
	// Keep diagnostics independent of arbitrary provider fields, which may
	// include credential material alongside the selected traffic fields.
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, dst) != nil {
		return trafficContractError()
	}
	return nil
}

func validTrafficCounters(c trafficCounters) bool {
	return c.ID != nil && *c.ID > 0 && c.Up != nil && *c.Up >= 0 && c.Down != nil && *c.Down >= 0 && *c.Up <= math.MaxInt64-*c.Down
}

func validTrafficName(name string) bool {
	return chiralv1.ValidStatsName(name)
}

func trafficContractError() error {
	return &ContractError{Message: "3x-ui returned invalid or ambiguous local traffic counters"}
}
