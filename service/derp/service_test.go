//go:build with_gvisor

package derp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/log"
	"github.com/sagernet/tailscale/derp/derpserver"
	"github.com/sagernet/tailscale/types/key"
)

func newDERPLifecycleTestService(t *testing.T) (*Service, string, string) {
	t.Helper()
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		tcpListener.Close()
		t.Fatal(err)
	}
	runContext, cancel := context.WithCancel(context.Background())
	service := &Service{
		logger:     log.NewNOPFactory().Logger(),
		httpServer: &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})},
		stunConn:   udpConn,
		runContext: runContext,
		runCancel:  cancel,
		server: derpserver.New(key.NewNode(), func(string, ...any) {
		}),
		closeDone: make(chan struct{}),
	}
	httpServer := service.httpServer
	service.workerGroup.Add(2)
	go func() {
		defer service.workerGroup.Done()
		_ = httpServer.Serve(tcpListener)
	}()
	go func() {
		defer service.workerGroup.Done()
		service.loopSTUNPacket(udpConn)
	}()
	return service, tcpListener.Addr().String(), udpConn.LocalAddr().String()
}

func assertDERPAddressesRebind(t *testing.T, tcpAddress, udpAddress string) {
	t.Helper()
	tcpListener, err := net.Listen("tcp", tcpAddress)
	if err != nil {
		t.Fatalf("TCP address did not rebind: %v", err)
	}
	_ = tcpListener.Close()
	udpAddressValue, err := net.ResolveUDPAddr("udp", udpAddress)
	if err != nil {
		t.Fatal(err)
	}
	udpConn, err := net.ListenUDP("udp", udpAddressValue)
	if err != nil {
		t.Fatalf("STUN address did not rebind: %v", err)
	}
	_ = udpConn.Close()
}

func TestDERPFailedStartRollsBackOwnedResources(t *testing.T) {
	service, tcpAddress, udpAddress := newDERPLifecycleTestService(t)
	startError := errors.New("injected start failure")
	service.lifecycleAccess.Lock()
	err := service.failStartLocked(startError)
	service.lifecycleAccess.Unlock()
	if !errors.Is(err, startError) {
		t.Fatalf("missing start failure: %v", err)
	}
	assertDERPAddressesRebind(t, tcpAddress, udpAddress)
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDERPCloseAllowsRepeatedRebindCycles(t *testing.T) {
	for range 5 {
		service, tcpAddress, udpAddress := newDERPLifecycleTestService(t)
		closed := make(chan error, 1)
		go func() { closed <- service.Close() }()
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("DERP Close did not join HTTP/STUN/server resources")
		}
		assertDERPAddressesRebind(t, tcpAddress, udpAddress)
	}
}
