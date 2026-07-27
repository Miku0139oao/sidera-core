package config_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/config"
	"github.com/Miku0139oao/sidera-core/include"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/stretchr/testify/require"
)

func xrayVLESSDocument(streamSettings string) string {
	return `{
		"outbounds": [{
			"protocol": "vless",
			"settings": {
				"address": "192.168.1.1",
				"port": 443,
				"id": "27848739-7e62-4138-9fd3-098a63964b6b",
				"encryption": "none"
			},
			"streamSettings": ` + streamSettings + `
		}]
	}`
}

func xrayRoutingDocument(rule string) string {
	return `{
		"outbounds": [
			{"protocol": "freedom", "tag": "direct"},
			{"protocol": "blackhole", "tag": "block"}
		],
		"routing": {"rules": [` + rule + `]}
	}`
}

func decodeXrayTestConfig(t *testing.T, content string) option.Options {
	t.Helper()
	options, dialect, err := config.Decode(include.Context(context.Background()), []byte(content))
	require.NoError(t, err)
	require.Equal(t, config.DialectXray, dialect)
	return options
}

func requireXrayTestError(t *testing.T, content string, message string) {
	t.Helper()
	_, dialect, err := config.Decode(include.Context(context.Background()), []byte(content))
	require.Equal(t, config.DialectXray, dialect)
	require.ErrorContains(t, err, message)
}

func TestDecodeXrayTLSCompatibility(t *testing.T) {
	t.Run("removed allowInsecure", func(t *testing.T) {
		requireXrayTestError(t, xrayVLESSDocument(`{
			"network": "raw",
			"security": "tls",
			"tlsSettings": {"allowInsecure": true}
		}`), "allowInsecure has been removed")
	})

	t.Run("default fingerprint", func(t *testing.T) {
		options := decodeXrayTestConfig(t, xrayVLESSDocument(`{
			"network": "raw",
			"security": "tls",
			"tlsSettings": {}
		}`))
		vlessOptions := options.Outbounds[0].Options.(*option.VLESSOutboundOptions)
		require.NotNil(t, vlessOptions.TLS.UTLS)
		require.Equal(t, "chrome", vlessOptions.TLS.UTLS.Fingerprint)
	})

	t.Run("unsafe means standard TLS", func(t *testing.T) {
		options := decodeXrayTestConfig(t, xrayVLESSDocument(`{
			"network": "raw",
			"security": "tls",
			"tlsSettings": {"fingerprint": "unsafe"}
		}`))
		vlessOptions := options.Outbounds[0].Options.(*option.VLESSOutboundOptions)
		require.Nil(t, vlessOptions.TLS.UTLS)
		require.False(t, vlessOptions.TLS.Insecure)
	})

	t.Run("scalar ALPN", func(t *testing.T) {
		options := decodeXrayTestConfig(t, xrayVLESSDocument(`{
			"network": "raw",
			"security": "tls",
			"tlsSettings": {"alpn": "h2,http/1.1"}
		}`))
		vlessOptions := options.Outbounds[0].Options.(*option.VLESSOutboundOptions)
		require.Equal(t, []string{"h2", "http/1.1"}, []string(vlessOptions.TLS.ALPN))
	})

	t.Run("invalid versions are ignored", func(t *testing.T) {
		options := decodeXrayTestConfig(t, xrayVLESSDocument(`{
			"network": "raw",
			"security": "tls",
			"tlsSettings": {"minVersion": "bogus", "maxVersion": "future"}
		}`))
		vlessOptions := options.Outbounds[0].Options.(*option.VLESSOutboundOptions)
		require.Empty(t, vlessOptions.TLS.MinVersion)
		require.Empty(t, vlessOptions.TLS.MaxVersion)
	})

	t.Run("unsupported fingerprint", func(t *testing.T) {
		requireXrayTestError(t, xrayVLESSDocument(`{
			"network": "raw",
			"security": "tls",
			"tlsSettings": {"fingerprint": "randomizednoalpn"}
		}`), "has no current Sidera equivalent")
	})

	t.Run("REALITY default fingerprint", func(t *testing.T) {
		options := decodeXrayTestConfig(t, xrayVLESSDocument(`{
			"network": "raw",
			"security": "reality",
			"realitySettings": {
				"serverName": "www.example.com",
				"publicKey": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
				"shortId": "0123456789abcdef"
			}
		}`))
		vlessOptions := options.Outbounds[0].Options.(*option.VLESSOutboundOptions)
		require.NotNil(t, vlessOptions.TLS.UTLS)
		require.Equal(t, "chrome", vlessOptions.TLS.UTLS.Fingerprint)
	})
}

func TestDecodeXrayRoutingCompatibility(t *testing.T) {
	t.Run("scalar lists split on commas", func(t *testing.T) {
		options := decodeXrayTestConfig(t, xrayRoutingDocument(`{
			"domain": "full:a.example,full:b.example",
			"network": "tcp,udp",
			"outboundTag": "direct"
		}`))
		rule := options.Route.Rules[0].DefaultOptions
		require.Equal(t, []string{"a.example", "b.example"}, []string(rule.Domain))
		require.Equal(t, []string{"tcp", "udp"}, []string(rule.Network))
	})

	t.Run("rules require an effective matcher", func(t *testing.T) {
		requireXrayTestError(t, xrayRoutingDocument(`{
			"outboundTag": "block"
		}`), "no effective fields")
	})

	t.Run("empty port cannot become match all", func(t *testing.T) {
		requireXrayTestError(t, xrayRoutingDocument(`{
			"port": "",
			"outboundTag": "block"
		}`), "empty port expression")
	})

	t.Run("empty users are ignored beside exact users", func(t *testing.T) {
		options := decodeXrayTestConfig(t, xrayRoutingDocument(`{
			"user": ["", "alice"],
			"outboundTag": "direct"
		}`))
		require.Equal(t, []string{"alice"}, []string(options.Route.Rules[0].DefaultOptions.AuthUser))
	})

	t.Run("empty-only user matcher is rejected", func(t *testing.T) {
		requireXrayTestError(t, xrayRoutingDocument(`{
			"user": [""],
			"outboundTag": "block"
		}`), "no translatable matchers")
	})

	t.Run("regexp users are rejected", func(t *testing.T) {
		requireXrayTestError(t, xrayRoutingDocument(`{
			"user": ["regexp:^admin"],
			"outboundTag": "block"
		}`), "regexp user rules")
	})

	t.Run("dotless domain", func(t *testing.T) {
		options := decodeXrayTestConfig(t, xrayRoutingDocument(`{
			"domain": ["dotless:ads", "dotless:"],
			"outboundTag": "block"
		}`))
		require.Equal(t, []string{"^[^.]*ads[^.]*$", "^[^.]*$"}, []string(options.Route.Rules[0].DefaultOptions.DomainRegex))
	})

	t.Run("negated IP is rejected early", func(t *testing.T) {
		requireXrayTestError(t, xrayRoutingDocument(`{
			"ip": ["!10.0.0.0/8"],
			"outboundTag": "direct"
		}`), "negated IP rules")
	})

	t.Run("array network elements are not comma split", func(t *testing.T) {
		requireXrayTestError(t, xrayRoutingDocument(`{
			"network": ["tcp,udp"],
			"outboundTag": "direct"
		}`), "unsupported network")
	})
}

func TestDecodeXrayWireGuardOutboundAsEndpoint(t *testing.T) {
	options := decodeXrayTestConfig(t, `{
		"outbounds": [{
			"protocol": "wireguard",
			"tag": "warp-discord",
			"settings": {
				"secretKey": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
				"address": ["172.16.0.2/32", "2606:4700:110:8765::2/128"],
				"peers": [{
					"publicKey": "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
					"allowedIPs": ["0.0.0.0/0", "::/0"],
					"endpoint": "engage.cloudflareclient.com:2408",
					"keepAlive": 25
				}],
				"reserved": [1, 2, 3],
				"noKernelTun": true,
				"mtu": 1280,
				"domainStrategy": "ForceIP"
			}
		}]
	}`)
	require.Empty(t, options.Outbounds)
	require.Len(t, options.Endpoints, 1)
	require.Equal(t, "warp-discord", options.Endpoints[0].Tag)
	wireGuard := options.Endpoints[0].Options.(*option.WireGuardEndpointOptions)
	require.EqualValues(t, 1280, wireGuard.MTU)
	require.Len(t, wireGuard.Peers, 1)
	require.Equal(t, "engage.cloudflareclient.com", wireGuard.Peers[0].Address)
	require.EqualValues(t, 2408, wireGuard.Peers[0].Port)
	require.Equal(t, []uint8{1, 2, 3}, wireGuard.Peers[0].Reserved)
}

func TestDecodeXrayWireGuardRequiresUserspaceMode(t *testing.T) {
	requireXrayTestError(t, `{
		"outbounds": [{
			"protocol": "wireguard",
			"tag": "warp",
			"settings": {
				"secretKey": "secret",
				"address": ["172.16.0.2/32"],
				"peers": [{"publicKey": "public", "allowedIPs": ["0.0.0.0/0"], "endpoint": "127.0.0.1:2408"}]
			}
		}]
	}`, "kernel TUN mode is not supported")
}

func TestDecodeXrayTransportCompatibility(t *testing.T) {
	for _, field := range []string{"network", "method"} {
		t.Run("empty "+field, func(t *testing.T) {
			requireXrayTestError(t, xrayVLESSDocument(`{
				"`+field+`": "",
				"security": "tls",
				"tlsSettings": {}
			}`), "cannot be empty")
		})
	}

	t.Run("custom gRPC path", func(t *testing.T) {
		requireXrayTestError(t, xrayVLESSDocument(`{
			"network": "grpc",
			"security": "tls",
			"tlsSettings": {},
			"grpcSettings": {"serviceName": "/foo/Bar"}
		}`), "custom gRPC service paths")
	})

	t.Run("WebSocket edge semantics", func(t *testing.T) {
		options := decodeXrayTestConfig(t, xrayVLESSDocument(`{
			"network": "ws",
			"security": "tls",
			"tlsSettings": {"alpn": ["h2"]},
			"wsSettings": {"path": "/ws?ed=-1"}
		}`))
		vlessOptions := options.Outbounds[0].Options.(*option.VLESSOutboundOptions)
		require.Equal(t, uint32(math.MaxUint32), vlessOptions.Transport.WebsocketOptions.MaxEarlyData)
		require.Equal(t, "/ws", vlessOptions.Transport.WebsocketOptions.Path)
		require.Equal(t, []string{"h2"}, []string(vlessOptions.TLS.ALPN))
	})

	t.Run("WebSocket standard TLS preserves explicit ALPN", func(t *testing.T) {
		options := decodeXrayTestConfig(t, xrayVLESSDocument(`{
			"network": "ws",
			"security": "tls",
			"tlsSettings": {"fingerprint": "unsafe", "alpn": ["h2"]},
			"wsSettings": {"path": "/ws"}
		}`))
		vlessOptions := options.Outbounds[0].Options.(*option.VLESSOutboundOptions)
		require.Equal(t, []string{"h2"}, []string(vlessOptions.TLS.ALPN))
	})
}

func TestDecodeXraySOCKSUsesMixedInbound(t *testing.T) {
	options := decodeXrayTestConfig(t, `{
		"inbounds": [{
			"protocol": "socks",
			"listen": "127.0.0.1",
			"port": 1080,
			"settings": {
				"auth": "password",
				"udp": true,
				"accounts": [{"user": "alice", "pass": "secret"}]
			}
		}],
		"outbounds": [{"protocol": "freedom"}]
	}`)
	require.Equal(t, "mixed", options.Inbounds[0].Type)
	mixedOptions := options.Inbounds[0].Options.(*option.HTTPMixedInboundOptions)
	require.Len(t, mixedOptions.Users, 1)
	require.Equal(t, "alice", mixedOptions.Users[0].Username)
	require.Equal(t, "secret", mixedOptions.Users[0].Password)
}

func TestDecodeXrayMarksVLESSPacketEncoding(t *testing.T) {
	options := decodeXrayTestConfig(t, xrayVLESSDocument(`{
		"network": "raw",
		"security": "tls",
		"tlsSettings": {}
	}`))
	vlessOptions := options.Outbounds[0].Options.(*option.VLESSOutboundOptions)
	require.True(t, vlessOptions.XrayPacketEncoding)
}

func TestDecodeXrayVLESSInboundWithRealityEncryption(t *testing.T) {
	decryption := "mlkem768x25519plus.native.600s." + strings.Repeat("A", 86)
	options := decodeXrayTestConfig(t, `{
		"inbounds": [{
			"protocol": "vless",
			"tag": "vless-in",
			"listen": "0.0.0.0",
			"port": 443,
			"settings": {
				"clients": [{
					"email": "alice",
					"id": "27848739-7e62-4138-9fd3-098a63964b6b",
					"flow": "xtls-rprx-vision"
				}],
				"decryption": "`+decryption+`",
				"testseed": [900, 500, 900, 256]
			},
			"streamSettings": {
				"network": "raw",
				"security": "reality",
				"realitySettings": {
					"show": true,
					"target": "gateway.example:443",
					"serverNames": ["gateway.example"],
					"privateKey": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
					"maxTimeDiff": 1000,
					"shortIds": ["0123456789abcdef"]
				}
			}
		}],
		"outbounds": [{"protocol": "freedom", "tag": "direct"}]
	}`)
	require.Len(t, options.Inbounds, 1)
	require.Equal(t, "vless", options.Inbounds[0].Type)
	vlessOptions := options.Inbounds[0].Options.(*option.VLESSInboundOptions)
	require.Equal(t, decryption, vlessOptions.Decryption)
	require.Len(t, vlessOptions.Users, 1)
	require.Equal(t, "alice", vlessOptions.Users[0].Name)
	require.Equal(t, "xtls-rprx-vision", vlessOptions.Users[0].Flow)
	require.NotNil(t, vlessOptions.TLS)
	require.Equal(t, "gateway.example", vlessOptions.TLS.ServerName)
	require.Equal(t, "gateway.example", vlessOptions.TLS.Reality.Handshake.Server)
	require.Equal(t, uint16(443), vlessOptions.TLS.Reality.Handshake.ServerPort)
	require.Empty(t, vlessOptions.TLS.Reality.MinClientVersion)
	require.Equal(t, time.Second, time.Duration(vlessOptions.TLS.Reality.MaxTimeDifference))
}

func TestDecodeXrayVLESSInboundDefaultsToRawTCP(t *testing.T) {
	options := decodeXrayTestConfig(t, `{
		"inbounds": [{
			"protocol": "vless",
			"port": 443,
			"settings": {
				"clients": [{"id": "27848739-7e62-4138-9fd3-098a63964b6b"}],
				"decryption": "none"
			}
		}]
	}`)
	require.Len(t, options.Inbounds, 1)
	require.Equal(t, "vless", options.Inbounds[0].Type)
}

func TestDecodeXrayVLESSEncryptionSecuresPublicOutbound(t *testing.T) {
	encryption := "mlkem768x25519plus.native.0rtt." + strings.Repeat("A", 43)
	options := decodeXrayTestConfig(t, `{
		"outbounds": [{
			"protocol": "vless",
			"settings": {
				"address": "public.example",
				"port": 443,
				"id": "27848739-7e62-4138-9fd3-098a63964b6b",
				"encryption": "`+encryption+`"
			}
		}]
	}`)
	vlessOptions := options.Outbounds[0].Options.(*option.VLESSOutboundOptions)
	require.Equal(t, encryption, vlessOptions.Encryption)
}

func TestDecodeXrayRejectsCustomVLESSVisionSeed(t *testing.T) {
	requireXrayTestError(t, `{
		"inbounds": [{
			"protocol": "vless",
			"port": 443,
			"settings": {
				"clients": [{"id": "27848739-7e62-4138-9fd3-098a63964b6b"}],
				"decryption": "none",
				"testseed": [1, 2, 3, 4]
			},
			"streamSettings": {"network": "raw", "security": "none"}
		}]
	}`, "custom VLESS testseed")
}

func TestDecodeXrayLiveServerManagementAndHysteria2(t *testing.T) {
	options := decodeXrayTestConfig(t, `{
		"api": {
			"tag": "api",
			"services": ["HandlerService", "LoggerService", "StatsService", "RoutingService"]
		},
		"metrics": {"listen": "127.0.0.1:11111", "tag": "metrics_out"},
		"stats": {},
		"policy": {
			"levels": {
				"0": {
					"statsUserUplink": true,
					"statsUserDownlink": true,
					"statsUserOnline": true
				}
			},
			"system": {
				"statsInboundUplink": true,
				"statsInboundDownlink": true,
				"statsOutboundUplink": false,
				"statsOutboundDownlink": false
			}
		},
		"inbounds": [
			{
				"tag": "api",
				"protocol": "tunnel",
				"listen": "127.0.0.1",
				"port": 62789,
				"settings": {"rewriteAddress": "127.0.0.1"}
			},
			{
				"tag": "vless-in",
				"protocol": "vless",
				"listen": "0.0.0.0",
				"port": 443,
				"settings": {
					"clients": [{
						"email": "alice",
						"id": "27848739-7e62-4138-9fd3-098a63964b6b"
					}],
					"decryption": "none"
				},
				"streamSettings": {"network": "raw", "security": "none"}
			},
			{
				"tag": "hy2-in",
				"protocol": "hysteria",
				"listen": "0.0.0.0",
				"port": 14231,
				"settings": {
					"version": 2,
					"clients": [{"email": "bob", "auth": "secret-password"}]
				},
				"streamSettings": {
					"network": "hysteria",
					"security": "tls",
					"hysteriaSettings": {
						"version": 2,
						"udpIdleTimeout": 60,
						"masquerade": {"type": "", "headers": {}}
					},
					"finalmask": {
						"quicParams": {
							"congestion": "bbr",
							"bbrProfile": "aggressive",
							"initStreamReceiveWindow": 8388608,
							"maxStreamReceiveWindow": 8388608,
							"initConnectionReceiveWindow": 20971520,
							"maxConnectionReceiveWindow": 20971520,
							"maxIdleTimeout": 30,
							"keepAlivePeriod": 10,
							"maxIncomingStreams": 1024
						}
					},
					"tlsSettings": {
						"serverName": "server.example",
						"alpn": ["h3"],
						"minVersion": "1.2",
						"maxVersion": "1.3",
						"certificates": [{
							"certificateFile": "/tmp/fullchain.pem",
							"keyFile": "/tmp/private.key",
							"usage": "encipherment",
							"ocspStapling": 3600
						}]
					}
				}
			}
		],
		"outbounds": [
			{
				"tag": "direct",
				"protocol": "freedom",
				"settings": {
					"domainStrategy": "AsIs",
					"finalRules": [{"action": "allow"}]
				}
			},
			{"tag": "blocked", "protocol": "blackhole", "settings": {}}
		],
		"routing": {
			"domainStrategy": "AsIs",
			"rules": [
				{"type": "field", "inboundTag": ["api"], "outboundTag": "api"},
				{"type": "field", "ip": ["geoip:private"], "outboundTag": "blocked"},
				{"type": "field", "protocol": ["bittorrent"], "outboundTag": "blocked"}
			]
		}
	}`)

	require.Len(t, options.Inbounds, 2)
	require.Equal(t, "vless", options.Inbounds[0].Type)
	require.Equal(t, "hysteria2", options.Inbounds[1].Type)
	hysteriaOptions := options.Inbounds[1].Options.(*option.Hysteria2InboundOptions)
	require.Equal(t, uint16(14231), hysteriaOptions.ListenPort)
	require.Equal(t, time.Minute, time.Duration(hysteriaOptions.UDPTimeout))
	require.True(t, hysteriaOptions.IgnoreClientBandwidth)
	require.Equal(t, "aggressive", hysteriaOptions.BBRProfile)
	require.Equal(t, uint64(8388608), hysteriaOptions.StreamReceiveWindow.Value())
	require.Equal(t, uint64(20971520), hysteriaOptions.ConnectionReceiveWindow.Value())
	require.Equal(t, 1024, hysteriaOptions.MaxConcurrentStreams)
	require.Len(t, hysteriaOptions.Users, 1)
	require.Equal(t, "bob", hysteriaOptions.Users[0].Name)
	require.NotNil(t, hysteriaOptions.TLS)
	require.True(t, hysteriaOptions.TLS.DisableSessionTickets)
	require.Equal(t, "/tmp/fullchain.pem", hysteriaOptions.TLS.CertificatePath)

	require.NotNil(t, options.Experimental)
	require.NotNil(t, options.Experimental.V2RayAPI)
	require.Equal(t, "127.0.0.1:62789", options.Experimental.V2RayAPI.Listen)
	require.NotNil(t, options.Experimental.V2RayAPI.Metrics)
	require.Equal(t, "127.0.0.1:11111", options.Experimental.V2RayAPI.Metrics.Listen)
	require.ElementsMatch(t, []string{"vless-in", "hy2-in"}, options.Experimental.V2RayAPI.Stats.Inbounds)
	require.ElementsMatch(t, []string{"alice", "bob"}, options.Experimental.V2RayAPI.Stats.Users)
	require.ElementsMatch(t, []string{"alice", "bob"}, options.Experimental.V2RayAPI.Stats.UsersOnline)

	require.Len(t, options.Route.Rules, 2)
	privateRule := options.Route.Rules[0].DefaultOptions
	require.Contains(t, []string(privateRule.IPCIDR), "10.0.0.0/8")
	require.Contains(t, []string(privateRule.IPCIDR), "100.64.0.0/10")
	require.Equal(t, "blocked", privateRule.RouteOptions.Outbound)
	bittorrentRule := options.Route.Rules[1].DefaultOptions
	require.Equal(t, []string{"bittorrent"}, []string(bittorrentRule.Protocol))
	require.Equal(t, "blocked", bittorrentRule.RouteOptions.Outbound)
}

func TestDecodeXrayRejectsNonEquivalentHysteriaWindows(t *testing.T) {
	requireXrayTestError(t, `{
		"inbounds": [{
			"protocol": "hysteria",
			"port": 14231,
			"settings": {"version": 2, "users": [{"auth": "secret"}]},
			"streamSettings": {
				"network": "hysteria",
				"security": "tls",
				"hysteriaSettings": {"version": 2},
				"finalmask": {"quicParams": {
					"congestion": "bbr",
					"initStreamReceiveWindow": 8388608,
					"maxStreamReceiveWindow": 16777216
				}},
				"tlsSettings": {"certificates": [{
					"certificateFile": "/tmp/fullchain.pem",
					"keyFile": "/tmp/private.key"
				}]}
			}
		}]
	}`, "cannot be represented separately")
}
