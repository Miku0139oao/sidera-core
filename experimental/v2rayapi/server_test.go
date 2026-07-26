package v2rayapi

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestServerCloseOwnsGRPCListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcServer := grpc.NewServer()
	server := &Server{grpcServer: grpcServer, tcpListener: listener}
	go grpcServer.Serve(listener)
	require.NoError(t, server.Close())
}
