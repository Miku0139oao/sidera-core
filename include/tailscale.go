//go:build with_tailscale

package include

import (
	"github.com/Miku0139oao/sidera-core/adapter/certificate"
	"github.com/Miku0139oao/sidera-core/adapter/endpoint"
	"github.com/Miku0139oao/sidera-core/adapter/service"
	"github.com/Miku0139oao/sidera-core/dns"
	"github.com/Miku0139oao/sidera-core/protocol/tailscale"
	"github.com/Miku0139oao/sidera-core/service/derp"
)

func registerTailscaleEndpoint(registry *endpoint.Registry) {
	tailscale.RegisterEndpoint(registry)
}

func registerTailscaleTransport(registry *dns.TransportRegistry) {
	tailscale.RegistryTransport(registry)
}

func registerTailscaleCertificateProvider(registry *certificate.Registry) {
	tailscale.RegisterCertificateProvider(registry)
}

func registerDERPService(registry *service.Registry) {
	derp.Register(registry)
}
