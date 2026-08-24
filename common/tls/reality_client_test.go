//go:build with_utls

package tls

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	utls "github.com/metacubex/utls"
	"golang.org/x/net/http2"
)

func TestPopulateRealitySessionIDZerosReservedByte(t *testing.T) {
	sessionID := make([]byte, 32)
	for index := range sessionID {
		sessionID[index] = 0xff
	}
	shortID := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	now := time.Date(2200, time.January, 1, 0, 0, 0, 0, time.UTC)

	populateRealitySessionID(sessionID, now, shortID)
	if got, want := sessionID[:3], []byte{26, 7, 11}; string(got) != string(want) {
		t.Fatalf("capability version = %v, want %v", got, want)
	}
	if sessionID[3] != 0 {
		t.Fatalf("reserved session byte = %d, want 0", sessionID[3])
	}
	if got, want := binary.BigEndian.Uint32(sessionID[4:8]), uint32(now.Unix()); got != want {
		t.Fatalf("session timestamp = %d, want %d", got, want)
	}
	if got := sessionID[8:16]; string(got) != string(shortID[:]) {
		t.Fatalf("short ID = %v, want %v", got, shortID)
	}
	for index, value := range sessionID[16:] {
		if value != 0 {
			t.Fatalf("session byte %d = %d, want 0", index+16, value)
		}
	}
}

func TestRealityFallbackIsBoundedAndJoined(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	trackedClient := &closeTrackingConn{Conn: clientSide}
	requestReceived := make(chan struct{})
	releaseResponse := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		server := new(http2.Server)
		server.ServeConn(serverSide, &http2.ServeConnOpts{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			close(requestReceived)
			<-releaseResponse
			_, _ = writer.Write([]byte("decoy response"))
		})})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	fallbackDone := make(chan error, 1)
	go func() {
		fallbackDone <- realityClientFallback(ctx, context.Background(), trackedClient, "example.com", utls.HelloChrome_Auto)
	}()

	select {
	case <-requestReceived:
	case <-time.After(time.Second):
		t.Fatal("decoy did not receive fallback request")
	}
	select {
	case err := <-fallbackDone:
		t.Fatalf("fallback returned before response completed: %v", err)
	default:
	}
	if trackedClient.closeCount.Load() != 0 {
		t.Fatal("fallback connection closed before the decoy response completed")
	}

	close(releaseResponse)
	select {
	case err := <-fallbackDone:
		if err != nil {
			t.Fatalf("fallback failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fallback did not join after the response completed")
	}
	if trackedClient.closeCount.Load() != 1 {
		t.Fatalf("fallback close count = %d, want 1", trackedClient.closeCount.Load())
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("fallback HTTP/2 server did not stop")
	}
}

func TestRealityFallbackCancelsStalledDecoy(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	trackedClient := &closeTrackingConn{Conn: clientSide}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := realityClientFallback(ctx, context.Background(), trackedClient, "example.com", utls.HelloChrome_Auto)
	if err == nil {
		t.Fatal("stalled fallback unexpectedly succeeded")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("stalled fallback took %v", elapsed)
	}
	if trackedClient.closeCount.Load() != 1 {
		t.Fatalf("fallback close count = %d, want 1", trackedClient.closeCount.Load())
	}
}

func TestRealityCloseWritePreservesResponseAfterEOF(t *testing.T) {
	certificate := newRealityTestCertificate(t)
	clientSide, serverSide := net.Pipe()
	deadline := time.Now().Add(5 * time.Second)
	_ = clientSide.SetDeadline(deadline)
	_ = serverSide.SetDeadline(deadline)

	serverResult := make(chan error, 1)
	go func() {
		serverTLS := utls.Server(serverSide, &utls.Config{Certificates: []utls.Certificate{certificate}})
		if err := serverTLS.Handshake(); err != nil {
			serverResult <- err
			return
		}
		serverConn := &realityConnWrapper{Conn: serverTLS}
		request, err := io.ReadAll(serverConn)
		if err != nil {
			serverResult <- err
			return
		}
		if string(request) != "request" {
			serverResult <- &unexpectedRealityPayloadError{got: string(request), want: "request"}
			return
		}
		if _, err = serverConn.Write([]byte("response")); err == nil {
			err = serverConn.CloseWrite()
		}
		_ = serverConn.Close()
		serverResult <- err
	}()

	clientTLS := utls.UClient(clientSide, &utls.Config{InsecureSkipVerify: true}, utls.HelloGolang)
	if err := clientTLS.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	clientConn, err := wrapClientReadWait(&realityClientConnWrapper{UConn: clientTLS})
	if err != nil {
		t.Fatalf("wrap client read-wait: %v", err)
	}
	if _, err = clientConn.Write([]byte("request")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	halfCloser, loaded := clientConn.(interface{ CloseWrite() error })
	if !loaded {
		t.Fatalf("read-wait wrapper %T hid CloseWrite", clientConn)
	}
	if err = halfCloser.CloseWrite(); err != nil {
		t.Fatalf("half-close request: %v", err)
	}
	response, err := io.ReadAll(clientConn)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if string(response) != "response" {
		t.Fatalf("response = %q, want %q", response, "response")
	}
	if err := <-serverResult; err != nil {
		t.Fatalf("server: %v", err)
	}
}

type closeTrackingConn struct {
	net.Conn
	closeCount atomic.Int32
}

func (c *closeTrackingConn) Close() error {
	c.closeCount.Add(1)
	return c.Conn.Close()
}

type unexpectedRealityPayloadError struct {
	got  string
	want string
}

func (e *unexpectedRealityPayloadError) Error() string {
	return "payload = " + e.got + ", want " + e.want
}

func newRealityTestCertificate(t *testing.T) utls.Certificate {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER})
	certificate, err := utls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}
