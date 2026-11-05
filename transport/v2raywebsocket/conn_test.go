package v2raywebsocket

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/ws"
)

type websocketTestAddr string

func (a websocketTestAddr) Network() string { return string(a) }
func (a websocketTestAddr) String() string  { return string(a) }

type websocketMemoryConn struct {
	readAccess  sync.Mutex
	writeAccess sync.Mutex
	reader      *bytes.Reader
	wire        bytes.Buffer
	maxWrite    int
	closed      atomic.Bool
}

func newWebsocketMemoryConn(input []byte) *websocketMemoryConn {
	return &websocketMemoryConn{reader: bytes.NewReader(input)}
}

func (c *websocketMemoryConn) Read(p []byte) (int, error) {
	c.readAccess.Lock()
	defer c.readAccess.Unlock()
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	return c.reader.Read(p)
}

func (c *websocketMemoryConn) Write(p []byte) (int, error) {
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	if c.maxWrite > 0 && len(p) > c.maxWrite {
		p = p[:c.maxWrite]
	}
	return c.wire.Write(p)
}

func (c *websocketMemoryConn) Close() error {
	c.closed.Store(true)
	return nil
}

func (c *websocketMemoryConn) LocalAddr() net.Addr { return websocketTestAddr("local") }

func (c *websocketMemoryConn) RemoteAddr() net.Addr             { return websocketTestAddr("remote") }
func (c *websocketMemoryConn) SetDeadline(time.Time) error      { return nil }
func (c *websocketMemoryConn) SetReadDeadline(time.Time) error  { return nil }
func (c *websocketMemoryConn) SetWriteDeadline(time.Time) error { return nil }
func (c *websocketMemoryConn) Bytes() []byte {
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()
	return bytes.Clone(c.wire.Bytes())
}

type websocketQueueDialer struct {
	access sync.Mutex
	conns  []net.Conn
	err    error
	dials  atomic.Int32
}

func (d *websocketQueueDialer) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	d.dials.Add(1)
	d.access.Lock()
	defer d.access.Unlock()
	if d.err != nil {
		return nil, d.err
	}
	if len(d.conns) == 0 {
		return nil, io.ErrClosedPipe
	}
	conn := d.conns[0]
	d.conns = d.conns[1:]
	return conn, nil
}

func (d *websocketQueueDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, io.ErrClosedPipe
}

func serveWebsocketHandshake(conn net.Conn, payload []byte, protocol string) error {
	defer conn.Close()
	request, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return err
	}
	request.Body.Close()
	if protocol != "" && request.Header.Get("Sec-WebSocket-Protocol") != protocol {
		return fmt.Errorf("protocol header = %q, want %q", request.Header.Get("Sec-WebSocket-Protocol"), protocol)
	}
	acceptInput := request.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	acceptHash := sha1.Sum([]byte(acceptInput))
	response := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(acceptHash[:]) + "\r\n"
	if protocol != "" {
		response += "Sec-WebSocket-Protocol: " + protocol + "\r\n"
	}
	response += "\r\n"
	wire := []byte(response)
	if payload != nil {
		wire = append(wire, 0x82, byte(len(payload)))
		wire = append(wire, payload...)
	}
	if _, err = conn.Write(wire); err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, conn)
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func TestCoalescedHandshakePayloadAndConcurrentHeaders(t *testing.T) {
	const connectionCount = 8
	payload := []byte("coalesced-websocket-payload")
	dialer := &websocketQueueDialer{}
	serverErrors := make(chan error, connectionCount)
	for range connectionCount {
		clientSide, serverSide := net.Pipe()
		dialer.conns = append(dialer.conns, clientSide)
		go func() { serverErrors <- serveWebsocketHandshake(serverSide, payload, "sidera-test") }()
	}
	headers := make(http.Header)
	headers.Set("Sec-WebSocket-Protocol", "sidera-test")
	client := &Client{
		dialer:     dialer,
		requestURL: url.URL{Scheme: "ws", Host: "example.test", Path: "/tunnel"},
		headers:    headers,
	}

	start := make(chan struct{})
	var group sync.WaitGroup
	for range connectionCount {
		group.Go(func() {
			<-start
			conn, err := client.DialContext(context.Background())
			if err != nil {
				t.Errorf("DialContext: %v", err)
				return
			}
			actual := make([]byte, len(payload))
			if _, err = io.ReadFull(conn, actual); err != nil {
				t.Errorf("ReadFull: %v", err)
			} else if !bytes.Equal(actual, payload) {
				t.Errorf("payload = %q, want %q", actual, payload)
			}
			if err = conn.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
	}
	close(start)
	group.Wait()
	for range connectionCount {
		if err := <-serverErrors; err != nil {
			t.Fatal(err)
		}
	}
	if protocol := client.headers.Get("Sec-WebSocket-Protocol"); protocol != "sidera-test" {
		t.Fatalf("shared protocol header mutated to %q", protocol)
	}
}

func newOwnedWebsocketBuffer(payload []byte) *buf.Buffer {
	buffer := buf.NewSize(ws.MaxHeaderSize + len(payload))
	buffer.Resize(ws.MaxHeaderSize, len(payload))
	copy(buffer.Bytes(), payload)
	return buffer
}

func TestClientFramesUseInjectedCryptographicMasksAndShortWrites(t *testing.T) {
	rawConn := newWebsocketMemoryConn(nil)
	rawConn.maxWrite = 1
	conn := NewConn(rawConn, nil, ws.StateClientSide)
	conn.Writer.maskSource = bytes.NewReader([]byte{
		1, 2, 3, 4,
		5, 6, 7, 8,
		9, 10, 11, 12,
	})
	if _, err := conn.Write([]byte("one")); err != nil {
		t.Fatal(err)
	}
	buffer := newOwnedWebsocketBuffer([]byte("two"))
	if err := conn.WriteBuffer(buffer); err != nil {
		t.Fatal(err)
	}
	if buffer.Cap() != 0 {
		t.Fatal("WriteBuffer did not release its owned buffer")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	reader := bytes.NewReader(rawConn.Bytes())
	for index, expectedMask := range [][4]byte{{1, 2, 3, 4}, {5, 6, 7, 8}, {9, 10, 11, 12}} {
		frame, err := ws.ReadFrame(reader)
		if err != nil {
			t.Fatalf("ReadFrame(%d): %v", index, err)
		}
		if !frame.Header.Masked {
			t.Fatalf("frame %d is not masked", index)
		}
		if frame.Header.Mask != expectedMask {
			t.Fatalf("frame %d mask = %v, want %v", index, frame.Header.Mask, expectedMask)
		}
	}
	if reader.Len() != 0 {
		t.Fatalf("%d trailing frame bytes", reader.Len())
	}
}

var errMaskEntropy = errors.New("mask entropy unavailable")

type failingMaskReader struct{}

func (failingMaskReader) Read([]byte) (int, error) { return 0, errMaskEntropy }

func TestClientMaskEntropyFailureWritesNothingAndReleasesBuffers(t *testing.T) {
	for _, testCase := range []struct {
		name string
		run  func(*WebsocketConn) error
	}{
		{name: "Write", run: func(conn *WebsocketConn) error {
			_, err := conn.Write([]byte("payload"))
			return err
		}},
		{name: "WriteBuffer", run: func(conn *WebsocketConn) error {
			buffer := newOwnedWebsocketBuffer([]byte("payload"))
			err := conn.WriteBuffer(buffer)
			if buffer.Cap() != 0 {
				t.Error("owned buffer was not released")
			}
			return err
		}},
		{name: "Close", run: func(conn *WebsocketConn) error { return conn.Close() }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			rawConn := newWebsocketMemoryConn(nil)
			conn := NewConn(rawConn, nil, ws.StateClientSide)
			conn.Writer.maskSource = failingMaskReader{}
			err := testCase.run(conn)
			if !errors.Is(err, errMaskEntropy) {
				t.Fatalf("error = %v, want entropy error", err)
			}
			if wire := rawConn.Bytes(); len(wire) != 0 {
				t.Fatalf("wrote %d bytes after entropy failure", len(wire))
			}
		})
	}
}

func TestWebsocketHeaderValidationEnabled(t *testing.T) {
	payload := []byte{'x'}
	mask := [4]byte{1, 2, 3, 4}
	ws.Cipher(payload, mask, 0)
	wire := append([]byte{0x82, 0x81, 1, 2, 3, 4}, payload...)
	conn := NewConn(newWebsocketMemoryConn(wire), nil, ws.StateClientSide)
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("accepted a masked server frame")
	}
}

func TestEarlyWebsocketCloseAndLateSetup(t *testing.T) {
	dialer := &websocketQueueDialer{}
	client := &Client{dialer: dialer, maxEarlyData: 1}
	conn := newEarlyWebsocketConn(client, context.Background())
	readResult := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		readResult <- err
	}()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-readResult:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Read error = %v, want net.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Read remained blocked after Close")
	}
	if _, err := conn.Write([]byte("x")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Write error = %v, want net.ErrClosed", err)
	}
	if dialer.dials.Load() != 0 {
		t.Fatalf("closed lazy connection performed %d dials", dialer.dials.Load())
	}

	lateRawConn := newWebsocketMemoryConn(nil)
	lateConn := NewConn(lateRawConn, nil, ws.StateClientSide)
	if err := conn.finishSetup(lateConn, nil); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("late setup error = %v, want net.ErrClosed", err)
	}
	if !lateRawConn.closed.Load() {
		t.Fatal("late WebSocket result was not closed")
	}
}

func TestEarlyWebsocketWriteBufferFailureReleasesOwnership(t *testing.T) {
	dialer := &websocketQueueDialer{err: io.ErrClosedPipe}
	client := &Client{
		dialer:       dialer,
		requestURL:   url.URL{Scheme: "ws", Host: "example.test", Path: "/"},
		headers:      make(http.Header),
		maxEarlyData: 32,
	}
	conn := newEarlyWebsocketConn(client, context.Background())
	buffer := newOwnedWebsocketBuffer([]byte("payload"))
	if err := conn.WriteBuffer(buffer); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("WriteBuffer error = %v, want io.ErrClosedPipe", err)
	}
	if buffer.Cap() != 0 {
		t.Fatal("failed first WriteBuffer did not release ownership")
	}
}

func TestWebsocketConcurrentReadWriteClose(t *testing.T) {
	var input bytes.Buffer
	for range 128 {
		input.Write([]byte{0x82, 0x01, 'x'})
	}
	rawConn := newWebsocketMemoryConn(input.Bytes())
	conn := NewConn(rawConn, nil, ws.StateServerSide)
	start := make(chan struct{})
	var group sync.WaitGroup
	for range 4 {
		group.Add(2)
		go func() {
			defer group.Done()
			<-start
			for range 32 {
				_, _ = conn.Read(make([]byte, 1))
			}
		}()
		go func() {
			defer group.Done()
			<-start
			for range 32 {
				_, _ = conn.Write([]byte("x"))
			}
		}()
	}
	close(start)
	_ = conn.Close()
	group.Wait()
}
