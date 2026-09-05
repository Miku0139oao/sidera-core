package sniff_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"testing"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/common/sniff"
)

func BenchmarkTLSClientHelloComplete(b *testing.B) {
	hello := tlsClientHelloRecord("www.example.com", []uint16{tls.VersionTLS13, tls.VersionTLS12})
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var metadata adapter.InboundContext
		if err := sniff.TLSClientHello(ctx, &metadata, bytes.NewReader(hello)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTLSClientHelloFragmented(b *testing.B) {
	hello := tlsClientHelloRecord("www.example.com", []uint16{tls.VersionTLS13, tls.VersionTLS12})
	truncated := hello[:len(hello)/2]
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var metadata adapter.InboundContext
		err := sniff.TLSClientHello(ctx, &metadata, bytes.NewReader(truncated))
		if !errors.Is(err, sniff.ErrNeedMoreData) {
			b.Fatalf("err=%v, want ErrNeedMoreData", err)
		}
	}
}

func BenchmarkTLSClientHelloNonTLS(b *testing.B) {
	payload := []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var metadata adapter.InboundContext
		err := sniff.TLSClientHello(ctx, &metadata, bytes.NewReader(payload))
		if err == nil {
			b.Fatal("expected non-TLS input to fail")
		}
	}
}
