//go:build !with_ocm

package include

import (
	"context"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/adapter/service"
	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/log"
	"github.com/Miku0139oao/sidera-core/option"
	E "github.com/sagernet/sing/common/exceptions"
)

func registerOCMService(registry *service.Registry) {
	service.Register[option.OCMServiceOptions](registry, C.TypeOCM, func(ctx context.Context, logger log.ContextLogger, tag string, options option.OCMServiceOptions) (adapter.Service, error) {
		return nil, E.New(`OCM is not included in this build, rebuild with -tags with_ocm`)
	})
}
