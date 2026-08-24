package vless

import (
	"context"

	C "github.com/Miku0139oao/sidera-core/constant"
)

func encryptionHandshakeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, C.TCPTimeout)
}
