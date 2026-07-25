//go:build with_cloudflared

package include

import (
	"github.com/Miku0139oao/sidera-core/adapter/inbound"
	"github.com/Miku0139oao/sidera-core/protocol/cloudflare"
)

func registerCloudflaredInbound(registry *inbound.Registry) {
	cloudflare.RegisterInbound(registry)
}
