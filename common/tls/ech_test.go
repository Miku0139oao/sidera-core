//go:build go1.24

package tls

import (
	"context"
	stdTLS "crypto/tls"
	"net"
	"net/netip"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"

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
	queries atomic.Int32
}

func (r *echTestDNSRouter) Start(adapter.StartStage) error { return nil }
func (r *echTestDNSRouter) Close() error                   { return nil }
func (r *echTestDNSRouter) Exchange(_ context.Context, message *mDNS.Msg, _ adapter.DNSQueryOptions) (*mDNS.Msg, error) {
	r.queries.Add(1)
	response := new(mDNS.Msg)
	response.SetReply(message)
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
