package route

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

func tcpTestDestination() adapter.InboundContext {
	return adapter.InboundContext{Destination: M.Socksaddr{Addr: netip.MustParseAddr("1.1.1.1"), Port: 443}}
}

func udpTestDestination() adapter.InboundContext {
	return adapter.InboundContext{Destination: M.Socksaddr{Addr: netip.MustParseAddr("1.1.1.1"), Port: 53}}
}

type ctxAwareConn struct {
	net.Conn
	ctx context.Context
}

func (c *ctxAwareConn) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}

func (c *ctxAwareConn) Write(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}

type capturingConnDialer struct {
	conn        net.Conn
	capturedCtx atomic.Value
	dialErr     error
}

func (d *capturingConnDialer) captured() context.Context {
	ctx, _ := d.capturedCtx.Load().(context.Context)
	return ctx
}

func (d *capturingConnDialer) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	d.capturedCtx.Store(ctx)
	if d.dialErr != nil {
		return nil, d.dialErr
	}
	return &ctxAwareConn{Conn: d.conn, ctx: ctx}, nil
}

func (d *capturingConnDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("unexpected ListenPacket")
}

type ctxAwarePacketConn struct {
	net.PacketConn
	ctx context.Context
}

func (c *ctxAwarePacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, nil, err
	}
	return c.PacketConn.ReadFrom(p)
}

func (c *ctxAwarePacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.PacketConn.WriteTo(p, addr)
}

func (c *ctxAwarePacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	if err := c.ctx.Err(); err != nil {
		return M.Socksaddr{}, err
	}
	if reader, ok := c.PacketConn.(N.PacketReader); ok {
		return reader.ReadPacket(buffer)
	}
	n, addr, err := c.ReadFrom(buffer.FreeBytes())
	if err != nil {
		return M.Socksaddr{}, err
	}
	buffer.Truncate(n)
	return M.SocksaddrFromNet(addr), nil
}

func (c *ctxAwarePacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	if err := c.ctx.Err(); err != nil {
		buffer.Release()
		return err
	}
	if writer, ok := c.PacketConn.(N.PacketWriter); ok {
		return writer.WritePacket(buffer, destination)
	}
	defer buffer.Release()
	_, err := c.WriteTo(buffer.Bytes(), destination.UDPAddr())
	return err
}

type packetSlot struct {
	data []byte
	addr net.Addr
}

type loopbackPacketConn struct {
	incoming    chan packetSlot
	peer        *loopbackPacketConn
	closed      chan struct{}
	closeOnce   sync.Once
	local       net.Addr
	deadlineErr error
	ctx         context.Context
}

func linkedPacketConns(deadlineErr error) (*loopbackPacketConn, *loopbackPacketConn) {
	left := newLoopbackPacketConn(1001, deadlineErr)
	right := newLoopbackPacketConn(1002, deadlineErr)
	left.peer = right
	right.peer = left
	return left, right
}

func newLoopbackPacketConn(port int, deadlineErr error) *loopbackPacketConn {
	return &loopbackPacketConn{
		incoming:    make(chan packetSlot, 8),
		closed:      make(chan struct{}),
		local:       &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port},
		deadlineErr: deadlineErr,
	}
}

func (c *loopbackPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if c.ctx != nil {
		if err := c.ctx.Err(); err != nil {
			return 0, nil, err
		}
	}
	select {
	case pkt := <-c.incoming:
		n := copy(p, pkt.data)
		return n, pkt.addr, nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	}
}

func (c *loopbackPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if c.ctx != nil {
		if err := c.ctx.Err(); err != nil {
			return 0, err
		}
	}
	pkt := packetSlot{data: append([]byte(nil), p...), addr: addr}
	select {
	case c.peer.incoming <- pkt:
		return len(p), nil
	case <-c.closed:
		return 0, net.ErrClosed
	case <-c.peer.closed:
		return 0, net.ErrClosed
	}
}

func (c *loopbackPacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	n, addr, err := c.ReadFrom(buffer.FreeBytes())
	if err != nil {
		return M.Socksaddr{}, err
	}
	buffer.Truncate(n)
	return M.SocksaddrFromNet(addr), nil
}

func (c *loopbackPacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	defer buffer.Release()
	_, err := c.WriteTo(buffer.Bytes(), destination.UDPAddr())
	return err
}

func (c *loopbackPacketConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *loopbackPacketConn) LocalAddr() net.Addr              { return c.local }
func (c *loopbackPacketConn) SetDeadline(time.Time) error      { return c.deadlineErr }
func (c *loopbackPacketConn) SetReadDeadline(time.Time) error  { return c.deadlineErr }
func (c *loopbackPacketConn) SetWriteDeadline(time.Time) error { return c.deadlineErr }

type capturingPacketDialer struct {
	conn        net.PacketConn
	capturedCtx atomic.Value
	listenErr   error
}

func (d *capturingPacketDialer) captured() context.Context {
	ctx, _ := d.capturedCtx.Load().(context.Context)
	return ctx
}

func (d *capturingPacketDialer) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return nil, errors.New("unexpected DialContext")
}

func (d *capturingPacketDialer) ListenPacket(ctx context.Context, _ M.Socksaddr) (net.PacketConn, error) {
	d.capturedCtx.Store(ctx)
	if d.listenErr != nil {
		return nil, d.listenErr
	}
	return &ctxAwarePacketConn{PacketConn: d.conn, ctx: ctx}, nil
}

var (
	_ N.Dialer       = (*capturingConnDialer)(nil)
	_ N.Dialer       = (*capturingPacketDialer)(nil)
	_ N.PacketConn   = (*loopbackPacketConn)(nil)
	_ net.PacketConn = (*loopbackPacketConn)(nil)
)

func requireCapturedLive(t *testing.T, ctx context.Context) {
	t.Helper()
	require.NotNil(t, ctx)
	require.NoError(t, ctx.Err(), "original dial/listen context was cancelled after setup returned")
}

func TestRelayContextReleasedOnlyAfterLastDirection(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().NewLogger("connection-context-test"))
	defer manager.Close()
	ctx, operation, ok := manager.beginOperation(context.Background())
	require.True(t, ok)
	relay := manager.beginRelays(2)
	require.NotNil(t, relay)
	operation.transferTo(relay)
	operation.finish()
	manager.operationGroup.Wait()
	require.NoError(t, ctx.Err())
	relay.Done()
	require.NoError(t, ctx.Err(), "one finished direction must not cancel the remaining relay")
	relay.Done()
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	manager.relayGroup.Wait()
	manager.access.Lock()
	require.Empty(t, manager.activeRelays)
	manager.access.Unlock()
}

func TestNewConnectionKeepsDialContextAliveAfterReturn(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().NewLogger("connection-context-test"))
	defer manager.Close()

	client, incoming := net.Pipe()
	remote, backend := net.Pipe()
	defer client.Close()
	defer backend.Close()

	dialer := &capturingConnDialer{conn: remote}
	manager.NewConnection(context.Background(), dialer, incoming, tcpTestDestination(), nil)
	requireCapturedLive(t, dialer.captured())

	writeErr := make(chan error, 1)
	go func() {
		_, err := client.Write([]byte("ping"))
		writeErr <- err
	}()
	_ = backend.SetReadDeadline(time.Now().Add(time.Second))
	got := make([]byte, 4)
	n, err := backend.Read(got)
	require.NoError(t, err)
	require.Equal(t, "ping", string(got[:n]))
	select {
	case err = <-writeErr:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("client write did not complete")
	}
}

func TestNewPacketConnectionKeepsListenContextAliveAfterReturn(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().NewLogger("connection-context-test"))
	defer manager.Close()

	client, inbound := linkedPacketConns(nil)
	remote, backend := linkedPacketConns(nil)
	defer client.Close()
	defer inbound.Close()
	defer remote.Close()
	defer backend.Close()

	dialer := &capturingPacketDialer{conn: remote}
	manager.NewPacketConnection(context.Background(), dialer, inbound, udpTestDestination(), nil)
	requireCapturedLive(t, dialer.captured())

	payload := []byte("pkt")
	dest := M.Socksaddr{Addr: netip.MustParseAddr("8.8.8.8"), Port: 53}
	writeErr := make(chan error, 1)
	go func() {
		writeErr <- client.WritePacket(buf.As(payload).ToOwned(), dest)
	}()
	buffer := buf.NewPacket()
	defer buffer.Release()
	readErr := make(chan error, 1)
	var gotDest M.Socksaddr
	go func() {
		var err error
		gotDest, err = backend.ReadPacket(buffer)
		readErr <- err
	}()
	select {
	case err := <-readErr:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("packet read did not complete")
	}
	require.Equal(t, payload, buffer.Bytes())
	require.Equal(t, dest, gotDest)
	select {
	case err := <-writeErr:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("packet write did not complete")
	}
}

func TestNewPacketConnectionKeepsTimeoutCancelerParentAliveAfterReturn(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().NewLogger("connection-context-test"))
	defer manager.Close()

	client, inbound := linkedPacketConns(os.ErrInvalid)
	remote, backend := linkedPacketConns(os.ErrInvalid)
	defer client.Close()
	defer inbound.Close()
	defer remote.Close()
	defer backend.Close()

	dialer := &capturingPacketDialer{conn: remote}
	metadata := udpTestDestination()
	metadata.UDPTimeout = time.Minute
	manager.NewPacketConnection(context.Background(), dialer, inbound, metadata, nil)
	requireCapturedLive(t, dialer.captured())

	payload := []byte("keep")
	dest := M.Socksaddr{Addr: netip.MustParseAddr("8.8.8.8"), Port: 53}
	writeErr := make(chan error, 1)
	go func() {
		writeErr <- client.WritePacket(buf.As(payload).ToOwned(), dest)
	}()
	buffer := buf.NewPacket()
	defer buffer.Release()
	readErr := make(chan error, 1)
	go func() {
		_, err := backend.ReadPacket(buffer)
		readErr <- err
	}()
	select {
	case err := <-readErr:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("timeout-canceler packet path became unusable after setup return")
	}
	require.Equal(t, payload, buffer.Bytes())
	select {
	case err := <-writeErr:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("timeout-canceler packet write did not complete")
	}
}

func TestNewConnectionCancelsDialContextOnFailedSetup(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().NewLogger("connection-context-test"))
	defer manager.Close()

	incoming, peer := net.Pipe()
	defer peer.Close()
	dialer := &capturingConnDialer{dialErr: errors.New("dial failed")}
	manager.NewConnection(context.Background(), dialer, incoming, tcpTestDestination(), nil)
	ctx := dialer.captured()
	require.NotNil(t, ctx)
	require.Error(t, ctx.Err(), "failed setup left dial context live")
}

func TestNewPacketConnectionCancelsListenContextOnFailedSetup(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().NewLogger("connection-context-test"))
	defer manager.Close()

	inbound, peer := linkedPacketConns(nil)
	defer inbound.Close()
	defer peer.Close()
	dialer := &capturingPacketDialer{listenErr: errors.New("listen failed")}
	manager.NewPacketConnection(context.Background(), dialer, inbound, udpTestDestination(), nil)
	ctx := dialer.captured()
	require.NotNil(t, ctx)
	require.Error(t, ctx.Err(), "failed setup left listen context live")
}

func TestNewConnectionParentCancelCancelsDialContext(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().NewLogger("connection-context-test"))
	defer manager.Close()

	client, incoming := net.Pipe()
	remote, backend := net.Pipe()
	defer client.Close()
	defer backend.Close()

	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	dialer := &capturingConnDialer{conn: remote}
	manager.NewConnection(parent, dialer, incoming, tcpTestDestination(), nil)
	requireCapturedLive(t, dialer.captured())
	cancel()
	require.Error(t, dialer.captured().Err())
}

func TestNewConnectionManagerCloseCancelsEstablishedDialContext(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().NewLogger("connection-context-test"))
	client, incoming := net.Pipe()
	remote, backend := net.Pipe()
	defer client.Close()
	defer backend.Close()

	dialer := &capturingConnDialer{conn: remote}
	closed := make(chan error, 1)
	manager.NewConnection(context.Background(), dialer, incoming, tcpTestDestination(), func(err error) {
		closed <- err
	})
	requireCapturedLive(t, dialer.captured())
	require.NoError(t, manager.Close())
	require.Error(t, dialer.captured().Err())
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("relay close callback was not invoked")
	}
}

type closeAfterDialDialer struct {
	manager     *ConnectionManager
	conn        net.Conn
	capturedCtx atomic.Value
	closed      chan struct{}
}

func (d *closeAfterDialDialer) captured() context.Context {
	ctx, _ := d.capturedCtx.Load().(context.Context)
	return ctx
}

func (d *closeAfterDialDialer) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	d.capturedCtx.Store(ctx)
	go func() {
		_ = d.manager.Close()
		close(d.closed)
	}()
	// Close joins this setup operation, so waiting for Close here deadlocks the
	// fixture itself. Wait for cancellation, then let setup return and be joined.
	<-ctx.Done()
	return &ctxAwareConn{Conn: d.conn, ctx: ctx}, nil
}

func (d *closeAfterDialDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("unexpected ListenPacket")
}

func TestNewConnectionCloseDuringDialCancelsAndRejectsPublication(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().NewLogger("connection-context-test"))
	incoming, peer := net.Pipe()
	remote, backend := net.Pipe()
	defer peer.Close()
	defer backend.Close()

	dialer := &closeAfterDialDialer{
		manager: manager,
		conn:    remote,
		closed:  make(chan struct{}),
	}
	closed := make(chan error, 1)
	manager.NewConnection(context.Background(), dialer, incoming, tcpTestDestination(), func(err error) {
		closed <- err
	})
	ctx := dialer.captured()
	require.NotNil(t, ctx)
	require.Error(t, ctx.Err())
	select {
	case err := <-closed:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("failed publication did not invoke onClose")
	}
	require.Nil(t, manager.beginRelays(1))
	select {
	case <-dialer.closed:
	case <-time.After(time.Second):
		t.Fatal("manager Close did not join failed publication")
	}
}

type failingHandshakeConn struct{ net.Conn }

func (*failingHandshakeConn) HandshakeSuccess() error {
	return errors.New("injected handshake failure")
}

func TestNewConnectionFailedHandshakeCancelsDialContext(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().NewLogger("connection-context-test"))
	defer manager.Close()

	incoming, peer := net.Pipe()
	defer peer.Close()
	remote, backend := net.Pipe()
	defer backend.Close()

	dialer := &capturingConnDialer{conn: remote}
	manager.NewConnection(context.Background(), dialer, &failingHandshakeConn{incoming}, tcpTestDestination(), nil)
	ctx := dialer.captured()
	require.NotNil(t, ctx)
	require.Error(t, ctx.Err(), "handshake failure left dial context live")
}
