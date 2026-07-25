//go:build with_naive_outbound

package include

import (
	"github.com/Miku0139oao/sidera-core/adapter/outbound"
	"github.com/Miku0139oao/sidera-core/protocol/naive"
)

func registerNaiveOutbound(registry *outbound.Registry) {
	naive.RegisterOutbound(registry)
}
