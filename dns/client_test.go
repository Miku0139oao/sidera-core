package dns

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	C "github.com/Miku0139oao/sidera-core/constant"

	mDNS "github.com/miekg/dns"
)

type clientTestTransport struct {
	calls    atomic.Int32
	response func(message *mDNS.Msg) *mDNS.Msg
}

func (t *clientTestTransport) Type() string                   { return "test" }
func (t *clientTestTransport) Tag() string                    { return "test" }
func (t *clientTestTransport) Dependencies() []string         { return nil }
func (t *clientTestTransport) Start(adapter.StartStage) error { return nil }
func (t *clientTestTransport) Close() error                   { return nil }
func (t *clientTestTransport) Reset()                         {}
func (t *clientTestTransport) Exchange(_ context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	t.calls.Add(1)
	return t.response(message), nil
}

func (t *clientTestTransport) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(*mDNS.Msg, error)) {
	callback(t.Exchange(ctx, message))
}

func testAResponse(message *mDNS.Msg, ttl uint32) *mDNS.Msg {
	response := new(mDNS.Msg)
	response.SetReply(message)
	response.Answer = []mDNS.RR{&mDNS.A{
		Hdr: mDNS.RR_Header{
			Name:   message.Question[0].Name,
			Rrtype: mDNS.TypeA,
			Class:  mDNS.ClassINET,
			Ttl:    ttl,
		},
		A: netip.MustParseAddr("192.0.2.1").AsSlice(),
	}}
	return response
}

func testAQuery() *mDNS.Msg {
	message := new(mDNS.Msg)
	message.SetQuestion("cache.example.", mDNS.TypeA)
	return message
}

func TestComputeTimeToLiveIgnoresZeroWithPositiveTTL(t *testing.T) {
	response := testAResponse(testAQuery(), 0)
	response.Answer = append(response.Answer, &mDNS.A{
		Hdr: mDNS.RR_Header{
			Name:   response.Question[0].Name,
			Rrtype: mDNS.TypeA,
			Class:  mDNS.ClassINET,
			Ttl:    300,
		},
		A: netip.MustParseAddr("192.0.2.2").AsSlice(),
	})

	ttl, initialized := computeTimeToLive(response)
	if !initialized || ttl != 300 {
		t.Fatalf("computed TTL = %d, initialized = %v; want 300 from the positive record", ttl, initialized)
	}
	if applied := applyResponseOptions(response.Question[0], response, adapter.DNSQueryOptions{}); applied != 300 {
		t.Fatalf("applied TTL = %d, want 300", applied)
	}

	zeroOnly := testAResponse(testAQuery(), 0)
	if ttl, initialized = computeTimeToLive(zeroOnly); !initialized || ttl != 0 {
		t.Fatalf("zero-only response TTL = %d, initialized = %v; want a true zero TTL", ttl, initialized)
	}
	if applied := applyResponseOptions(zeroOnly.Question[0], zeroOnly, adapter.DNSQueryOptions{}); applied != 0 {
		t.Fatalf("zero-only applied TTL = %d, want 0", applied)
	}

	empty := new(mDNS.Msg)
	if ttl, initialized = computeTimeToLive(empty); initialized || ttl != 0 {
		t.Fatalf("empty response TTL = %d, initialized = %v; want uninitialized", ttl, initialized)
	}
}

func TestZeroTTLAdditionalRecordDoesNotDisableCaching(t *testing.T) {
	transport := &clientTestTransport{response: func(message *mDNS.Msg) *mDNS.Msg {
		response := testAResponse(message, 300)
		response.Extra = append(response.Extra, &mDNS.A{
			Hdr: mDNS.RR_Header{
				Name:   "additional.example.",
				Rrtype: mDNS.TypeA,
				Class:  mDNS.ClassINET,
				Ttl:    0,
			},
			A: netip.MustParseAddr("192.0.2.53").AsSlice(),
		})
		return response
	}}
	client := NewClient(ClientOptions{Context: context.Background(), DisableExpire: true})

	for range 2 {
		response, err := client.Exchange(context.Background(), transport, testAQuery(), adapter.DNSQueryOptions{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ttl := response.Answer[0].Header().Ttl; ttl != 300 {
			t.Fatalf("response TTL = %d, want 300", ttl)
		}
	}
	if calls := transport.calls.Load(); calls != 1 {
		t.Fatalf("upstream calls = %d, want 1 with a zero-TTL additional record", calls)
	}
}

func TestZeroTTLResponseIsNotCached(t *testing.T) {
	transport := &clientTestTransport{response: func(message *mDNS.Msg) *mDNS.Msg {
		return testAResponse(message, 0)
	}}
	client := NewClient(ClientOptions{Context: context.Background(), DisableExpire: true})

	for range 2 {
		response, err := client.Exchange(context.Background(), transport, testAQuery(), adapter.DNSQueryOptions{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ttl := response.Answer[0].Header().Ttl; ttl != 0 {
			t.Fatalf("response TTL = %d, want 0", ttl)
		}
	}
	if calls := transport.calls.Load(); calls != 2 {
		t.Fatalf("upstream calls = %d, want 2 for a zero-TTL response", calls)
	}
}

func TestDNSCacheRewriteTTLIsolation(t *testing.T) {
	transport := &clientTestTransport{response: func(message *mDNS.Msg) *mDNS.Msg {
		return testAResponse(message, 300)
	}}
	client := NewClient(ClientOptions{Context: context.Background(), DisableExpire: true})

	exchange := func(rewriteTTL *uint32) *mDNS.Msg {
		t.Helper()
		response, err := client.Exchange(context.Background(), transport, testAQuery(), adapter.DNSQueryOptions{RewriteTTL: rewriteTTL}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	ttl60a, ttl60b, ttl120 := uint32(60), uint32(60), uint32(120)
	if ttl := exchange(&ttl60a).Answer[0].Header().Ttl; ttl != ttl60a {
		t.Fatalf("rewritten TTL = %d, want %d", ttl, ttl60a)
	}
	if ttl := exchange(&ttl60b).Answer[0].Header().Ttl; ttl != ttl60b {
		t.Fatalf("cached rewritten TTL = %d, want %d", ttl, ttl60b)
	}
	if calls := transport.calls.Load(); calls != 1 {
		t.Fatalf("equal rewrite policies used %d upstream calls, want 1", calls)
	}
	if ttl := exchange(&ttl120).Answer[0].Header().Ttl; ttl != ttl120 {
		t.Fatalf("isolated rewritten TTL = %d, want %d", ttl, ttl120)
	}
	if ttl := exchange(nil).Answer[0].Header().Ttl; ttl != 300 {
		t.Fatalf("unmodified TTL = %d, want 300", ttl)
	}
	if calls := transport.calls.Load(); calls != 3 {
		t.Fatalf("isolated rewrite policies used %d upstream calls, want 3", calls)
	}
}

func TestStaleRejectedCachedResponseTriggersRefresh(t *testing.T) {
	reject := func(*mDNS.Msg) bool { return false }
	newStaleClient := func() (*Client, *clientTestTransport, chan struct{}) {
		t.Helper()
		refreshed := make(chan struct{}, 1)
		transport := &clientTestTransport{response: func(message *mDNS.Msg) *mDNS.Msg {
			select {
			case refreshed <- struct{}{}:
			default:
			}
			return testAResponse(message, 300)
		}}
		client := NewClient(ClientOptions{Context: context.Background(), OptimisticTimeout: time.Hour})
		return client, transport, refreshed
	}
	primeStale := func(client *Client, transport *clientTestTransport, options adapter.DNSQueryOptions) {
		t.Helper()
		message := testAQuery()
		key := client.cacheKey(transport, message, message.Question[0], options)
		client.cache.AddWithLifetime(key, testAResponse(message, 300), -time.Minute)
	}
	waitRefresh := func(refreshed chan struct{}, path string) {
		t.Helper()
		select {
		case <-refreshed:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: a stale rejected cache entry was not refreshed from upstream", path)
		}
	}

	t.Run("Exchange", func(t *testing.T) {
		client, transport, refreshed := newStaleClient()
		options := adapter.DNSQueryOptions{}
		primeStale(client, transport, options)
		if _, err := client.Exchange(context.Background(), transport, testAQuery(), options, reject); !errors.Is(err, ErrResponseRejectedCached) {
			t.Fatalf("cached exchange error = %v, want %v", err, ErrResponseRejectedCached)
		}
		waitRefresh(refreshed, "Exchange")
	})

	t.Run("Lookup", func(t *testing.T) {
		client, transport, refreshed := newStaleClient()
		options := adapter.DNSQueryOptions{Strategy: C.DomainStrategyIPv4Only}
		primeStale(client, transport, options)
		if _, err := client.Lookup(context.Background(), transport, "cache.example", options, reject); !errors.Is(err, ErrResponseRejectedCached) {
			t.Fatalf("cached lookup error = %v, want %v", err, ErrResponseRejectedCached)
		}
		waitRefresh(refreshed, "Lookup")
	})
}

func TestCachedResponseCheckerIsolation(t *testing.T) {
	transport := &clientTestTransport{response: func(message *mDNS.Msg) *mDNS.Msg {
		return testAResponse(message, 300)
	}}
	client := NewClient(ClientOptions{Context: context.Background(), DisableExpire: true})

	options := adapter.DNSQueryOptions{Strategy: C.DomainStrategyIPv4Only}
	if _, err := client.Exchange(context.Background(), transport, testAQuery(), options, nil); err != nil {
		t.Fatal(err)
	}
	var rejectedChecks atomic.Int32
	reject := func(*mDNS.Msg) bool {
		rejectedChecks.Add(1)
		return false
	}
	response, err := client.Exchange(context.Background(), transport, testAQuery(), options, reject)
	if !errors.Is(err, ErrResponseRejectedCached) {
		t.Fatalf("cached exchange error = %v, want %v", err, ErrResponseRejectedCached)
	}
	if response == nil {
		t.Fatal("cached rejection did not return the rejected response")
	}
	if checks := rejectedChecks.Load(); checks != 1 {
		t.Fatalf("cached exchange checker calls = %d, want 1", checks)
	}
	if calls := transport.calls.Load(); calls != 1 {
		t.Fatalf("cached rejection used %d upstream calls, want 1", calls)
	}

	_, err = client.Lookup(context.Background(), transport, "cache.example", options, reject)
	if !errors.Is(err, ErrResponseRejectedCached) {
		t.Fatalf("cached lookup error = %v, want %v", err, ErrResponseRejectedCached)
	}
	if checks := rejectedChecks.Load(); checks != 2 {
		t.Fatalf("cached lookup checker calls = %d, want 2", checks)
	}
	if calls := transport.calls.Load(); calls != 1 {
		t.Fatalf("cached lookup used %d upstream calls, want 1", calls)
	}

	addresses, err := client.Lookup(context.Background(), transport, "cache.example", options, func(*mDNS.Msg) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 1 || addresses[0] != netip.MustParseAddr("192.0.2.1") {
		t.Fatalf("unexpected cached addresses: %v", addresses)
	}
	if calls := transport.calls.Load(); calls != 1 {
		t.Fatalf("checker isolation caused %d upstream calls, want 1", calls)
	}
}
