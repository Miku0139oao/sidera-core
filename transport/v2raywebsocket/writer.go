package v2raywebsocket

import (
	"crypto/rand"
	"encoding/binary"
	"io"

	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/ws"
)

type Writer struct {
	writer     io.Writer
	isServer   bool
	maskSource io.Reader
}

func NewWriter(writer io.Writer, state ws.State) *Writer {
	return &Writer{
		writer:     writer,
		isServer:   state == ws.StateServerSide,
		maskSource: rand.Reader,
	}
}

func (w *Writer) Write(payload []byte) error {
	return w.writeFrame(ws.OpBinary, payload)
}

func (w *Writer) writeFrame(opCode ws.OpCode, payload []byte) error {
	dataLen := len(payload)
	headerLen := websocketHeaderLen(dataLen, !w.isServer)
	frame := make([]byte, headerLen+dataLen)
	copy(frame[headerLen:], payload)
	if err := w.writeHeader(frame[:headerLen], opCode, dataLen); err != nil {
		return err
	}
	if !w.isServer {
		mask := [4]byte(frame[headerLen-4 : headerLen])
		ws.Cipher(frame[headerLen:], mask, 0)
	}
	return writeAll(w.writer, frame)
}

func (w *Writer) WriteBuffer(buffer *buf.Buffer) error {
	defer buffer.Release()
	dataLen := buffer.Len()
	headerLen := websocketHeaderLen(dataLen, !w.isServer)
	header := buffer.ExtendHeader(headerLen)
	if err := w.writeHeader(header, ws.OpBinary, dataLen); err != nil {
		return err
	}
	if !w.isServer {
		mask := [4]byte(header[headerLen-4:])
		ws.Cipher(buffer.Bytes()[headerLen:], mask, 0)
	}
	return wrapWsError(writeAll(w.writer, buffer.Bytes()))
}

func (w *Writer) writeHeader(header []byte, opCode ws.OpCode, dataLen int) error {
	header[0] = byte(opCode) | 0x80
	payloadLengthBytes := len(header) - 1
	if !w.isServer {
		payloadLengthBytes -= 4
		header[1] = 1 << 7
	} else {
		header[1] = 0
	}
	switch payloadLengthBytes {
	case 1:
		header[1] |= byte(dataLen)
	case 3:
		header[1] |= 126
		binary.BigEndian.PutUint16(header[2:4], uint16(dataLen))
	case 9:
		header[1] |= 127
		binary.BigEndian.PutUint64(header[2:10], uint64(dataLen))
	}
	if !w.isServer {
		if _, err := io.ReadFull(w.maskSource, header[len(header)-4:]); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) FrontHeadroom() int {
	return ws.MaxHeaderSize
}

func websocketHeaderLen(dataLen int, masked bool) int {
	headerLen := 2
	if dataLen >= 126 && dataLen < 65536 {
		headerLen += 2
	} else if dataLen >= 65536 {
		headerLen += 8
	}
	if masked {
		headerLen += 4
	}
	return headerLen
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
