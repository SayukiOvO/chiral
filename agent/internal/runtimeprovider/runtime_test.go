package runtimeprovider

import "testing"

func TestUserTrafficFromFoldsOnlyUserTraffic(t *testing.T) {
	got := UserTrafficFrom([]Stat{
		{Name: "user>>>alice@p.n>>>traffic>>>uplink", Value: 10},
		{Name: "user>>>alice@p.n>>>traffic>>>downlink", Value: 90},
		{Name: "user>>>idle@p.n>>>traffic>>>uplink", Value: 0},
		{Name: "inbound>>>vless-in>>>traffic>>>uplink", Value: 999},
		{Name: "user>>>malformed", Value: 999},
	})
	if len(got) != 1 {
		t.Fatalf("expected one active user, got %+v", got)
	}
	if got[0].Email != "alice@p.n" || got[0].Up != 10 || got[0].Down != 90 {
		t.Fatalf("unexpected folded traffic: %+v", got[0])
	}
}
