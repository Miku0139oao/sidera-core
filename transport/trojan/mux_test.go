package trojan

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type muxTestConn struct {
	*bytes.Reader
}

func (c *muxTestConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *muxTestConn) Close() error                     { return nil }
func (c *muxTestConn) LocalAddr() net.Addr              { return M.Socksaddr{} }
func (c *muxTestConn) RemoteAddr() net.Addr             { return M.Socksaddr{} }
func (c *muxTestConn) SetDeadline(time.Time) error      { return nil }
func (c *muxTestConn) SetReadDeadline(time.Time) error  { return nil }
func (c *muxTestConn) SetWriteDeadline(time.Time) error { return nil }

type muxTestHandler struct {
	payload []byte
	err     error
}

func (h *muxTestHandler) NewConnectionEx(_ context.Context, conn net.Conn, _ M.Socksaddr, _ M.Socksaddr, _ N.CloseHandlerFunc) {
	h.payload = make([]byte, len("coalesced-trojan-payload"))
	_, h.err = io.ReadFull(conn, h.payload)
}

func (h *muxTestHandler) NewPacketConnectionEx(context.Context, N.PacketConn, M.Socksaddr, M.Socksaddr, N.CloseHandlerFunc) {
}

func TestCoalescedHandshakePayloadPreserved(t *testing.T) {
	payload := []byte("coalesced-trojan-payload")
	var wire bytes.Buffer
	wire.WriteByte(CommandTCP)
	if err := M.SocksaddrSerializer.WriteAddrPort(&wire, M.ParseSocksaddr("example.test:443")); err != nil {
		t.Fatal(err)
	}
	wire.Write(payload)
	handler := &muxTestHandler{}
	conn := &muxTestConn{Reader: bytes.NewReader(wire.Bytes())}
	if err := newMuxConnection0(context.Background(), conn, M.Socksaddr{}, handler); err != nil {
		t.Fatal(err)
	}
	if handler.err != nil {
		t.Fatal(handler.err)
	}
	if !bytes.Equal(handler.payload, payload) {
		t.Fatalf("payload = %q, want %q", handler.payload, payload)
	}
}
