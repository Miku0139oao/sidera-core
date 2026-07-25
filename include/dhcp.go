//go:build with_dhcp

package include

import (
	"github.com/Miku0139oao/sidera-core/dns"
	"github.com/Miku0139oao/sidera-core/dns/transport/dhcp"
)

func registerDHCPTransport(registry *dns.TransportRegistry) {
	dhcp.RegisterTransport(registry)
}
