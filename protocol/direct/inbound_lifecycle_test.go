package direct

import (
	"sync/atomic"
	"testing"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/stretchr/testify/require"
)

type directLifecycleNAT struct {
	purges atomic.Int32
}

func (n *directLifecycleNAT) NewPacket([][]byte, M.Socksaddr, M.Socksaddr, any) {}
func (n *directLifecycleNAT) NewPacketBatch([]*buf.Buffer, []M.Socksaddr, M.Socksaddr, any) {
}
func (n *directLifecycleNAT) Purge() { n.purges.Add(1) }

func TestDirectCloseDrainsUDPNAT(t *testing.T) {
	nat := new(directLifecycleNAT)
	inbound := &Inbound{udpNat: nat}
	require.NoError(t, inbound.Close())
	require.NoError(t, inbound.Close())
	require.Equal(t, int32(1), nat.purges.Load())
}
