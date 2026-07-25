//go:build with_openvpn

package include

import (
	"github.com/Miku0139oao/sidera-core/adapter/endpoint"
	"github.com/Miku0139oao/sidera-core/dns"
	"github.com/Miku0139oao/sidera-core/protocol/openvpn"
)

func registerOpenVPNEndpoints(registry *endpoint.Registry) {
	openvpn.RegisterEndpoint(registry)
}

func registerOpenVPNDNSTransport(registry *dns.TransportRegistry) {
	openvpn.RegisterDNSTransport(registry)
}
