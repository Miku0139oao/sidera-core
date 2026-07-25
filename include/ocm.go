//go:build with_ocm

package include

import (
	"github.com/Miku0139oao/sidera-core/adapter/service"
	"github.com/Miku0139oao/sidera-core/service/ocm"
)

func registerOCMService(registry *service.Registry) {
	ocm.RegisterService(registry)
}
