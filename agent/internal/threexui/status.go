package threexui

import (
	"context"
	"strings"
)

const statusEndpoint = "panel/api/server/status"

// Usage is one current/total resource pair reported by 3x-ui.
type Usage struct {
	Current uint64 `json:"current"`
	Total   uint64 `json:"total"`
}

// DiskIO is the cumulative disk IO reported by 3x-ui.
type DiskIO struct {
	Read  uint64 `json:"read"`
	Write uint64 `json:"write"`
}

// XrayStatus describes 3x-ui's view of its managed Xray process.
type XrayStatus struct {
	State    string `json:"state"`
	ErrorMsg string `json:"errorMsg"`
	Version  string `json:"version"`
}

// NetIO is 3x-ui's current network throughput and packet rate.
type NetIO struct {
	Up         uint64 `json:"up"`
	Down       uint64 `json:"down"`
	PacketUp   uint64 `json:"pktUp"`
	PacketDown uint64 `json:"pktDown"`
}

// NetTraffic is 3x-ui's cumulative host network traffic.
type NetTraffic struct {
	Sent       uint64 `json:"sent"`
	Received   uint64 `json:"recv"`
	PacketSent uint64 `json:"pktSent"`
	PacketRecv uint64 `json:"pktRecv"`
}

// PublicIP is the public address pair observed by 3x-ui.
type PublicIP struct {
	IPv4 string `json:"ipv4"`
	IPv6 string `json:"ipv6"`
}

// AppStats describes the 3x-ui process itself.
type AppStats struct {
	Threads uint32 `json:"threads"`
	Memory  uint64 `json:"mem"`
	Uptime  uint64 `json:"uptime"`
}

// Status is the read-only machine snapshot returned by 3x-ui. The API may add
// fields over time; encoding/json intentionally ignores additions so a panel
// upgrade does not break existing telemetry.
type Status struct {
	CPU          float64    `json:"cpu"`
	CPUCores     int        `json:"cpuCores"`
	LogicalPro   int        `json:"logicalPro"`
	CPUSpeedMHz  float64    `json:"cpuSpeedMhz"`
	Memory       Usage      `json:"mem"`
	Swap         Usage      `json:"swap"`
	Disk         Usage      `json:"disk"`
	DiskIO       DiskIO     `json:"diskIO"`
	Xray         XrayStatus `json:"xray"`
	PanelVersion string     `json:"panelVersion"`
	PanelGUID    string     `json:"panelGuid"`
	Uptime       uint64     `json:"uptime"`
	Loads        []float64  `json:"loads"`
	TCPCount     int        `json:"tcpCount"`
	UDPCount     int        `json:"udpCount"`
	NetIO        NetIO      `json:"netIO"`
	NetTraffic   NetTraffic `json:"netTraffic"`
	PublicIP     PublicIP   `json:"publicIP"`
	AppStats     AppStats   `json:"appStats"`
}

// Status returns 3x-ui's current machine and Xray snapshot without mutating
// either the panel or the managed process.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var status Status
	if err := c.getEnvelope(ctx, statusEndpoint, &status); err != nil {
		return Status{}, err
	}
	// An envelope with an empty object still decodes successfully. Treat the
	// provider's identity field as the schema canary so a login page/proxy or
	// incompatible response cannot be reported as a healthy zero-value panel.
	if strings.TrimSpace(status.PanelVersion) == "" {
		return Status{}, &ContractError{Message: "3x-ui status has no panelVersion"}
	}
	if strings.TrimSpace(status.Xray.State) == "" {
		return Status{}, &ContractError{Message: "3x-ui status has no xray.state"}
	}
	return status, nil
}
