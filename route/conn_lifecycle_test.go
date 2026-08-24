package route

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

type replaceableTestConn struct {
	net.Conn
}

func (c *replaceableTestConn) Upstream() any           { return c.Conn }
func (c *replaceableTestConn) ReaderReplaceable() bool { return true }
func (c *replaceableTestConn) WriterReplaceable() bool { return true }

func tcpConnPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer listener.Close()
	accepted := make(chan *net.TCPConn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptTCP()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()
	client, err := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
	require.NoError(t, err)
	select {
	case server := <-accepted:
		return client, server
	case err = <-acceptErr:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("accept timed out")
	}
	return nil, nil
}

type onePacketReader struct {
	payload []byte
	read    bool
}

func (r *onePacketReader) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	if r.read {
		return M.Socksaddr{}, io.EOF
	}
	r.read = true
	_, err := buffer.Write(r.payload)
	return M.Socksaddr{Port: 53}, err
}

type headroomPacketWriter struct {
	front   int
	payload []byte
}

func (w *headroomPacketWriter) FrontHeadroom() int { return w.front }
func (w *headroomPacketWriter) WritePacket(buffer *buf.Buffer, _ M.Socksaddr) error {
	if buffer.Start() < w.front {
		return errors.New("missing packet headroom")
	}
	w.payload = append([]byte(nil), buffer.Bytes()...)
	buffer.Release()
	return nil
}

func TestCopyPacketExactPreservesDestinationHeadroom(t *testing.T) {
	payload := []byte("packet")
	reader := &onePacketReader{payload: payload}
	writer := &headroomPacketWriter{front: 16}
	n, err := copyPacketExact(writer, reader)
	require.ErrorIs(t, err, io.EOF)
	require.EqualValues(t, len(payload), n)
	require.Equal(t, payload, writer.payload)
}

type blockingSetupDialer struct {
	entered chan struct{}
	once    sync.Once
}

func (d *blockingSetupDialer) DialContext(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
	d.once.Do(func() { close(d.entered) })
	<-ctx.Done()
	return nil, context.Cause(ctx)
}

func (d *blockingSetupDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("unexpected ListenPacket")
}

func TestConnectionManagerCloseCancelsBlockedSetup(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().NewLogger("connection-test"))
	dialer := &blockingSetupDialer{entered: make(chan struct{})}
	incoming, peer := net.Pipe()
	defer peer.Close()
	operationDone := make(chan struct{})
	go func() {
		manager.NewConnection(t.Context(), dialer, incoming, adapter.InboundContext{Destination: M.Socksaddr{Addr: netip.MustParseAddr("1.1.1.1"), Port: 443}}, nil)
		close(operationDone)
	}()
	select {
	case <-dialer.entered:
	case <-time.After(time.Second):
		t.Fatal("dial setup did not block")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel blocked setup")
	}
	select {
	case <-operationDone:
	case <-time.After(time.Second):
		t.Fatal("setup operation did not finish after cancellation")
	}
}

type staticConnDialer struct{ conn net.Conn }

func (d *staticConnDialer) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return d.conn, nil
}

func (d *staticConnDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("unexpected ListenPacket")
}

func TestConnectionManagerCloseOwnsUntrackedRelayEndpoints(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().NewLogger("connection-test"))
	client, incoming := net.Pipe()
	remote, backend := net.Pipe()
	defer client.Close()
	defer backend.Close()
	closed := make(chan error, 1)
	manager.NewConnection(t.Context(), &staticConnDialer{conn: remote}, incoming, adapter.InboundContext{Destination: M.Socksaddr{Addr: netip.MustParseAddr("1.1.1.1"), Port: 443}}, func(err error) {
		closed <- err
	})
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("relay close callback was not invoked")
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	_ = backend.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	_, clientErr := client.Read(one[:])
	_, backendErr := backend.Read(one[:])
	require.Error(t, clientErr)
	require.Error(t, backendErr)
}

var (
	_ N.Dialer = (*blockingSetupDialer)(nil)
	_ N.Dialer = (*staticConnDialer)(nil)
)

func TestConnectionManagerCloseJoinsSetupOperations(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().NewLogger("connection-test"))
	_, finishOperation, loaded := manager.beginOperation(t.Context())
	require.True(t, loaded)
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before setup completion: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	finishOperation()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close did not join setup completion")
	}
	_, _, loaded = manager.beginOperation(t.Context())
	require.False(t, loaded)
}

func TestConnectionManagerCloseJoinsActiveRelays(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().NewLogger("connection-test"))
	relay := manager.beginRelays(1)
	require.NotNil(t, relay)
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before active relay completion: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	relay.Done()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close did not join active relay completion")
	}
	require.Nil(t, manager.beginRelays(1))
}

func TestConnectionManagerRejectsLateRegistrationsAfterClose(t *testing.T) {
	manager := NewConnectionManager(log.NewNOPFactory().NewLogger("connection-test"))
	require.NoError(t, manager.Close())

	tracked, peer := net.Pipe()
	defer peer.Close()
	returned := manager.TrackConn(tracked)
	require.Same(t, tracked, returned)
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	_, err := peer.Read(one[:])
	require.Error(t, err)

	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	returnedPacket := manager.TrackPacketConn(packet)
	require.Same(t, packet, returnedPacket)
	_ = packet.SetReadDeadline(time.Now().Add(time.Millisecond))
	_, _, err = packet.ReadFrom(one[:])
	require.Error(t, err)
	require.Zero(t, manager.Count())
}

func TestRelayPreservesWrappedCloseWriteResponseAfterEOF(t *testing.T) {
	client, relayInbound := tcpConnPair(t)
	relayOutbound, backend := tcpConnPair(t)
	defer client.Close()
	defer relayInbound.Close()
	defer relayOutbound.Close()
	defer backend.Close()

	backendResult := make(chan error, 1)
	go func() {
		request, err := io.ReadAll(backend)
		if err == nil && string(request) != "request" {
			err = errors.New("unexpected request")
		}
		if err == nil {
			_, err = backend.Write([]byte("response"))
		}
		_ = backend.CloseWrite()
		backendResult <- err
	}()

	callback := make(chan error, 1)
	state := &connectionRelayState{onClose: func(err error) { callback <- err }}
	manager := NewConnectionManager(log.NewNOPFactory().NewLogger("relay-test"))
	wrappedOutbound := &replaceableTestConn{Conn: relayOutbound}
	go manager.connectionCopy(context.Background(), relayInbound, wrappedOutbound, false, state)
	go manager.connectionCopy(context.Background(), wrappedOutbound, relayInbound, true, state)

	_, err := client.Write([]byte("request"))
	require.NoError(t, err)
	require.NoError(t, client.CloseWrite())
	response, err := io.ReadAll(client)
	require.NoError(t, err)
	require.Equal(t, "response", string(response))
	require.NoError(t, <-backendResult)
	select {
	case err = <-callback:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("relay completion timed out")
	}
}

type relayErrorConn struct {
	readErr   error
	closed    chan struct{}
	closeOnce sync.Once
}

func newRelayErrorConn(readErr error) *relayErrorConn {
	return &relayErrorConn{readErr: readErr, closed: make(chan struct{})}
}

func (c *relayErrorConn) Read([]byte) (int, error) {
	if c.readErr != nil {
		return 0, c.readErr
	}
	<-c.closed
	return 0, net.ErrClosed
}

func (c *relayErrorConn) Write(payload []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
		return len(payload), nil
	}
}

func (c *relayErrorConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *relayErrorConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *relayErrorConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *relayErrorConn) SetDeadline(time.Time) error      { return nil }
func (c *relayErrorConn) SetReadDeadline(time.Time) error  { return nil }
func (c *relayErrorConn) SetWriteDeadline(time.Time) error { return nil }

func TestRelayReportsOriginatingError(t *testing.T) {
	originErr := errors.New("originating read error")
	origin := newRelayErrorConn(originErr)
	peer := newRelayErrorConn(nil)
	callback := make(chan error, 1)
	state := &connectionRelayState{onClose: func(err error) { callback <- err }}
	manager := NewConnectionManager(log.NewNOPFactory().NewLogger("relay-test"))

	go manager.connectionCopy(context.Background(), origin, peer, false, state)
	go manager.connectionCopy(context.Background(), peer, origin, true, state)

	select {
	case err := <-callback:
		require.ErrorIs(t, err, originErr)
	case <-time.After(time.Second):
		t.Fatal("relay completion timed out")
	}
	select {
	case <-callback:
		t.Fatal("relay callback was invoked more than once")
	case <-time.After(20 * time.Millisecond):
	}
}
