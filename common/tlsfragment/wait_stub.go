//go:build !(linux || darwin || windows)

package tf

import (
	"context"
	"net"
	"time"
)

func writeAndWaitAck(ctx context.Context, conn *net.TCPConn, payload []byte, fallbackDelay time.Duration) (int, error) {
	written, err := writeFull(conn.Write, payload)
	if err != nil {
		return written, err
	}
	return written, waitForDelay(ctx, fallbackDelay)
}
