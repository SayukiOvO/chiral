package chiralv1

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestAcknowledgedStatsMaximumFrameFitsDefaultGRPCReceiveLimit(t *testing.T) {
	entries := make([]*StatEntry, MaxStatsBatchEntries)
	for i := range entries {
		name := fmt.Sprintf("%04x", i) + strings.Repeat("x", MaxStatsNameBytes-4)
		entries[i] = &StatEntry{Scope: StatScope_STAT_SCOPE_OUTBOUND, Name: name,
			UplinkBytes: math.MaxInt64, DownlinkBytes: math.MaxInt64}
	}
	frame := &AgentFrame{Frame: &AgentFrame_Stats{Stats: &StatsReport{
		ReporterId: strings.Repeat("a", 32), Sequence: math.MaxInt64, Entries: entries,
	}}}
	if size := proto.Size(frame); size >= 4<<20 {
		t.Fatalf("largest bounded stats frame is %d bytes; default gRPC limit is 4 MiB", size)
	}
}

func TestStatsKeyValidation(t *testing.T) {
	for _, key := range []string{"user>>>alice", "inbound>>>" + strings.Repeat("x", MaxStatsNameBytes), "outbound>>>contains>>>delimiter"} {
		if _, _, ok := SplitStatsKey(key); !ok {
			t.Fatalf("valid key rejected: %q", key)
		}
	}
	for _, key := range []string{"user>>>", "bad>>>name", "name", "user>>> name", "user>>>name\x7f", "user>>>\xff", "user>>>" + strings.Repeat("x", MaxStatsNameBytes+1)} {
		if _, _, ok := SplitStatsKey(key); ok {
			t.Fatalf("invalid key accepted: %q", key)
		}
	}
}
