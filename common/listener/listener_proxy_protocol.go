package listener

import (
	std_bufio "bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"time"

	C "github.com/Miku0139oao/sidera-core/constant"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/pires/go-proxyproto"
)

type proxyProtocolListener struct {
	net.Listener
	owner *Listener
}

func (l *proxyProtocolListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		upstream := M.SocksaddrFromNet(conn.RemoteAddr()).Addr.Unmap()
		trusted := false
		for _, prefix := range l.owner.proxyProtocolTrustedUpstream {
			if prefix.Contains(upstream) {
				trusted = true
				break
			}
		}
		if trusted {
			return &proxyProtocolConn{
				Conn:           conn,
				acceptNoHeader: l.owner.listenOptions.ProxyProtocolAcceptNoHeader,
			}, nil
		}
		if l.owner.listenOptions.ProxyProtocolAcceptNoHeader {
			return conn, nil
		}
		conn.Close()
	}
}

type proxyProtocolConn struct {
	net.Conn
	acceptNoHeader bool
	once           sync.Once
	prepared       net.Conn
	prepareErr     error
}

func (c *proxyProtocolConn) prepare() error {
	c.once.Do(func() {
		c.prepared, c.prepareErr = c.readHeader()
	})
	return c.prepareErr
}

func (c *proxyProtocolConn) readHeader() (net.Conn, error) {
	if err := c.Conn.SetReadDeadline(time.Now().Add(C.TCPTimeout)); err != nil {
		return c.Conn, err
	}
	header, reader, parseErr := readProxyProtocolHeader(c.Conn)
	if netErr, isNetError := parseErr.(net.Error); isNetError && netErr.Timeout() {
		parseErr = proxyproto.ErrNoProxyProtocol
	}
	if err := c.Conn.SetReadDeadline(time.Time{}); err != nil {
		return c.Conn, err
	}
	if parseErr != nil && !(c.acceptNoHeader && errors.Is(parseErr, proxyproto.ErrNoProxyProtocol)) {
		return c.Conn, parseErr
	}

	conn := &proxyProtocolPreparedConn{Conn: c.Conn, reader: reader}
	if header != nil && !header.Command.IsLocal() {
		conn.source = header.SourceAddr
		conn.destination = header.DestinationAddr
	}
	return conn, nil
}

func readProxyProtocolHeader(conn net.Conn) (*proxyproto.Header, *std_bufio.Reader, error) {
	reader := std_bufio.NewReaderSize(conn, 256)
	firstByte, err := reader.Peek(1)
	if err == nil && firstByte[0] == proxyproto.SIGV2[0] {
		signature, signatureErr := reader.Peek(len(proxyproto.SIGV2))
		if signatureErr == nil && bytes.Equal(signature, proxyproto.SIGV2) {
			fixedHeader, fixedHeaderErr := reader.Peek(16)
			if fixedHeaderErr != nil {
				return nil, reader, fixedHeaderErr
			}
			payloadLength := int(binary.BigEndian.Uint16(fixedHeader[14:16]))
			reader = std_bufio.NewReaderSize(reader, 16+payloadLength)
		}
	}
	header, err := proxyproto.Read(reader)
	return header, reader, err
}

func (c *proxyProtocolConn) Read(p []byte) (int, error) {
	if err := c.prepare(); err != nil {
		return 0, err
	}
	return c.prepared.Read(p)
}

func (c *proxyProtocolConn) Close() error {
	return c.Conn.Close()
}

func (c *proxyProtocolConn) LocalAddr() net.Addr {
	if c.prepare() == nil {
		return c.prepared.LocalAddr()
	}
	return c.Conn.LocalAddr()
}

func (c *proxyProtocolConn) RemoteAddr() net.Addr {
	if c.prepare() == nil {
		return c.prepared.RemoteAddr()
	}
	return c.Conn.RemoteAddr()
}

type proxyProtocolPreparedConn struct {
	net.Conn
	reader      *std_bufio.Reader
	source      net.Addr
	destination net.Addr
}

func (c *proxyProtocolPreparedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *proxyProtocolPreparedConn) LocalAddr() net.Addr {
	if c.destination != nil {
		return c.destination
	}
	return c.Conn.LocalAddr()
}

func (c *proxyProtocolPreparedConn) RemoteAddr() net.Addr {
	if c.source != nil {
		return c.source
	}
	return c.Conn.RemoteAddr()
}
