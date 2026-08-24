package vless

import (
	"context"
	"net"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/stretchr/testify/require"
)

func TestEncryptionHandshakeDeadlineFollowsCancellation(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	clear, err := setEncryptionHandshakeDeadline(ctx, clientConn)
	require.NoError(t, err)
	defer clear()
	readDone := make(chan error, 1)
	go func() {
		var buffer [1]byte
		_, readErr := clientConn.Read(buffer[:])
		readDone <- readErr
	}()
	cancel()
	select {
	case readErr := <-readDone:
		require.Error(t, readErr)
	case <-time.After(time.Second):
		t.Fatal("canceled encryption handshake did not unblock")
	}
}

func TestXrayCompatiblePacketEncoding(t *testing.T) {
	dialer := &vlessDialer{xudp: true, xrayPacketEncoding: true}
	require.False(t, dialer.useXUDP(M.Socksaddr{Port: 53}))
	require.False(t, dialer.useXUDP(M.Socksaddr{Port: 443}))
	require.True(t, dialer.useXUDP(M.Socksaddr{Port: 1234}))

	dialer.xrayPacketEncoding = false
	require.True(t, dialer.useXUDP(M.Socksaddr{Port: 53}))
}
