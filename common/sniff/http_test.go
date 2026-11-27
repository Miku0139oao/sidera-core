package sniff_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/common/sniff"

	"github.com/stretchr/testify/require"
)

func TestSniffHTTP1(t *testing.T) {
	t.Parallel()
	pkt := "GET / HTTP/1.1\r\nHost: www.google.com\r\nAccept: */*\r\n\r\n"
	var metadata adapter.InboundContext
	err := sniff.HTTPHost(context.Background(), &metadata, strings.NewReader(pkt))
	require.NoError(t, err)
	require.Equal(t, metadata.Domain, "www.google.com")
}

func TestSniffHTTP1WithPort(t *testing.T) {
	t.Parallel()
	pkt := "GET / HTTP/1.1\r\nHost: www.gov.cn:8080\r\nAccept: */*\r\n\r\n"
	var metadata adapter.InboundContext
	err := sniff.HTTPHost(context.Background(), &metadata, strings.NewReader(pkt))
	require.NoError(t, err)
	require.Equal(t, metadata.Domain, "www.gov.cn")
}

func TestSniffHTTP1IPLiteral(t *testing.T) {
	t.Parallel()
	pkt := "GET / HTTP/1.1\r\nHost: 192.0.2.1\r\nAccept: */*\r\n\r\n"
	var metadata adapter.InboundContext
	err := sniff.HTTPHost(context.Background(), &metadata, strings.NewReader(pkt))
	require.NoError(t, err)
	require.Empty(t, metadata.Domain)
}

func TestSniffHTTP1IPLiteralWithPort(t *testing.T) {
	t.Parallel()
	pkt := "GET / HTTP/1.1\r\nHost: [2001:db8::1]:8080\r\nAccept: */*\r\n\r\n"
	var metadata adapter.InboundContext
	err := sniff.HTTPHost(context.Background(), &metadata, strings.NewReader(pkt))
	require.NoError(t, err)
	require.Empty(t, metadata.Domain)
}

func TestSniffHTTP1Domain(t *testing.T) {
	t.Parallel()
	pkt := "GET / HTTP/1.1\r\nHost: example.com\r\nAccept: */*\r\n\r\n"
	var metadata adapter.InboundContext
	err := sniff.HTTPHost(context.Background(), &metadata, strings.NewReader(pkt))
	require.NoError(t, err)
	require.Equal(t, "example.com", metadata.Domain)
}
