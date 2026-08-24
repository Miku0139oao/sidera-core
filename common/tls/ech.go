//go:build go1.24

package tls

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/pem"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/dns"
	"github.com/Miku0139oao/sidera-core/option"
	E "github.com/sagernet/sing/common/exceptions"
	aTLS "github.com/sagernet/sing/common/tls"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"

	mDNS "github.com/miekg/dns"
	"golang.org/x/crypto/cryptobyte"
)

func parseECHClientConfig(ctx context.Context, clientConfig ECHCapableConfig, options option.OutboundTLSOptions) (Config, error) {
	var echConfig []byte
	if len(options.ECH.Config) > 0 {
		echConfig = []byte(strings.Join(options.ECH.Config, "\n"))
	} else if options.ECH.ConfigPath != "" {
		content, err := filemanager.ReadFile(ctx, options.ECH.ConfigPath)
		if err != nil {
			return nil, E.Cause(err, "read ECH config")
		}
		echConfig = content
	}
	//nolint:staticcheck
	if options.ECH.PQSignatureSchemesEnabled || options.ECH.DynamicRecordSizingDisabled {
		return nil, E.New("legacy ECH options are deprecated in sing-box 1.12.0 and removed in sing-box 1.13.0")
	}

	config := &ECHClientConfig{
		config:          clientConfig,
		queryServerName: options.ECH.QueryServerName,
	}
	if len(echConfig) > 0 {
		block, rest := pem.Decode(echConfig)
		if block == nil || block.Type != "ECH CONFIGS" || len(rest) > 0 {
			return nil, E.New("invalid ECH configs pem")
		}
		config.updateECHConfigLocked(block.Bytes)
		return config, nil
	}

	config.dynamic = true
	config.dnsRouter = service.FromContext[adapter.DNSRouter](ctx)
	return config, nil
}

func parseECHServerConfig(ctx context.Context, options option.InboundTLSOptions, tlsConfig *tls.Config, echKeyPath *string) error {
	var echKey []byte
	if len(options.ECH.Key) > 0 {
		echKey = []byte(strings.Join(options.ECH.Key, "\n"))
	} else if options.ECH.KeyPath != "" {
		content, err := filemanager.ReadFile(ctx, options.ECH.KeyPath)
		if err != nil {
			return E.Cause(err, "read ECH keys")
		}
		echKey = content
		*echKeyPath = options.ECH.KeyPath
	} else {
		return E.New("missing ECH keys")
	}
	echKeys, err := parseECHKeys(echKey)
	if err != nil {
		return E.Cause(err, "parse ECH keys")
	}
	tlsConfig.EncryptedClientHelloKeys = echKeys
	//nolint:staticcheck
	if options.ECH.PQSignatureSchemesEnabled || options.ECH.DynamicRecordSizingDisabled {
		return E.New("legacy ECH options are deprecated in sing-box 1.12.0 and removed in sing-box 1.13.0")
	}
	return nil
}

func (c *STDServerConfig) setECHServerConfig(echKey []byte) error {
	echKeys, err := parseECHKeys(echKey)
	if err != nil {
		return err
	}
	c.access.Lock()
	config := c.config.Clone()
	config.EncryptedClientHelloKeys = echKeys
	c.config = config
	c.access.Unlock()
	return nil
}

func parseECHKeys(echKey []byte) ([]tls.EncryptedClientHelloKey, error) {
	block, _ := pem.Decode(echKey)
	if block == nil || block.Type != "ECH KEYS" {
		return nil, E.New("invalid ECH keys pem")
	}
	echKeys, err := UnmarshalECHKeys(block.Bytes)
	if err != nil {
		return nil, E.Cause(err, "parse ECH keys")
	}
	return echKeys, nil
}

// ECHClientConfig publishes immutable client-config generations. A connection
// keeps the generation it captured even if DNS refresh or an ECH retry installs
// a newer config list while its handshake is active.
type ECHClientConfig struct {
	access          sync.Mutex
	config          ECHCapableConfig
	dnsRouter       adapter.DNSRouter
	queryServerName string
	dynamic         bool
	retryPending    bool
	lastTTL         time.Duration
	lastUpdate      time.Time
}

func (s *ECHClientConfig) ServerName() string {
	s.access.Lock()
	defer s.access.Unlock()
	return s.config.ServerName()
}

func (s *ECHClientConfig) SetServerName(serverName string) {
	s.mutate(func(config ECHCapableConfig) {
		config.SetServerName(serverName)
	})
}

func (s *ECHClientConfig) NextProtos() []string {
	s.access.Lock()
	defer s.access.Unlock()
	return append([]string(nil), s.config.NextProtos()...)
}

func (s *ECHClientConfig) SetNextProtos(nextProtos []string) {
	nextProtos = append([]string(nil), nextProtos...)
	s.mutate(func(config ECHCapableConfig) {
		config.SetNextProtos(nextProtos)
	})
}

func (s *ECHClientConfig) HandshakeTimeout() time.Duration {
	s.access.Lock()
	defer s.access.Unlock()
	return s.config.HandshakeTimeout()
}

func (s *ECHClientConfig) SetHandshakeTimeout(timeout time.Duration) {
	s.mutate(func(config ECHCapableConfig) {
		config.SetHandshakeTimeout(timeout)
	})
}

func (s *ECHClientConfig) STDConfig() (*STDConfig, error) {
	config := s.snapshot()
	stdConfig, err := config.STDConfig()
	if err != nil || stdConfig == nil {
		return stdConfig, err
	}
	return stdConfig.Clone(), nil
}

func (s *ECHClientConfig) Client(conn net.Conn) (Conn, error) {
	return s.snapshot().Client(conn)
}

func (s *ECHClientConfig) ClientHandshake(ctx context.Context, conn net.Conn) (aTLS.Conn, error) {
	config, err := s.configForHandshake(ctx)
	if err != nil {
		return nil, err
	}
	return aTLS.ClientHandshake(ctx, conn, config)
}

func (s *ECHClientConfig) ECHConfigList() []byte {
	s.access.Lock()
	defer s.access.Unlock()
	return append([]byte(nil), s.config.ECHConfigList()...)
}

func (s *ECHClientConfig) SetECHConfigList(configList []byte) {
	configList = append([]byte(nil), configList...)
	s.access.Lock()
	s.updateECHConfigLocked(configList)
	s.access.Unlock()
}

func (s *ECHClientConfig) SetECHRetryConfigList(configList []byte) {
	configList = append([]byte(nil), configList...)
	s.access.Lock()
	s.updateECHConfigLocked(configList)
	s.retryPending = true
	s.access.Unlock()
}

func (s *ECHClientConfig) Clone() Config {
	s.access.Lock()
	defer s.access.Unlock()
	return &ECHClientConfig{
		config:          s.config.Clone().(ECHCapableConfig),
		dnsRouter:       s.dnsRouter,
		queryServerName: s.queryServerName,
		dynamic:         s.dynamic,
		retryPending:    s.retryPending,
		lastTTL:         s.lastTTL,
		lastUpdate:      s.lastUpdate,
	}
}

func (s *ECHClientConfig) snapshot() ECHCapableConfig {
	s.access.Lock()
	defer s.access.Unlock()
	return s.config
}

func (s *ECHClientConfig) mutate(update func(ECHCapableConfig)) {
	s.access.Lock()
	config := s.config.Clone().(ECHCapableConfig)
	update(config)
	s.config = config
	s.access.Unlock()
}

func (s *ECHClientConfig) updateECHConfigLocked(configList []byte) {
	config := s.config.Clone().(ECHCapableConfig)
	config.SetECHConfigList(append([]byte(nil), configList...))
	s.config = config
}

func (s *ECHClientConfig) configForHandshake(ctx context.Context) (ECHCapableConfig, error) {
	s.access.Lock()
	defer s.access.Unlock()

	if s.retryPending {
		s.retryPending = false
		return s.config, nil
	}
	if !s.dynamic {
		return s.config, nil
	}
	if len(s.config.ECHConfigList()) != 0 && s.lastTTL != 0 && time.Since(s.lastUpdate) <= s.lastTTL {
		return s.config, nil
	}

	queryServerName := s.queryServerName
	if queryServerName == "" {
		queryServerName = s.config.ServerName()
	}
	message := &mDNS.Msg{
		MsgHdr: mDNS.MsgHdr{
			RecursionDesired: true,
		},
		Question: []mDNS.Question{
			{
				Name:   mDNS.Fqdn(queryServerName),
				Qtype:  mDNS.TypeHTTPS,
				Qclass: mDNS.ClassINET,
			},
		},
	}
	response, err := s.dnsRouter.Exchange(ctx, message, adapter.DNSQueryOptions{})
	if err != nil {
		return nil, E.Cause(err, "fetch ECH config list")
	}
	if response.Rcode != mDNS.RcodeSuccess {
		return nil, E.Cause(dns.RcodeError(response.Rcode), "fetch ECH config list")
	}

	var found bool
match:
	for _, rr := range response.Answer {
		switch resource := rr.(type) {
		case *mDNS.HTTPS:
			for _, value := range resource.Value {
				if value.Key().String() == "ech" {
					echConfigList, err := base64.StdEncoding.DecodeString(value.String())
					if err != nil {
						return nil, E.Cause(err, "decode ECH config")
					}
					s.lastTTL = time.Duration(rr.Header().Ttl) * time.Second
					s.lastUpdate = time.Now()
					s.updateECHConfigLocked(echConfigList)
					found = true
					break match
				}
			}
		}
	}
	if !found || len(s.config.ECHConfigList()) == 0 {
		return nil, E.New("no ECH config found in DNS records")
	}
	return s.config, nil
}

func UnmarshalECHKeys(raw []byte) ([]tls.EncryptedClientHelloKey, error) {
	var keys []tls.EncryptedClientHelloKey
	rawString := cryptobyte.String(raw)
	for !rawString.Empty() {
		var key tls.EncryptedClientHelloKey
		if !rawString.ReadUint16LengthPrefixed((*cryptobyte.String)(&key.PrivateKey)) {
			return nil, E.New("error parsing private key")
		}
		if !rawString.ReadUint16LengthPrefixed((*cryptobyte.String)(&key.Config)) {
			return nil, E.New("error parsing config")
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, E.New("empty ECH keys")
	}
	return keys, nil
}
