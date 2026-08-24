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
)

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
