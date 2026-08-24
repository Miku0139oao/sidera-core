package clashapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Miku0139oao/sidera-core/log"
)

func TestDecodeJSONRequestIsBounded(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		body          []byte
		contentLength int64
	}{
		{name: "declared", body: []byte(`{}`), contentLength: maxJSONRequestBytes + 1},
		{name: "streamed", body: bytes.Repeat([]byte(" "), maxJSONRequestBytes+1), contentLength: -1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPatch, "/configs", bytes.NewReader(testCase.body))
			request.ContentLength = testCase.contentLength
			response := httptest.NewRecorder()
			var target map[string]any
			if decodeJSONRequest(response, request, &target) {
				t.Fatal("oversized JSON was accepted")
			}
			if response.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status=%d, want %d", response.Code, http.StatusRequestEntityTooLarge)
			}
		})
	}
}

func TestQuietLogStreamStopsWithRequestContext(t *testing.T) {
	factory := log.NewDefaultFactory(context.Background(), log.Formatter{}, io.Discard, "", nil, true)
	if err := factory.Start(); err != nil {
		t.Fatal(err)
	}
	defer factory.Close()

	requestContext, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/logs", nil).WithContext(requestContext)
	response := httptest.NewRecorder()
	returned := make(chan struct{})
	go func() {
		getLogs(context.Background(), factory)(response, request)
		close(returned)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("quiet log stream retained the canceled request")
	}
}
