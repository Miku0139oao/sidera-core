//go:build !with_openvpn

package include

import (
	"context"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/adapter/endpoint"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/dns"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	E "github.com/sagernet/sing/common/exceptions"
)

func registerOpenVPNEndpoints(registry *endpoint.Registry) {
	endpoint.Register[option.OpenVPNClientEndpointOptions](registry, C.TypeOpenVPNClient, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.OpenVPNClientEndpointOptions) (adapter.Endpoint, error) {
		if !options.System {
			return nil, E.New(`OpenVPN is not included in this build, rebuild with -tags with_openvpn,with_gvisor for system:false`)
		}
		return nil, E.New(`OpenVPN is not included in this build, rebuild with -tags with_openvpn`)
	})
	endpoint.Register[option.OpenVPNServerEndpointOptions](registry, C.TypeOpenVPNServer, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.OpenVPNServerEndpointOptions) (adapter.Endpoint, error) {
		if !options.System {
			return nil, E.New(`OpenVPN is not included in this build, rebuild with -tags with_openvpn,with_gvisor for system:false`)
		}
		return nil, E.New(`OpenVPN is not included in this build, rebuild with -tags with_openvpn`)
	})
}

func registerOpenVPNDNSTransport(registry *dns.TransportRegistry) {
	dns.RegisterTransport[option.OpenVPNDNSServerOptions](registry, C.DNSTypeOpenVPN, func(ctx context.Context, logger log.ContextLogger, tag string, options option.OpenVPNDNSServerOptions) (adapter.DNSTransport, error) {
		return nil, E.New(`OpenVPN is not included in this build, rebuild with -tags with_openvpn`)
	})
}
