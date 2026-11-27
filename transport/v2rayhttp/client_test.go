package v2rayhttp

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type httpRoundTripFunc func(request *http.Request) (*http.Response, error)

func (f httpRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestDialHTTP2RequestContextDetachedFromDialContext(t *testing.T) {
	dialContext, cancelDial := context.WithCancel(context.Background())
	client := &Client{
		http2:      true,
		requestURL: url.URL{Scheme: "https", Host: "example.test", Path: "/tunnel"},
		method:     http.MethodPut,
		headers:    make(http.Header),
	}
	client.transport.Store(httpRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		// Simulate parallel dialing: the dial context is canceled once another
		// connection wins. The tunnel request context must survive it.
		cancelDial()
		if err := request.Context().Err(); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader("tunnel-data")),
		}, nil
	}))

	conn, err := client.dialHTTP2(dialContext)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	data, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read after dial context cancellation: %v", err)
	}
	if string(data) != "tunnel-data" {
		t.Fatalf("data = %q, want %q", data, "tunnel-data")
	}
}
