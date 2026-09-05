package xrayencryption

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing/common/buf"
)

func TestXorConnWriteZeroLength(t *testing.T) {
	sink := &shortWriteConn{maxWrite: 16}
	conn := NewXorConn(sink, NewCTR(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 16)), nil, 0, 0)
	n, err := conn.Write(nil)
	if n != 0 || err != nil {
		t.Fatalf("Write(nil) = %d, %v, want 0, nil", n, err)
	}
	n, err = conn.Write([]byte{})
	if n != 0 || err != nil {
		t.Fatalf("Write(empty) = %d, %v, want 0, nil", n, err)
	}
	if sink.calls.Load() != 0 {
		t.Fatalf("zero-length write touched the sink %d times", sink.calls.Load())
	}
}

func TestXorConnWriteMissingCTR(t *testing.T) {
	sink := &shortWriteConn{maxWrite: 16}
	conn := NewXorConn(sink, nil, nil, 0, 0)
	input := xorTestRecord(t, 17)
	original := append([]byte(nil), input...)
	n, err := conn.Write(input)
	if n != 0 || err == nil {
		t.Fatalf("Write missing CTR = %d, %v, want 0, error", n, err)
	}
	if !bytes.Equal(input, original) {
		t.Fatal("missing-CTR write mutated caller input")
	}
	if sink.calls.Load() != 0 {
		t.Fatal("missing-CTR write touched the sink")
	}
}

func TestXorConnWriteInputImmutable(t *testing.T) {
	input := xorTestRecord(t, 17)
	original := append([]byte(nil), input...)
	sink := &shortWriteConn{maxWrite: 1 << 20}
	key, iv := xorTestKeyIV()
	conn := NewXorConn(sink, NewCTR(key, iv), nil, 0, 0)
	n, err := conn.Write(input)
	if err != nil || n != len(input) {
		t.Fatalf("Write = %d, %v, want %d, nil", n, err, len(input))
	}
	if !bytes.Equal(input, original) {
		t.Fatal("Write mutated caller input")
	}
	if !bytes.Equal(sink.Bytes(), xorRecordHeaders(t, key, iv, original)) {
		t.Fatal("wire bytes diverged from header-XOR fixture")
	}
}

func TestXorConnWriteFragmentedHeader(t *testing.T) {
	splits := [][]int{{1, 4}, {2, 3}, {3, 2}, {4, 1}, {1, 1, 1, 2}, {2, 2, 1}}
	for _, split := range splits {
		t.Run(fmt.Sprintf("%v", split), func(t *testing.T) {
			input := xorTestRecord(t, 17)
			original := append([]byte(nil), input...)
			sink := &shortWriteConn{maxWrite: 1 << 20}
			key, iv := xorTestKeyIV()
			conn := NewXorConn(sink, NewCTR(key, iv), nil, 0, 0)
			offset := 0
			for _, part := range split {
				end := offset + part
				if end > len(input) {
					end = len(input)
				}
				n, err := conn.Write(input[offset:end])
				if err != nil || n != end-offset {
					t.Fatalf("Write(%d:%d) = %d, %v", offset, end, n, err)
				}
				offset = end
			}
			if offset != 5 {
				if n, err := conn.Write(input[offset:]); err != nil || n != len(input)-offset {
					t.Fatalf("Write remainder = %d, %v", n, err)
				}
			} else if _, err := conn.Write(input[5:]); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(input, original) {
				t.Fatal("fragmented write mutated caller input")
			}
			if !bytes.Equal(sink.Bytes(), xorRecordHeaders(t, key, iv, original)) {
				t.Fatalf("fragmented header split %v changed wire bytes", split)
			}
		})
	}
}

func TestXorConnWriteRepeatedRecords(t *testing.T) {
	record := xorTestRecord(t, 64)
	var input []byte
	for range 8 {
		input = append(input, record...)
	}
	original := append([]byte(nil), input...)
	key, iv := xorTestKeyIV()
	want := xorRecordHeaders(t, key, iv, original)

	t.Run("single write", func(t *testing.T) {
		sink := &shortWriteConn{maxWrite: 1 << 20}
		conn := NewXorConn(sink, NewCTR(key, iv), nil, 0, 0)
		n, err := conn.Write(input)
		if err != nil || n != len(input) {
			t.Fatalf("Write = %d, %v, want %d, nil", n, err, len(input))
		}
		if !bytes.Equal(input, original) {
			t.Fatal("repeated-record write mutated caller input")
		}
		if !bytes.Equal(sink.Bytes(), want) {
			t.Fatal("single write of repeated records changed wire bytes")
		}
	})

	t.Run("sequential writes", func(t *testing.T) {
		sink := &shortWriteConn{maxWrite: 1 << 20}
		conn := NewXorConn(sink, NewCTR(key, iv), nil, 0, 0)
		for i := 0; i < 8; i++ {
			piece := input[i*len(record) : (i+1)*len(record)]
			n, err := conn.Write(piece)
			if err != nil || n != len(piece) {
				t.Fatalf("Write record %d = %d, %v", i, n, err)
			}
		}
		if !bytes.Equal(input, original) {
			t.Fatal("sequential writes mutated caller input")
		}
		if !bytes.Equal(sink.Bytes(), want) {
			t.Fatal("sequential record writes changed CTR progression")
		}
	})
}

func TestXorConnWriteShortWrites(t *testing.T) {
	input := xorTestRecord(t, 17)
	original := append([]byte(nil), input...)
	sink := &shortWriteConn{maxWrite: 3}
	key, iv := xorTestKeyIV()
	conn := NewXorConn(sink, NewCTR(key, iv), nil, 0, 0)
	n, err := conn.Write(input)
	if err != nil || n != len(input) {
		t.Fatalf("Write = %d, %v, want %d, nil", n, err, len(input))
	}
	if sink.calls.Load() <= 1 {
		t.Fatal("short-write sink was not exercised")
	}
	if !bytes.Equal(input, original) {
		t.Fatal("short write mutated caller input")
	}
	if !bytes.Equal(sink.Bytes(), xorRecordHeaders(t, key, iv, original)) {
		t.Fatal("short writes changed assembled wire bytes")
	}
}

func TestXorConnWriteFailureCount(t *testing.T) {
	sentinel := errors.New("write failed")
	input := xorTestRecord(t, 17)
	original := append([]byte(nil), input...)
	sink := &shortWriteConn{maxWrite: 4, firstError: sentinel}
	conn := NewXorConn(sink, NewCTR(xorTestKeyIV()), nil, 0, 0)
	n, err := conn.Write(input)
	if n != 4 || !errors.Is(err, sentinel) {
		t.Fatalf("partial Write = %d, %v, want 4, sentinel", n, err)
	}
	if !bytes.Equal(input, original) {
		t.Fatal("failed write mutated caller input")
	}

	emptySink := &shortWriteConn{maxWrite: 0}
	conn = NewXorConn(emptySink, NewCTR(xorTestKeyIV()), nil, 0, 0)
	n, err = conn.Write(input)
	if n != 0 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero-progress Write = %d, %v, want 0, short write", n, err)
	}
}

func TestXorConnWriteOversizeFallback(t *testing.T) {
	record := xorTestRecord(t, 16640)
	var input []byte
	for len(input) <= buf.MaxPooledBufferSize {
		input = append(input, record...)
	}
	if len(input) <= buf.MaxPooledBufferSize {
		t.Fatalf("fixture %d did not exceed pooled size %d", len(input), buf.MaxPooledBufferSize)
	}
	original := append([]byte(nil), input...)
	sink := &shortWriteConn{maxWrite: 1 << 20}
	key, iv := xorTestKeyIV()
	conn := NewXorConn(sink, NewCTR(key, iv), nil, 0, 0)
	n, err := conn.Write(input)
	if err != nil || n != len(input) {
		t.Fatalf("oversize Write = %d, %v, want %d, nil", n, err, len(input))
	}
	if !bytes.Equal(input, original) {
		t.Fatal("oversize write mutated caller input")
	}
	if !bytes.Equal(sink.Bytes(), xorRecordHeaders(t, key, iv, original)) {
		t.Fatal("oversize fallback changed wire bytes")
	}
}

func TestXorConnConcurrentReadWrite(t *testing.T) {
	record := xorTestRecord(t, 17)
	keyW, ivW := xorTestKeyIV()
	keyR := bytes.Repeat([]byte{5}, 32)
	ivR := bytes.Repeat([]byte{6}, 16)
	const rounds = 64
	plain := bytes.Repeat(record, rounds)
	stub := &xorConcurrentConn{reads: xorRecordHeaders(t, keyR, ivR, plain)}
	conn := NewXorConn(stub, NewCTR(keyW, ivW), NewCTR(keyR, ivR), 0, 0)

	var wait sync.WaitGroup
	errs := make(chan error, 2)
	wait.Add(2)
	go func() {
		defer wait.Done()
		for range rounds {
			if _, err := conn.Write(record); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wait.Done()
		got := make([]byte, len(record))
		for range rounds {
			if _, err := io.ReadFull(conn, got); err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(got, record) {
				errs <- fmt.Errorf("read mismatch")
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() {
		wait.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent Read/Write timed out")
	}
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
	want := xorRecordHeaders(t, keyW, ivW, bytes.Repeat(record, rounds))
	if !bytes.Equal(stub.written(), want) {
		t.Fatal("concurrent writes changed CTR progression")
	}
}

func xorTestKeyIV() ([]byte, []byte) {
	return bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 16)
}

func xorTestRecord(t *testing.T, payload int) []byte {
	t.Helper()
	if payload < 17 || payload > maxRecordSize {
		t.Fatalf("invalid record payload %d", payload)
	}
	record := make([]byte, 5+payload)
	EncodeHeader(record, payload)
	for i := range payload {
		record[5+i] = byte(i)
	}
	return record
}

func xorRecordHeaders(t *testing.T, key, iv, data []byte) []byte {
	t.Helper()
	out := append([]byte(nil), data...)
	stream := NewCTR(key, iv)
	offset := 0
	for offset < len(out) {
		if offset+5 > len(out) {
			t.Fatalf("truncated record at %d/%d", offset, len(out))
		}
		length, err := DecodeHeader(out[offset : offset+5])
		if err != nil {
			t.Fatal(err)
		}
		stream.XORKeyStream(out[offset:offset+5], out[offset:offset+5])
		offset += 5 + length
	}
	if offset != len(out) {
		t.Fatalf("fixture leftover %d bytes", len(out)-offset)
	}
	return out
}

type xorConcurrentConn struct {
	writeMu sync.Mutex
	readMu  sync.Mutex
	writes  []byte
	reads   []byte
	roff    int
}

func (c *xorConcurrentConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.writes = append(c.writes, p...)
	return len(p), nil
}

func (c *xorConcurrentConn) written() []byte {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return append([]byte(nil), c.writes...)
}

func (c *xorConcurrentConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if c.roff >= len(c.reads) {
		return 0, io.EOF
	}
	n := copy(p, c.reads[c.roff:])
	c.roff += n
	return n, nil
}

func (c *xorConcurrentConn) Close() error                     { return nil }
func (c *xorConcurrentConn) LocalAddr() net.Addr              { return testAddr("local") }
func (c *xorConcurrentConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (c *xorConcurrentConn) SetDeadline(time.Time) error      { return nil }
func (c *xorConcurrentConn) SetReadDeadline(time.Time) error  { return nil }
func (c *xorConcurrentConn) SetWriteDeadline(time.Time) error { return nil }
