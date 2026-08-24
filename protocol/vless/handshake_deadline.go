package vless

import (
	"context"
	"net"
	"time"

	C "github.com/Miku0139oao/sidera-core/constant"
)

func setEncryptionHandshakeDeadline(ctx context.Context, conn net.Conn) (func(), error) {
	deadline := time.Now().Add(C.TCPTimeout)
	if contextDeadline, loaded := ctx.Deadline(); loaded && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
	})
	return func() {
		stopCancellation()
		_ = conn.SetDeadline(time.Time{})
	}, nil
}
