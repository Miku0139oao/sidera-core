package v2rayhttp

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type httpTestHandler struct {
	called atomic.Bool
}

func (h *httpTestHandler) NewConnectionEx(context.Context, net.Conn, M.Socksaddr, M.Socksaddr, N.CloseHandlerFunc) {
	h.called.Store(true)
}

type httpSignalHandler struct {
	called chan struct{}
	once   sync.Once
}

func (h *httpSignalHandler) NewConnectionEx(_ context.Context, conn net.Conn, _ M.Socksaddr, _ M.Socksaddr, _ N.CloseHandlerFunc) {
	h.once.Do(func() { close(h.called) })
	conn.Close()
}

func TestHTTP1EarlyBodyTimeout(t *testing.T) {
	handler := &httpTestHandler{}
	server, err := NewServer(context.Background(), logger.NOP(), option.V2RayHTTPOptions{
		Path:   "/tunnel",
		Method: http.MethodPut,
	}, nil, handler)
	if err != nil {
		t.Fatal(err)
	}
	server.http1EarlyBodyTimeout = 50 * time.Millisecond
	requestDone := make(chan struct{})
	var requestDoneOnce sync.Once
	originalHandler := server.httpServer.Handler
	server.httpServer.Handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		originalHandler.ServeHTTP(writer, request)
		requestDoneOnce.Do(func() { close(requestDone) })
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	defer func() {
		_ = server.Close()
		<-serveDone
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	if _, err = conn.Write([]byte("PUT /tunnel HTTP/1.1\r\nHost: example.test\r\nContent-Length: 1\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestDone:
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Fatalf("slow body retained handler for %v", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not terminate slow body handler")
	}
	if handler.called.Load() {
		t.Fatal("slow request reached transport handler")
	}
}

type httpPayloadHandler struct {
	payload chan string
}

func (h *httpPayloadHandler) NewConnectionEx(_ context.Context, conn net.Conn, _ M.Socksaddr, _ M.Socksaddr, _ N.CloseHandlerFunc) {
	defer conn.Close()
	payload := make([]byte, 5)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return
	}
	select {
	case h.payload <- string(payload):
	default:
	}
}

func TestHTTP1ChunkedBodyHijacksImmediately(t *testing.T) {
	handler := &httpPayloadHandler{payload: make(chan string, 1)}
	server, err := NewServer(context.Background(), logger.NOP(), option.V2RayHTTPOptions{
		Path:   "/tunnel",
		Method: http.MethodPut,
	}, nil, handler)
	if err != nil {
		t.Fatal(err)
	}
	// A chunked tunnel body never reaches EOF; if the server tried to buffer it
	// the handler would only run after this timeout.
	server.http1EarlyBodyTimeout = 5 * time.Second
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	defer func() {
		_ = server.Close()
		<-serveDone
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.Write([]byte("PUT /tunnel HTTP/1.1\r\nHost: example.test\r\nTransfer-Encoding: chunked\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	// Send one non-terminal chunk: the request body stays open like a tunnel.
	if _, err = conn.Write([]byte("5\r\nhello\r\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-handler.payload:
		if payload != "hello" {
			t.Fatalf("tunnel received chunk framing instead of payload: %q", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("chunked request body was buffered instead of hijacked")
	}
}
