//go:build with_openconnect

package include

import (
	"github.com/Miku0139oao/sidera-core/adapter/endpoint"
	"github.com/Miku0139oao/sidera-core/dns"
	"github.com/Miku0139oao/sidera-core/protocol/openconnect"
)

func registerOpenConnectEndpoint(registry *endpoint.Registry) {
	openconnect.RegisterEndpoint(registry)
}

func registerOpenConnectDNSTransport(registry *dns.TransportRegistry) {
	openconnect.RegisterDNSTransport(registry)
}
