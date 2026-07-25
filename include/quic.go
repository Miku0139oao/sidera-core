//go:build with_quic

package include

import (
	"github.com/Miku0139oao/sidera-core/adapter/inbound"
	"github.com/Miku0139oao/sidera-core/adapter/outbound"
	"github.com/Miku0139oao/sidera-core/adapter/service"
	"github.com/Miku0139oao/sidera-core/dns"
	"github.com/Miku0139oao/sidera-core/dns/transport/quic"
	"github.com/Miku0139oao/sidera-core/protocol/hysteria"
	"github.com/Miku0139oao/sidera-core/protocol/hysteria2"
	_ "github.com/Miku0139oao/sidera-core/protocol/naive/quic"
	"github.com/Miku0139oao/sidera-core/protocol/tuic"
	_ "github.com/Miku0139oao/sidera-core/transport/v2rayquic"
)

func registerQUICInbounds(registry *inbound.Registry) {
	hysteria.RegisterInbound(registry)
	tuic.RegisterInbound(registry)
	hysteria2.RegisterInbound(registry)
}

func registerQUICOutbounds(registry *outbound.Registry) {
	hysteria.RegisterOutbound(registry)
	tuic.RegisterOutbound(registry)
	hysteria2.RegisterOutbound(registry)
}

func registerQUICTransports(registry *dns.TransportRegistry) {
	quic.RegisterTransport(registry)
	quic.RegisterHTTP3Transport(registry)
}

func registerQUICServices(registry *service.Registry) {
	hysteria2.RegisterRealmService(registry)
}
