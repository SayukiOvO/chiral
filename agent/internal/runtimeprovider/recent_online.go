package runtimeprovider

import (
	"context"
	"time"
)

// RecentOnlineReporter is optional and distinct from OnlineUsers. A provider
// exposing only cached users or stored IPs must never present those as the
// Runtime.OnlineUsers current snapshot, even with complete=false.
type RecentOnlineReporter interface {
	RecentOnline(context.Context) (RecentOnlineSample, error)
}

type RecentOnlineSample struct {
	// RetrievedAt is the local time the provider HTTP read began, not when
	// an address connected. Using its start includes collection time in age.
	RetrievedAt time.Time
	Users       []RecentOnlineUser
}

type RecentOnlineUser struct {
	Email          string
	ObservedOnline bool
	IPs            []string
}
