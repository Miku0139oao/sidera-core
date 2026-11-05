package v2raygrpclite

import (
	std_bufio "bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/baderror"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/common/varbin"
)

// kanged from: https://github.com/Qv2ray/gun-lite

var _ net.Conn = (*GunConn)(nil)

type GunConn struct {
	stateAccess   sync.Mutex
	readAccess    sync.Mutex
	writeAccess   sync.Mutex
	rawReader     io.Reader
	reader        *std_bufio.Reader
	writer        io.Writer
	flusher       http.Flusher
	ready         chan struct{}
	readyOnce     sync.Once
	setupDone     bool
	closed        bool
	writeClosed   bool
	err           error
	cancel        context.CancelCauseFunc
	readRemaining int
	closeOnce     sync.Once
	closeErr      error
}

func newGunConn(reader io.Reader, writer io.Writer, flusher http.Flusher) *GunConn {
	conn := &GunConn{
		rawReader: reader,
		reader:    std_bufio.NewReader(reader),
		writer:    writer,
		flusher:   flusher,
		ready:     make(chan struct{}),
		setupDone: true,
	}
	conn.signalReady()
	return conn
}

func newLateGunConn(writer io.Writer, cancelFunctions ...context.CancelCauseFunc) *GunConn {
	var cancel context.CancelCauseFunc
	if len(cancelFunctions) > 0 {
		cancel = cancelFunctions[0]
	}
	return &GunConn{
		ready:  make(chan struct{}),
		writer: writer,
		cancel: cancel,
	}
}

func (c *GunConn) signalReady() {
	c.readyOnce.Do(func() { close(c.ready) })
}

func (c *GunConn) setup(reader io.Reader, err error) {
	c.stateAccess.Lock()
	if c.setupDone || c.closed {
		c.stateAccess.Unlock()
		common.Close(reader)
		c.signalReady()
		return
	}
	c.setupDone = true
	if reader == nil && err == nil {
		err = io.ErrUnexpectedEOF
	}
	if reader != nil && err == nil {
		c.rawReader = reader
		c.reader = std_bufio.NewReader(reader)
	}
	c.err = err
	writer := c.writer
	cancel := c.cancel
	c.stateAccess.Unlock()
	c.signalReady()
	if err != nil {
		common.Close(reader)
		if cancel != nil {
			cancel(err)
		}
		if pipeWriter, isPipeWriter := writer.(*io.PipeWriter); isPipeWriter {
			_ = pipeWriter.CloseWithError(err)
		}
	}
}

func (c *GunConn) awaitReader() (*std_bufio.Reader, error) {
	<-c.ready
	c.stateAccess.Lock()
	defer c.stateAccess.Unlock()
	if c.closed {
		return nil, net.ErrClosed
	}
	if c.err != nil {
		return nil, c.err
	}
	if c.reader == nil {
		return nil, net.ErrClosed
	}
	return c.reader, nil
}

func (c *GunConn) writerForWrite() (io.Writer, http.Flusher, error) {
	c.stateAccess.Lock()
	defer c.stateAccess.Unlock()
	if c.closed {
		return nil, nil, net.ErrClosed
	}
	if c.err != nil {
		return nil, nil, c.err
	}
	if c.writer == nil {
		return nil, nil, net.ErrClosed
	}
	return c.writer, c.flusher, nil
}

func (c *GunConn) Read(b []byte) (n int, err error) {
	if len(b) == 0 {
		return 0, nil
	}
	c.readAccess.Lock()
	defer c.readAccess.Unlock()
	n, err = c.read(b)
	return n, baderror.WrapH2(err)
}

func (c *GunConn) read(b []byte) (n int, err error) {
	reader, err := c.awaitReader()
	if err != nil {
		return 0, err
	}
	if c.readRemaining > 0 {
		if len(b) > c.readRemaining {
			b = b[:c.readRemaining]
		}
		n, err = reader.Read(b)
		c.readRemaining -= n
		return n, err
	}

	for c.readRemaining == 0 {
		var header [6]byte
		if _, err = io.ReadFull(reader, header[:]); err != nil {
			return 0, err
		}
		if header[0] != 0 || header[5] != 0x0A {
			return 0, E.New("v2ray gRPC lite: invalid frame header")
		}

		dataLen, readErr := binary.ReadUvarint(reader)
		if readErr != nil {
			return 0, readErr
		}
		frameLen := 1 + uint64(varbin.UvarintLen(dataLen)) + dataLen
		if frameLen != uint64(binary.BigEndian.Uint32(header[1:5])) {
			return 0, E.New("v2ray gRPC lite: invalid frame length")
		}
		if dataLen > uint64(^uint(0)>>1) {
			return 0, E.New("v2ray gRPC lite: frame is too large")
		}
		c.readRemaining = int(dataLen)
	}
	if len(b) > c.readRemaining {
		b = b[:c.readRemaining]
	}

	n, err = reader.Read(b)
	c.readRemaining -= n
	return n, err
}

func (c *GunConn) Write(b []byte) (n int, err error) {
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()
	writer, flusher, err := c.writerForWrite()
	if err != nil {
		return 0, err
	}
	varLen := varbin.UvarintLen(uint64(len(b)))
	frameLen := 1 + varLen + len(b)
	if uint64(frameLen) > uint64(^uint32(0)) {
		return 0, E.New("v2ray gRPC lite: frame is too large")
	}
	buffer := buf.NewSize(5 + frameLen)
	defer buffer.Release()
	header := buffer.Extend(6 + varLen)
	header[0] = 0x00
	binary.BigEndian.PutUint32(header[1:5], uint32(frameLen))
	header[5] = 0x0A
	binary.PutUvarint(header[6:], uint64(len(b)))
	common.Must1(buffer.Write(b))
	if err = writeAll(writer, buffer.Bytes()); err != nil {
		return 0, baderror.WrapH2(err)
	}
	if flusher != nil {
		flusher.Flush()
	}
	return len(b), nil
}

func (c *GunConn) WriteBuffer(buffer *buf.Buffer) error {
	defer buffer.Release()
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()
	writer, flusher, err := c.writerForWrite()
	if err != nil {
		return err
	}
	dataLen := buffer.Len()
	varLen := varbin.UvarintLen(uint64(dataLen))
	frameLen := 1 + varLen + dataLen
	if uint64(frameLen) > uint64(^uint32(0)) {
		return E.New("v2ray gRPC lite: frame is too large")
	}
	header := buffer.ExtendHeader(6 + varLen)
	header[0] = 0x00
	binary.BigEndian.PutUint32(header[1:5], uint32(frameLen))
	header[5] = 0x0A
	binary.PutUvarint(header[6:], uint64(dataLen))
	if err = writeAll(writer, buffer.Bytes()); err != nil {
		return baderror.WrapH2(err)
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

func writeAll(writer io.Writer, content []byte) error {
	for len(content) > 0 {
		n, err := writer.Write(content)
		if n < 0 || n > len(content) {
			return io.ErrShortWrite
		}
		content = content[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (c *GunConn) FrontHeadroom() int {
	return 6 + binary.MaxVarintLen64
}

func (c *GunConn) CloseWrite() error {
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()
	c.stateAccess.Lock()
	if c.closed {
		c.stateAccess.Unlock()
		return net.ErrClosed
	}
	if c.writeClosed {
		c.stateAccess.Unlock()
		return nil
	}
	c.writeClosed = true
	writer := c.writer
	c.writer = nil
	c.stateAccess.Unlock()
	if closer, loaded := writer.(io.Closer); loaded {
		return baderror.WrapH2(closer.Close())
	}
	return nil
}

func (c *GunConn) Close() error {
	c.closeOnce.Do(func() {
		c.stateAccess.Lock()
		c.closed = true
		c.err = net.ErrClosed
		reader := c.rawReader
		writer := c.writer
		cancel := c.cancel
		c.stateAccess.Unlock()
		if cancel != nil {
			cancel(net.ErrClosed)
		}
		c.signalReady()
		c.closeErr = common.Close(reader, writer)
	})
	return c.closeErr
}

func (c *GunConn) LocalAddr() net.Addr {
	return M.Socksaddr{}
}

func (c *GunConn) RemoteAddr() net.Addr {
	return M.Socksaddr{}
}

func (c *GunConn) SetDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *GunConn) SetReadDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *GunConn) SetWriteDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *GunConn) NeedAdditionalReadDeadline() bool {
	return true
}
