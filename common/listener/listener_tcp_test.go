package listener

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/logger"
	N "github.com/sagernet/sing/common/network"

	"github.com/pires/go-proxyproto"
	"github.com/stretchr/testify/require"
)

type testConnectionHandler func(context.Context, net.Conn, adapter.InboundContext, N.CloseHandlerFunc)

func (h testConnectionHandler) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	h(ctx, conn, metadata, onClose)
}

func proxyProtocolTestListenOptions() option.ListenOptions {
	return option.ListenOptions{
		Listen:        common.Ptr(badoption.Addr(netip.MustParseAddr("127.0.0.1"))),
		ProxyProtocol: true,
		ProxyProtocolTrustedUpstream: &[]badoption.Prefix{
			badoption.Prefix(netip.MustParsePrefix("127.0.0.0/8")),
		},
	}
}

func TestProxyProtocolListenerDoesNotBlockAcceptLoop(t *testing.T) {
	metadataChannel := make(chan adapter.InboundContext, 1)
	listener := New(Options{
		Context: context.Background(),
		Logger:  logger.NOP(),
		Network: []string{N.NetworkTCP},
		Listen:  proxyProtocolTestListenOptions(),
		ConnectionHandler: testConnectionHandler(func(_ context.Context, conn net.Conn, metadata adapter.InboundContext, _ N.CloseHandlerFunc) {
			conn.Close()
			metadataChannel <- metadata
		}),
	})
	require.NoError(t, listener.Start())
	t.Cleanup(func() { require.NoError(t, listener.Close()) })

	upstreamAddress := listener.TCPListener().Addr().String()
	stalledConn, err := net.Dial("tcp", upstreamAddress)
	require.NoError(t, err)
	t.Cleanup(func() { stalledConn.Close() })

	proxiedConn, err := net.Dial("tcp", upstreamAddress)
	require.NoError(t, err)
	t.Cleanup(func() { proxiedConn.Close() })
	source := &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 12345}
	destination := &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 443}
	_, err = proxyproto.HeaderProxyFromAddrs(2, source, destination).WriteTo(proxiedConn)
	require.NoError(t, err)

	select {
	case metadata := <-metadataChannel:
		require.Equal(t, "203.0.113.7", metadata.Source.Addr.String())
		require.Equal(t, uint16(12345), metadata.Source.Port)
		require.Equal(t, "192.0.2.10", metadata.OriginDestination.Addr.String())
		require.Equal(t, uint16(443), metadata.OriginDestination.Port)
	case <-time.After(time.Second):
		t.Fatal("a stalled PROXY header blocked a later connection")
	}
}

func TestProxyProtocolStrictRejectsInvalidHeader(t *testing.T) {
	handled := make(chan struct{}, 1)
	listener := New(Options{
		Context: context.Background(),
		Logger:  logger.NOP(),
		Network: []string{N.NetworkTCP},
		Listen:  proxyProtocolTestListenOptions(),
		ConnectionHandler: testConnectionHandler(func(_ context.Context, conn net.Conn, _ adapter.InboundContext, _ N.CloseHandlerFunc) {
			conn.Close()
			handled <- struct{}{}
		}),
	})
	require.NoError(t, listener.Start())
	t.Cleanup(func() { require.NoError(t, listener.Close()) })

	for _, payload := range []string{"hello", "PROXY broken\r\n"} {
		conn, err := net.Dial("tcp", listener.TCPListener().Addr().String())
		require.NoError(t, err)
		_, err = conn.Write([]byte(payload))
		require.NoError(t, err)
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
		_, err = conn.Read(make([]byte, 1))
		require.Error(t, err)
		require.NoError(t, conn.Close())
	}

	select {
	case <-handled:
		t.Fatal("connection without a valid required PROXY header reached the handler")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestProxyProtocolAcceptsV1AndV2(t *testing.T) {
	testCases := []struct {
		name        string
		writeHeader func(net.Conn) error
	}{
		{
			name: "v1",
			writeHeader: func(conn net.Conn) error {
				_, err := fmt.Fprint(conn, "PROXY TCP4 203.0.113.7 192.0.2.10 12345 443\r\n")
				return err
			},
		},
		{
			name: "v2",
			writeHeader: func(conn net.Conn) error {
				_, err := proxyproto.HeaderProxyFromAddrs(2,
					&net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 12345},
					&net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 443},
				).WriteTo(conn)
				return err
			},
		},
		{
			name: "v2 with large TLV",
			writeHeader: func(conn net.Conn) error {
				header := proxyproto.HeaderProxyFromAddrs(2,
					&net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 12345},
					&net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 443},
				)
				if err := header.SetTLVs([]proxyproto.TLV{{Type: 0xe0, Value: bytes.Repeat([]byte{1}, 65520)}}); err != nil {
					return err
				}
				_, err := header.WriteTo(conn)
				return err
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			metadataChannel := make(chan adapter.InboundContext, 1)
			listener := New(Options{
				Context: context.Background(),
				Logger:  logger.NOP(),
				Network: []string{N.NetworkTCP},
				Listen:  proxyProtocolTestListenOptions(),
				ConnectionHandler: testConnectionHandler(func(_ context.Context, conn net.Conn, metadata adapter.InboundContext, _ N.CloseHandlerFunc) {
					conn.Close()
					metadataChannel <- metadata
				}),
			})
			require.NoError(t, listener.Start())
			t.Cleanup(func() { require.NoError(t, listener.Close()) })

			conn, err := net.Dial("tcp", listener.TCPListener().Addr().String())
			require.NoError(t, err)
			t.Cleanup(func() { conn.Close() })
			require.NoError(t, testCase.writeHeader(conn))

			select {
			case metadata := <-metadataChannel:
				require.Equal(t, "203.0.113.7:12345", metadata.Source.String())
				require.Equal(t, "192.0.2.10:443", metadata.OriginDestination.String())
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for proxied connection")
			}
		})
	}
}

func TestProxyProtocolDirectListener(t *testing.T) {
	type acceptedConnection struct {
		source  string
		payload string
		err     error
	}
	accepted := make(chan acceptedConnection, 1)
	listener := New(Options{
		Context: context.Background(),
		Logger:  logger.NOP(),
		Listen:  proxyProtocolTestListenOptions(),
	})
	tcpListener, err := listener.ListenTCP()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	go func() {
		conn, err := tcpListener.Accept()
		if err != nil {
			accepted <- acceptedConnection{err: err}
			return
		}
		defer conn.Close()
		payload := make([]byte, len("hello"))
		_, err = io.ReadFull(conn, payload)
		accepted <- acceptedConnection{source: conn.RemoteAddr().String(), payload: string(payload), err: err}
	}()

	conn, err := net.Dial("tcp", tcpListener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	_, err = proxyproto.HeaderProxyFromAddrs(2,
		&net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 12345},
		&net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 443},
	).WriteTo(conn)
	require.NoError(t, err)
	_, err = conn.Write([]byte("hello"))
	require.NoError(t, err)

	select {
	case result := <-accepted:
		require.NoError(t, result.err)
		require.Equal(t, "203.0.113.7:12345", result.source)
		require.Equal(t, "hello", result.payload)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for direct listener connection")
	}
}

func TestProxyProtocolCloseInterruptsHeaderRead(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { clientConn.Close() })
	conn := &proxyProtocolConn{Conn: serverConn}
	readDone := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		readDone <- err
	}()

	require.NoError(t, conn.Close())
	select {
	case err := <-readDone:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("closing the connection did not interrupt PROXY header parsing")
	}
}

func TestProxyProtocolOptionalPreservesRawConnection(t *testing.T) {
	type acceptedConnection struct {
		metadata adapter.InboundContext
		payload  string
		err      error
	}
	accepted := make(chan acceptedConnection, 1)
	listenOptions := proxyProtocolTestListenOptions()
	listenOptions.ProxyProtocolAcceptNoHeader = true
	listener := New(Options{
		Context: context.Background(),
		Logger:  logger.NOP(),
		Network: []string{N.NetworkTCP},
		Listen:  listenOptions,
		ConnectionHandler: testConnectionHandler(func(_ context.Context, conn net.Conn, metadata adapter.InboundContext, _ N.CloseHandlerFunc) {
			defer conn.Close()
			payload := make([]byte, len("hello"))
			_, err := io.ReadFull(conn, payload)
			accepted <- acceptedConnection{metadata: metadata, payload: string(payload), err: err}
		}),
	})
	require.NoError(t, listener.Start())
	t.Cleanup(func() { require.NoError(t, listener.Close()) })

	conn, err := net.Dial("tcp", listener.TCPListener().Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	_, err = conn.Write([]byte("hello"))
	require.NoError(t, err)

	select {
	case result := <-accepted:
		require.NoError(t, result.err)
		require.Equal(t, "hello", result.payload)
		require.True(t, result.metadata.Source.Addr.IsLoopback())
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for raw connection")
	}
}

func TestProxyProtocolRejectsUntrustedUpstream(t *testing.T) {
	handled := make(chan struct{}, 1)
	listenOptions := proxyProtocolTestListenOptions()
	listenOptions.ProxyProtocolTrustedUpstream = &[]badoption.Prefix{
		badoption.Prefix(netip.MustParsePrefix("192.0.2.0/24")),
	}
	listener := New(Options{
		Context: context.Background(),
		Logger:  logger.NOP(),
		Network: []string{N.NetworkTCP},
		Listen:  listenOptions,
		ConnectionHandler: testConnectionHandler(func(_ context.Context, conn net.Conn, _ adapter.InboundContext, _ N.CloseHandlerFunc) {
			conn.Close()
			handled <- struct{}{}
		}),
	})
	require.NoError(t, listener.Start())
	t.Cleanup(func() { require.NoError(t, listener.Close()) })

	conn, err := net.Dial("tcp", listener.TCPListener().Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	_, _ = proxyproto.HeaderProxyFromAddrs(2,
		&net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 12345},
		&net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 443},
	).WriteTo(conn)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	_, err = conn.Read(make([]byte, 1))
	require.Error(t, err)

	select {
	case <-handled:
		t.Fatal("untrusted upstream reached the handler")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestProxyProtocolAcceptNoHeaderRequiresProxyProtocol(t *testing.T) {
	listener := New(Options{
		Context: context.Background(),
		Logger:  logger.NOP(),
		Listen: option.ListenOptions{
			Listen:                      common.Ptr(badoption.Addr(netip.MustParseAddr("127.0.0.1"))),
			ProxyProtocolAcceptNoHeader: true,
		},
	})
	_, err := listener.ListenTCP()
	require.ErrorContains(t, err, "requires proxy_protocol")
}

func TestProxyProtocolRequiresTrustedUpstream(t *testing.T) {
	listener := New(Options{
		Context: context.Background(),
		Logger:  logger.NOP(),
		Listen: option.ListenOptions{
			Listen:        common.Ptr(badoption.Addr(netip.MustParseAddr("127.0.0.1"))),
			ProxyProtocol: true,
		},
	})
	_, err := listener.ListenTCP()
	require.ErrorContains(t, err, "requires proxy_protocol_trusted_upstream")
}

func TestProxyProtocolRequiresTCPListener(t *testing.T) {
	listener := New(Options{
		Context: context.Background(),
		Logger:  logger.NOP(),
		Network: []string{N.NetworkUDP},
		Listen:  proxyProtocolTestListenOptions(),
	})
	require.ErrorContains(t, listener.Start(), "requires a TCP listener")
}
