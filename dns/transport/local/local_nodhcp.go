//go:build !with_dhcp

package local

import (
	"context"

	"github.com/Miku0139oao/sidera-core/dns"
	"github.com/Miku0139oao/sidera-core/log"
	N "github.com/sagernet/sing/common/network"
)

func newDHCPTransport(transportAdapter dns.TransportAdapter, ctx context.Context, dialer N.Dialer, logger log.ContextLogger) dhcpTransport {
	return nil
}
