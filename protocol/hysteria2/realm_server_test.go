package hysteria2

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/common/logger"

	"github.com/go-chi/chi/v5"
)

func registerRealmForTest(t *testing.T, server *server, user *realmUser, realmID string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/"+realmID, strings.NewReader(`{"addresses":["127.0.0.1:1234"]}`))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", realmID)
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
	ctx = context.WithValue(ctx, contextKeyUser, user)
	response := httptest.NewRecorder()
	server.handleRegister(response, request.WithContext(ctx))
	return response
}

func TestRealmGlobalAndPerUserLimitsReleaseOnExpiry(t *testing.T) {
	alice := &realmUser{name: "alice", maxRealms: 1}
	bob := &realmUser{name: "bob", maxRealms: 2}
	carol := &realmUser{name: "carol", maxRealms: 1}
	server := newServer(logger.NOP(), nil)
	server.maxSessions = 2
	defer server.closeAll()

	if response := registerRealmForTest(t, server, alice, "alice-one"); response.Code != http.StatusOK {
		t.Fatalf("first registration status = %d, body=%s", response.Code, response.Body.String())
	}
	response := registerRealmForTest(t, server, alice, "alice-two")
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "realm_limit_reached") {
		t.Fatalf("per-user overload status = %d, body=%s", response.Code, response.Body.String())
	}
	if response = registerRealmForTest(t, server, bob, "bob-one"); response.Code != http.StatusOK {
		t.Fatalf("second user registration status = %d, body=%s", response.Code, response.Body.String())
	}
	response = registerRealmForTest(t, server, carol, "carol-one")
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "realm_capacity_reached") {
		t.Fatalf("global overload status = %d, body=%s", response.Code, response.Body.String())
	}
	if len(server.sessions) != server.maxSessions {
		t.Fatalf("session count = %d, want %d", len(server.sessions), server.maxSessions)
	}

	expired := server.realms["alice-one"]
	expired.expires = time.Now().Add(-time.Second)
	if !server.removeExpiredSession(expired) {
		t.Fatal("expired session was not released")
	}
	if response = registerRealmForTest(t, server, carol, "carol-one"); response.Code != http.StatusOK {
		t.Fatalf("registration after release status = %d, body=%s", response.Code, response.Body.String())
	}
	if server.userCounts["alice"] != 0 {
		t.Fatalf("alice permit was not released: %d", server.userCounts["alice"])
	}
	if len(server.sessions) != server.maxSessions {
		t.Fatalf("session count after reuse = %d, want %d", len(server.sessions), server.maxSessions)
	}
}

func TestRealmConcurrentAdmissionIsBounded(t *testing.T) {
	user := &realmUser{name: "concurrent", maxRealms: 4}
	server := newServer(logger.NOP(), nil)
	server.maxSessions = 4
	defer server.closeAll()

	var admitted atomic.Int32
	var overloaded atomic.Int32
	var waitGroup sync.WaitGroup
	for index := range 32 {
		waitGroup.Go(func() {
			response := registerRealmForTest(t, server, user, fmt.Sprintf("concurrent-%d", index))
			switch response.Code {
			case http.StatusOK:
				admitted.Add(1)
			case http.StatusTooManyRequests:
				overloaded.Add(1)
			default:
				t.Errorf("unexpected concurrent registration status %d: %s", response.Code, response.Body.String())
			}
		})
	}
	waitGroup.Wait()
	if admitted.Load() != 4 || overloaded.Load() != 28 {
		t.Fatalf("admitted=%d overloaded=%d, want 4/28", admitted.Load(), overloaded.Load())
	}
	server.access.Lock()
	sessionCount := len(server.sessions)
	userCount := server.userCounts[user.name]
	server.access.Unlock()
	if sessionCount != 4 || userCount != 4 {
		t.Fatalf("retained sessions=%d user count=%d, want 4/4", sessionCount, userCount)
	}
}

func TestRealmDefaultPerUserLimitIsFinite(t *testing.T) {
	user := &realmUser{name: "default-limit"}
	server := newServer(logger.NOP(), nil)
	server.maxSessions = defaultMaxRealmsPerUser + 1
	defer server.closeAll()
	for index := range defaultMaxRealmsPerUser {
		response := registerRealmForTest(t, server, user, "realm-"+strings.Repeat("a", index%8)+string(rune('A'+index%26)))
		if response.Code != http.StatusOK {
			t.Fatalf("registration %d status = %d, body=%s", index, response.Code, response.Body.String())
		}
	}
	response := registerRealmForTest(t, server, user, "over-limit")
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "realm_limit_reached") {
		t.Fatalf("default-limit overload status = %d, body=%s", response.Code, response.Body.String())
	}
	if len(server.sessions) != defaultMaxRealmsPerUser {
		t.Fatalf("default limit retained %d sessions", len(server.sessions))
	}
}

func TestRealmDuplicateTokenRejected(t *testing.T) {
	_, err := NewRealmService(context.Background(), logger.NOP(), "realm", option.HysteriaRealmServiceOptions{
		Users: []option.HysteriaRealmUser{
			{Name: "alice", Token: "duplicate"},
			{Name: "bob", Token: "duplicate"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate token") {
		t.Fatalf("duplicate token error = %v", err)
	}
}
