package node

import (
	"testing"
	"time"

	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

func TestNonHeartbeatActivityDoesNotRefreshHeartbeatTime(t *testing.T) {
	session := &Session{}
	session.touch(&chiralv1.Heartbeat{})

	sentinel := time.Unix(1_777_777_777, 0)
	session.mu.Lock()
	session.lastHBAt = sentinel
	session.mu.Unlock()

	// Acks, events, stats and online reports call touch(nil). They keep the
	// session alive, but cannot refresh the runtime observation in lastHB.
	session.touch(nil)
	session.mu.Lock()
	defer session.mu.Unlock()
	if !session.lastHBAt.Equal(sentinel) {
		t.Fatalf("non-heartbeat activity moved heartbeat time to %v", session.lastHBAt)
	}
}
