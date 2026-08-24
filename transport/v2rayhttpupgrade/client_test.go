package v2rayhttpupgrade

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"

	M "github.com/sagernet/sing/common/metadata"
)

type upgradeTestDialer struct {
	conn net.Conn
}

func (d *upgradeTestDialer) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	return d.conn, nil
}

func (d *upgradeTestDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, io.ErrClosedPipe
}

func TestCoalescedHandshakePayloadPreserved(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	payload := []byte("coalesced-upgrade-payload")
	serverDone := make(chan error, 1)
	go func() {
		defer serverSide.Close()
		request, err := http.ReadRequest(bufio.NewReader(serverSide))
		if err != nil {
			serverDone <- err
			return
		}
		request.Body.Close()
		response := append([]byte("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"), payload...)
		_, err = serverSide.Write(response)
		serverDone <- err
	}()

	client := &Client{
		dialer:     &upgradeTestDialer{conn: clientSide},
		requestURL: url.URL{Scheme: "http", Host: "example.test", Path: "/tunnel"},
		headers:    make(http.Header),
		host:       "example.test",
	}
	conn, err := client.DialContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	actual := make([]byte, len(payload))
	if _, err = io.ReadFull(conn, actual); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, payload) {
		t.Fatalf("payload = %q, want %q", actual, payload)
	}
	if err = <-serverDone; err != nil {
		t.Fatal(err)
	}
}
