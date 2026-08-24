package tf

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/sagernet/sing/common/winiphlpapi"

	"golang.org/x/sys/windows"
)

func writeAndWaitAck(ctx context.Context, conn *net.TCPConn, payload []byte, fallbackDelay time.Duration) (int, error) {
	start := time.Now()
	fullConn := &fullWriteConn{Conn: conn}
	err := winiphlpapi.WriteAndWaitAck(ctx, fullConn, payload)
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) && fullConn.written == 0 {
			written, writeErr := writeFull(conn.Write, payload)
			if writeErr != nil {
				return written, writeErr
			}
			return written, waitForDelay(ctx, fallbackDelay)
		}
		return fullConn.written, err
	}
	if time.Since(start) <= 20*time.Millisecond {
		return fullConn.written, waitForDelay(ctx, fallbackDelay)
	}
	return fullConn.written, nil
}

type fullWriteConn struct {
	net.Conn
	written int
}

func (c *fullWriteConn) Write(payload []byte) (int, error) {
	written, err := writeFull(c.Conn.Write, payload)
	c.written += written
	return written, err
}
