package xrayencryption

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEncryptedVLESSHandshakeOverNoDeadlineConn(t *testing.T) {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := new(ServerInstance)
	if err = server.Init([][]byte{privateKey.Bytes()}, 2, 0, 0, testPadding); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client := new(ClientInstance)
	if err = client.Init([][]byte{privateKey.PublicKey().Bytes()}, 2, 0, testPadding); err != nil {
		t.Fatal(err)
	}

	rawServer, rawClient := tcpPipe(t)
	serverTransport := &noDeadlineConn{Conn: rawServer}
	clientTransport := &noDeadlineConn{Conn: rawClient}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	type result struct {
		conn *CommonConn
		err  error
	}
	serverResult := make(chan result, 1)
	clientResult := make(chan result, 1)
	go func() {
		conn, handshakeErr := server.HandshakeContext(ctx, serverTransport, nil)
		serverResult <- result{conn: conn, err: handshakeErr}
	}()
	go func() {
		conn, handshakeErr := client.HandshakeContext(ctx, clientTransport)
		clientResult <- result{conn: conn, err: handshakeErr}
	}()
	serverHandshake := receive(t, serverResult)
	clientHandshake := receive(t, clientResult)
	cancel()
	if serverHandshake.err != nil {
		t.Fatal(serverHandshake.err)
	}
	if clientHandshake.err != nil {
		t.Fatal(clientHandshake.err)
	}
	if serverTransport.deadlineCalls.Load() != 0 || clientTransport.deadlineCalls.Load() != 0 {
		t.Fatal("encryption handshake attempted to use unsupported deadlines")
	}
	exchange(t, clientHandshake.conn, serverHandshake.conn, []byte("no-deadline request"))
	exchange(t, serverHandshake.conn, clientHandshake.conn, []byte("no-deadline response"))
}

func TestHandshakeTimeoutCallbackCleanupRace(t *testing.T) {
	conn := newCallbackRaceConn()
	handshakeStarted := make(chan struct{})
	releaseHandshake := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	type result struct {
		conn *CommonConn
		err  error
	}
	resultChannel := make(chan result, 1)
	go func() {
		resultConn, err := handshakeWithContext(ctx, conn, func(context.Context) (*CommonConn, error) {
			close(handshakeStarted)
			<-releaseHandshake
			return NewCommonConn(conn, true), nil
		})
		resultChannel <- result{conn: resultConn, err: err}
	}()
	<-handshakeStarted
	select {
	case <-conn.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("timeout callback did not start")
	}
	close(releaseHandshake)
	select {
	case result := <-resultChannel:
		t.Fatalf("handshake returned before its timeout callback completed: %v", result.err)
	case <-time.After(20 * time.Millisecond):
	}
	close(conn.releaseClose)
	handshakeResult := receive(t, resultChannel)
	if handshakeResult.conn != nil || !errors.Is(handshakeResult.err, context.DeadlineExceeded) {
		t.Fatalf("handshake result = %v, %v, want nil, deadline exceeded", handshakeResult.conn, handshakeResult.err)
	}
}

func TestEncryptionHandshakeCancellationInterruptsPaddingGap(t *testing.T) {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	client := new(ClientInstance)
	if err = client.Init(
		[][]byte{privateKey.PublicKey().Bytes()},
		0,
		0,
		"100-35-35.100-5000-5000.100-35-35",
	); err != nil {
		t.Fatal(err)
	}
	conn := newPaddingGapConn()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, handshakeErr := client.HandshakeContext(ctx, conn)
		result <- handshakeErr
	}()
	select {
	case <-conn.firstWrite:
	case <-time.After(time.Second):
		t.Fatal("client did not reach the configured padding gap")
	}
	cancel()
	select {
	case err = <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("HandshakeContext error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled padding gap did not return promptly")
	}
	if writes := conn.writeCount.Load(); writes != 1 {
		t.Fatalf("writes after cancellation = %d, want 1", writes)
	}
}

func TestServerCloseDuringHandshakeAdmission(t *testing.T) {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := new(ServerInstance)
	if err = server.Init([][]byte{privateKey.Bytes()}, 0, 0, 0, testPadding); err != nil {
		t.Fatal(err)
	}
	rawServer, rawClient := net.Pipe()
	defer rawClient.Close()
	handshakeResult := make(chan error, 1)
	go func() {
		_, handshakeErr := server.HandshakeContext(context.Background(), rawServer, nil)
		handshakeResult <- handshakeErr
	}()
	deadline := time.Now().Add(time.Second)
	for {
		server.RWLock.RLock()
		active := server.activeHandshakes
		server.RWLock.RUnlock()
		if active == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("handshake was not admitted")
		}
		time.Sleep(time.Millisecond)
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- server.Close() }()
	if err = receive(t, closeResult); err != nil {
		t.Fatal(err)
	}
	if err = receive(t, handshakeResult); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("handshake concurrent with Close = %v, want net.ErrClosed", err)
	}
}

func TestServerReplayStateGlobalBound(t *testing.T) {
	now := time.Unix(1000, 0)
	server := replayTestServer(now, 8, 2)
	ticketA := [16]byte{1}
	ticketB := [16]byte{2}
	server.Sessions[ticketA] = replayTestSession(now.Add(time.Minute))
	server.Sessions[ticketB] = replayTestSession(now.Add(time.Minute))

	if _, err := server.acceptResume(ticketA, [32]byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.acceptResume(ticketB, [32]byte{2}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.acceptResume(ticketA, [32]byte{3}); !errors.Is(err, errGlobalReplayLimit) {
		t.Fatalf("third replay entry error = %v, want global limit", err)
	}
	if server.replayEntries != 2 || len(server.Sessions[ticketA].NfsKeys) != 1 || len(server.Sessions[ticketB].NfsKeys) != 1 {
		t.Fatalf("replay state exceeded bound: total=%d A=%d B=%d", server.replayEntries, len(server.Sessions[ticketA].NfsKeys), len(server.Sessions[ticketB].NfsKeys))
	}
	if _, err := server.acceptResume(ticketA, [32]byte{1}); !errors.Is(err, errReplayDetected) {
		t.Fatalf("known replay at global capacity = %v, want replay detected", err)
	}
}

func TestServerReplayStateGlobalBoundConcurrent(t *testing.T) {
	now := time.Unix(1000, 0)
	server := replayTestServer(now, 128, 8)
	ticket := [16]byte{1}
	server.Sessions[ticket] = replayTestSession(now.Add(time.Minute))
	var successes atomic.Int32
	var wait sync.WaitGroup
	for index := range 64 {
		wait.Go(func() {
			key := [32]byte{byte(index + 1)}
			if _, err := server.acceptResume(ticket, key); err == nil {
				successes.Add(1)
			} else if !errors.Is(err, errGlobalReplayLimit) {
				t.Errorf("acceptResume(%d): %v", index, err)
			}
		})
	}
	wait.Wait()
	if successes.Load() != 8 || server.replayEntries != 8 || len(server.Sessions[ticket].NfsKeys) != 8 {
		t.Fatalf("concurrent replay bound: successes=%d total=%d ticket=%d", successes.Load(), server.replayEntries, len(server.Sessions[ticket].NfsKeys))
	}
}

func TestServerReplayStatePerTicketBoundBeforeRetention(t *testing.T) {
	now := time.Unix(1000, 0)
	server := replayTestServer(now, 1, 8)
	ticket := [16]byte{1}
	server.Sessions[ticket] = replayTestSession(now.Add(time.Minute))
	if _, err := server.acceptResume(ticket, [32]byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.acceptResume(ticket, [32]byte{2}); !errors.Is(err, errTicketResumeLimit) {
		t.Fatalf("second resume error = %v, want per-ticket limit", err)
	}
	if _, loaded := server.Sessions[ticket]; loaded {
		t.Fatal("overused ticket was not invalidated")
	}
	if server.replayEntries != 0 {
		t.Fatalf("global replay count after invalidation = %d, want 0", server.replayEntries)
	}
}

func TestSessionCapacityDoesNotEvictLiveTicket(t *testing.T) {
	now := time.Unix(1000, 0)
	server := replayTestServer(now, 8, 8)
	server.generation = 7
	server.sessionLimit = 1
	liveTicket := [16]byte{1}
	server.Sessions[liveTicket] = replayTestSession(now.Add(time.Minute))
	newTicket := [16]byte{2}
	seconds, err := server.issueTicket(server.generation, &newTicket, bytes.Repeat([]byte{3}, 64), 60)
	if err != nil {
		t.Fatal(err)
	}
	if seconds != 0 || DecodeLength(newTicket[:2]) != 0 {
		t.Fatalf("capacity-degraded ticket lifetime = %d/%d, want zero", seconds, DecodeLength(newTicket[:2]))
	}
	if len(server.Sessions) != 1 || server.Sessions[liveTicket] == nil {
		t.Fatal("issuing at capacity evicted the live ticket")
	}
}

func TestTicketExpiryIsExact(t *testing.T) {
	issuedAt := time.Unix(1000, 123)
	current := issuedAt
	server := replayTestServer(issuedAt, 8, 8)
	server.now = func() time.Time { return current }
	ticket := [16]byte{1}
	seconds, err := server.issueTicket(server.generation, &ticket, bytes.Repeat([]byte{1}, 64), 60)
	if err != nil || seconds != 60 {
		t.Fatalf("issueTicket = %d, %v", seconds, err)
	}
	session := server.Sessions[ticket]
	wantExpiry := issuedAt.Add(60 * time.Second)
	if session == nil || !session.ExpireAt.Equal(wantExpiry) {
		t.Fatalf("ticket expiry = %v, want %v", session, wantExpiry)
	}
	current = wantExpiry
	if _, err = server.acceptResume(ticket, [32]byte{1}); !errors.Is(err, errExpiredTicket) {
		t.Fatalf("resume at advertised expiry = %v, want expired ticket", err)
	}
}

func TestReplayRejectedAtExactExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	current := now
	server := replayTestServer(now, 8, 8)
	server.now = func() time.Time { return current }
	ticket := [16]byte{1}
	expiry := now.Add(time.Second)
	server.Sessions[ticket] = replayTestSession(expiry)
	current = expiry.Add(-time.Nanosecond)
	if _, err := server.acceptResume(ticket, [32]byte{1}); err != nil {
		t.Fatalf("resume immediately before expiry: %v", err)
	}
	current = expiry
	if _, err := server.acceptResume(ticket, [32]byte{2}); !errors.Is(err, errExpiredTicket) {
		t.Fatalf("resume at exact expiry = %v, want expired ticket", err)
	}
	if server.replayEntries != 0 {
		t.Fatalf("expired replay state retained globally: %d", server.replayEntries)
	}
	if _, err := server.acceptResume(ticket, [32]byte{1}); !errors.Is(err, errExpiredTicket) {
		t.Fatalf("replay after expiry = %v, want expired ticket", err)
	}
}

func TestWritesAreImmutableAndHandleShortWrites(t *testing.T) {
	t.Run("random XOR", func(t *testing.T) {
		sink := &shortWriteConn{maxWrite: 3}
		input := make([]byte, 5+17)
		EncodeHeader(input, 17)
		copy(input[5:], []byte("0123456789abcdefg"))
		original := append([]byte(nil), input...)
		key := bytes.Repeat([]byte{1}, 32)
		iv := bytes.Repeat([]byte{2}, 16)
		conn := NewXorConn(sink, NewCTR(key, iv), nil, 0, 0)
		n, err := conn.Write(input)
		if err != nil || n != len(input) {
			t.Fatalf("Write = %d, %v, want %d, nil", n, err, len(input))
		}
		if !bytes.Equal(input, original) {
			t.Fatal("random-mode XOR mutated caller input")
		}
		if sink.calls.Load() <= 1 {
			t.Fatal("test writer did not exercise short writes")
		}
		wire := sink.Bytes()
		expected := append([]byte(nil), original...)
		NewCTR(key, iv).XORKeyStream(expected[:5], expected[:5])
		if !bytes.Equal(wire, expected) {
			t.Fatal("random-mode XOR wire output changed")
		}
	})

	t.Run("AEAD", func(t *testing.T) {
		sink := &shortWriteConn{maxWrite: 3}
		conn := NewCommonConn(sink, false)
		conn.UnitedKey = bytes.Repeat([]byte{3}, 32)
		conn.AEAD = NewAEAD([]byte("client"), conn.UnitedKey, false)
		input := []byte("caller-owned plaintext")
		original := append([]byte(nil), input...)
		n, err := conn.Write(input)
		if err != nil || n != len(input) {
			t.Fatalf("Write = %d, %v, want %d, nil", n, err, len(input))
		}
		if !bytes.Equal(input, original) {
			t.Fatal("AEAD write mutated caller input")
		}
		if sink.calls.Load() <= 1 {
			t.Fatal("test writer did not exercise short writes")
		}
	})

	t.Run("partial error count", func(t *testing.T) {
		sentinel := errors.New("write failed")
		sink := &shortWriteConn{maxWrite: 4, firstError: sentinel}
		input := make([]byte, 5+17)
		EncodeHeader(input, 17)
		original := append([]byte(nil), input...)
		conn := NewXorConn(sink, NewCTR(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 16)), nil, 0, 0)
		n, err := conn.Write(input)
		if n != 4 || !errors.Is(err, sentinel) {
			t.Fatalf("partial Write = %d, %v, want 4, sentinel", n, err)
		}
		if !bytes.Equal(input, original) {
			t.Fatal("failed random-mode write mutated caller input")
		}
	})
}

func replayTestServer(now time.Time, perTicket, global int) *ServerInstance {
	return &ServerInstance{
		generation:   1,
		Sessions:     make(map[[16]byte]*ServerSession),
		sessionLimit: 8,
		resumeLimit:  perTicket,
		replayLimit:  global,
		now:          func() time.Time { return now },
	}
}

func replayTestSession(expiry time.Time) *ServerSession {
	return &ServerSession{
		PfsKey:   bytes.Repeat([]byte{1}, 64),
		ExpireAt: expiry,
		NfsKeys:  make(map[[32]byte]struct{}),
	}
}

type noDeadlineConn struct {
	net.Conn
	deadlineCalls atomic.Int32
}

func (c *noDeadlineConn) SetDeadline(time.Time) error {
	c.deadlineCalls.Add(1)
	return os.ErrInvalid
}

func (c *noDeadlineConn) SetReadDeadline(time.Time) error {
	c.deadlineCalls.Add(1)
	return os.ErrInvalid
}

func (c *noDeadlineConn) SetWriteDeadline(time.Time) error {
	c.deadlineCalls.Add(1)
	return os.ErrInvalid
}

type callbackRaceConn struct {
	closeStarted chan struct{}
	releaseClose chan struct{}
	closeOnce    sync.Once
}

func newCallbackRaceConn() *callbackRaceConn {
	return &callbackRaceConn{
		closeStarted: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
}

func (c *callbackRaceConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *callbackRaceConn) Write(b []byte) (int, error)      { return len(b), nil }
func (c *callbackRaceConn) LocalAddr() net.Addr              { return testAddr("local") }
func (c *callbackRaceConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (c *callbackRaceConn) SetDeadline(time.Time) error      { return os.ErrInvalid }
func (c *callbackRaceConn) SetReadDeadline(time.Time) error  { return os.ErrInvalid }
func (c *callbackRaceConn) SetWriteDeadline(time.Time) error { return os.ErrInvalid }
func (c *callbackRaceConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closeStarted)
		<-c.releaseClose
	})
	return nil
}

type paddingGapConn struct {
	firstWrite chan struct{}
	closed     chan struct{}
	closeOnce  sync.Once
	writeOnce  sync.Once
	writeCount atomic.Int32
}

func newPaddingGapConn() *paddingGapConn {
	return &paddingGapConn{firstWrite: make(chan struct{}), closed: make(chan struct{})}
}

func (c *paddingGapConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, net.ErrClosed
}

func (c *paddingGapConn) Write(b []byte) (int, error) {
	c.writeCount.Add(1)
	c.writeOnce.Do(func() { close(c.firstWrite) })
	return len(b), nil
}

func (c *paddingGapConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}
func (c *paddingGapConn) LocalAddr() net.Addr              { return testAddr("local") }
func (c *paddingGapConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (c *paddingGapConn) SetDeadline(time.Time) error      { return os.ErrInvalid }
func (c *paddingGapConn) SetReadDeadline(time.Time) error  { return os.ErrInvalid }
func (c *paddingGapConn) SetWriteDeadline(time.Time) error { return os.ErrInvalid }

type shortWriteConn struct {
	access     sync.Mutex
	buffer     bytes.Buffer
	maxWrite   int
	firstError error
	calls      atomic.Int32
}

func (c *shortWriteConn) Write(b []byte) (int, error) {
	c.access.Lock()
	defer c.access.Unlock()
	call := c.calls.Add(1)
	n := min(len(b), c.maxWrite)
	_, _ = c.buffer.Write(b[:n])
	if call == 1 && c.firstError != nil {
		return n, c.firstError
	}
	return n, nil
}

func (c *shortWriteConn) Bytes() []byte {
	c.access.Lock()
	defer c.access.Unlock()
	return append([]byte(nil), c.buffer.Bytes()...)
}
func (c *shortWriteConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *shortWriteConn) Close() error                     { return nil }
func (c *shortWriteConn) LocalAddr() net.Addr              { return testAddr("local") }
func (c *shortWriteConn) RemoteAddr() net.Addr             { return testAddr("remote") }
func (c *shortWriteConn) SetDeadline(time.Time) error      { return nil }
func (c *shortWriteConn) SetReadDeadline(time.Time) error  { return nil }
func (c *shortWriteConn) SetWriteDeadline(time.Time) error { return nil }

type testAddr string

func (a testAddr) Network() string { return string(a) }
func (a testAddr) String() string  { return string(a) }
