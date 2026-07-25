//go:build with_ccm && (!darwin || cgo)

package include

import (
	"github.com/Miku0139oao/sidera-core/adapter/service"
	"github.com/Miku0139oao/sidera-core/service/ccm"
)

func registerCCMService(registry *service.Registry) {
	ccm.RegisterService(registry)
}
