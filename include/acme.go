//go:build with_acme

package include

import (
	"github.com/Miku0139oao/sidera-core/adapter/certificate"
	"github.com/Miku0139oao/sidera-core/service/acme"
)

func registerACMECertificateProvider(registry *certificate.Registry) {
	acme.RegisterCertificateProvider(registry)
}
