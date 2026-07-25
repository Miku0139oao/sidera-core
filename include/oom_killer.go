package include

import (
	"github.com/Miku0139oao/sidera-core/adapter/service"
	"github.com/Miku0139oao/sidera-core/service/oomkiller"
)

func registerOOMKillerService(registry *service.Registry) {
	oomkiller.RegisterService(registry)
}
