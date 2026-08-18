package xrayencryption

import (
	"bytes"
	"crypto/ecdh"
	"crypto/mlkem"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

const testPadding = "100-35-35"

func TestMLKEMRoundTrip(t *testing.T) {
	privateKey, err := mlkem.GenerateKey768()
	if err != nil {
		t.Fatal(err)
	}
	serverKey := base64.RawURLEncoding.EncodeToString(privateKey.Bytes())
	clientKey := base64.RawURLEncoding.EncodeToString(privateKey.EncapsulationKey().Bytes())

	for _, mode := range []string{"native", "xorpub", "random"} {
		t.Run(mode, func(t *testing.T) {
			server, err := ParseDecryption(methodName + "." + mode + ".0s." + testPadding + "." + serverKey)
			if err != nil {
				t.Fatal(err)
			}
			client, err := ParseEncryption(methodName + "." + mode + ".1rtt." + testPadding + "." + clientKey)
			if err != nil {
				t.Fatal(err)
			}
			serverConn, clientConn := handshakePair(t, server, client)
			defer serverConn.Close()
			defer clientConn.Close()

			exchange(t, clientConn, serverConn, []byte("client to server"))
			exchange(t, serverConn, clientConn, []byte("server to client"))
		})
	}
}

func TestZeroRTTReconnect(t *testing.T) {
	privateKey, err := mlkem.GenerateKey768()
	if err != nil {
		t.Fatal(err)
	}
	serverKey := base64.RawURLEncoding.EncodeToString(privateKey.Bytes())
	clientKey := base64.RawURLEncoding.EncodeToString(privateKey.EncapsulationKey().Bytes())
	server, err := NewServer(methodName + ".random.60-61s." + testPadding + "." + serverKey)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := NewClient(methodName + ".random.0rtt." + testPadding + "." + clientKey)
	if err != nil {
		t.Fatal(err)
	}

	firstServerConn, firstClientConn := handshakePair(t, server, client)
	firstServerConn.Close()
	firstClientConn.Close()
	if !time.Now().Before(client.Expire) || len(client.Ticket) != 16 || len(client.PfsKey) != 64 {
		t.Fatal("first handshake did not install a resumable ticket")
	}

	rawServer, rawClient := net.Pipe()
	t.Cleanup(func() {
		rawServer.Close()
		rawClient.Close()
	})
	type handshakeResult struct {
		conn *CommonConn
		err  error
	}
	serverResult := make(chan handshakeResult, 1)
	request := []byte("zero-rtt request")
	requestRead := make(chan error, 1)
	go func() {
		conn, handshakeErr := server.Handshake(rawServer, nil)
		serverResult <- handshakeResult{conn: conn, err: handshakeErr}
		if handshakeErr != nil {
			requestRead <- handshakeErr
			return
		}
		got := make([]byte, len(request))
		_, readErr := io.ReadFull(conn, got)
		if readErr == nil && string(got) != string(request) {
			readErr = io.ErrUnexpectedEOF
		}
		requestRead <- readErr
	}()

	resumedClient, err := client.Handshake(rawClient)
	if err != nil {
		t.Fatal(err)
	}
	if resumedClient.Client != client || resumedClient.PeerAEAD != nil {
		t.Fatal("client did not take the 0-RTT path")
	}
	if _, err := resumedClient.Write(request); err != nil {
		t.Fatal(err)
	}
	if err := receive(t, requestRead); err != nil {
		t.Fatal(err)
	}
	resumedServer := receive(t, serverResult)
	if resumedServer.err != nil {
		t.Fatal(resumedServer.err)
	}
	exchange(t, resumedServer.conn, resumedClient, []byte("resumed response"))
}

func TestDefaultPaddingRoundTrip(t *testing.T) {
	privateKey, err := mlkem.GenerateKey768()
	if err != nil {
		t.Fatal(err)
	}
	serverKey := base64.RawURLEncoding.EncodeToString(privateKey.Bytes())
	clientKey := base64.RawURLEncoding.EncodeToString(privateKey.EncapsulationKey().Bytes())
	server, err := ParseDecryption(methodName + ".native.60s." + serverKey)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := ParseEncryption(methodName + ".native.0rtt." + clientKey)
	if err != nil {
		t.Fatal(err)
	}
	serverConn, clientConn := handshakePair(t, server, client)
	exchange(t, clientConn, serverConn, []byte("client to server"))
	exchange(t, serverConn, clientConn, []byte("server to client"))
}

func TestParseConfigs(t *testing.T) {
	serverNone, err := ParseDecryption("none")
	if err != nil || serverNone != nil {
		t.Fatalf("ParseDecryption(none) = %v, %v", serverNone, err)
	}
	clientNone, err := ParseEncryption("none")
	if err != nil || clientNone != nil {
		t.Fatalf("ParseEncryption(none) = %v, %v", clientNone, err)
	}

	privateKey, err := mlkem.GenerateKey768()
	if err != nil {
		t.Fatal(err)
	}
	serverKey := base64.RawURLEncoding.EncodeToString(privateKey.Bytes())
	server, err := ParseDecryption(methodName + ".xorpub.120-300s." + testPadding + "." + serverKey)
	if err != nil {
		t.Fatal(err)
	}
	if server.XorMode != 1 || server.SecondsFrom != 120 || server.SecondsTo != 300 {
		t.Fatalf("unexpected parsed server config: mode=%d range=%d-%d", server.XorMode, server.SecondsFrom, server.SecondsTo)
	}

	invalid := []struct {
		name   string
		server bool
		value  string
	}{
		{"empty server", true, ""},
		{"empty client", false, ""},
		{"method", true, "other.native.0s." + serverKey},
		{"mode", true, methodName + ".unknown.0s." + serverKey},
		{"seconds", true, methodName + ".native.bad." + serverKey},
		{"negative seconds", true, methodName + ".native.-1s." + serverKey},
		{"handshake", false, methodName + ".native.2rtt." + serverKey},
		{"missing key", true, methodName + ".native.0s." + testPadding},
		{"bad key", false, methodName + ".native.1rtt." + strings.Repeat("x", 24)},
		{"padding after key", true, methodName + ".native.0s." + serverKey + "." + testPadding},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			var parseErr error
			if test.server {
				_, parseErr = ParseDecryption(test.value)
			} else {
				_, parseErr = ParseEncryption(test.value)
			}
			if parseErr == nil {
				t.Fatalf("accepted invalid config %q", test.value)
			}
		})
	}
}

func TestClientEncryptionFromDecryption(t *testing.T) {
	x25519Private, err := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	mlkemPrivate, err := mlkem.GenerateKey768()
	if err != nil {
		t.Fatal(err)
	}
	x25519ServerKey := base64.RawURLEncoding.EncodeToString(x25519Private.Bytes())
	x25519ClientKey := base64.RawURLEncoding.EncodeToString(x25519Private.PublicKey().Bytes())
	mlkemServerKey := base64.RawURLEncoding.EncodeToString(mlkemPrivate.Bytes())
	mlkemClientKey := base64.RawURLEncoding.EncodeToString(mlkemPrivate.EncapsulationKey().Bytes())

	clientConfig, err := ClientEncryptionFromDecryption(
		methodName + ".xorpub.120-300s." + testPadding + "." + x25519ServerKey + "." + mlkemServerKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	expected := methodName + ".xorpub.0rtt." + testPadding + "." + x25519ClientKey + "." + mlkemClientKey
	if clientConfig != expected {
		t.Fatalf("ClientEncryptionFromDecryption() = %q, want %q", clientConfig, expected)
	}

	clientConfig, err = ClientEncryptionFromDecryption(methodName + ".native.0s." + x25519ServerKey)
	if err != nil {
		t.Fatal(err)
	}
	expected = methodName + ".native.1rtt." + x25519ClientKey
	if clientConfig != expected {
		t.Fatalf("ClientEncryptionFromDecryption() = %q, want %q", clientConfig, expected)
	}

	clientConfig, err = ClientEncryptionFromDecryption("none")
	if err != nil || clientConfig != "none" {
		t.Fatalf("ClientEncryptionFromDecryption(none) = %q, %v", clientConfig, err)
	}
	if _, err = ClientEncryptionFromDecryption(""); err == nil {
		t.Fatal("ClientEncryptionFromDecryption accepted an empty config")
	}
}

func TestClientEncryptionFromDecryptionRoundTrip(t *testing.T) {
	x25519Private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mlkemPrivate, err := mlkem.GenerateKey768()
	if err != nil {
		t.Fatal(err)
	}
	x25519Server := base64.RawURLEncoding.EncodeToString(x25519Private.Bytes())
	mlkemServer := base64.RawURLEncoding.EncodeToString(mlkemPrivate.Bytes())

	cases := []struct {
		name    string
		server  string
		wantRTT string
	}{
		{"0s", "0s", "1rtt"},
		{"0-0s", "0-0s", "1rtt"},
		{"0-1s", "0-1s", "0rtt"},
		{"60-61s", "60-61s", "0rtt"},
	}
	orders := []struct {
		name string
		keys string
	}{
		{"mlkem", mlkemServer},
		{"x25519-mlkem", x25519Server + "." + mlkemServer},
		{"mlkem-x25519", mlkemServer + "." + x25519Server},
	}
	for _, mode := range []string{"native", "xorpub", "random"} {
		for _, padding := range []string{"", testPadding} {
			for _, lifetime := range cases {
				for _, order := range orders {
					name := mode + "/" + lifetime.name + "/" + order.name
					if padding != "" {
						name += "/padding"
					}
					t.Run(name, func(t *testing.T) {
						decryption := methodName + "." + mode + "." + lifetime.server + "."
						if padding != "" {
							decryption += padding + "."
						}
						decryption += order.keys
						clientConfig, deriveErr := ClientEncryptionFromDecryption(decryption)
						if deriveErr != nil {
							t.Fatal(deriveErr)
						}
						if !strings.Contains(clientConfig, "."+lifetime.wantRTT+".") {
							t.Fatalf("derived %q, want handshake %s", clientConfig, lifetime.wantRTT)
						}
						server, parseErr := ParseDecryption(decryption)
						if parseErr != nil {
							t.Fatal(parseErr)
						}
						defer server.Close()
						client, parseErr := ParseEncryption(clientConfig)
						if parseErr != nil {
							t.Fatal(parseErr)
						}
						serverConn, clientConn := handshakePair(t, server, client)
						exchange(t, clientConn, serverConn, []byte("client to server"))
						exchange(t, serverConn, clientConn, []byte("server to client"))
					})
				}
			}
		}
	}
}

func TestPaddingAndHeaderParsing(t *testing.T) {
	var lengths, gaps [][3]int
	if err := ParsePadding("100-35-40.75-0-10.50-1-2", &lengths, &gaps); err != nil {
		t.Fatal(err)
	}
	if len(lengths) != 2 || len(gaps) != 1 || lengths[0] != [3]int{100, 35, 40} || gaps[0] != [3]int{75, 0, 10} {
		t.Fatalf("unexpected padding parse: lengths=%v gaps=%v", lengths, gaps)
	}
	for _, invalid := range []string{"99-35-35", "100-34-35", "100-35", "100--35"} {
		lengths, gaps = nil, nil
		if err := ParsePadding(invalid, &lengths, &gaps); err == nil {
			t.Fatalf("accepted invalid padding %q", invalid)
		}
	}
	lengths, gaps = nil, nil
	if err := ParsePadding("101-35-35", &lengths, &gaps); err != nil {
		t.Fatalf("rejected Xray-compatible probability above 100: %v", err)
	}

	header := make([]byte, 5)
	EncodeHeader(header, 17)
	if length, err := DecodeHeader(header); err != nil || length != 17 {
		t.Fatalf("DecodeHeader = %d, %v", length, err)
	}
	EncodeHeader(header, 16640)
	if length, err := DecodeHeader(header); err != nil || length != 16640 {
		t.Fatalf("DecodeHeader(max) = %d, %v", length, err)
	}
	for _, invalid := range [][]byte{{23, 3, 3, 0, 16}, {23, 3, 3, 65, 1}, {22, 3, 3, 0, 17}, {23, 3}} {
		if _, err := DecodeHeader(invalid); err == nil {
			t.Fatalf("accepted invalid header %v", invalid)
		}
	}
}

func handshakePair(t *testing.T, server *ServerInstance, client *ClientInstance) (*CommonConn, *CommonConn) {
	t.Helper()
	rawServer, rawClient := tcpPipe(t)
	type result struct {
		conn *CommonConn
		err  error
	}
	serverResult := make(chan result, 1)
	clientResult := make(chan result, 1)
	go func() {
		conn, err := server.Handshake(rawServer, nil)
		serverResult <- result{conn, err}
	}()
	go func() {
		conn, err := client.Handshake(rawClient)
		clientResult <- result{conn, err}
	}()
	serverHandshake := receive(t, serverResult)
	clientHandshake := receive(t, clientResult)
	if serverHandshake.err != nil {
		t.Fatal(serverHandshake.err)
	}
	if clientHandshake.err != nil {
		t.Fatal(clientHandshake.err)
	}
	return serverHandshake.conn, clientHandshake.conn
}

func tcpPipe(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	type acceptResult struct {
		conn net.Conn
		err  error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		accepted <- acceptResult{conn, acceptErr}
	}()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	result := receive(t, accepted)
	if result.err != nil {
		client.Close()
		t.Fatal(result.err)
	}
	deadline := time.Now().Add(5 * time.Second)
	if err = client.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err = result.conn.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		result.conn.Close()
		client.Close()
	})
	return result.conn, client
}

func exchange(t *testing.T, writer net.Conn, reader net.Conn, payload []byte) {
	t.Helper()
	writeResult := make(chan error, 1)
	go func() {
		_, err := writer.Write(payload)
		writeResult <- err
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("round trip mismatch: got %q, want %q", got, payload)
	}
	if err := receive(t, writeResult); err != nil {
		t.Fatal(err)
	}
}

func receive[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("operation timed out")
		var zero T
		return zero
	}
}
