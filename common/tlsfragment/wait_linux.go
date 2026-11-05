package tf

import (
	"context"
	"net"
	"time"

	"github.com/sagernet/sing/common/control"

	"golang.org/x/sys/unix"
)

func writeAndWaitAck(ctx context.Context, conn *net.TCPConn, payload []byte, fallbackDelay time.Duration) (int, error) {
	written, err := writeFull(conn.Write, payload)
	if err != nil {
		return written, err
	}
	err = control.Conn(conn, func(fd uintptr) error {
		start := time.Now()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			tcpInfo, err := unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
			if err != nil {
				return err
			}
			if tcpInfo.Unacked == 0 {
				if time.Since(start) <= 20*time.Millisecond {
					// under transparent proxy
					return waitForDelay(ctx, fallbackDelay)
				}
				return nil
			}
			if waitErr := waitForDelay(ctx, 10*time.Millisecond); waitErr != nil {
				return waitErr
			}
		}
	})
	return written, err
}
