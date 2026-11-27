package ocm

import (
	"net"
	"testing"
	"time"
)

type readDeadlineRecordingConn struct {
	net.Conn
	deadline time.Time
	calls    int
}

func (c *readDeadlineRecordingConn) SetReadDeadline(deadline time.Time) error {
	c.deadline = deadline
	c.calls++
	return nil
}

func TestExtendWebSocketIdleDeadlinesRefreshesBothDirections(t *testing.T) {
	clientConn := &readDeadlineRecordingConn{}
	upstreamConn := &readDeadlineRecordingConn{}

	before := time.Now()
	extendWebSocketIdleDeadlines(clientConn, upstreamConn)
	after := time.Now()

	earliest := before.Add(webSocketIdleTimeout)
	latest := after.Add(webSocketIdleTimeout)
	for name, conn := range map[string]*readDeadlineRecordingConn{
		"client":   clientConn,
		"upstream": upstreamConn,
	} {
		if conn.calls != 1 {
			t.Fatalf("%s read deadline set %d times, want 1", name, conn.calls)
		}
		if conn.deadline.Before(earliest) || conn.deadline.After(latest) {
			t.Fatalf("%s read deadline %v outside [%v, %v]", name, conn.deadline, earliest, latest)
		}
	}
}
