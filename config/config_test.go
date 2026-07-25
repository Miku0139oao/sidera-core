package config_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/Miku0139oao/sidera-core/config"
	"github.com/Miku0139oao/sidera-core/include"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/stretchr/testify/require"
)

func TestDetectDialect(t *testing.T) {
	testCases := []struct {
		name    string
		content string
		dialect config.Dialect
		error   string
	}{
		{
			name:    "empty defaults to sing-box",
			content: `{}`,
			dialect: config.DialectSingBox,
		},
		{
			name: "sing-box endpoint",
			content: `{
				// Extended JSON comments remain supported.
				"inbounds": [{"type": "mixed", "listen_port": 1080}]
			}`,
			dialect: config.DialectSingBox,
		},
		{
			name:    "Xray endpoint",
			content: `{"outbounds":[{"protocol":"freedom"}]}`,
			dialect: config.DialectXray,
		},
		{
			name:    "Xray log",
			content: `{"log":{"loglevel":"warning"}}`,
			dialect: config.DialectXray,
		},
		{
			name:    "mixed root fields",
			content: `{"route":{},"routing":{}}`,
			error:   "ambiguous config dialect",
		},
		{
			name:    "mixed endpoint fields",
			content: `{"outbounds":[{"type":"direct","protocol":"freedom"}]}`,
			error:   "ambiguous config dialect",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			dialect, err := config.Detect([]byte(testCase.content))
			if testCase.error != "" {
				require.ErrorContains(t, err, testCase.error)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.dialect, dialect)
		})
	}
}

func TestDecodeSingBox(t *testing.T) {
	ctx := include.Context(context.Background())
	options, dialect, err := config.Decode(ctx, []byte(`{
		"inbounds": [{"type": "mixed", "tag": "local", "listen": "127.0.0.1", "listen_port": 1080}],
		"outbounds": [{"type": "direct", "tag": "direct"}]
	}`))
	require.NoError(t, err)
	require.Equal(t, config.DialectSingBox, dialect)
	require.Len(t, options.Inbounds, 1)
	require.Len(t, options.Outbounds, 1)
}

func TestDecodeXrayVLESSClient(t *testing.T) {
	ctx := include.Context(context.Background())
	options, dialect, err := config.Decode(ctx, []byte(`{
		"log": {"access": "none", "loglevel": "warning"},
		"inbounds": [{
			"tag": "local",
			"listen": "127.0.0.1",
			"port": 1080,
			"protocol": "socks",
			"settings": {"auth": "noauth", "udp": true},
			"sniffing": {"enabled": true, "destOverride": ["http", "tls"], "routeOnly": true}
		}],
		"outbounds": [{
			"tag": "proxy",
			"protocol": "vless",
			"settings": {"vnext": [{
				"address": "proxy.example.com",
				"port": 443,
				"users": [{"id": "27848739-7e62-4138-9fd3-098a63964b6b", "encryption": "none"}]
			}]},
			"streamSettings": {
				"network": "ws",
				"security": "tls",
				"tlsSettings": {"serverName": "proxy.example.com", "fingerprint": "chrome"},
				"wsSettings": {"path": "/ws?ed=2048", "headers": {"Host": "cdn.example.com"}}
			}
		}, {
			"tag": "direct",
			"protocol": "freedom",
			"settings": {}
		}, {
			"tag": "block",
			"protocol": "blackhole",
			"settings": {"response": {"type": "none"}}
		}],
		"routing": {
			"domainStrategy": "AsIs",
			"rules": [{
				"type": "field",
				"domain": ["full:api.example.com", "domain:example.org", "keyword:ads", "regexp:^x"],
				"outboundTag": "block"
			}, {
				"type": "field",
				"ip": ["10.0.0.0/8"],
				"port": "53,80,1000-2000",
				"network": "tcp,udp",
				"outboundTag": "direct"
			}]
		}
	}`))
	require.NoError(t, err)
	require.Equal(t, config.DialectXray, dialect)
	require.Len(t, options.Inbounds, 1)
	require.Equal(t, "socks", options.Inbounds[0].Type)
	require.Len(t, options.Outbounds, 3)

	vlessOptions, loaded := options.Outbounds[0].Options.(*option.VLESSOutboundOptions)
	require.True(t, loaded)
	require.Equal(t, "proxy.example.com", vlessOptions.Server)
	require.Equal(t, uint16(443), vlessOptions.ServerPort)
	require.Equal(t, "27848739-7e62-4138-9fd3-098a63964b6b", vlessOptions.UUID)
	require.NotNil(t, vlessOptions.TLS)
	require.Equal(t, "proxy.example.com", vlessOptions.TLS.ServerName)
	require.NotNil(t, vlessOptions.TLS.UTLS)
	require.Equal(t, "chrome", vlessOptions.TLS.UTLS.Fingerprint)
	require.NotNil(t, vlessOptions.Transport)
	require.Equal(t, "ws", vlessOptions.Transport.Type)
	require.Equal(t, "/ws", vlessOptions.Transport.WebsocketOptions.Path)
	require.Equal(t, uint32(2048), vlessOptions.Transport.WebsocketOptions.MaxEarlyData)
	require.Len(t, options.Route.Rules, 3)
}

func TestDecodeXrayReality(t *testing.T) {
	ctx := include.Context(context.Background())
	options, dialect, err := config.Decode(ctx, []byte(`{
		"outbounds": [{
			"tag": "proxy",
			"protocol": "vless",
			"settings": {
				"address": "proxy.example.com",
				"port": 443,
				"id": "27848739-7e62-4138-9fd3-098a63964b6b",
				"flow": "xtls-rprx-vision",
				"encryption": "none"
			},
			"streamSettings": {
				"network": "raw",
				"security": "reality",
				"realitySettings": {
					"fingerprint": "chrome",
					"serverName": "www.example.com",
					"publicKey": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
					"shortId": "0123456789abcdef",
					"spiderX": "/"
				}
			}
		}]
	}`))
	require.NoError(t, err)
	require.Equal(t, config.DialectXray, dialect)
	vlessOptions := options.Outbounds[0].Options.(*option.VLESSOutboundOptions)
	require.Equal(t, "xtls-rprx-vision", vlessOptions.Flow)
	require.NotNil(t, vlessOptions.TLS)
	require.NotNil(t, vlessOptions.TLS.Reality)
	require.True(t, vlessOptions.TLS.Reality.Enabled)
	require.Equal(t, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", vlessOptions.TLS.Reality.PublicKey)
	require.Equal(t, "0123456789abcdef", vlessOptions.TLS.Reality.ShortID)
}

func TestDecodeXrayRequiresSecurityForPublicVLESS(t *testing.T) {
	ctx := include.Context(context.Background())
	_, dialect, err := config.Decode(ctx, []byte(`{
		"outbounds": [{
			"protocol": "vless",
			"settings": {
				"address": "public.example.com",
				"port": 443,
				"id": "27848739-7e62-4138-9fd3-098a63964b6b",
				"encryption": "none"
			}
		}]
	}`))
	require.Equal(t, config.DialectXray, dialect)
	require.ErrorContains(t, err, "VLESS without TLS")

	_, dialect, err = config.Decode(ctx, []byte(`{
		"outbounds": [{
			"protocol": "vless",
			"settings": {
				"address": "192.168.1.1",
				"port": 443,
				"id": "27848739-7e62-4138-9fd3-098a63964b6b",
				"encryption": "none"
			}
		}]
	}`))
	require.NoError(t, err)
	require.Equal(t, config.DialectXray, dialect)
}

func TestDecodeXrayDefaultsInboundListenToAnyIP(t *testing.T) {
	ctx := include.Context(context.Background())
	options, _, err := config.Decode(ctx, []byte(`{
		"inbounds": [{
			"protocol": "socks",
			"port": 1080,
			"settings": {"auth": "noauth", "udp": true}
		}],
		"outbounds": [{"protocol": "freedom"}]
	}`))
	require.NoError(t, err)
	socksOptions := options.Inbounds[0].Options.(*option.SocksInboundOptions)
	require.Equal(t, netip.IPv4Unspecified(), socksOptions.Listen.Build(netip.Addr{}))
}

func TestDecodeXrayRejectsDisabledSOCKSUDP(t *testing.T) {
	ctx := include.Context(context.Background())
	_, dialect, err := config.Decode(ctx, []byte(`{
		"inbounds": [{
			"protocol": "socks",
			"port": 1080,
			"settings": {"auth": "noauth", "udp": false}
		}],
		"outbounds": [{"protocol": "freedom"}]
	}`))
	require.Equal(t, config.DialectXray, dialect)
	require.ErrorContains(t, err, "udp=false")
}

func TestDecodeXrayRoutingAliasPrecedence(t *testing.T) {
	ctx := include.Context(context.Background())
	options, _, err := config.Decode(ctx, []byte(`{
		"outbounds": [
			{"protocol": "freedom", "tag": "direct"},
			{"protocol": "blackhole", "tag": "block"}
		],
		"routing": {"rules": [{
			"domain": ["full:a.example"],
			"domains": null,
			"outboundTag": "block"
		}, {
			"domain": ["full:a.example"],
			"domains": ["full:b.example"],
			"outboundTag": "block"
		}, {
			"sourceIP": [],
			"source": ["10.0.0.0/8"],
			"outboundTag": "direct"
		}]}
	}`))
	require.NoError(t, err)
	require.Len(t, options.Route.Rules, 3)
	require.Equal(t, []string{"a.example"}, []string(options.Route.Rules[0].DefaultOptions.Domain))
	require.Empty(t, options.Route.Rules[0].DefaultOptions.DomainKeyword)
	require.Equal(t, []string{"b.example"}, []string(options.Route.Rules[1].DefaultOptions.Domain))
	require.Empty(t, options.Route.Rules[2].DefaultOptions.SourceIPCIDR)
}

func TestDecodeXrayRejectsConsoleAccessLog(t *testing.T) {
	ctx := include.Context(context.Background())
	_, dialect, err := config.Decode(ctx, []byte(`{
		"log": {"loglevel": "warning"},
		"outbounds": [{"protocol": "freedom"}]
	}`))
	require.Equal(t, config.DialectXray, dialect)
	require.ErrorContains(t, err, "console access logging")
}

func TestDecodeXrayRejectsUnsupportedFields(t *testing.T) {
	ctx := include.Context(context.Background())
	_, dialect, err := config.Decode(ctx, []byte(`{
		"routing": {},
		"dns": {"queryStrategy": "UseIP"}
	}`))
	require.Equal(t, config.DialectXray, dialect)
	require.ErrorContains(t, err, "Xray field is not supported: dns")
}

func TestDecodeXrayRejectsUnknownFields(t *testing.T) {
	ctx := include.Context(context.Background())
	_, dialect, err := config.Decode(ctx, []byte(`{
		"outbounds": [{
			"protocol": "freedom",
			"settings": {"unexpected": true}
		}]
	}`))
	require.Equal(t, config.DialectXray, dialect)
	require.ErrorContains(t, err, "unknown field")
}
