//go:build go1.24

package tls

import (
	"context"
	stdTLS "crypto/tls"
	"encoding/pem"
	"net"
	"net/netip"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/option"

	mDNS "github.com/miekg/dns"
)

func TestECHRefreshAndRetryPublishImmutableGenerations(t *testing.T) {
	const serverName = "public.example"
	oldConfig := []byte("old ECH config")
	refreshedConfig := []byte("refreshed ECH config")
	retryConfig := []byte("retry ECH config")
	started := make(chan *echTestConfig, 2)
	release := make(chan struct{})

	dnsRouter := &echTestDNSRouter{config: refreshedConfig}
	config := &ECHClientConfig{
		config: &echTestConfig{
			serverName: serverName,
			echConfig:  append([]byte(nil), oldConfig...),
			started:    started,
			release:    release,
		},
		dnsRouter:  dnsRouter,
		dynamic:    true,
		lastTTL:    time.Hour,
		lastUpdate: time.Now(),
	}

	clientOne, peerOne := net.Pipe()
	defer peerOne.Close()
	resultOne := make(chan error, 1)
	go func() {
		_, err := config.ClientHandshake(context.Background(), clientOne)
		resultOne <- err
	}()
	generationOne := <-started
	if got := string(generationOne.echConfig); got != string(oldConfig) {
		t.Fatalf("first handshake config = %q, want %q", got, oldConfig)
	}

	// Expire the DNS generation while the first handshake is still reading it.
	config.access.Lock()
	config.lastUpdate = time.Time{}
	config.access.Unlock()

	clientTwo, peerTwo := net.Pipe()
	defer peerTwo.Close()
	resultTwo := make(chan error, 1)
	go func() {
		_, err := config.ClientHandshake(context.Background(), clientTwo)
		resultTwo <- err
	}()
	generationTwo := <-started
	if got := string(generationTwo.echConfig); got != string(refreshedConfig) {
		t.Fatalf("refreshed handshake config = %q, want %q", got, refreshedConfig)
	}

	// Model the default dialer's ECH rejection retry while both captured
	// generations are in active handshakes.
	if !setECHRetryConfig(config, retryConfig) {
		t.Fatal("ECH retry config was not accepted")
	}
	if got := string(config.ECHConfigList()); got != string(retryConfig) {
		t.Fatalf("published retry config = %q, want %q", got, retryConfig)
	}
	if got := string(generationOne.echConfig); got != string(oldConfig) {
		t.Fatalf("retry mutated first active generation: %q", got)
	}
	if got := string(generationTwo.echConfig); got != string(refreshedConfig) {
		t.Fatalf("retry mutated refreshed active generation: %q", got)
	}

	// Even if the DNS TTL expires between rejection and retry, the next
	// handshake must consume the server-provided retry generation first.
	config.access.Lock()
	config.lastUpdate = time.Time{}
	config.access.Unlock()
	retryGeneration, err := config.configForHandshake(context.Background())
	if err != nil {
		t.Fatalf("capture retry generation: %v", err)
	}
	if got := string(retryGeneration.ECHConfigList()); got != string(retryConfig) {
		t.Fatalf("captured retry config = %q, want %q", got, retryConfig)
	}
	if got := dnsRouter.queries.Load(); got != 1 {
		t.Fatalf("DNS refresh count = %d, want 1 before retry consumption", got)
	}

	close(release)
	if err := <-resultOne; err != nil {
		t.Fatalf("first handshake failed: %v", err)
	}
	if err := <-resultTwo; err != nil {
		t.Fatalf("second handshake failed: %v", err)
	}
}

type echTestConfig struct {
	serverName string
	echConfig  []byte
	started    chan *echTestConfig
	release    <-chan struct{}
}

func (c *echTestConfig) ServerName() string                { return c.serverName }
func (c *echTestConfig) SetServerName(serverName string)   { c.serverName = serverName }
func (c *echTestConfig) NextProtos() []string              { return nil }
func (c *echTestConfig) SetNextProtos([]string)            {}
func (c *echTestConfig) HandshakeTimeout() time.Duration   { return 0 }
func (c *echTestConfig) SetHandshakeTimeout(time.Duration) {}
func (c *echTestConfig) STDConfig() (*STDConfig, error)    { return &stdTLS.Config{}, nil }
func (c *echTestConfig) ECHConfigList() []byte             { return c.echConfig }
func (c *echTestConfig) SetECHConfigList(config []byte) {
	c.echConfig = append(c.echConfig[:0], config...)
}
func (c *echTestConfig) Clone() Config { return c.clone() }
func (c *echTestConfig) Client(conn net.Conn) (Conn, error) {
	c.started <- c
	return &echTestConn{Conn: conn, generation: c, release: c.release}, nil
}

func (c *echTestConfig) clone() *echTestConfig {
	return &echTestConfig{
		serverName: c.serverName,
		echConfig:  append([]byte(nil), c.echConfig...),
		started:    c.started,
		release:    c.release,
	}
}

type echTestConn struct {
	net.Conn
	generation *echTestConfig
	release    <-chan struct{}
}

func (c *echTestConn) NetConn() net.Conn { return c.Conn }

func (c *echTestConn) HandshakeContext(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.release:
			return nil
		default:
			// Deliberately read the captured generation throughout the active
			// handshake so -race detects any in-place refresh or retry.
			if len(c.generation.echConfig) > 0 {
				_ = c.generation.echConfig[0]
			}
			runtime.Gosched()
		}
	}
}

func (c *echTestConn) ConnectionState() ConnectionState { return ConnectionState{} }

type echTestDNSRouter struct {
	config  []byte
	empty   bool
	queries atomic.Int32
}

func (r *echTestDNSRouter) Start(adapter.StartStage) error { return nil }
func (r *echTestDNSRouter) Close() error                   { return nil }
func (r *echTestDNSRouter) Exchange(_ context.Context, message *mDNS.Msg, _ adapter.DNSQueryOptions) (*mDNS.Msg, error) {
	r.queries.Add(1)
	response := new(mDNS.Msg)
	response.SetReply(message)
	if r.empty {
		return response, nil
	}
	response.Answer = []mDNS.RR{
		&mDNS.HTTPS{SVCB: mDNS.SVCB{
			Hdr:      mDNS.RR_Header{Name: message.Question[0].Name, Rrtype: mDNS.TypeHTTPS, Class: mDNS.ClassINET, Ttl: 60},
			Priority: 1,
			Target:   ".",
			Value:    []mDNS.SVCBKeyValue{&mDNS.SVCBECHConfig{ECH: append([]byte(nil), r.config...)}},
		}},
	}
	return response, nil
}

func (r *echTestDNSRouter) ExchangeAsync(ctx context.Context, message *mDNS.Msg, options adapter.DNSQueryOptions, callback func(*mDNS.Msg, error)) {
	response, err := r.Exchange(ctx, message, options)
	callback(response, err)
}

func (r *echTestDNSRouter) Lookup(context.Context, string, adapter.DNSQueryOptions) ([]netip.Addr, error) {
	return nil, nil
}
func (r *echTestDNSRouter) ClearCache()                                    {}
func (r *echTestDNSRouter) LookupReverseMapping(netip.Addr) (string, bool) { return "", false }
func (r *echTestDNSRouter) ResetNetwork()                                  {}

func TestECHClientConfigPreservesSessionIDGenerator(t *testing.T) {
	echConfigPEM := pem.EncodeToMemory(&pem.Block{Type: "ECH CONFIGS", Bytes: []byte{1, 2, 3, 4}})
	options := option.OutboundTLSOptions{
		ECH: &option.OutboundECHOptions{
			Enabled: true,
			Config:  []string{string(echConfigPEM)},
		},
	}

	// Mirror protocol/shadowtls/outbound.go: a config that supports
	// SetSessionIDGenerator must keep being discoverable through the wrapper.
	t.Run("generator capability is forwarded", func(t *testing.T) {
		config, err := parseECHClientConfig(context.Background(), &echSessionIDTestConfig{serverName: "public.example"}, options)
		if err != nil {
			t.Fatalf("parse ECH client config: %v", err)
		}
		idConfig, loaded := config.(WithSessionIDGenerator)
		if !loaded {
			t.Fatalf("%T does not implement WithSessionIDGenerator", config)
		}
		generator := func([]byte, []byte) error { return nil }
		idConfig.SetSessionIDGenerator(generator)

		wrapper, loaded := config.(*echClientConfigWithSessionIDGenerator)
		if !loaded {
			t.Fatalf("%T is not the capability-preserving wrapper", config)
		}
		wrapper.access.Lock()
		inner := wrapper.config
		wrapper.access.Unlock()
		innerConfig, loaded := inner.(*echSessionIDTestConfig)
		if !loaded {
			t.Fatalf("wrapped config = %T", inner)
		}
		if innerConfig.generator == nil {
			t.Fatal("SetSessionIDGenerator was not forwarded to the wrapped config")
		}
		if _, loaded = config.Clone().(WithSessionIDGenerator); !loaded {
			t.Fatalf("clone of %T lost WithSessionIDGenerator", config)
		}
	})

	// A std-backed config must not advertise the uTLS-only capability, or the
	// ShadowTLS outbound would skip its STDConfig fallback.
	t.Run("std config keeps fallback", func(t *testing.T) {
		config, err := parseECHClientConfig(context.Background(), &echTestConfig{serverName: "public.example"}, options)
		if err != nil {
			t.Fatalf("parse ECH client config: %v", err)
		}
		if _, loaded := config.(WithSessionIDGenerator); loaded {
			t.Fatalf("%T advertises WithSessionIDGenerator for a std config", config)
		}
		if _, err = config.STDConfig(); err != nil {
			t.Fatalf("STDConfig fallback failed: %v", err)
		}
	})
}

func TestECHRefreshFallsBackToCachedConfig(t *testing.T) {
	cached := []byte("cached ECH config")
	config := &ECHClientConfig{
		config:     &echTestConfig{serverName: "public.example", echConfig: append([]byte(nil), cached...)},
		dnsRouter:  &echTestDNSRouter{empty: true},
		dynamic:    true,
		lastTTL:    time.Hour,
		lastUpdate: time.Time{},
	}

	generation, err := config.configForHandshake(context.Background())
	if err != nil {
		t.Fatalf("refresh without an ECH record failed despite a cached config: %v", err)
	}
	if got := string(generation.ECHConfigList()); got != string(cached) {
		t.Fatalf("returned config = %q, want cached %q", got, cached)
	}
}

func TestECHRefreshWithoutAnyConfigFails(t *testing.T) {
	config := &ECHClientConfig{
		config:    &echTestConfig{serverName: "public.example"},
		dnsRouter: &echTestDNSRouter{empty: true},
		dynamic:   true,
	}

	if _, err := config.configForHandshake(context.Background()); err == nil {
		t.Fatal("refresh without any ECH config unexpectedly succeeded")
	}
}

func TestECHRefreshDoesNotHoldAccessLock(t *testing.T) {
	cached := []byte("cached ECH config")
	config := &ECHClientConfig{
		config:     &echTestConfig{serverName: "public.example", echConfig: append([]byte(nil), cached...)},
		dynamic:    true,
		lastTTL:    time.Hour,
		lastUpdate: time.Time{},
	}
	router := &echLockProbeDNSRouter{
		echTestDNSRouter: echTestDNSRouter{config: []byte("fresh ECH config")},
		owner:            config,
	}
	config.dnsRouter = router

	if _, err := config.configForHandshake(context.Background()); err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	if router.lockHeld.Load() {
		t.Fatal("dnsRouter.Exchange was called while the access mutex was held")
	}
}

type echSessionIDTestConfig struct {
	serverName string
	echConfig  []byte
	generator  func([]byte, []byte) error
}

func (c *echSessionIDTestConfig) ServerName() string                { return c.serverName }
func (c *echSessionIDTestConfig) SetServerName(serverName string)   { c.serverName = serverName }
func (c *echSessionIDTestConfig) NextProtos() []string              { return nil }
func (c *echSessionIDTestConfig) SetNextProtos([]string)            {}
func (c *echSessionIDTestConfig) HandshakeTimeout() time.Duration   { return 0 }
func (c *echSessionIDTestConfig) SetHandshakeTimeout(time.Duration) {}
func (c *echSessionIDTestConfig) STDConfig() (*STDConfig, error)    { return &stdTLS.Config{}, nil }
func (c *echSessionIDTestConfig) ECHConfigList() []byte             { return c.echConfig }
func (c *echSessionIDTestConfig) SetECHConfigList(config []byte) {
	c.echConfig = append(c.echConfig[:0], config...)
}
func (c *echSessionIDTestConfig) Clone() Config {
	cloned := *c
	cloned.echConfig = append([]byte(nil), c.echConfig...)
	return &cloned
}
func (c *echSessionIDTestConfig) Client(net.Conn) (Conn, error) { return nil, nil }
func (c *echSessionIDTestConfig) SetSessionIDGenerator(generator func([]byte, []byte) error) {
	c.generator = generator
}

type echLockProbeDNSRouter struct {
	echTestDNSRouter
	owner    *ECHClientConfig
	lockHeld atomic.Bool
}

func (r *echLockProbeDNSRouter) Exchange(ctx context.Context, message *mDNS.Msg, options adapter.DNSQueryOptions) (*mDNS.Msg, error) {
	if r.owner.access.TryLock() {
		r.owner.access.Unlock()
	} else {
		r.lockHeld.Store(true)
	}
	return r.echTestDNSRouter.Exchange(ctx, message, options)
}
