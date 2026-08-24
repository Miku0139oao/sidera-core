package v2rayhttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing/common/buf"
)

type httpTestAddr string

func (a httpTestAddr) Network() string { return string(a) }
func (a httpTestAddr) String() string  { return string(a) }

type httpRecordingConn struct {
	access sync.Mutex
	wire   bytes.Buffer
	closed atomic.Bool
}

func (c *httpRecordingConn) Read([]byte) (int, error) { return 0, io.EOF }
func (c *httpRecordingConn) Write(p []byte) (int, error) {
	c.access.Lock()
	defer c.access.Unlock()
	return c.wire.Write(p)
}
func (c *httpRecordingConn) Close() error {
	c.closed.Store(true)
	return nil
}
func (c *httpRecordingConn) LocalAddr() net.Addr              { return httpTestAddr("local") }
func (c *httpRecordingConn) RemoteAddr() net.Addr             { return httpTestAddr("remote") }
func (c *httpRecordingConn) SetDeadline(time.Time) error      { return nil }
func (c *httpRecordingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *httpRecordingConn) SetWriteDeadline(time.Time) error { return nil }
func (c *httpRecordingConn) Bytes() []byte {
	c.access.Lock()
	defer c.access.Unlock()
	return bytes.Clone(c.wire.Bytes())
}

type httpCloseSpyReader struct {
	closed atomic.Bool
}

func (r *httpCloseSpyReader) Read([]byte) (int, error) { return 0, io.EOF }
func (r *httpCloseSpyReader) Close() error {
	r.closed.Store(true)
	return nil
}

func TestLateHTTPConnCloseBeforeSetup(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	writer := &httpRecordingConn{}
	conn := NewLateHTTPConn(writer, cancel)
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

	lateReader := &httpCloseSpyReader{}
	conn.Setup(lateReader, nil)
	if !lateReader.closed.Load() {
		t.Fatal("late response reader was published instead of closed")
	}
	if _, err := conn.Write([]byte("x")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Write error = %v, want net.ErrClosed", err)
	}
}

func TestHTTP2WrapperClosedWriteBufferReleasesOwnership(t *testing.T) {
	wrapper := NewHTTP2Wrapper(NewHTTPConn(bytes.NewReader(nil), io.Discard))
	wrapper.CloseWrapper()
	buffer := buf.NewSize(7)
	buffer.Extend(7)
	if err := wrapper.WriteBuffer(buffer); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("WriteBuffer error = %v, want net.ErrClosed", err)
	}
	if buffer.Cap() != 0 {
		t.Fatal("closed wrapper did not release owned buffer")
	}
}

func TestHTTP1ConcurrentFirstWriteEmitsOneRequest(t *testing.T) {
	rawConn := &httpRecordingConn{}
	request := &http.Request{
		Method: http.MethodPut,
		URL:    &url.URL{Path: "/tunnel"},
		Header: make(http.Header),
		Host:   "example.test",
	}
	conn := NewHTTP1Conn(rawConn, request)
	start := make(chan struct{})
	var group sync.WaitGroup
	for _, payload := range [][]byte{[]byte("first"), []byte("second")} {
		payload := payload
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			if n, err := conn.Write(payload); err != nil || n != len(payload) {
				t.Errorf("Write(%q) = %d, %v", payload, n, err)
			}
		}()
	}
	close(start)
	group.Wait()

	wire := rawConn.Bytes()
	if count := bytes.Count(wire, []byte("PUT /tunnel HTTP/1.1\r\n")); count != 1 {
		t.Fatalf("request preface count = %d, wire = %q", count, wire)
	}
	if !bytes.Contains(wire, []byte("first")) || !bytes.Contains(wire, []byte("second")) {
		t.Fatalf("wire does not contain both payloads: %q", wire)
	}
}
