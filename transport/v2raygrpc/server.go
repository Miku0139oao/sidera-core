package v2raygrpc

import (
	"context"
	"net"
	"os"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/common/tls"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"golang.org/x/net/http2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
)

var _ adapter.V2RayServerTransport = (*Server)(nil)

type Server struct {
	ctx     context.Context
	logger  logger.ContextLogger
	handler adapter.V2RayServerTransportHandler
	server  *grpc.Server
}

func NewServer(ctx context.Context, logger logger.ContextLogger, options option.V2RayGRPCOptions, tlsConfig tls.ServerConfig, handler adapter.V2RayServerTransportHandler) (*Server, error) {
	var serverOptions []grpc.ServerOption
	if tlsConfig != nil {
		if !common.Contains(tlsConfig.NextProtos(), http2.NextProtoTLS) {
			tlsConfig.SetNextProtos(append([]string{"h2"}, tlsConfig.NextProtos()...))
		}
		serverOptions = append(serverOptions, grpc.Creds(NewTLSTransportCredentials(tlsConfig)))
	}
	if options.IdleTimeout > 0 {
		serverOptions = append(serverOptions, grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    time.Duration(options.IdleTimeout),
			Timeout: time.Duration(options.PingTimeout),
		}))
	}
	server := &Server{ctx, logger, handler, grpc.NewServer(serverOptions...)}
	RegisterGunServiceCustomNameServer(server.server, server, options.ServiceName)
	return server, nil
}

func (s *Server) Tun(server GunService_TunServer) error {
	streamContext := server.Context()
	routeContext, routeCancel := context.WithCancelCause(log.ContextWithNewID(streamContext))
	conn := NewGRPCConn(server, routeCancel)
	var source M.Socksaddr
	if remotePeer, loaded := peer.FromContext(server.Context()); loaded {
		source = M.SocksaddrFromNet(remotePeer.Addr)
	}

	done := make(chan struct{})
	go s.handler.NewConnectionEx(routeContext, conn, source, M.Socksaddr{}, N.OnceClose(func(it error) {
		close(done)
	}))
	select {
	case <-done:
		routeCancel(nil)
		conn.Close()
		return nil
	case <-conn.Done():
		routeCancel(net.ErrClosed)
		return nil
	case <-streamContext.Done():
		cause := context.Cause(streamContext)
		routeCancel(cause)
		conn.Close()
		return cause
	}
}

func (s *Server) mustEmbedUnimplementedGunServiceServer() {
}

func (s *Server) Network() []string {
	return []string{N.NetworkTCP}
}

func (s *Server) Serve(listener net.Listener) error {
	return s.server.Serve(listener)
}

func (s *Server) ServePacket(listener net.PacketConn) error {
	return os.ErrInvalid
}

func (s *Server) Close() error {
	s.server.Stop()
	return nil
}
