package tuic

import (
	"context"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/common/listener"
	"github.com/Miku0139oao/sidera-core/option"
	quicTUIC "github.com/sagernet/sing-quic/tuic"
	"github.com/sagernet/sing/common/auth"
)

const (
	oldUserID = "27848739-7e62-4138-9fd3-098a63964b6b"
	newUserID = "55e367ce-3d4d-4c9c-99f2-a31dc4e76846"
)

func TestManagedUserIdentityPublishedBeforeServiceStart(t *testing.T) {
	inbound := &Inbound{
		started: true,
		managedUsers: []adapter.ManagedUser{{
			Name: "old", UUID: oldUserID, Password: "old-password",
		}},
		users: map[string]option.TUICUser{
			oldUserID: {Name: "old", UUID: oldUserID, Password: "old-password"},
		},
	}
	started := make(chan string, 1)
	release := make(chan struct{})
	updated := make(chan error, 1)
	go func() {
		inbound.serviceAccess.Lock()
		defer inbound.serviceAccess.Unlock()
		updated <- inbound.replaceServiceLocked(
			nil,
			[]adapter.ManagedUser{{Name: "new", UUID: newUserID, Password: "new-password"}},
			map[string]option.TUICUser{
				newUserID: {Name: "new", UUID: newUserID, Password: "new-password"},
			},
			func(*quicTUIC.Service[string]) (*listener.Listener, error) {
				started <- authenticatedUserName(auth.ContextWithUser(context.Background(), "new"))
				<-release
				return listener.New(listener.Options{}), nil
			},
		)
	}()

	select {
	case identity := <-started:
		if identity != "new" {
			t.Fatalf("identity at service admission = %q, want new", identity)
		}
	case <-time.After(time.Second):
		t.Fatal("service start barrier was not reached")
	}
	select {
	case err := <-updated:
		t.Fatalf("update returned before start barrier release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-updated:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("managed-user update did not complete")
	}
	_ = inbound.listener.Close()
}

func TestAuthenticatedIdentitySurvivesNewGenerationPublication(t *testing.T) {
	oldContext := auth.ContextWithUser(context.Background(), "old")
	if got := authenticatedUserName(oldContext); got != "old" {
		t.Fatalf("old authenticated identity changed after publication: %q", got)
	}
}

func TestDuplicateUUIDLeavesActiveGenerationUnchanged(t *testing.T) {
	inbound := &Inbound{
		managedUsers: []adapter.ManagedUser{{Name: "old", UUID: oldUserID, Password: "old-password"}},
		users: map[string]option.TUICUser{
			oldUserID: {Name: "old", UUID: oldUserID, Password: "old-password"},
		},
	}
	oldService := inbound.server
	err := inbound.UpdateManagedUsers([]adapter.ManagedUser{
		{Name: "alice", UUID: newUserID, Password: "first"},
		{Name: "bob", UUID: newUserID, Password: "second"},
	})
	if err == nil {
		t.Fatal("duplicate UUID update succeeded")
	}
	if inbound.server != oldService {
		t.Fatal("duplicate update changed the active service")
	}
	users := inbound.ManagedUsers()
	if len(users) != 1 || users[0].Name != "old" {
		t.Fatalf("duplicate update changed managed users: %+v", users)
	}
	if got := inbound.userName(oldUserID); got != "old" {
		t.Fatalf("duplicate update changed credential map: %q", got)
	}
}
