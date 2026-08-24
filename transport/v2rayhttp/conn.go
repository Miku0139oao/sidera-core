package v2rayhttp

import (
	std_bufio "bufio"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Miku0139oao/sidera-core/log"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/baderror"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	F "github.com/sagernet/sing/common/format"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type HTTPConn struct {
	net.Conn
	request        *http.Request
	requestWritten bool
	responseRead   bool
	responseCache  *buf.Buffer
	readAccess     sync.Mutex
	writeAccess    sync.Mutex
	closed         atomic.Bool
	closeOnce      sync.Once
	closeErr       error
}

func NewHTTP1Conn(conn net.Conn, request *http.Request) *HTTPConn {
	if request.Header.Get("Host") == "" {
		request.Header.Set("Host", request.Host)
	}
	return &HTTPConn{
		Conn:    conn,
		request: request,
	}
}

func (c *HTTPConn) Read(b []byte) (n int, err error) {
	if len(b) == 0 {
		return 0, nil
	}
	c.readAccess.Lock()
	defer c.readAccess.Unlock()
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	if !c.responseRead {
		reader := std_bufio.NewReader(c.Conn)
		response, err := http.ReadResponse(reader, c.request)
		if err != nil {
			return 0, E.Cause(err, "read response")
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return 0, E.New("v2ray-http: unexpected status: ", response.Status)
		}
		if cacheLen := reader.Buffered(); cacheLen > 0 {
			c.responseCache = buf.NewSize(cacheLen)
			_, err = c.responseCache.ReadFullFrom(reader, cacheLen)
			if err != nil {
				c.responseCache.Release()
				c.responseCache = nil
				return 0, E.Cause(err, "read cache")
			}
		}
		c.responseRead = true
	}
	if c.responseCache != nil {
		n, err = c.responseCache.Read(b)
		if err == io.EOF {
			c.responseCache.Release()
			c.responseCache = nil
		}
		if n > 0 {
			return n, nil
		}
	}
	return c.Conn.Read(b)
}

func (c *HTTPConn) Write(b []byte) (int, error) {
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	if !c.requestWritten {
		if err := c.writeRequest(b); err != nil {
			return 0, E.Cause(err, "write request")
		}
		c.requestWritten = true
		return len(b), nil
	}
	return c.Conn.Write(b)
}

func (c *HTTPConn) writeRequest(payload []byte) error {
	writer := bufio.NewBufferedWriter(c.Conn, buf.New())
	const CRLF = "\r\n"
	if _, err := writer.Write([]byte(F.ToString(c.request.Method, " ", c.request.URL.RequestURI(), " HTTP/1.1", CRLF))); err != nil {
		return err
	}
	for key, value := range c.request.Header {
		if _, err := writer.Write([]byte(F.ToString(key, ": ", strings.Join(value, ", "), CRLF))); err != nil {
			return err
		}
	}
	if _, err := writer.Write([]byte(CRLF)); err != nil {
		return err
	}
	if _, err := writer.Write(payload); err != nil {
		return err
	}
	return writer.Fallthrough()
}

func (c *HTTPConn) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		c.closeErr = c.Conn.Close()
		c.readAccess.Lock()
		if c.responseCache != nil {
			c.responseCache.Release()
			c.responseCache = nil
		}
		c.readAccess.Unlock()
	})
	return c.closeErr
}

func (c *HTTPConn) ReaderReplaceable() bool {
	c.readAccess.Lock()
	defer c.readAccess.Unlock()
	return c.responseRead
}

func (c *HTTPConn) WriterReplaceable() bool {
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()
	return c.requestWritten
}

func (c *HTTPConn) NeedHandshake() bool {
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()
	return !c.requestWritten
}

func (c *HTTPConn) Upstream() any {
	return c.Conn
}

var _ N.WriteCloser = (*HTTP2Conn)(nil)

type HTTP2Conn struct {
	stateAccess sync.Mutex
	readAccess  sync.Mutex
	writeAccess sync.Mutex
	reader      io.Reader
	writer      io.Writer
	ready       chan struct{}
	readyOnce   sync.Once
	setupDone   bool
	closed      bool
	writeClosed bool
	err         error
	cancel      context.CancelCauseFunc
	closeOnce   sync.Once
	closeErr    error
}

func NewHTTPConn(reader io.Reader, writer io.Writer) *HTTP2Conn {
	conn := &HTTP2Conn{
		reader:    reader,
		writer:    writer,
		ready:     make(chan struct{}),
		setupDone: true,
	}
	conn.signalReady()
	return conn
}

func NewLateHTTPConn(writer io.Writer, cancelFunctions ...context.CancelCauseFunc) *HTTP2Conn {
	var cancel context.CancelCauseFunc
	if len(cancelFunctions) > 0 {
		cancel = cancelFunctions[0]
	}
	return &HTTP2Conn{
		ready:  make(chan struct{}),
		writer: writer,
		cancel: cancel,
	}
}

func (c *HTTP2Conn) signalReady() {
	c.readyOnce.Do(func() { close(c.ready) })
}

func (c *HTTP2Conn) Setup(reader io.Reader, err error) {
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
	if err == nil {
		c.reader = reader
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
		closePipeWriter(writer, err)
	}
}

func (c *HTTP2Conn) awaitReader() (io.Reader, error) {
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

func (c *HTTP2Conn) writerForWrite() (io.Writer, error) {
	c.stateAccess.Lock()
	defer c.stateAccess.Unlock()
	if c.closed {
		return nil, net.ErrClosed
	}
	if c.err != nil {
		return nil, c.err
	}
	if c.writer == nil {
		return nil, net.ErrClosed
	}
	return c.writer, nil
}

func (c *HTTP2Conn) Read(b []byte) (n int, err error) {
	if len(b) == 0 {
		return 0, nil
	}
	c.readAccess.Lock()
	defer c.readAccess.Unlock()
	reader, err := c.awaitReader()
	if err != nil {
		return 0, err
	}
	n, err = reader.Read(b)
	return n, baderror.WrapH2(err)
}

func (c *HTTP2Conn) Write(b []byte) (n int, err error) {
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()
	writer, err := c.writerForWrite()
	if err != nil {
		return 0, err
	}
	n, err = writer.Write(b)
	return n, baderror.WrapH2(err)
}

func (c *HTTP2Conn) CloseWrite() error {
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

func (c *HTTP2Conn) Close() error {
	c.closeOnce.Do(func() {
		c.stateAccess.Lock()
		c.closed = true
		c.err = net.ErrClosed
		reader := c.reader
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

func closePipeWriter(writer io.Writer, err error) {
	if pipeWriter, isPipeWriter := writer.(*io.PipeWriter); isPipeWriter {
		_ = pipeWriter.CloseWithError(err)
	}
}

func (c *HTTP2Conn) LocalAddr() net.Addr {
	return M.Socksaddr{}
}

func (c *HTTP2Conn) RemoteAddr() net.Addr {
	return M.Socksaddr{}
}

func (c *HTTP2Conn) SetDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *HTTP2Conn) SetReadDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *HTTP2Conn) SetWriteDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *HTTP2Conn) NeedAdditionalReadDeadline() bool {
	return true
}

type ServerHTTPConn struct {
	*HTTP2Conn
	Flusher http.Flusher
}

func (c *ServerHTTPConn) Write(b []byte) (n int, err error) {
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()
	writer, err := c.writerForWrite()
	if err != nil {
		return 0, err
	}
	n, err = writer.Write(b)
	if err == nil {
		c.Flusher.Flush()
	}
	return n, baderror.WrapH2(err)
}

type HTTP2ConnWrapper struct {
	N.ExtendedConn
	access sync.Mutex
	closed atomic.Bool
}

func NewHTTP2Wrapper(conn net.Conn) *HTTP2ConnWrapper {
	return &HTTP2ConnWrapper{
		ExtendedConn: bufio.NewExtendedConn(conn),
	}
}

func (w *HTTP2ConnWrapper) Write(p []byte) (n int, err error) {
	w.access.Lock()
	defer w.access.Unlock()
	if w.closed.Load() {
		return 0, net.ErrClosed
	}
	return w.ExtendedConn.Write(p)
}

func (w *HTTP2ConnWrapper) WriteBuffer(buffer *buf.Buffer) error {
	w.access.Lock()
	defer w.access.Unlock()
	if w.closed.Load() {
		buffer.Release()
		return net.ErrClosed
	}
	return w.ExtendedConn.WriteBuffer(buffer)
}

func (w *HTTP2ConnWrapper) CloseWrapper() {
	w.closed.Store(true)
}

func (w *HTTP2ConnWrapper) Close() error {
	w.CloseWrapper()
	return w.ExtendedConn.Close()
}

func (w *HTTP2ConnWrapper) Upstream() any {
	return w.ExtendedConn
}

func DupContext(ctx context.Context) context.Context {
	id, loaded := log.IDFromContext(ctx)
	if !loaded {
		return context.Background()
	}
	return log.ContextWithID(context.Background(), id)
}
