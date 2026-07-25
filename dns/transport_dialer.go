package dns

import (
	"context"

	"github.com/Miku0139oao/sidera-core/common/dialer"
	"github.com/Miku0139oao/sidera-core/option"
	N "github.com/sagernet/sing/common/network"
)

func NewLocalDialer(ctx context.Context, options option.LocalDNSServerOptions) (N.Dialer, error) {
	return dialer.NewWithOptions(dialer.Options{
		Context:        ctx,
		Options:        options.DialerOptions,
		DirectResolver: true,
	})
}

func NewRemoteDialer(ctx context.Context, options option.RemoteDNSServerOptions) (N.Dialer, error) {
	return dialer.NewWithOptions(dialer.Options{
		Context:        ctx,
		Options:        options.DialerOptions,
		RemoteIsDomain: options.ServerIsDomain(),
		DirectResolver: true,
	})
}
