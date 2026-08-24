//go:build with_utls

package tls

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	utls "github.com/metacubex/utls"
	"github.com/stretchr/testify/require"
)

func TestParseRealityClientVersion(t *testing.T) {
	version, err := parseRealityClientVersion("26.3.27")
	require.NoError(t, err)
	require.Equal(t, []byte{26, 3, 27}, version)

	version, err = parseRealityClientVersion("1.8")
	require.NoError(t, err)
	require.Equal(t, []byte{1, 8, 0}, version)

	_, err = parseRealityClientVersion("26.3.27.1")
	require.Error(t, err)
}

func TestRealityServerHandshakeCancellationReachesSilentClientAndTarget(t *testing.T) {
	clientPeer, serverSocket := net.Pipe()
	targetSocket, targetPeer := net.Pipe()
	defer clientPeer.Close()
	defer targetPeer.Close()
	trackedClient := &deadlineTrackingConn{Conn: serverSocket}
	trackedTarget := &deadlineTrackingConn{Conn: targetSocket}
	targetDialed := make(chan struct{})

	privateKey := make([]byte, 32)
	if _, err := rand.Read(privateKey); err != nil {
		t.Fatal(err)
	}
	config := &RealityServerConfig{
		config: &utls.RealityConfig{
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				close(targetDialed)
				return trackedTarget, nil
			},
			Type:        "tcp",
			Dest:        "target.test:443",
			ServerNames: map[string]bool{"example.com": true},
			PrivateKey:  privateKey,
			Config:      utls.Config{},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := config.ServerHandshake(ctx, trackedClient)
		result <- err
	}()
	select {
	case <-targetDialed:
	case <-time.After(time.Second):
		t.Fatal("REALITY target was not dialed")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handshake error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled REALITY handshake did not join")
	}
	if trackedClient.deadlines.Load() == 0 || trackedTarget.deadlines.Load() == 0 {
		t.Fatalf("deadline propagation counts: client=%d target=%d", trackedClient.deadlines.Load(), trackedTarget.deadlines.Load())
	}
	if trackedClient.closes.Load() == 0 || trackedTarget.closes.Load() == 0 {
		t.Fatalf("cancellation close counts: client=%d target=%d", trackedClient.closes.Load(), trackedTarget.closes.Load())
	}

	assertRealityPeerClosed(t, clientPeer)
	assertRealityPeerClosed(t, targetPeer)
}

type deadlineTrackingConn struct {
	net.Conn
	deadlines atomic.Int32
	closes    atomic.Int32
}

func (c *deadlineTrackingConn) SetDeadline(deadline time.Time) error {
	c.deadlines.Add(1)
	return c.Conn.SetDeadline(deadline)
}

func (c *deadlineTrackingConn) Close() error {
	c.closes.Add(1)
	return c.Conn.Close()
}

func assertRealityPeerClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("peer remained readable after handshake cancellation")
	}
}
