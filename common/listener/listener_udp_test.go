package listener

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

type udpIntegrityPacketHandler struct {
	packets chan []byte
}

func (h *udpIntegrityPacketHandler) NewPacket(buffer *buf.Buffer, source M.Socksaddr) {
	packet := append([]byte(nil), buffer.Bytes()...)
	h.packets <- packet
}

// isDatagramTooLarge reports the EMSGSIZE error the kernel returns when a
// datagram does not fit the sender's socket send buffer.
func isDatagramTooLarge(err error) bool {
	return errors.Is(err, syscall.EMSGSIZE)
}

func TestUDPListenerDeliversExactDatagramOrCountsTruncatedDrop(t *testing.T) {
	handler := &udpIntegrityPacketHandler{packets: make(chan []byte, 8)}
	listener := New(Options{
		Context: context.Background(),
		Logger:  logger.NOP(),
		Network: []string{N.NetworkUDP},
		Listen: option.ListenOptions{
			Listen:      common.Ptr(badoption.Addr(netip.MustParseAddr("127.0.0.1"))),
			UDPFragment: common.Ptr(true),
		},
		PacketHandler: handler,
	})
	require.NoError(t, listener.Start())
	t.Cleanup(func() { require.NoError(t, listener.Close()) })

	client, err := net.DialUDP("udp", nil, listener.UDPConn().LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	for _, size := range []int{8193, 16385, 65507} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			payload := bytes.Repeat([]byte{byte(size), byte(size >> 8), byte(size >> 16)}, (size+2)/3)[:size]
			beforeDrops := listener.UDPTruncatedPackets()
			_, err = client.Write(payload)
			if isDatagramTooLarge(err) {
				// The host socket buffer is smaller than the datagram, so the
				// kernel rejected it before the listener ever saw it. That is a
				// property of this runner, not of the listener.
				t.Skipf("write of %d bytes rejected by the host socket buffer: %v", size, err)
			}
			require.NoError(t, err)
			deadline := time.NewTimer(2 * time.Second)
			defer deadline.Stop()
			for {
				select {
				case received := <-handler.packets:
					require.Equal(t, payload, received, "a truncated prefix must never reach the handler")
					return
				case <-deadline.C:
					require.Greater(t, listener.UDPTruncatedPackets(), beforeDrops, "a missing oversized datagram must be an observed truncated drop")
					return
				case <-time.After(5 * time.Millisecond):
					if listener.UDPTruncatedPackets() > beforeDrops {
						select {
						case received := <-handler.packets:
							require.Equal(t, payload, received, "a counted truncated datagram was also forwarded")
						default:
						}
						return
					}
				}
			}
		})
	}
}
