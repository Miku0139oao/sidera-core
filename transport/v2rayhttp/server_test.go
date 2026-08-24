package v2rayhttp

import (
	"context"
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
