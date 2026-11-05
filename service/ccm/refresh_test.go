package ccm

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/log"
)

type blockingRefreshTransport struct {
	calls   atomic.Int32
	entered chan struct{}
	once    sync.Once
}

func (t *blockingRefreshTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	t.once.Do(func() { close(t.entered) })
	<-request.Context().Done()
	return nil, context.Cause(request.Context())
}

func TestCloseCancelsSingleRefreshAndAllWaiters(t *testing.T) {
	transport := &blockingRefreshTransport{entered: make(chan struct{})}
	refreshCtx, refreshCancel := context.WithCancel(t.Context())
	service := &Service{
		ctx: t.Context(), logger: log.NewNOPFactory().Logger(),
		httpClient:  &http.Client{Transport: transport},
		credentials: &oauthCredentials{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Minute).UnixMilli()},
		refreshCtx:  refreshCtx, refreshCancel: refreshCancel,
	}
	const waiterCount = 64
	results := make(chan error, waiterCount)
	for range waiterCount {
		go func() {
			_, err := service.getAccessToken(t.Context())
			results <- err
		}()
	}
	select {
	case <-transport.entered:
	case <-time.After(time.Second):
		t.Fatal("refresh request did not start")
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	for range waiterCount {
		select {
		case err := <-results:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("waiter error = %v, want context.Canceled", err)
			}
		case <-time.After(time.Second):
			t.Fatal("refresh waiter was not released")
		}
	}
	if calls := transport.calls.Load(); calls != 1 {
		t.Fatalf("refresh requests = %d, want 1", calls)
	}
}
