//go:build !linux && !darwin

package route

import (
	"os"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/sagernet/sing/common/logger"
)

func newNeighborResolver(_ logger.ContextLogger, _ []string) (adapter.NeighborResolver, error) {
	return nil, os.ErrInvalid
}
