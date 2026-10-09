package chiralv1

import (
	"fmt"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestMaximumRecentOnlineFrameFitsDefaultGRPCMessage(t *testing.T) {
	report := &RecentOnlineReport{ReadOk: true, RetrievedAgeSeconds: ^uint32(0)}
	for i := 0; i < MaxRecentOnlineUsers; i++ {
		prefix := fmt.Sprintf("%04d", i)
		user := &RecentOnlineUser{Email: prefix + strings.Repeat("e", MaxRecentOnlineEmailBytes-len(prefix)), ObservedOnline: true}
		if i < MaxRecentOnlineIPs/2 {
			user.Ips = []string{strings.Repeat("f", 45), strings.Repeat("f", 45)}
		}
		report.Users = append(report.Users, user)
	}
	frame := &AgentFrame{Frame: &AgentFrame_RecentOnline{RecentOnline: report}}
	if size := proto.Size(frame); size >= 4<<20 {
		t.Fatalf("maximum recent frame is %d bytes", size)
	}
}
