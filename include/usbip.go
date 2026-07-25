//go:build with_usbip && (linux || (darwin && cgo) || windows)

package include

import (
	"github.com/Miku0139oao/sidera-core/adapter/service"
	"github.com/Miku0139oao/sidera-core/service/usbip"
)

func registerUSBIPServices(registry *service.Registry) {
	usbip.RegisterService(registry)
}
