//go:build !with_acme

package include

import (
	"context"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/adapter/certificate"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	E "github.com/sagernet/sing/common/exceptions"
)

func registerACMECertificateProvider(registry *certificate.Registry) {
	certificate.Register[option.ACMECertificateProviderOptions](registry, C.TypeACME, func(ctx context.Context, logger log.ContextLogger, tag string, options option.ACMECertificateProviderOptions) (adapter.CertificateProviderService, error) {
		return nil, E.New(`ACME is not included in this build, rebuild with -tags with_acme`)
	})
}
