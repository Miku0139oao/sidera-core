package route

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/common/sniff"
	R "github.com/Miku0139oao/sidera-core/route/rule"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

type sniffTestPacketConn struct {
	access     sync.Mutex
	packets    [][]byte
	finalErr   error
	reads      int
	lastBuffer *buf.Buffer
	closed     chan struct{}
	close      sync.Once
}

func newSniffTestPacketConn(packetCount int, packetSize int, finalErr error) *sniffTestPacketConn {
	packets := make([][]byte, packetCount)
	for index := range packets {
		packets[index] = make([]byte, packetSize)
	}
	return &sniffTestPacketConn{
		packets:  packets,
		finalErr: finalErr,
		closed:   make(chan struct{}),
	}
}

func (c *sniffTestPacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	c.access.Lock()
	defer c.access.Unlock()
	select {
	case <-c.closed:
		return M.Socksaddr{}, net.ErrClosed
	default:
	}
	c.reads++
	if len(c.packets) == 0 {
		return M.Socksaddr{}, c.finalErr
	}
	packet := c.packets[0]
	c.packets = c.packets[1:]
	c.lastBuffer = buffer
	_, err := buffer.Write(packet)
	return M.Socksaddr{}, err
}

func (c *sniffTestPacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	buffer.Release()
	return nil
}

func (c *sniffTestPacketConn) Close() error {
	c.close.Do(func() { close(c.closed) })
	return nil
}

func (c *sniffTestPacketConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (c *sniffTestPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *sniffTestPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *sniffTestPacketConn) SetWriteDeadline(time.Time) error { return nil }

func TestFragmentedQUICSniffUsesCumulativeBudgetsAndNoDuplicateCache(t *testing.T) {
	originalSniffer := fragmentedQUICSniffer
	needMore := func(context.Context, *adapter.InboundContext, []byte) error {
		return sniff.ErrNeedMoreData
	}
	fragmentedQUICSniffer = needMore
	t.Cleanup(func() { fragmentedQUICSniffer = originalSniffer })

	packetConn := newSniffTestPacketConn(maxPacketSniffPackets+10, 1, errors.New("unexpected terminal read"))
	router := &Router{logger: logger.NOP()}
	metadata := new(adapter.InboundContext)
	action := &R.RuleActionSniff{
		SnifferNames:   []string{"quic"},
		PacketSniffers: []sniff.PacketSniffer{needMore},
		Timeout:        time.Second,
	}
	budget := new(packetSniffBudget)

	_, packets, err := router.actionSniff(context.Background(), metadata, action, nil, packetConn, nil, nil, budget)
	require.NoError(t, err)
	require.Len(t, packets, maxPacketSniffPackets)
	require.Equal(t, maxPacketSniffPackets, packetConn.reads)
	require.ErrorContains(t, metadata.SniffError, "packet sniff budget exceeded")

	_, duplicatePackets, err := router.actionSniff(context.Background(), metadata, action, nil, packetConn, nil, packets, budget)
	require.NoError(t, err)
	require.Empty(t, duplicatePackets, "a later sniff action must return only newly acquired packets")
	require.Equal(t, maxPacketSniffPackets, packetConn.reads)
	N.ReleaseMultiPacketBuffer(packets)
}

type sniffActionRule struct {
	action *R.RuleActionSniff
}

func (r *sniffActionRule) Match(*adapter.InboundContext) bool { return true }
func (r *sniffActionRule) String() string                     { return "sniff-test" }
func (r *sniffActionRule) Start() error                       { return nil }
func (r *sniffActionRule) Close() error                       { return nil }
func (r *sniffActionRule) Type() string                       { return "sniff-test" }
func (r *sniffActionRule) Action() adapter.RuleAction         { return r.action }

func TestFragmentedQUICSniffReturnsOwnedPacketsOnFatalRead(t *testing.T) {
	originalSniffer := fragmentedQUICSniffer
	needMore := func(context.Context, *adapter.InboundContext, []byte) error {
		return sniff.ErrNeedMoreData
	}
	fragmentedQUICSniffer = needMore
	t.Cleanup(func() { fragmentedQUICSniffer = originalSniffer })

	readErr := errors.New("read failed")
	packetConn := newSniffTestPacketConn(1, 32, readErr)
	router := &Router{logger: logger.NOP()}
	action := &R.RuleActionSniff{
		SnifferNames:   []string{"quic"},
		PacketSniffers: []sniff.PacketSniffer{needMore},
		Timeout:        time.Second,
	}

	_, packets, err := router.actionSniff(context.Background(), new(adapter.InboundContext), action, nil, packetConn, nil, nil, new(packetSniffBudget))
	require.ErrorIs(t, err, readErr)
	require.Len(t, packets, 1, "the caller must receive every owned packet so the fatal path can release it")
	N.ReleaseMultiPacketBuffer(packets)
}

func TestRoutePacketFatalSniffErrorReleasesCachedPackets(t *testing.T) {
	originalSniffer := fragmentedQUICSniffer
	needMore := func(context.Context, *adapter.InboundContext, []byte) error {
		return sniff.ErrNeedMoreData
	}
	fragmentedQUICSniffer = needMore
	t.Cleanup(func() { fragmentedQUICSniffer = originalSniffer })

	readErr := errors.New("read failed")
	packetConn := newSniffTestPacketConn(1, 32, readErr)
	action := &R.RuleActionSniff{
		SnifferNames:   []string{"quic"},
		PacketSniffers: []sniff.PacketSniffer{needMore},
		Timeout:        time.Second,
	}
	router := &Router{
		logger: logger.NOP(),
		rules:  []adapter.Rule{&sniffActionRule{action: action}},
	}

	err := router.routePacketConnection(context.Background(), packetConn, adapter.InboundContext{Domain: "preexisting.example"}, nil)
	require.ErrorIs(t, err, readErr)
	require.NotNil(t, packetConn.lastBuffer)
	require.Zero(t, packetConn.lastBuffer.RawCap(), "fatal routing must release the cached packet exactly once")
}

func TestPacketSniffAbsoluteAndByteBudgets(t *testing.T) {
	now := time.Now()
	budget := new(packetSniffBudget)
	budget.begin(now, 24*time.Hour)
	require.Equal(t, now.Add(maxPacketSniffDuration), budget.deadline)
	require.False(t, budget.exhausted(budget.deadline.Add(-time.Nanosecond)))
	require.True(t, budget.exhausted(budget.deadline))

	budget = new(packetSniffBudget)
	budget.begin(now, time.Second)
	budget.account(maxPacketSniffBytes)
	require.True(t, budget.exhausted(now))
}
