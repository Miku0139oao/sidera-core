package tf_test

import (
	"context"
	"crypto/tls"
	"net"
	"testing"

	tf "github.com/Miku0139oao/sidera-core/common/tlsfragment"

	"github.com/stretchr/testify/require"
)

func TestZeroLengthWriteDoesNotCompleteFragmentation(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	conn := tf.NewConn(clientConn, context.Background(), true, true, 0)
	if conn.WriterReplaceable() {
		t.Fatal("new fragment connection is unexpectedly replaceable")
	}
	n, err := conn.Write(nil)
	require.NoError(t, err)
	require.Zero(t, n)
	if conn.WriterReplaceable() {
		t.Fatal("zero-length write consumed the first TLS packet state")
	}
}

func TestTLSFragment(t *testing.T) {
	t.Parallel()
	tcpConn, err := net.Dial("tcp", "1.1.1.1:443")
	require.NoError(t, err)
	tlsConn := tls.Client(tf.NewConn(tcpConn, context.Background(), true, false, 0), &tls.Config{
		ServerName: "www.cloudflare.com",
	})
	require.NoError(t, tlsConn.Handshake())
}

func TestTLSRecordFragment(t *testing.T) {
	t.Parallel()
	tcpConn, err := net.Dial("tcp", "1.1.1.1:443")
	require.NoError(t, err)
	tlsConn := tls.Client(tf.NewConn(tcpConn, context.Background(), false, true, 0), &tls.Config{
		ServerName: "www.cloudflare.com",
	})
	require.NoError(t, tlsConn.Handshake())
}

func TestTLS2Fragment(t *testing.T) {
	t.Parallel()
	tcpConn, err := net.Dial("tcp", "1.1.1.1:443")
	require.NoError(t, err)
	tlsConn := tls.Client(tf.NewConn(tcpConn, context.Background(), true, true, 0), &tls.Config{
		ServerName: "www.cloudflare.com",
	})
	require.NoError(t, tlsConn.Handshake())
}
