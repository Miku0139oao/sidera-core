package settings

import "github.com/Miku0139oao/sidera-core/adapter"

type WIFIMonitor interface {
	ReadWIFIState() adapter.WIFIState
	Start() error
	Close() error
}
