package tf_test

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	tf "github.com/Miku0139oao/sidera-core/common/tlsfragment"

	"github.com/stretchr/testify/require"
)

func TestZeroLengthWriteReachesEarlyConnectionWithoutCompletingFragmentation(t *testing.T) {
	underlying := &scriptedConn{}
	conn := tf.NewConn(underlying, context.Background(), true, true, time.Nanosecond)
	if conn.WriterReplaceable() {
		t.Fatal("new fragment connection is unexpectedly replaceable")
	}
	n, err := conn.Write(nil)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, 1, underlying.writeCalls)
	require.Len(t, underlying.writes, 1)
	require.Empty(t, underlying.writes[0])
	if conn.WriterReplaceable() {
		t.Fatal("zero-length write consumed the first TLS packet state")
	}
}

func TestTLSFragmentFallsBackWhenNoSafeSplitExists(t *testing.T) {
	for _, serverName := range []string{"", ".", ".example.com"} {
		t.Run(serverName, func(t *testing.T) {
			payload := makeFragmentClientHello(serverName)
			underlying := &scriptedConn{}
			conn := tf.NewConn(underlying, context.Background(), true, true, time.Nanosecond)

			n, err := conn.Write(payload)
			require.NoError(t, err)
			require.Equal(t, len(payload), n)
			require.Equal(t, payload, underlying.joinedWrites())
			require.True(t, conn.WriterReplaceable())
		})
	}
}

func TestTLSFragmentRetriesShortWrites(t *testing.T) {
	payload := makeFragmentClientHello("a")
	underlying := &scriptedConn{writeLimit: 2}
	conn := tf.NewConn(underlying, context.Background(), true, true, time.Nanosecond)

	n, err := conn.Write(payload)
	require.NoError(t, err)
	require.Equal(t, len(payload), n)
	require.Greater(t, underlying.writeCalls, 2)
	require.True(t, conn.WriterReplaceable())
}

func TestTLSFragmentRejectsZeroProgress(t *testing.T) {
	payload := makeFragmentClientHello("a")
	underlying := &scriptedConn{steps: []writeStep{{n: 0}}}
	conn := tf.NewConn(underlying, context.Background(), true, true, time.Nanosecond)

	n, err := conn.Write(payload)
	require.ErrorIs(t, err, io.ErrNoProgress)
	require.Zero(t, n)
	require.False(t, conn.WriterReplaceable())
}

func TestTLSFragmentCallerByteAccounting(t *testing.T) {
	payload := makeFragmentClientHello("a")
	serverName := tf.IndexTLSServerName(payload)
	require.NotNil(t, serverName)
	sentinel := errors.New("scripted write failure")

	for _, test := range []struct {
		name                    string
		secondFragmentWireBytes int
		wantAdditionalBytes     int
	}{
		{name: "synthetic_header_only", secondFragmentWireBytes: 3},
		{name: "header_and_payload", secondFragmentWireBytes: 7, wantAdditionalBytes: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			underlying := &scriptedConn{steps: []writeStep{
				{n: -1},
				{n: test.secondFragmentWireBytes},
				{err: sentinel},
			}}
			conn := tf.NewConn(underlying, context.Background(), true, true, time.Nanosecond)

			n, err := conn.Write(payload)
			require.ErrorIs(t, err, sentinel)
			require.Equal(t, serverName.Index+test.wantAdditionalBytes, n)
			require.True(t, conn.WriterReplaceable())
		})
	}
}

func TestTLSFragmentCancellationStopsInterFragmentWait(t *testing.T) {
	payload := makeFragmentClientHello("a")
	ctx, cancel := context.WithCancel(context.Background())
	underlying := &scriptedConn{}
	underlying.onWrite = func(call int) {
		if call == 1 {
			cancel()
		}
	}
	conn := tf.NewConn(underlying, ctx, true, false, time.Hour)

	start := time.Now()
	n, err := conn.Write(payload)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), time.Second)
	serverName := tf.IndexTLSServerName(payload)
	require.NotNil(t, serverName)
	require.Equal(t, serverName.Index, n)
	require.Equal(t, 1, underlying.writeCalls)
}

func TestTLSFragmentCanceledBeforeWrite(t *testing.T) {
	payload := makeFragmentClientHello("a")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	underlying := &scriptedConn{}
	conn := tf.NewConn(underlying, ctx, true, true, time.Hour)

	n, err := conn.Write(payload)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, n)
	require.Zero(t, underlying.writeCalls)
}

func TestTLSFragmentCancellationInterruptsBlockedWrite(t *testing.T) {
	for _, serverName := range []string{"a", ""} {
		t.Run(serverName, func(t *testing.T) {
			payload := makeFragmentClientHello(serverName)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			underlying := newBlockingWriteConn()
			conn := tf.NewConn(underlying, ctx, true, false, time.Hour)

			start := time.Now()
			n, err := conn.Write(payload)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Zero(t, n)
			require.Less(t, time.Since(start), time.Second)
		})
	}
}

func TestTLSFragment(t *testing.T) {
	t.Parallel()
	tcpConn, err := net.Dial("tcp", "1.1.1.1:443")
	require.NoError(t, err)
	defer tcpConn.Close()
	tlsConn := tls.Client(tf.NewConn(tcpConn, context.Background(), true, false, 0), &tls.Config{
		ServerName: "www.cloudflare.com",
	})
	require.NoError(t, tlsConn.Handshake())
}

func TestTLSRecordFragment(t *testing.T) {
	t.Parallel()
	tcpConn, err := net.Dial("tcp", "1.1.1.1:443")
	require.NoError(t, err)
	defer tcpConn.Close()
	tlsConn := tls.Client(tf.NewConn(tcpConn, context.Background(), false, true, 0), &tls.Config{
		ServerName: "www.cloudflare.com",
	})
	require.NoError(t, tlsConn.Handshake())
}

func TestTLS2Fragment(t *testing.T) {
	t.Parallel()
	tcpConn, err := net.Dial("tcp", "1.1.1.1:443")
	require.NoError(t, err)
	defer tcpConn.Close()
	tlsConn := tls.Client(tf.NewConn(tcpConn, context.Background(), true, true, 0), &tls.Config{
		ServerName: "www.cloudflare.com",
	})
	require.NoError(t, tlsConn.Handshake())
}

type blockingWriteConn struct {
	closed    chan struct{}
	closeOnce sync.Once
}

func newBlockingWriteConn() *blockingWriteConn {
	return &blockingWriteConn{closed: make(chan struct{})}
}

func (c *blockingWriteConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, net.ErrClosed
}

func (c *blockingWriteConn) Write([]byte) (int, error) {
	<-c.closed
	return 0, net.ErrClosed
}

func (c *blockingWriteConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *blockingWriteConn) LocalAddr() net.Addr              { return testAddr("local") }
func (c *blockingWriteConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (c *blockingWriteConn) SetDeadline(time.Time) error      { return nil }
func (c *blockingWriteConn) SetReadDeadline(time.Time) error  { return nil }
func (c *blockingWriteConn) SetWriteDeadline(time.Time) error { return nil }

type writeStep struct {
	n   int
	err error
}

type scriptedConn struct {
	steps      []writeStep
	writeLimit int
	writeCalls int
	writes     [][]byte
	onWrite    func(call int)
}

func (c *scriptedConn) Read([]byte) (int, error) { return 0, io.EOF }

func (c *scriptedConn) Write(payload []byte) (int, error) {
	c.writeCalls++
	if c.onWrite != nil {
		c.onWrite(c.writeCalls)
	}
	step := writeStep{n: -1}
	if len(c.steps) > 0 {
		step = c.steps[0]
		c.steps = c.steps[1:]
	}
	n := step.n
	if n < 0 || n > len(payload) {
		n = len(payload)
	}
	if c.writeLimit > 0 && n > c.writeLimit {
		n = c.writeLimit
	}
	c.writes = append(c.writes, append([]byte(nil), payload[:n]...))
	return n, step.err
}

func (c *scriptedConn) Close() error                     { return nil }
func (c *scriptedConn) LocalAddr() net.Addr              { return testAddr("local") }
func (c *scriptedConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (c *scriptedConn) SetDeadline(time.Time) error      { return nil }
func (c *scriptedConn) SetReadDeadline(time.Time) error  { return nil }
func (c *scriptedConn) SetWriteDeadline(time.Time) error { return nil }

func (c *scriptedConn) joinedWrites() []byte {
	var payload []byte
	for _, write := range c.writes {
		payload = append(payload, write...)
	}
	return payload
}

type testAddr string

func (a testAddr) Network() string { return "test" }
func (a testAddr) String() string  { return string(a) }

func makeFragmentClientHello(serverName string) []byte {
	serverNameList := []byte{0}
	serverNameList = binary.BigEndian.AppendUint16(serverNameList, uint16(len(serverName)))
	serverNameList = append(serverNameList, serverName...)
	sniBody := binary.BigEndian.AppendUint16(nil, uint16(len(serverNameList)))
	sniBody = append(sniBody, serverNameList...)
	extension := binary.BigEndian.AppendUint16(nil, 0)
	extension = binary.BigEndian.AppendUint16(extension, uint16(len(sniBody)))
	extension = append(extension, sniBody...)
	// Keep data after the SNI so partial writes into a later synthetic record
	// can exercise caller-byte accounting beyond its five-byte header.
	extension = binary.BigEndian.AppendUint16(extension, 0x1234)
	extension = binary.BigEndian.AppendUint16(extension, 8)
	extension = append(extension, make([]byte, 8)...)

	body := []byte{0x03, 0x03}
	body = append(body, make([]byte, 32)...)
	body = append(body, 0)
	body = binary.BigEndian.AppendUint16(body, 2)
	body = append(body, 0x13, 0x01)
	body = append(body, 1, 0)
	body = binary.BigEndian.AppendUint16(body, uint16(len(extension)))
	body = append(body, extension...)

	handshake := []byte{1, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	handshake = append(handshake, body...)
	record := []byte{22, 0x03, 0x01}
	record = binary.BigEndian.AppendUint16(record, uint16(len(handshake)))
	return append(record, handshake...)
}
