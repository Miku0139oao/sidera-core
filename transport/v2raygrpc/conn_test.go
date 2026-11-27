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

type halfCloseGunService struct {
	blockingGunService
	closeWrite chan struct{}
	closeOnce  sync.Once
}

func (s *halfCloseGunService) CloseWrite() error {
	s.closeOnce.Do(func() { close(s.closeWrite) })
	return nil
}

func TestGRPCConnForwardsCloseWrite(t *testing.T) {
	service := &halfCloseGunService{
		blockingGunService: blockingGunService{ctx: context.Background()},
		closeWrite:         make(chan struct{}),
	}
	conn := NewGRPCConn(service, nil)
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-service.closeWrite:
	case <-time.After(time.Second):
		t.Fatal("CloseWrite was not forwarded")
	}
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

type closeThenReportHandler struct {
	entered chan struct{}
	release chan struct{}
	cause   chan error
	once    sync.Once
}

func (h *closeThenReportHandler) NewConnectionEx(ctx context.Context, conn net.Conn, _ M.Socksaddr, _ M.Socksaddr, onClose N.CloseHandlerFunc) {
	// Close the transport conn first: this closes conn.Done and races with done.
	conn.Close()
	h.once.Do(func() { close(h.entered) })
	<-h.release
	h.cause <- context.Cause(ctx)
	if onClose != nil {
		onClose(net.ErrClosed)
	}
}

func TestGRPCServerTunWaitsForHandlerAfterConnClose(t *testing.T) {
	handler := &closeThenReportHandler{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		cause:   make(chan error, 1),
	}
	server := &Server{handler: handler}
	tunResult := make(chan error, 1)
	go func() { tunResult <- server.Tun(&cancelTestStream{ctx: context.Background()}) }()
	select {
	case <-handler.entered:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	select {
	case err := <-tunResult:
		t.Fatalf("Tun returned before handler finished: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(handler.release)
	select {
	case cause := <-handler.cause:
		if !errors.Is(cause, net.ErrClosed) {
			t.Fatalf("route context cause = %v, want net.ErrClosed", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("handler did not observe route cancellation")
	}
	select {
	case err := <-tunResult:
		if err != nil {
			t.Fatalf("Tun error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Tun did not finish after handler reported close")
	}
}

type stuckRecvService struct {
	ctx      context.Context
	started  chan struct{}
	release  chan struct{}
	startOne sync.Once
}

func (s *stuckRecvService) Context() context.Context { return s.ctx }
func (s *stuckRecvService) Send(*Hunk) error         { return nil }
func (s *stuckRecvService) Recv() (*Hunk, error) {
	s.startOne.Do(func() { close(s.started) })
	// Deliberately ignore the stream context, like a Recv wedged in the gRPC
	// runtime: Read must still return when the stream context ends.
	<-s.release
	return &Hunk{Data: []byte("late")}, nil
}

func TestGRPCConnReadUnblocksOnStreamContext(t *testing.T) {
	streamContext, cancelStream := context.WithCancel(context.Background())
	service := &stuckRecvService{
		ctx:     streamContext,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	conn := NewGRPCConn(service, nil)
	readResult := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 4))
		readResult <- err
	}()
	select {
	case <-service.started:
	case <-time.After(time.Second):
		t.Fatal("Recv was not started")
	}
	cancelStream()
	select {
	case err := <-readResult:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Read error = %v, want net.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Read did not return when the stream context ended")
	}
	close(service.release)
}
