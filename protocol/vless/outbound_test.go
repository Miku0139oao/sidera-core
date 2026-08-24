package vless

import (
	"context"
	"net/netip"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/stretchr/testify/require"
)

func TestEncryptionHandshakeContextFollowsCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	handshakeCtx, cancelHandshake := encryptionHandshakeContext(parent)
	cancelParent()
	select {
	case <-handshakeCtx.Done():
		require.ErrorIs(t, handshakeCtx.Err(), context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("encryption handshake context was not canceled")
	}
	cancelHandshake()
}

func TestImportedXrayVisionRejectsUDP443(t *testing.T) {
	dialer := &vlessDialer{xrayVisionRejectUDP443: true}
	destination := M.Socksaddr{Addr: netip.MustParseAddr("1.1.1.1"), Port: 443}
	_, err := dialer.DialContext(t.Context(), "udp", destination)
	require.ErrorContains(t, err, "rejects UDP/443")
	_, err = dialer.ListenPacket(t.Context(), destination)
	require.ErrorContains(t, err, "rejects UDP/443")
}

func TestXrayCompatiblePacketEncoding(t *testing.T) {
	dialer := &vlessDialer{xudp: true, xrayPacketEncoding: true}
	require.False(t, dialer.useXUDP(M.Socksaddr{Port: 53}))
	require.False(t, dialer.useXUDP(M.Socksaddr{Port: 443}))
	require.True(t, dialer.useXUDP(M.Socksaddr{Port: 1234}))

	dialer.xrayPacketEncoding = false
	require.True(t, dialer.useXUDP(M.Socksaddr{Port: 53}))
}
