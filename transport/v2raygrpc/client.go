package v2raygrpc

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/common/tls"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/common"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"golang.org/x/net/http2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

var _ adapter.V2RayClientTransport = (*Client)(nil)

type clientDialAttempt struct {
	done chan struct{}
	conn *grpc.ClientConn
	err  error
}

type Client struct {
	ctx         context.Context
	cancel      context.CancelCauseFunc
	serverAddr  string
	serviceName string
	dialOptions []grpc.DialOption
	stateAccess sync.Mutex
	conn        *grpc.ClientConn
	attempt     *clientDialAttempt
	closed      bool
	closeOnce   sync.Once
	closeErr    error
}

func NewClient(ctx context.Context, dialer N.Dialer, serverAddr M.Socksaddr, options option.V2RayGRPCOptions, tlsConfig tls.Config) (adapter.V2RayClientTransport, error) {
	var dialOptions []grpc.DialOption
	if tlsConfig != nil {
		if !common.Contains(tlsConfig.NextProtos(), http2.NextProtoTLS) {
			tlsConfig.SetNextProtos(append([]string{http2.NextProtoTLS}, tlsConfig.NextProtos()...))
		}
		dialOptions = append(dialOptions, grpc.WithTransportCredentials(NewTLSTransportCredentials(tlsConfig)))
	} else {
		dialOptions = append(dialOptions, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	if options.IdleTimeout > 0 {
		dialOptions = append(dialOptions, grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                time.Duration(options.IdleTimeout),
			Timeout:             time.Duration(options.PingTimeout),
			PermitWithoutStream: options.PermitWithoutStream,
		}))
	}
	dialOptions = append(dialOptions, grpc.WithConnectParams(grpc.ConnectParams{
		Backoff: backoff.Config{
			BaseDelay:  500 * time.Millisecond,
			Multiplier: 1.5,
			Jitter:     0.2,
			MaxDelay:   19 * time.Second,
		},
		MinConnectTimeout: 5 * time.Second,
	}))
	dialOptions = append(dialOptions, grpc.WithContextDialer(func(ctx context.Context, server string) (net.Conn, error) {
		return dialer.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr(server))
	}))
	//nolint:staticcheck
	dialOptions = append(dialOptions, grpc.WithReturnConnectionError())
	clientContext, cancel := context.WithCancelCause(ctx)
	return &Client{
		ctx:         clientContext,
		cancel:      cancel,
		serverAddr:  serverAddr.String(),
		serviceName: options.ServiceName,
		dialOptions: dialOptions,
	}, nil
}

func (c *Client) startConnectLocked() *clientDialAttempt {
	attempt := &clientDialAttempt{done: make(chan struct{})}
	c.attempt = attempt
	go func() {
		//nolint:staticcheck
		conn, err := grpc.DialContext(c.ctx, c.serverAddr, c.dialOptions...)
		c.stateAccess.Lock()
		if c.closed || c.attempt != attempt {
			if err == nil {
				attempt.err = net.ErrClosed
			} else {
				attempt.err = err
			}
		} else {
			attempt.conn = conn
			attempt.err = err
			c.attempt = nil
			if err == nil {
				c.conn = conn
			}
		}
		closeLate := conn != nil && (err != nil || c.closed || attempt.conn != conn)
		c.stateAccess.Unlock()
		if closeLate {
			_ = conn.Close()
		}
		close(attempt.done)
	}()
	return attempt
}

func (c *Client) connect(ctx context.Context) (*grpc.ClientConn, error) {
	for {
		c.stateAccess.Lock()
		if c.closed {
			c.stateAccess.Unlock()
			return nil, net.ErrClosed
		}
		if cause := context.Cause(c.ctx); cause != nil {
			c.stateAccess.Unlock()
			return nil, cause
		}
		if c.conn != nil && c.conn.GetState() != connectivity.Shutdown {
			conn := c.conn
			c.stateAccess.Unlock()
			return conn, nil
		}
		c.conn = nil
		attempt := c.attempt
		if attempt == nil {
			attempt = c.startConnectLocked()
		}
		c.stateAccess.Unlock()

		select {
		case <-attempt.done:
			if attempt.err != nil {
				return nil, attempt.err
			}
			c.stateAccess.Lock()
			valid := !c.closed && c.conn == attempt.conn && attempt.conn != nil && attempt.conn.GetState() != connectivity.Shutdown
			c.stateAccess.Unlock()
			if valid {
				return attempt.conn, nil
			}
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-c.ctx.Done():
			cause := context.Cause(c.ctx)
			if cause == nil {
				cause = net.ErrClosed
			}
			return nil, cause
		}
	}
}

func (c *Client) DialContext(ctx context.Context) (net.Conn, error) {
	clientConn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	client := NewGunServiceClient(clientConn).(GunServiceCustomNameClient)
	streamContext, cancel := context.WithCancelCause(ctx)
	stream, err := client.TunCustomName(streamContext, c.serviceName)
	if err != nil {
		cancel(err)
		return nil, err
	}
	return NewGRPCConn(stream, cancel), nil
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.stateAccess.Lock()
		c.closed = true
		conn := c.conn
		c.conn = nil
		c.stateAccess.Unlock()
		c.cancel(net.ErrClosed)
		if conn != nil {
			c.closeErr = conn.Close()
		}
	})
	return c.closeErr
}
