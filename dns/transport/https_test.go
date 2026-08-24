package transport

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

type testHTTPSRoundTripper struct {
	roundTrip func(*http.Request) (*http.Response, error)
	calls     atomic.Int32
	closes    atomic.Int32
}

func (t *testHTTPSRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return t.roundTrip(request)
}

func (t *testHTTPSRoundTripper) Close() {
	t.closes.Add(1)
}

type countedReadCloser struct {
	reader io.Reader
	read   int
}

func (r *countedReadCloser) Read(payload []byte) (int, error) {
	n, err := r.reader.Read(payload)
	r.read += n
	return n, err
}

func (r *countedReadCloser) Close() error { return nil }

func testDoHMessage() *mDNS.Msg {
	message := new(mDNS.Msg)
	message.SetQuestion("example.com.", mDNS.TypeA)
	return message
}

func TestDoHRejectsOversizedDeclaredAndChunkedBodies(t *testing.T) {
	destination := &url.URL{Scheme: "https", Host: "dns.example", Path: "/dns-query"}
	tests := []struct {
		name          string
		contentLength int64
		wantRead      int
	}{
		{name: "declared", contentLength: maxDNSWireMessageSize + 1, wantRead: 0},
		{name: "chunked", contentLength: -1, wantRead: maxDNSWireMessageSize + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := &countedReadCloser{reader: bytes.NewReader(make([]byte, maxDNSWireMessageSize+1024))}
			roundTripper := &testHTTPSRoundTripper{roundTrip: func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    http.StatusOK,
					Status:        "200 OK",
					ContentLength: test.contentLength,
					Body:          body,
				}, nil
			}}
			transport := &HTTPSTransport{
				destination: destination,
				headers:     make(http.Header),
				transport:   roundTripper,
			}
			if _, err := transport.exchange(context.Background(), testDoHMessage()); err == nil {
				t.Fatal("accepted an oversized DNS response body")
			}
			if body.read != test.wantRead {
				t.Fatalf("body bytes read = %d, want %d", body.read, test.wantRead)
			}
		})
	}
}

func TestHTTPSTransportCloseIsSticky(t *testing.T) {
	roundTripper := &testHTTPSRoundTripper{roundTrip: func(*http.Request) (*http.Response, error) {
		t.Fatal("RoundTrip called after Close")
		return nil, nil
	}}
	transport := &HTTPSTransport{
		destination: &url.URL{Scheme: "https", Host: "dns.example", Path: "/dns-query"},
		headers:     make(http.Header),
		transport:   roundTripper,
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	transport.Reset()
	if _, err := transport.exchange(context.Background(), testDoHMessage()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("post-close exchange error = %v, want %v", err, net.ErrClosed)
	}
	if closes := roundTripper.closes.Load(); closes != 1 {
		t.Fatalf("transport closes = %d, want 1", closes)
	}
	if calls := roundTripper.calls.Load(); calls != 0 {
		t.Fatalf("post-close round trips = %d, want 0", calls)
	}
}
