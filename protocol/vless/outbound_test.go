package vless

import (
	"testing"

	M "github.com/sagernet/sing/common/metadata"
	"github.com/stretchr/testify/require"
)

func TestXrayCompatiblePacketEncoding(t *testing.T) {
	dialer := &vlessDialer{xudp: true, xrayPacketEncoding: true}
	require.False(t, dialer.useXUDP(M.Socksaddr{Port: 53}))
	require.False(t, dialer.useXUDP(M.Socksaddr{Port: 443}))
	require.True(t, dialer.useXUDP(M.Socksaddr{Port: 1234}))

	dialer.xrayPacketEncoding = false
	require.True(t, dialer.useXUDP(M.Socksaddr{Port: 53}))
}
