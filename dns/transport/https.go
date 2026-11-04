package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/common/dialer"
	"github.com/Miku0139oao/sidera-core/common/tls"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/dns"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	sHTTP "github.com/sagernet/sing/protocol/http"

	mDNS "github.com/miekg/dns"
	"golang.org/x/net/http2"
)

const (
	MimeType              = "application/dns-message"
	maxDNSWireMessageSize = 1<<16 - 1
)

var _ adapter.DNSTransport = (*HTTPSTransport)(nil)

type httpsRoundTripper interface {
	RoundTrip(request *http.Request) (*http.Response, error)
	Close()
}

func RegisterHTTPS(registry *dns.TransportRegistry) {
	dns.RegisterTransport[option.RemoteHTTPSDNSServerOptions](registry, C.DNSTypeHTTPS, NewHTTPS)
}

type HTTPSTransport struct {
	dns.TransportAdapter
	logger           logger.ContextLogger
	dialer           N.Dialer
	destination      *url.URL
	headers          http.Header
	serverAddr       M.Socksaddr
	fallback         *atomic.Bool
	transportAccess  sync.Mutex
	transport        httpsRoundTripper
	transportResetAt time.Time
	closed           bool
}

func NewHTTPS(ctx context.Context, logger log.ContextLogger, tag string, options option.RemoteHTTPSDNSServerOptions) (adapter.DNSTransport, error) {
	transportDialer, err := dns.NewRemoteDialer(ctx, options.RemoteDNSServerOptions)
	if err != nil {
		return nil, err
	}
	tlsOptions := common.PtrValueOrDefault(options.TLS)
	tlsOptions.Enabled = true
	tlsConfig, err := tls.NewClient(ctx, logger, options.Server, tlsOptions)
	if err != nil {
		return nil, err
	}
	if len(tlsConfig.NextProtos()) == 0 {
		tlsConfig.SetNextProtos([]string{http2.NextProtoTLS, "http/1.1"})
	}
	headers := options.Headers.Build()
	host := headers.Get("Host")
	if host != "" {
		headers.Del("Host")
	} else {
		if tlsConfig.ServerName() != "" {
			host = tlsConfig.ServerName()
		} else {
			host = options.Server
		}
	}
	destinationURL := url.URL{
		Scheme: "https",
		Host:   host,
	}
	if destinationURL.Host == "" {
		destinationURL.Host = options.Server
	}
	if options.ServerPort != 0 && options.ServerPort != 443 {
		destinationURL.Host = net.JoinHostPort(destinationURL.Host, strconv.Itoa(int(options.ServerPort)))
	}
	path := options.Path
	if path == "" {
		path = "/dns-query"
	}
	err = sHTTP.URLSetPath(&destinationURL, path)
	if err != nil {
		return nil, err
	}
	serverAddr := options.DNSServerAddressOptions.Build()
	if serverAddr.Port == 0 {
		serverAddr.Port = 443
	}
	if !serverAddr.IsValid() {
		return nil, E.New("invalid server address: ", serverAddr)
	}
	return NewHTTPSRaw(
		dns.NewTransportAdapterWithRemoteOptions(C.DNSTypeHTTPS, tag, options.RemoteDNSServerOptions),
		logger,
		transportDialer,
		&destinationURL,
		headers,
		serverAddr,
		tlsConfig,
	), nil
}

func NewHTTPSRaw(
	adapter dns.TransportAdapter,
	logger log.ContextLogger,
	dialer N.Dialer,
	destination *url.URL,
	headers http.Header,
	serverAddr M.Socksaddr,
	tlsConfig tls.Config,
) *HTTPSTransport {
	if tlsConfig != nil {
		dialer = tls.NewDialer(dialer, tlsConfig)
	}
	fallback := new(atomic.Bool)
	if destination.Scheme == "http" {
		// plain HTTP DoH used by Tailscale
		fallback.Store(true)
	}
	return &HTTPSTransport{
		TransportAdapter: adapter,
		logger:           logger,
		dialer:           dialer,
		destination:      destination,
		headers:          headers,
		serverAddr:       serverAddr,
		fallback:         fallback,
		transport:        NewHTTPSTransportWrapper(dialer, serverAddr, fallback),
	}
}

func (t *HTTPSTransport) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	return dialer.InitializeDetour(t.dialer)
}

func (t *HTTPSTransport) Close() error {
	t.transportAccess.Lock()
	if t.closed {
		t.transportAccess.Unlock()
		return nil
	}
	t.closed = true
	oldTransport := t.transport
	t.transport = nil
	t.transportAccess.Unlock()
	if oldTransport != nil {
		oldTransport.Close()
	}
	return nil
}

func (t *HTTPSTransport) Reset() {
	t.transportAccess.Lock()
	defer t.transportAccess.Unlock()
	if t.closed {
		return
	}
	t.resetTransportLocked()
}

func (t *HTTPSTransport) resetTransportLocked() {
	oldTransport := t.transport
	t.transport = NewHTTPSTransportWrapper(t.dialer, t.serverAddr, t.fallback)
	t.transportResetAt = time.Now()
	if oldTransport != nil {
		oldTransport.Close()
	}
}

func (t *HTTPSTransport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	startAt := time.Now()
	response, err := t.exchange(ctx, message)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			t.transportAccess.Lock()
			defer t.transportAccess.Unlock()
			if t.closed || t.transportResetAt.After(startAt) {
				return nil, err
			}
			t.resetTransportLocked()
		}
		return nil, err
	}
	return response, nil
}

func (t *HTTPSTransport) ExchangeAsync(ctx context.Context, message *mDNS.Msg, callback func(response *mDNS.Msg, err error)) {
	go func() {
		callback(t.Exchange(ctx, message))
	}()
}

func (t *HTTPSTransport) exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	exMessage := message.Copy()
	exMessage.Id = 0
	exMessage.Compress = true
	requestBuffer := buf.NewSize(1 + message.Len())
	rawMessage, err := exMessage.PackBuffer(requestBuffer.FreeBytes())
	if err != nil {
		requestBuffer.Release()
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.destination.String(), bytes.NewReader(rawMessage))
	if err != nil {
		requestBuffer.Release()
		return nil, err
	}
	request.Header = t.headers.Clone()
	request.Header.Set("Content-Type", MimeType)
	request.Header.Set("Accept", MimeType)
	t.transportAccess.Lock()
	if t.closed || t.transport == nil {
		t.transportAccess.Unlock()
		requestBuffer.Release()
		return nil, net.ErrClosed
	}
	currentTransport := t.transport
	t.transportAccess.Unlock()
	response, err := currentTransport.RoundTrip(request)
	requestBuffer.Release()
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, E.New("unexpected status: ", response.Status)
	}
	return ReadDNSResponseBody(response.Body, response.ContentLength)
}

// ReadDNSResponseBody reads one bounded DNS wire message from an HTTP response body.
func ReadDNSResponseBody(body io.Reader, contentLength int64) (*mDNS.Msg, error) {
	if contentLength > maxDNSWireMessageSize {
		return nil, E.New("DNS response exceeds wire size limit: ", contentLength)
	}
	var (
		rawMessage []byte
		err        error
	)
	if contentLength >= 0 {
		rawMessage = make([]byte, int(contentLength))
		_, err = io.ReadFull(body, rawMessage)
	} else {
		rawMessage, err = io.ReadAll(io.LimitReader(body, maxDNSWireMessageSize+1))
		if err == nil && len(rawMessage) > maxDNSWireMessageSize {
			return nil, E.New("DNS response exceeds wire size limit: ", len(rawMessage))
		}
	}
	if err != nil {
		return nil, err
	}
	var responseMessage mDNS.Msg
	if err = responseMessage.Unpack(rawMessage); err != nil {
		return nil, err
	}
	return &responseMessage, nil
}
