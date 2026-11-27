//go:build with_quic

package httpclient

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestCancelOnCloseBodyCancelsOnlyOnClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := &cancelOnCloseBody{
		ReadCloser: io.NopCloser(strings.NewReader("payload")),
		cancel:     cancel,
	}
	select {
	case <-ctx.Done():
		t.Fatal("body cancelled the request before Close")
	default:
	}
	content, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "payload" {
		t.Fatalf("unexpected body content: %q", content)
	}
	select {
	case <-ctx.Done():
		t.Fatal("reading the body cancelled the request")
	default:
	}
	if err = body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("Close did not cancel the request")
	}
}
