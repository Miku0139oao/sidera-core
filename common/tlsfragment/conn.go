package tf

import (
	"context"
	"encoding/binary"
	"io"
	"math/rand"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	C "github.com/Miku0139oao/sidera-core/constant"
	N "github.com/sagernet/sing/common/network"

	"golang.org/x/net/publicsuffix"
)

type Conn struct {
	net.Conn
	tcpConn            *net.TCPConn
	ctx                context.Context
	writeAccess        sync.Mutex
	firstPacketWritten atomic.Bool
	splitPacket        bool
	splitRecord        bool
	fallbackDelay      time.Duration
}

func NewConn(conn net.Conn, ctx context.Context, splitPacket bool, splitRecord bool, fallbackDelay time.Duration) *Conn {
	if ctx == nil {
		ctx = context.Background()
	}
	if fallbackDelay == 0 {
		fallbackDelay = C.TLSFragmentFallbackDelay
	}
	tcpConn, _ := N.UnwrapWriter(conn).(*net.TCPConn)
	return &Conn{
		Conn:          conn,
		tcpConn:       tcpConn,
		ctx:           ctx,
		splitPacket:   splitPacket,
		splitRecord:   splitRecord,
		fallbackDelay: fallbackDelay,
	}
}

func (c *Conn) Write(payload []byte) (n int, err error) {
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()

	// Some early connections use Write(nil) as a handshake trigger. Forward it,
	// but do not consume the one ClientHello fragmentation opportunity.
	if len(payload) == 0 {
		if err = c.ctx.Err(); err != nil {
			return 0, err
		}
		finishContextWrite := c.beginContextWrite()
		defer func() {
			if contextErr := finishContextWrite(); contextErr != nil {
				err = contextErr
			}
		}()
		return c.Conn.Write(payload)
	}
	if c.firstPacketWritten.Load() {
		return c.Conn.Write(payload)
	}

	serverName := IndexTLSServerName(payload)
	splitIndexes := chooseSplitIndexes(serverName, len(payload))
	fragments, ok := buildFragments(payload, splitIndexes, c.splitRecord)
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	finishContextWrite := c.beginContextWrite()
	defer func() {
		if contextErr := finishContextWrite(); contextErr != nil {
			err = contextErr
		}
	}()
	if !ok {
		return c.writeFirstUnsplit(payload)
	}

	var wireWritten int
	defer func() {
		if wireWritten > 0 {
			c.firstPacketWritten.Store(true)
		}
	}()

	if !c.splitPacket {
		wirePayload := joinFragments(fragments)
		wireWritten, err = writeFull(c.Conn.Write, wirePayload)
		return callerBytesForWire(fragments, wireWritten), err
	}

	if c.tcpConn != nil {
		if err = c.tcpConn.SetNoDelay(true); err != nil {
			return 0, err
		}
		defer func() {
			restoreErr := c.tcpConn.SetNoDelay(false)
			if err == nil && restoreErr != nil {
				err = restoreErr
			}
		}()
	}

	for index, fragment := range fragments {
		var fragmentWritten int
		if c.tcpConn != nil && index != len(fragments)-1 {
			fragmentWritten, err = writeAndWaitAck(c.ctx, c.tcpConn, fragment.wire, c.fallbackDelay)
		} else {
			fragmentWritten, err = writeFull(c.Conn.Write, fragment.wire)
			if err == nil && index != len(fragments)-1 {
				err = waitForDelay(c.ctx, c.fallbackDelay)
			}
		}
		wireWritten += fragmentWritten
		n += fragment.callerBytes(fragmentWritten)
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (c *Conn) writeFirstUnsplit(payload []byte) (int, error) {
	n, err := c.Conn.Write(payload)
	if n < 0 || n > len(payload) {
		return 0, io.ErrShortWrite
	}
	if n > 0 {
		c.firstPacketWritten.Store(true)
	}
	if n == 0 && err == nil {
		err = io.ErrNoProgress
	} else if n != len(payload) && err == nil {
		err = io.ErrShortWrite
	}
	return n, err
}

func (c *Conn) beginContextWrite() func() error {
	interruptDone := make(chan struct{})
	stopInterrupt := context.AfterFunc(c.ctx, func() {
		_ = c.Conn.SetWriteDeadline(time.Now())
		_ = c.Conn.Close()
		close(interruptDone)
	})
	return func() error {
		if !stopInterrupt() {
			<-interruptDone
		}
		return c.ctx.Err()
	}
}

type wireFragment struct {
	wire             []byte
	syntheticPrefix  int
	callerPayloadLen int
}

func (f wireFragment) callerBytes(wireBytes int) int {
	if wireBytes <= f.syntheticPrefix {
		return 0
	}
	callerBytes := wireBytes - f.syntheticPrefix
	if callerBytes > f.callerPayloadLen {
		return f.callerPayloadLen
	}
	return callerBytes
}

func buildFragments(payload []byte, splitIndexes []int, splitRecord bool) ([]wireFragment, bool) {
	if len(splitIndexes) == 0 {
		return nil, false
	}

	boundaries := make([]int, 0, len(splitIndexes)+2)
	boundaries = append(boundaries, 0)
	for _, splitIndex := range splitIndexes {
		if splitIndex <= recordLayerHeaderLen || splitIndex >= len(payload) || splitIndex <= boundaries[len(boundaries)-1] {
			continue
		}
		boundaries = append(boundaries, splitIndex)
	}
	if len(boundaries) == 1 {
		return nil, false
	}
	boundaries = append(boundaries, len(payload))

	fragments := make([]wireFragment, 0, len(boundaries)-1)
	for index := 0; index < len(boundaries)-1; index++ {
		start, end := boundaries[index], boundaries[index+1]
		if !splitRecord {
			fragments = append(fragments, wireFragment{
				wire:             payload[start:end],
				callerPayloadLen: end - start,
			})
			continue
		}

		payloadStart := start
		if index == 0 {
			payloadStart = recordLayerHeaderLen
		}
		fragmentPayloadLen := end - payloadStart
		if fragmentPayloadLen < 0 || fragmentPayloadLen > int(^uint16(0)) {
			return nil, false
		}
		wire := make([]byte, recordLayerHeaderLen+fragmentPayloadLen)
		copy(wire[:3], payload[:3])
		binary.BigEndian.PutUint16(wire[3:5], uint16(fragmentPayloadLen))
		copy(wire[recordLayerHeaderLen:], payload[payloadStart:end])

		fragment := wireFragment{
			wire:             wire,
			syntheticPrefix:  recordLayerHeaderLen,
			callerPayloadLen: end - start,
		}
		if index == 0 {
			// The first synthetic record header replaces the caller's original
			// record header byte-for-byte for write accounting purposes.
			fragment.syntheticPrefix = 0
			fragment.callerPayloadLen = end
		}
		fragments = append(fragments, fragment)
	}
	return fragments, true
}

func joinFragments(fragments []wireFragment) []byte {
	var size int
	for _, fragment := range fragments {
		size += len(fragment.wire)
	}
	payload := make([]byte, 0, size)
	for _, fragment := range fragments {
		payload = append(payload, fragment.wire...)
	}
	return payload
}

func callerBytesForWire(fragments []wireFragment, wireBytes int) int {
	var callerBytes int
	for _, fragment := range fragments {
		fragmentBytes := min(wireBytes, len(fragment.wire))
		callerBytes += fragment.callerBytes(fragmentBytes)
		wireBytes -= fragmentBytes
		if wireBytes <= 0 {
			break
		}
	}
	return callerBytes
}

func chooseSplitIndexes(serverName *MyServerName, payloadLen int) []int {
	if serverName == nil || serverName.ServerName == "" || serverName.Length != len(serverName.ServerName) ||
		serverName.Index <= recordLayerHeaderLen || serverName.Index+serverName.Length > payloadLen {
		return nil
	}

	labels := strings.Split(serverName.ServerName, ".")
	if publicSuffix := publicsuffix.List.PublicSuffix(serverName.ServerName); publicSuffix != "" {
		labelCount := len(labels) - strings.Count(serverName.ServerName, ".")
		if labelCount <= 0 {
			return nil
		}
		labels = labels[:labelCount]
	}
	if len(labels) > 1 && labels[0] == "..." {
		labels = labels[1:]
	}

	currentIndex := serverName.Index
	var splitIndexes []int
	for index, label := range labels {
		if label != "" {
			splitIndex := currentIndex + rand.Intn(len(label))
			if splitIndex > recordLayerHeaderLen && splitIndex < payloadLen &&
				(len(splitIndexes) == 0 || splitIndex > splitIndexes[len(splitIndexes)-1]) {
				splitIndexes = append(splitIndexes, splitIndex)
			}
		}
		currentIndex += len(label)
		if index != len(labels)-1 {
			currentIndex++
		}
	}
	return splitIndexes
}

func writeFull(write func([]byte) (int, error), payload []byte) (int, error) {
	var written int
	for written < len(payload) {
		n, err := write(payload[written:])
		if n < 0 || n > len(payload)-written {
			return written, io.ErrShortWrite
		}
		written += n
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrNoProgress
		}
	}
	return written, nil
}

func waitForDelay(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Conn) ReaderReplaceable() bool {
	return true
}

func (c *Conn) WriterReplaceable() bool {
	return c.firstPacketWritten.Load()
}

func (c *Conn) Upstream() any {
	return c.Conn
}
