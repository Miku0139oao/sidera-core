package hysteria

import (
	"context"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/common/listener"
	hy "github.com/sagernet/sing-quic/hysteria"
	"github.com/sagernet/sing/common/auth"
)

func TestManagedUserIdentityPublishedBeforeServiceStart(t *testing.T) {
	inbound := &Inbound{
		started:     true,
		users:       []adapter.ManagedUser{{Name: "old", Password: "old-password"}},
		userNameMap: map[string]string{"old-password": "old"},
	}
	started := make(chan string, 1)
	release := make(chan struct{})
	updated := make(chan error, 1)
	go func() {
		inbound.serviceAccess.Lock()
		defer inbound.serviceAccess.Unlock()
		updated <- inbound.replaceServiceLocked(
			nil,
			[]adapter.ManagedUser{{Name: "new", Password: "new-password"}},
			map[string]string{"new-password": "new"},
			func(*hy.Service[string]) (*listener.Listener, error) {
				started <- inbound.userName(auth.ContextWithUser(context.Background(), "new-password"))
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

func TestDuplicatePasswordLeavesActiveGenerationUnchanged(t *testing.T) {
	inbound := &Inbound{
		users:       []adapter.ManagedUser{{Name: "old", Password: "old-password"}},
		userNameMap: map[string]string{"old-password": "old"},
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
		t.Fatal("duplicate update changed the active service")
	}
	users := inbound.ManagedUsers()
	if len(users) != 1 || users[0].Name != "old" {
		t.Fatalf("duplicate update changed managed users: %+v", users)
	}
	if got := inbound.userName(auth.ContextWithUser(context.Background(), "old-password")); got != "old" {
		t.Fatalf("duplicate update changed credential map: %q", got)
	}
}
