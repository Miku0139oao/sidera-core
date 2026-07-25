package include

import (
	"context"

	"github.com/Miku0139oao/sidera-core"
	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/adapter/certificate"
	"github.com/Miku0139oao/sidera-core/adapter/endpoint"
	"github.com/Miku0139oao/sidera-core/adapter/inbound"
	"github.com/Miku0139oao/sidera-core/adapter/outbound"
	"github.com/Miku0139oao/sidera-core/adapter/service"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/dns"
	"github.com/Miku0139oao/sidera-core/dns/transport"
	"github.com/Miku0139oao/sidera-core/dns/transport/fakeip"
	"github.com/Miku0139oao/sidera-core/dns/transport/hosts"
	"github.com/Miku0139oao/sidera-core/dns/transport/local"
	"github.com/Miku0139oao/sidera-core/dns/transport/mdns"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/Miku0139oao/sidera-core/protocol/anytls"
	"github.com/Miku0139oao/sidera-core/protocol/block"
	"github.com/Miku0139oao/sidera-core/protocol/bridge"
	"github.com/Miku0139oao/sidera-core/protocol/direct"
	"github.com/Miku0139oao/sidera-core/protocol/group"
	"github.com/Miku0139oao/sidera-core/protocol/http"
	"github.com/Miku0139oao/sidera-core/protocol/mixed"
	"github.com/Miku0139oao/sidera-core/protocol/naive"
	"github.com/Miku0139oao/sidera-core/protocol/redirect"
	"github.com/Miku0139oao/sidera-core/protocol/shadowsocks"
	"github.com/Miku0139oao/sidera-core/protocol/shadowtls"
	"github.com/Miku0139oao/sidera-core/protocol/snell"
	"github.com/Miku0139oao/sidera-core/protocol/socks"
	"github.com/Miku0139oao/sidera-core/protocol/ssh"
	"github.com/Miku0139oao/sidera-core/protocol/tor"
	"github.com/Miku0139oao/sidera-core/protocol/trojan"
	"github.com/Miku0139oao/sidera-core/protocol/tun"
	"github.com/Miku0139oao/sidera-core/protocol/vless"
	"github.com/Miku0139oao/sidera-core/protocol/vmess"
	"github.com/Miku0139oao/sidera-core/service/api"
	originca "github.com/Miku0139oao/sidera-core/service/origin_ca"
	"github.com/Miku0139oao/sidera-core/service/resolved"
	"github.com/Miku0139oao/sidera-core/service/ssmapi"
	E "github.com/sagernet/sing/common/exceptions"
)

func Context(ctx context.Context) context.Context {
	return box.Context(ctx, InboundRegistry(), OutboundRegistry(), EndpointRegistry(), DNSTransportRegistry(), ServiceRegistry(), CertificateProviderRegistry())
}

func InboundRegistry() *inbound.Registry {
	registry := inbound.NewRegistry()

	tun.RegisterInbound(registry)
	redirect.RegisterRedirect(registry)
	redirect.RegisterTProxy(registry)
	direct.RegisterInbound(registry)

	socks.RegisterInbound(registry)
	http.RegisterInbound(registry)
	mixed.RegisterInbound(registry)

	shadowsocks.RegisterInbound(registry)
	snell.RegisterInbound(registry)
	vmess.RegisterInbound(registry)
	trojan.RegisterInbound(registry)
	naive.RegisterInbound(registry)
	shadowtls.RegisterInbound(registry)
	vless.RegisterInbound(registry)
	anytls.RegisterInbound(registry)

	registerQUICInbounds(registry)
	registerCloudflaredInbound(registry)
	registerStubForRemovedInbounds(registry)

	return registry
}

func OutboundRegistry() *outbound.Registry {
	registry := outbound.NewRegistry()

	direct.RegisterOutbound(registry)
	bridge.RegisterOutbound(registry)

	block.RegisterOutbound(registry)

	group.RegisterSelector(registry)
	group.RegisterURLTest(registry)

	socks.RegisterOutbound(registry)
	http.RegisterOutbound(registry)
	shadowsocks.RegisterOutbound(registry)
	snell.RegisterOutbound(registry)
	vmess.RegisterOutbound(registry)
	trojan.RegisterOutbound(registry)
	registerNaiveOutbound(registry)
	tor.RegisterOutbound(registry)
	ssh.RegisterOutbound(registry)
	shadowtls.RegisterOutbound(registry)
	vless.RegisterOutbound(registry)
	anytls.RegisterOutbound(registry)

	registerQUICOutbounds(registry)
	registerStubForRemovedOutbounds(registry)

	return registry
}

func EndpointRegistry() *endpoint.Registry {
	registry := endpoint.NewRegistry()

	registerWireGuardEndpoint(registry)
	registerOpenConnectEndpoint(registry)
	registerOpenVPNEndpoints(registry)
	registerTailscaleEndpoint(registry)

	return registry
}

func DNSTransportRegistry() *dns.TransportRegistry {
	registry := dns.NewTransportRegistry()

	transport.RegisterTCP(registry)
	transport.RegisterUDP(registry)
	transport.RegisterTLS(registry)
	transport.RegisterHTTPS(registry)
	hosts.RegisterTransport(registry)
	local.RegisterTransport(registry)
	mdns.RegisterTransport(registry)
	fakeip.RegisterTransport(registry)
	resolved.RegisterTransport(registry)

	registerQUICTransports(registry)
	registerDHCPTransport(registry)
	registerTailscaleTransport(registry)
	registerOpenConnectDNSTransport(registry)
	registerOpenVPNDNSTransport(registry)

	return registry
}

func ServiceRegistry() *service.Registry {
	registry := service.NewRegistry()

	api.RegisterService(registry)
	resolved.RegisterService(registry)
	ssmapi.RegisterService(registry)

	registerQUICServices(registry)
	registerDERPService(registry)
	registerCCMService(registry)
	registerOCMService(registry)
	registerOOMKillerService(registry)
	registerUSBIPServices(registry)

	return registry
}

func CertificateProviderRegistry() *certificate.Registry {
	registry := certificate.NewRegistry()

	registerACMECertificateProvider(registry)
	registerTailscaleCertificateProvider(registry)
	originca.RegisterCertificateProvider(registry)

	return registry
}

func registerStubForRemovedInbounds(registry *inbound.Registry) {
	inbound.Register[option.ShadowsocksInboundOptions](registry, C.TypeShadowsocksR, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ShadowsocksInboundOptions) (adapter.Inbound, error) {
		return nil, E.New("ShadowsocksR is deprecated and removed in sing-box 1.6.0")
	})
}

func registerStubForRemovedOutbounds(registry *outbound.Registry) {
	outbound.Register[option.ShadowsocksROutboundOptions](registry, C.TypeShadowsocksR, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ShadowsocksROutboundOptions) (adapter.Outbound, error) {
		return nil, E.New("ShadowsocksR is deprecated and removed in sing-box 1.6.0")
	})
	outbound.Register[option.StubOptions](registry, C.TypeWireGuard, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.StubOptions) (adapter.Outbound, error) {
		return nil, E.New("WireGuard outbound is deprecated in sing-box 1.11.0 and removed in sing-box 1.13.0, use WireGuard endpoint instead")
	})
}
