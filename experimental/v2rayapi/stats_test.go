package v2rayapi

import (
	"context"
	"net"
	"testing"

	"github.com/Miku0139oao/sidera-core/option"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestXrayStatsServiceCompatibility(t *testing.T) {
	require.Equal(t, "xray.app.stats.command.StatsService", StatsService_ServiceDesc.ServiceName)
	require.Equal(t, "/xray.app.stats.command.StatsService/QueryStats", StatsService_QueryStats_FullMethodName)
	require.Equal(t, "xray.app.stats.command.StatsService", string(File_experimental_v2rayapi_stats_proto.Services().ByName("StatsService").FullName()))

	service := NewStatsService(option.V2RayStatsServiceOptions{Enabled: true})
	service.loadOrCreateCounter("inbound>>>proxy>>>traffic>>>uplink").Store(42)
	service.loadOrCreateCounter("outbound>>>direct>>>traffic>>>uplink").Store(7)

	response, err := service.QueryStats(context.Background(), &QueryStatsRequest{Pattern: "inbound>>>"})
	require.NoError(t, err)
	require.Len(t, response.Stat, 1)
	require.Equal(t, int64(42), response.Stat[0].Value)

	response, err = service.QueryStats(context.Background(), &QueryStatsRequest{Pattern: "inbound>>>", Reset_: true})
	require.NoError(t, err)
	require.Equal(t, int64(42), response.Stat[0].Value)

	response, err = service.QueryStats(context.Background(), &QueryStatsRequest{Pattern: "inbound>>>"})
	require.NoError(t, err)
	require.Equal(t, int64(0), response.Stat[0].Value)

	_, err = service.GetStats(context.Background(), &GetStatsRequest{Name: "missing"})
	require.Equal(t, codes.NotFound, status.Code(err))

	service.loadOrCreateCounter("user>>>alice>>>traffic>>>uplink").Store(9)
	response, err = service.QueryStats(context.Background(), &QueryStatsRequest{
		Patterns: []string{"user>>>", "alice"},
		Reset_:   true,
	})
	require.NoError(t, err)
	require.Len(t, response.Stat, 1)
	require.Equal(t, int64(9), response.Stat[0].Value)
}

func TestXrayOnlineStatsCompatibility(t *testing.T) {
	service := NewStatsService(option.V2RayStatsServiceOptions{
		Enabled:     true,
		UsersOnline: []string{"alice"},
	})
	releaseFirst := service.trackOnline("alice", "198.51.100.10")
	releaseSecond := service.trackOnline("alice", "198.51.100.10")
	releaseThird := service.trackOnline("alice", "2001:db8::10")

	response, err := service.GetUsersStats(context.Background(), &GetUsersStatsRequest{})
	require.NoError(t, err)
	require.Len(t, response.Users, 1)
	require.Equal(t, "alice", response.Users[0].Email)
	require.Len(t, response.Users[0].Ips, 2)

	online, err := service.GetStatsOnline(context.Background(), &GetStatsRequest{Name: "user>>>alice>>>online"})
	require.NoError(t, err)
	require.Equal(t, int64(2), online.Stat.Value)

	releaseFirst()
	online, err = service.GetStatsOnline(context.Background(), &GetStatsRequest{Name: "user>>>alice>>>online"})
	require.NoError(t, err)
	require.Equal(t, int64(2), online.Stat.Value)

	releaseSecond()
	releaseThird()
	response, err = service.GetUsersStats(context.Background(), &GetUsersStatsRequest{})
	require.NoError(t, err)
	require.Empty(t, response.Users)
}

func TestXrayStatsGRPCWireCompatibility(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	service := NewStatsService(option.V2RayStatsServiceOptions{Enabled: true})
	service.loadOrCreateCounter("inbound>>>proxy>>>traffic>>>uplink").Store(42)
	RegisterStatsServiceServer(grpcServer, service)
	go grpcServer.Serve(listener)
	t.Cleanup(func() {
		grpcServer.Stop()
		listener.Close()
	})

	connection, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { connection.Close() })

	response, err := NewStatsServiceClient(connection).QueryStats(context.Background(), &QueryStatsRequest{})
	require.NoError(t, err)
	require.Len(t, response.Stat, 1)
	require.Equal(t, int64(42), response.Stat[0].Value)
}

type testHalfCloseConn struct {
	net.Conn
	writeClosed bool
}

func (c *testHalfCloseConn) CloseWrite() error {
	c.writeClosed = true
	return nil
}

func TestOnlineConnPreservesHalfClose(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	upstream := &testHalfCloseConn{Conn: client}
	conn := &onlineConn{Conn: upstream, release: func() {}}
	require.NoError(t, conn.CloseWrite())
	require.True(t, upstream.writeClosed)
}
