package v2raygrpc

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"google.golang.org/grpc/metadata"
)

type blockingGunService struct {
	ctx         context.Context
	recvStarted chan struct{}
	releaseRecv chan struct{}
	startOnce   sync.Once
}

func (s *blockingGunService) Context() context.Context { return s.ctx }
func (s *blockingGunService) Send(*Hunk) error         { return nil }
func (s *blockingGunService) Recv() (*Hunk, error) {
	s.startOnce.Do(func() { close(s.recvStarted) })
	<-s.releaseRecv
	return nil, io.EOF
}

func TestGRPCConnCloseUnblocksRecv(t *testing.T) {
	service := &blockingGunService{
		ctx:         context.Background(),
		recvStarted: make(chan struct{}),
		releaseRecv: make(chan struct{}),
	}
	conn := NewGRPCConn(service, nil)
	readResult := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		readResult <- err
	}()
	<-service.recvStarted
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-readResult:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Read error = %v, want net.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Read remained blocked in Recv after Close")
	}
	close(service.releaseRecv)
}

type cancelTestStream struct {
	ctx context.Context
}

func (s *cancelTestStream) Context() context.Context     { return s.ctx }
func (s *cancelTestStream) Send(*Hunk) error             { return nil }
func (s *cancelTestStream) Recv() (*Hunk, error)         { <-s.ctx.Done(); return nil, context.Cause(s.ctx) }
func (s *cancelTestStream) SetHeader(metadata.MD) error  { return nil }
func (s *cancelTestStream) SendHeader(metadata.MD) error { return nil }
func (s *cancelTestStream) SetTrailer(metadata.MD)       {}
func (s *cancelTestStream) SendMsg(any) error            { return nil }
func (s *cancelTestStream) RecvMsg(any) error            { <-s.ctx.Done(); return context.Cause(s.ctx) }

type cancelTestHandler struct {
	entered  chan struct{}
	canceled chan error
}

func (h *cancelTestHandler) NewConnectionEx(ctx context.Context, _ net.Conn, _ M.Socksaddr, _ M.Socksaddr, _ N.CloseHandlerFunc) {
	close(h.entered)
	<-ctx.Done()
	h.canceled <- context.Cause(ctx)
}

func TestGRPCServerStreamCancellationFinishesTunAndHandler(t *testing.T) {
	streamContext, cancel := context.WithCancel(context.Background())
	handler := &cancelTestHandler{entered: make(chan struct{}), canceled: make(chan error, 1)}
	server := &Server{handler: handler}
	tunResult := make(chan error, 1)
	go func() { tunResult <- server.Tun(&cancelTestStream{ctx: streamContext}) }()
	<-handler.entered
	cancel()
	select {
	case err := <-handler.canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handler context cause = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("handler context was not canceled")
	}
	select {
	case err := <-tunResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Tun error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Tun did not finish after stream cancellation")
	}
}
