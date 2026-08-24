package v2raygrpclite

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing/common/buf"
)

func TestGunConnFrameRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte("frame"), 100)
	var wire bytes.Buffer
	writer := newGunConn(bytes.NewReader(nil), &wire, nil)
	written, err := writer.Write(payload)
	if err != nil {
		t.Fatal(err)
	}
	if written != len(payload) {
		t.Fatalf("written = %d, want %d", written, len(payload))
	}

	reader := newGunConn(bytes.NewReader(wire.Bytes()), io.Discard, nil)
	decoded := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, decoded); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatal("decoded payload mismatch")
	}
}

func TestGunConnSkipsEmptyFrames(t *testing.T) {
	var wire bytes.Buffer
	writer := newGunConn(bytes.NewReader(nil), &wire, nil)
	if _, err := writer.Write(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	reader := newGunConn(bytes.NewReader(wire.Bytes()), io.Discard, nil)
	decoded := make([]byte, len("payload"))
	if _, err := io.ReadFull(reader, decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded) != "payload" {
		t.Fatalf("decoded = %q", decoded)
	}
}

func TestGunConnRejectsInvalidFrameLength(t *testing.T) {
	var wire bytes.Buffer
	wire.Write([]byte{0, 0, 0, 0, 1, 0x0A})
	wire.WriteByte(1)
	wire.WriteByte('x')
	reader := newGunConn(bytes.NewReader(wire.Bytes()), io.Discard, nil)
	if _, err := reader.Read(make([]byte, 1)); err == nil {
		t.Fatal("accepted an inconsistent gRPC frame length")
	}
}

func TestGunConnRejectsOversizedVarint(t *testing.T) {
	var wire bytes.Buffer
	wire.Write([]byte{0, 0xff, 0xff, 0xff, 0xff, 0x0A})
	var encoded [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(encoded[:], ^uint64(0))
	wire.Write(encoded[:n])
	reader := newGunConn(bytes.NewReader(wire.Bytes()), io.Discard, nil)
	if _, err := reader.Read(make([]byte, 1)); err == nil {
		t.Fatal("accepted an oversized gRPC frame")
	}
}

type shortGunWriter struct {
	access sync.Mutex
	wire   bytes.Buffer
	max    int
	zero   bool
}

func (w *shortGunWriter) Write(p []byte) (int, error) {
	w.access.Lock()
	defer w.access.Unlock()
	if w.zero {
		return 0, nil
	}
	if len(p) > w.max {
		p = p[:w.max]
	}
	return w.wire.Write(p)
}

func (w *shortGunWriter) Bytes() []byte {
	w.access.Lock()
	defer w.access.Unlock()
	return bytes.Clone(w.wire.Bytes())
}

type gunCloseSpyReader struct {
	closed atomic.Bool
}

func (r *gunCloseSpyReader) Read([]byte) (int, error) { return 0, io.EOF }
func (r *gunCloseSpyReader) Close() error {
	r.closed.Store(true)
	return nil
}

func ownedGunBuffer(payload []byte) *buf.Buffer {
	buffer := buf.NewSize(6 + binary.MaxVarintLen64 + len(payload))
	buffer.Resize(6+binary.MaxVarintLen64, len(payload))
	copy(buffer.Bytes(), payload)
	return buffer
}

func TestGunConnHandlesShortWritesAndBufferOwnership(t *testing.T) {
	writer := &shortGunWriter{max: 2}
	conn := newGunConn(bytes.NewReader(nil), writer, nil)
	if n, err := conn.Write([]byte("first")); err != nil || n != len("first") {
		t.Fatalf("Write = %d, %v", n, err)
	}
	buffer := ownedGunBuffer([]byte("second"))
	if err := conn.WriteBuffer(buffer); err != nil {
		t.Fatal(err)
	}
	if buffer.Cap() != 0 {
		t.Fatal("WriteBuffer did not release its buffer")
	}
	reader := newGunConn(bytes.NewReader(writer.Bytes()), io.Discard, nil)
	decoded := make([]byte, len("firstsecond"))
	if _, err := io.ReadFull(reader, decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded) != "firstsecond" {
		t.Fatalf("decoded = %q", decoded)
	}

	zeroWriter := &shortGunWriter{max: 1, zero: true}
	if _, err := newGunConn(bytes.NewReader(nil), zeroWriter, nil).Write([]byte("x")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero writer error = %v, want io.ErrShortWrite", err)
	}
}

func TestLateGunConnCloseBeforeSetup(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	pipeReader, pipeWriter := io.Pipe()
	defer pipeReader.Close()
	conn := newLateGunConn(pipeWriter, cancel)
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
	if !errors.Is(context.Cause(ctx), net.ErrClosed) {
		t.Fatalf("context cause = %v, want net.ErrClosed", context.Cause(ctx))
	}
	lateReader := &gunCloseSpyReader{}
	conn.setup(lateReader, nil)
	if !lateReader.closed.Load() {
		t.Fatal("late response body was not closed")
	}
	buffer := ownedGunBuffer([]byte("payload"))
	if err := conn.WriteBuffer(buffer); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("WriteBuffer error = %v, want net.ErrClosed", err)
	}
	if buffer.Cap() != 0 {
		t.Fatal("closed WriteBuffer did not release ownership")
	}
}

func TestGunConnConcurrentReadWriteClose(t *testing.T) {
	var input bytes.Buffer
	encoder := newGunConn(bytes.NewReader(nil), &input, nil)
	for range 128 {
		if _, err := encoder.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	writer := &shortGunWriter{max: 3}
	conn := newGunConn(bytes.NewReader(input.Bytes()), writer, nil)
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
