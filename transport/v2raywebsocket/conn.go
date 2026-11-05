package v2raywebsocket

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/ws"
	"github.com/sagernet/ws/wsutil"
)

type WebsocketConn struct {
	net.Conn
	*Writer
	state          ws.State
	reader         *wsutil.Reader
	controlHandler wsutil.FrameHandlerFunc
	remoteAddr     net.Addr
	readAccess     sync.Mutex
	writeAccess    sync.Mutex
	closed         atomic.Bool
	closeOnce      sync.Once
	closeErr       error
}

func NewConn(conn net.Conn, remoteAddr net.Addr, state ws.State) *WebsocketConn {
	websocketConn := &WebsocketConn{
		Conn:       conn,
		state:      state,
		remoteAddr: remoteAddr,
		Writer:     NewWriter(conn, state),
	}
	websocketConn.controlHandler = websocketConn.handleControl
	websocketConn.reader = &wsutil.Reader{
		Source:          conn,
		State:           state,
		SkipHeaderCheck: false,
		OnIntermediate:  websocketConn.controlHandler,
	}
	return websocketConn
}

func (c *WebsocketConn) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		_ = c.Conn.SetWriteDeadline(time.Now().Add(C.TCPTimeout))
		c.writeAccess.Lock()
		frameErr := c.Writer.writeFrame(ws.OpClose, ws.NewCloseFrameBody(ws.StatusNormalClosure, ""))
		c.writeAccess.Unlock()
		closeErr := c.Conn.Close()
		if frameErr != nil {
			c.closeErr = frameErr
		} else {
			c.closeErr = closeErr
		}
	})
	return c.closeErr
}

func (c *WebsocketConn) Read(b []byte) (n int, err error) {
	if len(b) == 0 {
		return 0, nil
	}
	c.readAccess.Lock()
	defer c.readAccess.Unlock()
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	var header ws.Header
	for {
		n, err = c.reader.Read(b)
		if n > 0 {
			return n, nil
		}
		if !E.IsMulti(err, io.EOF, wsutil.ErrNoFrameAdvance) {
			return 0, wrapWsError(err)
		}
		header, err = wrapWsError0(c.reader.NextFrame())
		if err != nil {
			return 0, err
		}
		if header.OpCode.IsControl() {
			if header.Length > ws.MaxControlFramePayloadSize {
				return 0, wsutil.ErrFrameTooLarge
			}
			if err = wrapWsError(c.controlHandler(header, c.reader)); err != nil {
				return 0, err
			}
			continue
		}
		if header.OpCode&ws.OpBinary == 0 {
			if err = wrapWsError(c.reader.Discard()); err != nil {
				return 0, err
			}
			continue
		}
	}
}

func (c *WebsocketConn) Write(p []byte) (n int, err error) {
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	if err = wrapWsError(c.Writer.Write(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *WebsocketConn) WriteBuffer(buffer *buf.Buffer) error {
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()
	if c.closed.Load() {
		buffer.Release()
		return net.ErrClosed
	}
	return wrapWsError(c.Writer.WriteBuffer(buffer))
}

func (c *WebsocketConn) handleControl(header ws.Header, reader io.Reader) error {
	payload := make([]byte, int(header.Length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return err
	}
	switch header.OpCode {
	case ws.OpPing:
		return c.writeControl(ws.OpPong, payload)
	case ws.OpPong:
		return nil
	case ws.OpClose:
		if len(payload) == 0 {
			if err := c.writeControl(ws.OpClose, nil); err != nil {
				return err
			}
			return wsutil.ClosedError{Code: ws.StatusNoStatusRcvd}
		}
		if len(payload) == 1 {
			err := E.New("invalid close frame payload length")
			_ = c.writeControl(ws.OpClose, ws.NewCloseFrameBody(ws.StatusProtocolError, err.Error()))
			return err
		}
		code, reason := ws.ParseCloseFrameData(payload)
		if err := ws.CheckCloseFrameData(code, reason); err != nil {
			_ = c.writeControl(ws.OpClose, ws.NewCloseFrameBody(ws.StatusProtocolError, err.Error()))
			return err
		}
		if err := c.writeControl(ws.OpClose, payload); err != nil {
			return err
		}
		return wsutil.ClosedError{Code: code, Reason: reason}
	default:
		return wsutil.ErrNotControlFrame
	}
}

func (c *WebsocketConn) writeControl(opCode ws.OpCode, payload []byte) error {
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()
	if c.closed.Load() {
		return net.ErrClosed
	}
	return c.Writer.writeFrame(opCode, payload)
}

func (c *WebsocketConn) RemoteAddr() net.Addr {
	if c.remoteAddr != nil {
		return c.remoteAddr
	}
	return c.Conn.RemoteAddr()
}

func (c *WebsocketConn) SetDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *WebsocketConn) SetReadDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *WebsocketConn) SetWriteDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *WebsocketConn) NeedAdditionalReadDeadline() bool {
	return true
}

func (c *WebsocketConn) Upstream() any {
	return c.Conn
}

type EarlyWebsocketConn struct {
	*Client
	ctx         context.Context
	cancel      context.CancelCauseFunc
	conn        atomic.Pointer[WebsocketConn]
	setupAccess sync.Mutex
	stateAccess sync.Mutex
	ready       chan struct{}
	readyOnce   sync.Once
	setupDone   bool
	closed      bool
	err         error
	pending     *WebsocketConn
	closeOnce   sync.Once
	closeErr    error
}

func newEarlyWebsocketConn(client *Client, ctx context.Context) *EarlyWebsocketConn {
	ownedContext, cancel := context.WithCancelCause(ctx)
	return &EarlyWebsocketConn{
		Client: client,
		ctx:    ownedContext,
		cancel: cancel,
		ready:  make(chan struct{}),
	}
}

func (c *EarlyWebsocketConn) Read(b []byte) (n int, err error) {
	if len(b) == 0 {
		return 0, nil
	}
	conn := c.conn.Load()
	if conn == nil {
		<-c.ready
		c.stateAccess.Lock()
		err = c.err
		closed := c.closed
		conn = c.conn.Load()
		c.stateAccess.Unlock()
		if closed {
			return 0, net.ErrClosed
		}
		if err != nil {
			return 0, err
		}
		if conn == nil {
			return 0, net.ErrClosed
		}
	}
	return wrapWsError0(conn.Read(b))
}

func (c *EarlyWebsocketConn) writeRequest(content []byte) (*WebsocketConn, error) {
	var (
		earlyData []byte
		lateData  []byte
		conn      *WebsocketConn
		err       error
	)
	if len(content) > int(c.maxEarlyData) {
		earlyData = content[:c.maxEarlyData]
		lateData = content[c.maxEarlyData:]
	} else {
		earlyData = content
	}
	if len(earlyData) > 0 {
		earlyDataString := base64.RawURLEncoding.EncodeToString(earlyData)
		if c.earlyDataHeaderName == "" {
			requestURL := c.requestURL
			requestURL.Path += earlyDataString
			conn, err = c.dialContext(c.ctx, &requestURL, c.headers)
		} else {
			headers := c.headers.Clone()
			headers.Set(c.earlyDataHeaderName, earlyDataString)
			conn, err = c.dialContext(c.ctx, &c.requestURL, headers)
		}
	} else {
		conn, err = c.dialContext(c.ctx, &c.requestURL, c.headers)
	}
	if err != nil {
		return nil, err
	}
	c.stateAccess.Lock()
	if c.closed || c.setupDone {
		c.stateAccess.Unlock()
		conn.Close()
		return nil, net.ErrClosed
	}
	c.pending = conn
	c.stateAccess.Unlock()
	if len(lateData) > 0 {
		if _, err = conn.Write(lateData); err != nil {
			conn.Close()
			return conn, err
		}
	}
	return conn, nil
}

func (c *EarlyWebsocketConn) finishSetup(conn *WebsocketConn, setupErr error) error {
	c.stateAccess.Lock()
	if c.pending == conn {
		c.pending = nil
	}
	if c.closed || c.setupDone {
		c.stateAccess.Unlock()
		if conn != nil {
			conn.Close()
		}
		return net.ErrClosed
	}
	c.setupDone = true
	c.err = setupErr
	if setupErr == nil {
		c.conn.Store(conn)
	}
	c.stateAccess.Unlock()
	c.readyOnce.Do(func() { close(c.ready) })
	if setupErr != nil && conn != nil {
		conn.Close()
	}
	return setupErr
}

func (c *EarlyWebsocketConn) terminalError() error {
	c.stateAccess.Lock()
	defer c.stateAccess.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	return c.err
}

func (c *EarlyWebsocketConn) Write(b []byte) (n int, err error) {
	conn := c.conn.Load()
	if conn != nil {
		return wrapWsError0(conn.Write(b))
	}
	c.setupAccess.Lock()
	defer c.setupAccess.Unlock()
	conn = c.conn.Load()
	if conn != nil {
		return wrapWsError0(conn.Write(b))
	}
	if err = c.terminalError(); err != nil {
		return 0, err
	}
	conn, err = c.writeRequest(b)
	err = c.finishSetup(conn, err)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *EarlyWebsocketConn) WriteBuffer(buffer *buf.Buffer) error {
	conn := c.conn.Load()
	if conn != nil {
		return wrapWsError(conn.WriteBuffer(buffer))
	}
	c.setupAccess.Lock()
	defer c.setupAccess.Unlock()
	conn = c.conn.Load()
	if conn != nil {
		return wrapWsError(conn.WriteBuffer(buffer))
	}
	if err := c.terminalError(); err != nil {
		buffer.Release()
		return err
	}
	defer buffer.Release()
	conn, err := c.writeRequest(buffer.Bytes())
	return c.finishSetup(conn, err)
}

func (c *EarlyWebsocketConn) Close() error {
	c.closeOnce.Do(func() {
		c.stateAccess.Lock()
		c.closed = true
		c.setupDone = true
		c.err = net.ErrClosed
		conn := c.conn.Load()
		pending := c.pending
		c.pending = nil
		c.stateAccess.Unlock()
		c.cancel(net.ErrClosed)
		c.readyOnce.Do(func() { close(c.ready) })
		if pending != nil && pending != conn {
			c.closeErr = pending.Close()
		}
		if conn != nil {
			c.closeErr = conn.Close()
		}
	})
	return c.closeErr
}

func (c *EarlyWebsocketConn) LocalAddr() net.Addr {
	conn := c.conn.Load()
	if conn == nil {
		return M.Socksaddr{}
	}
	return conn.LocalAddr()
}

func (c *EarlyWebsocketConn) RemoteAddr() net.Addr {
	conn := c.conn.Load()
	if conn == nil {
		return M.Socksaddr{}
	}
	return conn.RemoteAddr()
}

func (c *EarlyWebsocketConn) SetDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *EarlyWebsocketConn) SetReadDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *EarlyWebsocketConn) SetWriteDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *EarlyWebsocketConn) NeedAdditionalReadDeadline() bool {
	return true
}

func (c *EarlyWebsocketConn) Upstream() any {
	return common.PtrOrNil(c.conn.Load())
}

func (c *EarlyWebsocketConn) LazyHeadroom() bool {
	return c.conn.Load() == nil
}

func wrapWsError(err error) error {
	if err == nil {
		return nil
	}
	var closedErr wsutil.ClosedError
	if errors.As(err, &closedErr) {
		if closedErr.Code == ws.StatusNormalClosure || closedErr.Code == ws.StatusNoStatusRcvd {
			err = io.EOF
		}
	}
	return err
}

func wrapWsError0[T any](value T, err error) (T, error) {
	if err == nil {
		return value, nil
	}
	return value, wrapWsError(err)
}
