//go:build with_wireguard

package include

import (
	"github.com/Miku0139oao/sidera-core/adapter/endpoint"
	"github.com/Miku0139oao/sidera-core/protocol/wireguard"
)

func registerWireGuardEndpoint(registry *endpoint.Registry) {
	wireguard.RegisterEndpoint(registry)
}
