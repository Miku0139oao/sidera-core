package v2raygrpclite

import (
	std_bufio "bufio"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"os"
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
	rawReader     io.Reader
	reader        *std_bufio.Reader
	writer        io.Writer
	flusher       http.Flusher
	create        chan struct{}
	err           error
	readRemaining int
}

func newGunConn(reader io.Reader, writer io.Writer, flusher http.Flusher) *GunConn {
	return &GunConn{
		rawReader: reader,
		reader:    std_bufio.NewReader(reader),
		writer:    writer,
		flusher:   flusher,
	}
}

func newLateGunConn(writer io.Writer) *GunConn {
	return &GunConn{
		create: make(chan struct{}),
		writer: writer,
	}
}

func (c *GunConn) setup(reader io.Reader, err error) {
	if reader != nil {
		c.rawReader = reader
		c.reader = std_bufio.NewReader(reader)
	}
	c.err = err
	close(c.create)
}

func (c *GunConn) Read(b []byte) (n int, err error) {
	n, err = c.read(b)
	return n, baderror.WrapH2(err)
}

func (c *GunConn) read(b []byte) (n int, err error) {
	if c.reader == nil {
		<-c.create
		if c.err != nil {
			return 0, c.err
		}
	}

	if c.readRemaining > 0 {
		if len(b) > c.readRemaining {
			b = b[:c.readRemaining]
		}
		n, err = c.reader.Read(b)
		c.readRemaining -= n
		return
	}

	for c.readRemaining == 0 {
		var header [6]byte
		_, err = io.ReadFull(c.reader, header[:])
		if err != nil {
			return
		}
		if header[0] != 0 || header[5] != 0x0A {
			return 0, E.New("v2ray gRPC lite: invalid frame header")
		}

		dataLen, readErr := binary.ReadUvarint(c.reader)
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

	n, err = c.reader.Read(b)
	c.readRemaining -= n
	return
}

func (c *GunConn) Write(b []byte) (n int, err error) {
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
	_, err = c.writer.Write(buffer.Bytes())
	if err != nil {
		return 0, baderror.WrapH2(err)
	}
	if c.flusher != nil {
		c.flusher.Flush()
	}
	return len(b), nil
}

func (c *GunConn) WriteBuffer(buffer *buf.Buffer) error {
	defer buffer.Release()
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
	err := common.Error(c.writer.Write(buffer.Bytes()))
	if err != nil {
		return baderror.WrapH2(err)
	}
	if c.flusher != nil {
		c.flusher.Flush()
	}
	return nil
}

func (c *GunConn) FrontHeadroom() int {
	return 6 + binary.MaxVarintLen64
}

func (c *GunConn) Close() error {
	return common.Close(c.rawReader, c.writer)
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
