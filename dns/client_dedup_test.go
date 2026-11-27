package dns

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"

	mDNS "github.com/miekg/dns"
)

// blockingTestTransport keeps every exchange in flight until release is
// closed, so that concurrent queries for the same name can be observed while
// the first one is still pending.
type blockingTestTransport struct {
	calls   atomic.Int32
	release chan struct{}
}

func (t *blockingTestTransport) Type() string                   { return "blocking" }
func (t *blockingTestTransport) Tag() string                    { return "blocking" }
func (t *blockingTestTransport) Dependencies() []string         { return nil }
func (t *blockingTestTransport) Start(adapter.StartStage) error { return nil }
func (t *blockingTestTransport) Close() error                   { return nil }
func (t *blockingTestTransport) Reset()                         {}

func (t *blockingTestTransport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	t.calls.Add(1)
	select {
	case <-t.release:
		return testAResponse(message, 300), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *blockingTestTransport) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(*mDNS.Msg, error)) {
	go func() {
		response, err := t.Exchange(ctx, message)
		callback(response, err)
	}()
}

func waitTransportCalls(t *testing.T, transport *blockingTestTransport, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for transport.calls.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("transport calls = %d, want at least %d", transport.calls.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDedupKeyIncludesTimeout(t *testing.T) {
	transport := &blockingTestTransport{release: make(chan struct{})}
	client := NewClient(ClientOptions{Context: context.Background()})

	firstDone := make(chan error, 1)
	client.ExchangeAsync(context.Background(), transport, testAQuery(), adapter.DNSQueryOptions{Timeout: 5 * time.Second}, nil, func(_ *mDNS.Msg, err error) {
		firstDone <- err
	})
	waitTransportCalls(t, transport, 1)

	// A query with a different timeout must not be folded into the pending
	// long-running exchange: it should run (and time out) on its own.
	secondDone := make(chan error, 1)
	started := time.Now()
	client.ExchangeAsync(context.Background(), transport, testAQuery(), adapter.DNSQueryOptions{Timeout: 100 * time.Millisecond}, nil, func(_ *mDNS.Msg, err error) {
		secondDone <- err
	})
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("second exchange error = %v, want %v", err, context.DeadlineExceeded)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("second exchange waited %v, want its own timeout", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("query with a different timeout waited for the first query's exchange")
	}
	close(transport.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first exchange error = %v", err)
	}
	if calls := transport.calls.Load(); calls != 2 {
		t.Fatalf("transport calls = %d, want 2 for distinct timeouts", calls)
	}
}

func TestDedupSharesExchangeForEqualTimeouts(t *testing.T) {
	transport := &blockingTestTransport{release: make(chan struct{})}
	client := NewClient(ClientOptions{Context: context.Background()})
	const timeout = 5 * time.Second

	firstDone := make(chan error, 1)
	client.ExchangeAsync(context.Background(), transport, testAQuery(), adapter.DNSQueryOptions{Timeout: timeout}, nil, func(_ *mDNS.Msg, err error) {
		firstDone <- err
	})
	waitTransportCalls(t, transport, 1)

	secondDone := make(chan error, 1)
	client.ExchangeAsync(context.Background(), transport, testAQuery(), adapter.DNSQueryOptions{Timeout: timeout}, nil, func(response *mDNS.Msg, err error) {
		if err == nil && (response == nil || len(response.Answer) != 1) {
			err = errors.New("deduplicated exchange returned no response")
		}
		secondDone <- err
	})

	// The deduplicated query must not start a second upstream exchange.
	time.Sleep(50 * time.Millisecond)
	if calls := transport.calls.Load(); calls != 1 {
		t.Fatalf("deduplicated query used %d upstream calls, want 1", calls)
	}

	close(transport.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first exchange error = %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("deduplicated exchange error = %v", err)
	}
	if calls := transport.calls.Load(); calls != 1 {
		t.Fatalf("transport calls = %d, want 1 for equal timeouts", calls)
	}
}
