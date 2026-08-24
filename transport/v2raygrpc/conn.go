package v2raygrpc

import (
	"context"
	"net"
	"os"
	"sync"
	"time"

	"github.com/sagernet/sing/common/baderror"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

var (
	_ net.Conn      = (*GRPCConn)(nil)
	_ N.WriteCloser = (*GRPCConn)(nil)
)

type grpcRecvResult struct {
	hunk *Hunk
	err  error
}

type GRPCConn struct {
	GunService
	cache          []byte
	cancel         context.CancelCauseFunc
	readAccess     sync.Mutex
	writeAccess    sync.Mutex
	writeCloseOnce sync.Once
	writeCloseErr  error
	done           chan struct{}
	closeOnce      sync.Once
}

func NewGRPCConn(service GunService, cancel context.CancelCauseFunc) *GRPCConn {
	//nolint:staticcheck
	if client, isClient := service.(GunService_TunClient); isClient {
		service = &clientConnWrapper{client}
	}
	return &GRPCConn{
		GunService: service,
		cancel:     cancel,
		done:       make(chan struct{}),
	}
}

func (c *GRPCConn) Read(b []byte) (n int, err error) {
	if len(b) == 0 {
		return 0, nil
	}
	c.readAccess.Lock()
	defer c.readAccess.Unlock()
	select {
	case <-c.done:
		return 0, net.ErrClosed
	default:
	}
	if len(c.cache) > 0 {
		n = copy(b, c.cache)
		c.cache = c.cache[n:]
		return n, nil
	}
	recvResult := make(chan grpcRecvResult, 1)
	go func() {
		hunk, recvErr := c.Recv()
		recvResult <- grpcRecvResult{hunk, recvErr}
	}()
	var result grpcRecvResult
	select {
	case <-c.done:
		return 0, net.ErrClosed
	case result = <-recvResult:
	}
	select {
	case <-c.done:
		return 0, net.ErrClosed
	default:
	}
	if err = baderror.WrapGRPC(result.err); err != nil {
		return 0, err
	}
	if result.hunk == nil {
		return 0, net.ErrClosed
	}
	n = copy(b, result.hunk.Data)
	if n < len(result.hunk.Data) {
		c.cache = result.hunk.Data[n:]
	}
	return n, nil
}

func (c *GRPCConn) Write(b []byte) (n int, err error) {
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()
	select {
	case <-c.done:
		return 0, net.ErrClosed
	default:
	}
	if err = baderror.WrapGRPC(c.Send(&Hunk{Data: b})); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *GRPCConn) CloseWrite() error {
	c.writeAccess.Lock()
	defer c.writeAccess.Unlock()
	c.writeCloseOnce.Do(func() {
		if writeCloser, loaded := c.GunService.(N.WriteCloser); loaded {
			c.writeCloseErr = writeCloser.CloseWrite()
		}
	})
	return c.writeCloseErr
}

func (c *GRPCConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.cancel != nil {
			c.cancel(net.ErrClosed)
		}
	})
	return nil
}

func (c *GRPCConn) Done() <-chan struct{} {
	return c.done
}

func (c *GRPCConn) LocalAddr() net.Addr {
	return M.Socksaddr{}
}

func (c *GRPCConn) RemoteAddr() net.Addr {
	return M.Socksaddr{}
}

func (c *GRPCConn) SetDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *GRPCConn) SetReadDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *GRPCConn) SetWriteDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *GRPCConn) NeedAdditionalReadDeadline() bool {
	return true
}

func (c *GRPCConn) Upstream() any {
	return c.GunService
}

var _ N.WriteCloser = (*clientConnWrapper)(nil)

type clientConnWrapper struct {
	GunService_TunClient
}

func (c *clientConnWrapper) CloseWrite() error {
	return c.CloseSend()
}
