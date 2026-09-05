package sniff_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/common/sniff"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/sagernet/sing/common/buf"

	"github.com/stretchr/testify/require"
)

func be16(n int) []byte {
	return []byte{byte(n >> 8), byte(n)}
}

func u24(n int) []byte {
	return []byte{byte(n >> 16), byte(n >> 8), byte(n)}
}

// tlsClientHelloRecord builds a deterministic ClientHello TLS record.
// serverName == "" omits the SNI extension. supportedVersions, when non-empty,
// is encoded as the supported_versions extension (TLS 1.3 style).
func tlsClientHelloRecord(serverName string, supportedVersions []uint16) []byte {
	var body bytes.Buffer
	body.Write([]byte{0x03, 0x03})
	body.Write(make([]byte, 32))
	body.WriteByte(0)
	suites := []byte{0x13, 0x01, 0x00, 0x2f}
	body.Write(be16(len(suites)))
	body.Write(suites)
	body.Write([]byte{0x01, 0x00})

	var ext bytes.Buffer
	if serverName != "" {
		host := []byte(serverName)
		nameEntry := append([]byte{0x00}, append(be16(len(host)), host...)...)
		list := append(be16(len(nameEntry)), nameEntry...)
		ext.Write([]byte{0x00, 0x00})
		ext.Write(be16(len(list)))
		ext.Write(list)
	}
	if len(supportedVersions) > 0 {
		var list bytes.Buffer
		for _, version := range supportedVersions {
			list.Write(be16(int(version)))
		}
		data := append([]byte{byte(list.Len())}, list.Bytes()...)
		ext.Write([]byte{0x00, 0x2b})
		ext.Write(be16(len(data)))
		ext.Write(data)
	}
	if ext.Len() > 0 {
		body.Write(be16(ext.Len()))
		body.Write(ext.Bytes())
	}

	hs := append([]byte{0x01}, append(u24(body.Len()), body.Bytes()...)...)
	rec := append([]byte{0x16, 0x03, 0x01}, be16(len(hs))...)
	return append(rec, hs...)
}

type chunkConn struct {
	chunks [][]byte
}

func (c *chunkConn) Read(p []byte) (int, error) {
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[0])
	c.chunks[0] = c.chunks[0][n:]
	if len(c.chunks[0]) == 0 {
		c.chunks = c.chunks[1:]
	}
	return n, nil
}

func (c *chunkConn) Write([]byte) (int, error)        { return 0, io.ErrClosedPipe }
func (c *chunkConn) Close() error                     { return nil }
func (c *chunkConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *chunkConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *chunkConn) SetDeadline(time.Time) error      { return nil }
func (c *chunkConn) SetReadDeadline(time.Time) error  { return nil }
func (c *chunkConn) SetWriteDeadline(time.Time) error { return nil }

func TestTLSClientHelloSNI(t *testing.T) {
	t.Parallel()
	hello := tlsClientHelloRecord("www.example.com", []uint16{tls.VersionTLS13, tls.VersionTLS12})
	var metadata adapter.InboundContext
	err := sniff.TLSClientHello(context.Background(), &metadata, bytes.NewReader(hello))
	require.NoError(t, err)
	require.Equal(t, C.ProtocolTLS, metadata.Protocol)
	require.Equal(t, "www.example.com", metadata.Domain)
}

func TestTLSClientHelloNoSNI(t *testing.T) {
	t.Parallel()
	hello := tlsClientHelloRecord("", []uint16{tls.VersionTLS13})
	var metadata adapter.InboundContext
	err := sniff.TLSClientHello(context.Background(), &metadata, bytes.NewReader(hello))
	require.NoError(t, err)
	require.Equal(t, C.ProtocolTLS, metadata.Protocol)
	require.Empty(t, metadata.Domain)
}

func TestTLSClientHelloSupportedVersions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		versions []uint16
	}{
		{name: "tls12-legacy", versions: nil},
		{name: "tls12-extension", versions: []uint16{tls.VersionTLS12}},
		{name: "tls13", versions: []uint16{tls.VersionTLS13}},
		{name: "tls13-tls12", versions: []uint16{tls.VersionTLS13, tls.VersionTLS12}},
		{name: "tls10", versions: []uint16{tls.VersionTLS10}},
		{name: "tls11", versions: []uint16{tls.VersionTLS11}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hello := tlsClientHelloRecord("sni."+tc.name+".example", tc.versions)
			var metadata adapter.InboundContext
			err := sniff.TLSClientHello(context.Background(), &metadata, bytes.NewReader(hello))
			require.NoError(t, err)
			require.Equal(t, C.ProtocolTLS, metadata.Protocol)
			require.Equal(t, "sni."+tc.name+".example", metadata.Domain)
		})
	}
}

func TestTLSClientHelloTruncatedNeedsMoreData(t *testing.T) {
	t.Parallel()
	hello := tlsClientHelloRecord("www.example.com", []uint16{tls.VersionTLS13})
	for _, n := range []int{1, 5, 10, len(hello) / 2, len(hello) - 1} {
		var metadata adapter.InboundContext
		err := sniff.TLSClientHello(context.Background(), &metadata, bytes.NewReader(hello[:n]))
		require.ErrorIs(t, err, sniff.ErrNeedMoreData, "prefix length %d", n)
		require.Empty(t, metadata.Protocol)
		require.Empty(t, metadata.Domain)
	}
}

func TestTLSClientHelloMalformed(t *testing.T) {
	t.Parallel()
	cases := [][]byte{
		[]byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"),
		[]byte{0x16, 0x03, 0x01, 0x00, 0x04, 0x02, 0x00, 0x00, 0x00}, // server hello type
		bytes.Repeat([]byte{0x00}, 32),
		{0x17, 0x03, 0x03, 0x00, 0x01, 0x00}, // application data
	}
	for i, pkt := range cases {
		var metadata adapter.InboundContext
		err := sniff.TLSClientHello(context.Background(), &metadata, bytes.NewReader(pkt))
		require.Error(t, err, "case %d", i)
		require.False(t, errors.Is(err, sniff.ErrNeedMoreData), "case %d: %v", i, err)
		require.Empty(t, metadata.Protocol)
		require.Empty(t, metadata.Domain)
	}
}

func TestTLSClientHelloCanceledContextStillCapturesCompleteHello(t *testing.T) {
	t.Parallel()
	hello := tlsClientHelloRecord("cancel.example.com", []uint16{tls.VersionTLS13})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var metadata adapter.InboundContext
	err := sniff.TLSClientHello(ctx, &metadata, bytes.NewReader(hello))
	require.NoError(t, err)
	require.Equal(t, C.ProtocolTLS, metadata.Protocol)
	require.Equal(t, "cancel.example.com", metadata.Domain)
}

func TestTLSClientHelloPeekStreamFragmentedOwnership(t *testing.T) {
	t.Parallel()
	hello := tlsClientHelloRecord("peek.example.com", []uint16{tls.VersionTLS13, tls.VersionTLS12})
	split := 8
	conn := &chunkConn{chunks: [][]byte{append([]byte(nil), hello[:split]...), append([]byte(nil), hello[split:]...)}}
	sniffBuffer := buf.NewPacket()
	t.Cleanup(sniffBuffer.Release)

	var metadata adapter.InboundContext
	err := sniff.PeekStream(
		context.Background(),
		&metadata,
		conn,
		nil,
		sniffBuffer,
		time.Second,
		sniff.TLSClientHello,
	)
	require.NoError(t, err)
	require.Equal(t, C.ProtocolTLS, metadata.Protocol)
	require.Equal(t, "peek.example.com", metadata.Domain)
	require.Equal(t, hello, sniffBuffer.Bytes(), "PeekStream must retain the assembled payload in the caller buffer")

	var replay adapter.InboundContext
	err = sniff.TLSClientHello(context.Background(), &replay, bytes.NewReader(sniffBuffer.Bytes()))
	require.NoError(t, err)
	require.Equal(t, C.ProtocolTLS, replay.Protocol)
	require.Equal(t, "peek.example.com", replay.Domain)
	require.Equal(t, hello, sniffBuffer.Bytes(), "replay must not consume or mutate the owned buffer")
}

func TestTLSClientHelloPeekStreamPrefixBufferOwnership(t *testing.T) {
	t.Parallel()
	hello := tlsClientHelloRecord("prior.example.com", []uint16{tls.VersionTLS12})
	split := 20
	prior := buf.NewSize(len(hello[:split]))
	t.Cleanup(prior.Release)
	_, err := prior.Write(hello[:split])
	require.NoError(t, err)
	priorCopy := append([]byte(nil), prior.Bytes()...)

	conn := &chunkConn{chunks: [][]byte{append([]byte(nil), hello[split:]...)}}
	sniffBuffer := buf.NewPacket()
	t.Cleanup(sniffBuffer.Release)

	var metadata adapter.InboundContext
	err = sniff.PeekStream(
		context.Background(),
		&metadata,
		conn,
		[]*buf.Buffer{prior},
		sniffBuffer,
		time.Second,
		sniff.TLSClientHello,
	)
	require.NoError(t, err)
	require.Equal(t, C.ProtocolTLS, metadata.Protocol)
	require.Equal(t, "prior.example.com", metadata.Domain)
	require.Equal(t, priorCopy, prior.Bytes(), "previously captured buffers must stay owned and unmodified")
	require.Equal(t, hello[split:], sniffBuffer.Bytes())
}
