//go:build !linux

package resolved

import (
	"context"

	"github.com/Miku0139oao/sidera-core/adapter"
	boxService "github.com/Miku0139oao/sidera-core/adapter/service"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/dns"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	E "github.com/sagernet/sing/common/exceptions"
)

func RegisterService(registry *boxService.Registry) {
	boxService.Register[option.ResolvedServiceOptions](registry, C.TypeResolved, func(ctx context.Context, logger log.ContextLogger, tag string, options option.ResolvedServiceOptions) (adapter.Service, error) {
		return nil, E.New("resolved service is only supported on Linux")
	})
}

func RegisterTransport(registry *dns.TransportRegistry) {
	dns.RegisterTransport[option.ResolvedDNSServerOptions](registry, C.TypeResolved, func(ctx context.Context, logger log.ContextLogger, tag string, options option.ResolvedDNSServerOptions) (adapter.DNSTransport, error) {
		return nil, E.New("resolved DNS server is only supported on Linux")
	})
}
