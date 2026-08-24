package quic

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"

	mDNS "github.com/miekg/dns"
)

const testMaxDNSWireMessageSize = 1<<16 - 1

type testHTTP3RoundTripper struct {
	roundTrip func(*http.Request) (*http.Response, error)
	calls     atomic.Int32
	closes    atomic.Int32
}

func (t *testHTTP3RoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return t.roundTrip(request)
}

func (t *testHTTP3RoundTripper) Close() error {
	t.closes.Add(1)
	return nil
}

type countedHTTP3Body struct {
	reader io.Reader
	read   int
}

func (r *countedHTTP3Body) Read(payload []byte) (int, error) {
	n, err := r.reader.Read(payload)
	r.read += n
	return n, err
}

func (r *countedHTTP3Body) Close() error { return nil }

func testHTTP3Message() *mDNS.Msg {
	message := new(mDNS.Msg)
	message.SetQuestion("example.com.", mDNS.TypeA)
	return message
}

func TestHTTP3RejectsOversizedDeclaredAndChunkedBodies(t *testing.T) {
	destination := &url.URL{Scheme: "https", Host: "dns.example", Path: "/dns-query"}
	tests := []struct {
		name          string
		contentLength int64
		wantRead      int
	}{
		{name: "declared", contentLength: testMaxDNSWireMessageSize + 1, wantRead: 0},
		{name: "chunked", contentLength: -1, wantRead: testMaxDNSWireMessageSize + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := &countedHTTP3Body{reader: bytes.NewReader(make([]byte, testMaxDNSWireMessageSize+1024))}
			roundTripper := &testHTTP3RoundTripper{roundTrip: func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    http.StatusOK,
					Status:        "200 OK",
					ContentLength: test.contentLength,
					Body:          body,
				}, nil
			}}
			transport := &HTTP3Transport{
				destination: destination,
				headers:     make(http.Header),
				transport:   roundTripper,
			}
			if _, err := transport.Exchange(context.Background(), testHTTP3Message()); err == nil {
				t.Fatal("accepted an oversized DNS response body")
			}
			if body.read != test.wantRead {
				t.Fatalf("body bytes read = %d, want %d", body.read, test.wantRead)
			}
		})
	}
}

func TestHTTP3TransportCloseIsSticky(t *testing.T) {
	roundTripper := &testHTTP3RoundTripper{roundTrip: func(*http.Request) (*http.Response, error) {
		t.Fatal("RoundTrip called after Close")
		return nil, nil
	}}
	transport := &HTTP3Transport{
		destination: &url.URL{Scheme: "https", Host: "dns.example", Path: "/dns-query"},
		headers:     make(http.Header),
		transport:   roundTripper,
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	transport.Reset()
	if _, err := transport.Exchange(context.Background(), testHTTP3Message()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("post-close exchange error = %v, want %v", err, net.ErrClosed)
	}
	if closes := roundTripper.closes.Load(); closes != 1 {
		t.Fatalf("transport closes = %d, want 1", closes)
	}
	if calls := roundTripper.calls.Load(); calls != 0 {
		t.Fatalf("post-close round trips = %d, want 0", calls)
	}
}
