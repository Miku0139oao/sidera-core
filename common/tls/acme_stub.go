//go:build !with_acme

package tls

import (
	"context"
	"crypto/tls"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
)

func startACME(ctx context.Context, logger logger.Logger, options option.InboundACMEOptions) (*tls.Config, adapter.SimpleLifecycle, error) {
	return nil, nil, E.New(`ACME is not included in this build, rebuild with -tags with_acme`)
}
