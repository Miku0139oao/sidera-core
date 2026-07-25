//go:build !with_usbip || !(linux || (darwin && cgo) || windows)

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

func registerUSBIPServices(registry *service.Registry) {
	service.Register[option.USBIPServerServiceOptions](registry, C.TypeUSBIPServer, func(ctx context.Context, logger log.ContextLogger, tag string, options option.USBIPServerServiceOptions) (adapter.Service, error) {
		return nil, E.New(`USB/IP is not included in this build, rebuild with -tags with_usbip (supported on Linux, Windows, and macOS with CGO)`)
	})
	service.Register[option.USBIPClientServiceOptions](registry, C.TypeUSBIPClient, func(ctx context.Context, logger log.ContextLogger, tag string, options option.USBIPClientServiceOptions) (adapter.Service, error) {
		return nil, E.New(`USB/IP is not included in this build, rebuild with -tags with_usbip (supported on Linux, Windows, and macOS with CGO)`)
	})
}
