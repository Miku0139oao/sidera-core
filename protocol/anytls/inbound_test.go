package anytls

import (
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/common/listener"
	"github.com/anytls/sing-anytls/padding"
	"github.com/sagernet/sing/common/logger"
	N "github.com/sagernet/sing/common/network"
)

type blockingStreamRouter struct {
	entered  chan struct{}
	canceled chan struct{}
	release  chan struct{}
}

func (r *blockingStreamRouter) RouteConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext) error {
	r.RouteConnectionEx(ctx, conn, metadata, nil)
	return nil
}

func (r *blockingStreamRouter) RoutePacketConnection(context.Context, N.PacketConn, adapter.InboundContext) error {
	return errors.New("unexpected packet route")
}

func (r *blockingStreamRouter) RouteConnectionEx(ctx context.Context, _ net.Conn, _ adapter.InboundContext, _ N.CloseHandlerFunc) {
	close(r.entered)
	<-ctx.Done()
	close(r.canceled)
	<-r.release
}

func (r *blockingStreamRouter) RoutePacketConnectionEx(context.Context, N.PacketConn, adapter.InboundContext, N.CloseHandlerFunc) {
	panic("unexpected packet route")
}

func TestManagedUserUpdateDoesNotWaitForActiveSession(t *testing.T) {
	inbound := &Inbound{
		logger:        logger.NOP(),
		paddingScheme: append([]byte(nil), padding.DefaultPaddingScheme...),
		listener:      listener.New(listener.Options{}),
	}
	if err := inbound.UpdateManagedUsers([]adapter.ManagedUser{{Name: "old", Password: "old-password"}}); err != nil {
		t.Fatal(err)
	}

	serverConn, clientConn := net.Pipe()
	connectionDone := make(chan struct{})
	go func() {
		inbound.NewConnection(context.Background(), serverConn, adapter.InboundContext{}, nil)
		close(connectionDone)
	}()
	authHeader := make([]byte, 34)
	passwordHash := sha256.Sum256([]byte("old-password"))
	copy(authHeader, passwordHash[:])
	if _, err := clientConn.Write(authHeader); err != nil {
		t.Fatal(err)
	}

	updateDone := make(chan error, 1)
	go func() {
		updateDone <- inbound.UpdateManagedUsers([]adapter.ManagedUser{{Name: "new", Password: "new-password"}})
	}()
	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("managed-user update waited for an active AnyTLS session")
	}
	users := inbound.ManagedUsers()
	if len(users) != 1 || users[0].Name != "new" {
		t.Fatalf("unexpected users after update: %+v", users)
	}

	if err := inbound.Close(); err != nil {
		t.Fatal(err)
	}
	if err := inbound.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("second Close = %v, want os.ErrClosed", err)
	}
	select {
	case <-connectionDone:
	case <-time.After(time.Second):
		t.Fatal("AnyTLS close did not terminate the active session")
	}
	_ = clientConn.Close()
}

func TestConcurrentSessionOwnedCloseDoesNotCycle(t *testing.T) {
	inbound := &Inbound{
		logger:   logger.NOP(),
		listener: listener.New(listener.Options{}),
	}
	serverA, clientA := net.Pipe()
	serverB, clientB := net.Pipe()
	defer clientA.Close()
	defer clientB.Close()

	type sessionControl struct {
		start   chan struct{}
		untrack chan struct{}
		result  chan error
	}
	controls := []sessionControl{
		{start: make(chan struct{}), untrack: make(chan struct{}), result: make(chan error, 1)},
		{start: make(chan struct{}), untrack: make(chan struct{}), result: make(chan error, 1)},
	}
	tracked := make(chan struct{}, 2)
	for index, conn := range []net.Conn{serverA, serverB} {
		control := controls[index]
		go func() {
			_, cancel := context.WithCancel(context.Background())
			if !inbound.trackConnection(conn, cancel) {
				control.result <- os.ErrClosed
				return
			}
			tracked <- struct{}{}
			<-control.start
			control.result <- inbound.Close()
			<-control.untrack
			inbound.untrackConnection(conn)
			cancel()
		}()
	}
	<-tracked
	<-tracked

	close(controls[0].start)
	select {
	case err := <-controls[0].result:
		if err != nil {
			t.Fatalf("first session Close = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first session-owned Close blocked")
	}
	close(controls[1].start)
	select {
	case err := <-controls[1].result:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("second session Close = %v, want os.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second session-owned Close waited on the first session")
	}

	joined := make(chan error, 1)
	go func() { joined <- inbound.Close() }()
	select {
	case err := <-joined:
		t.Fatalf("external Close returned before sessions unwound: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(controls[1].untrack)
	close(controls[0].untrack)
	select {
	case err := <-joined:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("joining Close = %v, want os.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("AnyTLS close cycle did not terminate")
	}
}

func TestCloseCancelsAndJoinsStreamHandlers(t *testing.T) {
	router := &blockingStreamRouter{
		entered:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
	inbound := &Inbound{
		router:   router,
		logger:   logger.NOP(),
		listener: listener.New(listener.Options{}),
	}
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	handlerDone := make(chan struct{})
	go func() {
		(*inboundHandler)(inbound).NewConnectionEx(context.Background(), serverConn, adapter.InboundContext{}.Source, adapter.InboundContext{}.Destination, nil)
		close(handlerDone)
	}()
	select {
	case <-router.entered:
	case <-time.After(time.Second):
		t.Fatal("stream handler did not enter router")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- inbound.Close() }()
	select {
	case <-router.canceled:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the stream handler context")
	}
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before stream handler exited: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(router.release)
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not join the released stream handler")
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("stream handler did not exit")
	}
}

func TestDuplicatePasswordDoesNotReplaceUsers(t *testing.T) {
	inbound := &Inbound{
		logger:        logger.NOP(),
		paddingScheme: append([]byte(nil), padding.DefaultPaddingScheme...),
	}
	if err := inbound.UpdateManagedUsers([]adapter.ManagedUser{{Name: "old", Password: "old-password"}}); err != nil {
		t.Fatal(err)
	}
	oldService := inbound.service
	err := inbound.UpdateManagedUsers([]adapter.ManagedUser{
		{Name: "alice", Password: "duplicate"},
		{Name: "bob", Password: "duplicate"},
	})
	if err == nil {
		t.Fatal("duplicate password update succeeded")
	}
	if inbound.service != oldService {
		t.Fatal("duplicate password changed the active service")
	}
	users := inbound.ManagedUsers()
	if len(users) != 1 || users[0].Name != "old" {
		t.Fatalf("duplicate password changed users: %+v", users)
	}
}
