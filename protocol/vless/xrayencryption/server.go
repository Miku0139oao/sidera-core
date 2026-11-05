// SPDX-License-Identifier: MPL-2.0
//
// Adapted from Xray-core proxy/vless/encryption/server.go at commit
// 6e3322d219140a025285ded1114fe17a5edb74d8. Portions copyright the
// Xray-core contributors and are licensed under the Mozilla Public License 2.0.

package xrayencryption

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/mlkem"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"lukechampine.com/blake3"
)

const (
	maxServerSessions = 4096
	maxSessionResumes = 1024
	maxReplayEntries  = 65536
)

var (
	errExpiredTicket     = errors.New("expired ticket")
	errReplayDetected    = errors.New("replay detected")
	errTicketResumeLimit = errors.New("ticket resume limit exceeded")
	errGlobalReplayLimit = errors.New("global replay limit exceeded")
)

type ServerSession struct {
	PfsKey   []byte
	ExpireAt time.Time
	NfsKeys  map[[32]byte]struct{}
}

type ServerInstance struct {
	NfsSKeys      []any
	NfsPKeysBytes [][]byte
	Hash32s       [][32]byte
	RelaysLength  int
	XorMode       uint32
	SecondsFrom   int64
	SecondsTo     int64
	PaddingLens   [][3]int
	PaddingGaps   [][3]int

	RWLock           sync.RWMutex
	Closed           bool
	generation       uint64
	lifecycleCtx     context.Context
	lifecycleCancel  context.CancelFunc
	handshakeWait    sync.WaitGroup
	activeHandshakes int
	Sessions         map[[16]byte]*ServerSession
	replayEntries    int
	sessionLimit     int
	resumeLimit      int
	replayLimit      int
	now              func() time.Time
}

func (i *ServerInstance) Init(nfsSKeysBytes [][]byte, xorMode uint32, secondsFrom, secondsTo int64, padding string) error {
	if i.NfsSKeys != nil {
		return errors.New("already initialized")
	}
	if len(nfsSKeysBytes) == 0 {
		return errors.New("empty server key list")
	}
	if xorMode > 2 {
		return fmt.Errorf("invalid XOR mode %d", xorMode)
	}
	if secondsFrom < 0 || secondsTo < 0 {
		return errors.New("negative ticket lifetime")
	}

	i.NfsSKeys = make([]any, len(nfsSKeysBytes))
	i.NfsPKeysBytes = make([][]byte, len(nfsSKeysBytes))
	i.Hash32s = make([][32]byte, len(nfsSKeysBytes))
	for index, key := range nfsSKeysBytes {
		if len(key) == 32 {
			privateKey, err := ecdh.X25519().NewPrivateKey(key)
			if err != nil {
				return err
			}
			i.NfsSKeys[index] = privateKey
			i.NfsPKeysBytes[index] = privateKey.PublicKey().Bytes()
			i.RelaysLength += 64
		} else {
			privateKey, err := mlkem.NewDecapsulationKey768(key)
			if err != nil {
				return err
			}
			i.NfsSKeys[index] = privateKey
			i.NfsPKeysBytes[index] = privateKey.EncapsulationKey().Bytes()
			i.RelaysLength += 1120
		}
		i.Hash32s[index] = blake3.Sum256(i.NfsPKeysBytes[index])
	}
	i.RelaysLength -= 32
	i.XorMode = xorMode
	i.SecondsFrom = secondsFrom
	i.SecondsTo = secondsTo
	if err := ParsePadding(padding, &i.PaddingLens, &i.PaddingGaps); err != nil {
		return err
	}
	i.sessionLimit = maxServerSessions
	i.resumeLimit = maxSessionResumes
	i.replayLimit = maxReplayEntries
	i.now = time.Now
	i.generation = 1
	i.lifecycleCtx, i.lifecycleCancel = context.WithCancel(context.Background())
	if secondsFrom > 0 || secondsTo > 0 {
		i.Sessions = make(map[[16]byte]*ServerSession, min(maxServerSessions, 1024))
	}
	return nil
}

func (i *ServerInstance) Close() error {
	i.RWLock.Lock()
	if i.Closed {
		i.RWLock.Unlock()
		return nil
	}
	i.Closed = true
	i.generation++
	cancel := i.lifecycleCancel
	i.Sessions = nil
	i.replayEntries = 0
	i.RWLock.Unlock()
	if cancel != nil {
		cancel()
	}
	i.handshakeWait.Wait()
	return nil
}

func (i *ServerInstance) Handshake(conn net.Conn, fallback *[]byte) (*CommonConn, error) {
	return i.HandshakeContext(context.Background(), conn, fallback)
}

func (i *ServerInstance) HandshakeContext(ctx context.Context, conn net.Conn, fallback *[]byte) (*CommonConn, error) {
	i.RWLock.Lock()
	if i.NfsSKeys == nil || i.lifecycleCtx == nil {
		i.RWLock.Unlock()
		return nil, errors.New("uninitialized")
	}
	if i.Closed {
		i.RWLock.Unlock()
		return nil, net.ErrClosed
	}
	generation := i.generation
	lifecycleCtx := i.lifecycleCtx
	i.handshakeWait.Add(1)
	i.activeHandshakes++
	i.RWLock.Unlock()
	defer i.finishHandshake()

	handshakeCtx, cancelHandshake := context.WithCancel(ctx)
	lifecycleCancellationDone := make(chan struct{})
	stopLifecycleCancellation := context.AfterFunc(lifecycleCtx, func() {
		cancelHandshake()
		close(lifecycleCancellationDone)
	})
	result, err := handshakeWithContext(handshakeCtx, conn, func(ctx context.Context) (*CommonConn, error) {
		return i.handshake(ctx, conn, fallback, generation)
	})
	if !stopLifecycleCancellation() {
		<-lifecycleCancellationDone
	}
	cancelHandshake()

	i.RWLock.RLock()
	generationValid := !i.Closed && i.generation == generation
	i.RWLock.RUnlock()
	if !generationValid {
		if result != nil {
			_ = result.Close()
		}
		return nil, net.ErrClosed
	}
	return result, err
}

func (i *ServerInstance) finishHandshake() {
	i.RWLock.Lock()
	i.activeHandshakes--
	i.RWLock.Unlock()
	i.handshakeWait.Done()
}

func (i *ServerInstance) handshake(ctx context.Context, conn net.Conn, fallback *[]byte, generation uint64) (*CommonConn, error) {
	c := NewCommonConn(conn, true)

	ivAndRelays := make([]byte, 16+i.RelaysLength)
	if _, err := io.ReadFull(conn, ivAndRelays); err != nil {
		return nil, err
	}
	if fallback != nil {
		*fallback = append(*fallback, ivAndRelays...)
	}
	iv := ivAndRelays[:16]
	relays := ivAndRelays[16:]
	var nfsKey []byte
	var lastCTR cipher.Stream
	for index, key := range i.NfsSKeys {
		if lastCTR != nil {
			lastCTR.XORKeyStream(relays, relays[:32])
		}
		keyLength := 32
		if _, ok := key.(*mlkem.DecapsulationKey768); ok {
			keyLength = 1088
		}
		if i.XorMode > 0 {
			NewCTR(i.NfsPKeysBytes[index], iv).XORKeyStream(relays, relays[:keyLength])
		}
		switch privateKey := key.(type) {
		case *ecdh.PrivateKey:
			publicKey, err := ecdh.X25519().NewPublicKey(relays[:keyLength])
			if err != nil {
				return nil, err
			}
			if publicKey.Bytes()[31] > 127 {
				return nil, errors.New("highest bit of peer X25519 public key is set")
			}
			nfsKey, err = privateKey.ECDH(publicKey)
			if err != nil {
				return nil, err
			}
		case *mlkem.DecapsulationKey768:
			var err error
			nfsKey, err = privateKey.Decapsulate(relays[:keyLength])
			if err != nil {
				return nil, err
			}
		}
		if index == len(i.NfsSKeys)-1 {
			break
		}
		relays = relays[keyLength:]
		lastCTR = NewCTR(nfsKey, iv)
		lastCTR.XORKeyStream(relays, relays[:32])
		if !bytes.Equal(relays[:32], i.Hash32s[index+1][:]) {
			return nil, fmt.Errorf("unexpected relay hash: %v", relays[:32])
		}
		relays = relays[32:]
	}
	nfsAEAD := NewAEAD(iv, nfsKey, c.UseAES)

	encryptedLength := make([]byte, 18)
	if _, err := io.ReadFull(conn, encryptedLength); err != nil {
		return nil, err
	}
	if fallback != nil {
		*fallback = append(*fallback, encryptedLength...)
	}
	decryptedLength := make([]byte, 2)
	if _, err := nfsAEAD.Open(decryptedLength[:0], nil, encryptedLength, nil); err != nil {
		c.UseAES = false
		nfsAEAD = NewAEAD(iv, nfsKey, c.UseAES)
		if _, err := nfsAEAD.Open(decryptedLength[:0], nil, encryptedLength, nil); err != nil {
			return nil, err
		}
	}
	if fallback != nil {
		*fallback = nil
	}
	length := DecodeLength(decryptedLength)
	if length == 32 {
		return i.resume(conn, c, iv, nfsKey, nfsAEAD)
	}
	if length < 1184+32+16 {
		return nil, errors.New("PFS key exchange is too short")
	}

	encryptedPfsPublicKey := make([]byte, length)
	if _, err := io.ReadFull(conn, encryptedPfsPublicKey); err != nil {
		return nil, err
	}
	if _, err := nfsAEAD.Open(encryptedPfsPublicKey[:0], nil, encryptedPfsPublicKey, nil); err != nil {
		return nil, err
	}
	mlkemPublicKey, err := mlkem.NewEncapsulationKey768(encryptedPfsPublicKey[:1184])
	if err != nil {
		return nil, err
	}
	mlkemKey, encapsulatedPfsKey := mlkemPublicKey.Encapsulate()
	peerX25519PublicKey, err := ecdh.X25519().NewPublicKey(encryptedPfsPublicKey[1184 : 1184+32])
	if err != nil {
		return nil, err
	}
	x25519PrivateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	x25519Key, err := x25519PrivateKey.ECDH(peerX25519PublicKey)
	if err != nil {
		return nil, err
	}
	pfsKey := make([]byte, 64)
	copy(pfsKey, mlkemKey)
	copy(pfsKey[32:], x25519Key)
	pfsPublicKey := append(encapsulatedPfsKey, x25519PrivateKey.PublicKey().Bytes()...)
	c.UnitedKey = append(pfsKey, nfsKey...)
	c.AEAD = NewAEAD(pfsPublicKey, c.UnitedKey, c.UseAES)
	c.PeerAEAD = NewAEAD(encryptedPfsPublicKey[:1184+32], c.UnitedKey, c.UseAES)

	ticket := [16]byte{}
	if _, err := rand.Read(ticket[:]); err != nil {
		return nil, err
	}
	var seconds int64
	if i.SecondsTo == 0 {
		factor, err := randBetween(50, 100)
		if err != nil {
			return nil, err
		}
		seconds = i.SecondsFrom * factor / 100
	} else {
		seconds, err = randBetween(i.SecondsFrom, i.SecondsTo)
		if err != nil {
			return nil, err
		}
	}
	_, err = i.issueTicket(generation, &ticket, pfsKey, seconds)
	if err != nil {
		return nil, err
	}

	const pfsKeyExchangeLength = 1088 + 32 + 16
	const encryptedTicketLength = 32
	paddingLength, paddingLens, paddingGaps, err := CreatePadding(i.PaddingLens, i.PaddingGaps)
	if err != nil {
		return nil, err
	}
	serverHello := make([]byte, pfsKeyExchangeLength+encryptedTicketLength+paddingLength)
	nfsAEAD.Seal(serverHello[:0], MaxNonce, pfsPublicKey, nil)
	c.AEAD.Seal(serverHello[:pfsKeyExchangeLength], nil, ticket[:], nil)
	padding := serverHello[pfsKeyExchangeLength+encryptedTicketLength:]
	c.AEAD.Seal(padding[:0], nil, EncodeLength(paddingLength-18), nil)
	c.AEAD.Seal(padding[:18], nil, padding[18:paddingLength-16], nil)

	paddingLens[0] += pfsKeyExchangeLength + encryptedTicketLength
	for index, fragmentLength := range paddingLens {
		if fragmentLength > 0 {
			if err := writeFull(conn, serverHello[:fragmentLength]); err != nil {
				return nil, err
			}
			serverHello = serverHello[fragmentLength:]
		}
		if len(paddingGaps) > index {
			if err := waitContext(ctx, paddingGaps[index]); err != nil {
				return nil, err
			}
		}
	}
	if err := readAndOpenPadding(conn, nfsAEAD, encryptedLength); err != nil {
		return nil, err
	}

	if i.XorMode == 2 {
		c.Conn = NewXorConn(conn, NewCTR(c.UnitedKey, ticket[:]), NewCTR(c.UnitedKey, iv), 0, 0)
	}
	return c, nil
}

func (i *ServerInstance) issueTicket(generation uint64, ticket *[16]byte, pfsKey []byte, seconds int64) (int64, error) {
	copy(ticket[:], EncodeLength(0))
	if seconds <= 0 {
		return 0, nil
	}

	i.RWLock.Lock()
	defer i.RWLock.Unlock()
	if i.Closed || i.generation != generation {
		return 0, net.ErrClosed
	}
	now := i.currentTimeLocked()
	sessionLimit := i.sessionLimit
	if sessionLimit <= 0 {
		sessionLimit = maxServerSessions
	}
	replayLimit := i.replayLimit
	if replayLimit <= 0 {
		replayLimit = maxReplayEntries
	}
	if len(i.Sessions) >= sessionLimit || i.replayEntries >= replayLimit {
		i.pruneExpiredLocked(now)
		if len(i.Sessions) >= sessionLimit || i.replayEntries >= replayLimit {
			return 0, nil
		}
	}
	if i.Sessions == nil {
		i.Sessions = make(map[[16]byte]*ServerSession)
	}
	copy(ticket[:], EncodeLength(int(seconds)))
	if _, loaded := i.Sessions[*ticket]; loaded {
		return 0, errors.New("ticket collision")
	}
	i.Sessions[*ticket] = &ServerSession{
		PfsKey:   append([]byte(nil), pfsKey...),
		ExpireAt: now.Add(time.Duration(seconds) * time.Second),
	}
	return seconds, nil
}

func (i *ServerInstance) acceptResume(ticket [16]byte, nfsKey [32]byte) ([]byte, error) {
	i.RWLock.Lock()
	defer i.RWLock.Unlock()
	if i.Closed {
		return nil, net.ErrClosed
	}
	now := i.currentTimeLocked()
	session := i.Sessions[ticket]
	if session != nil && !now.Before(session.ExpireAt) {
		i.deleteSessionLocked(ticket)
		session = nil
	}
	if session == nil {
		return nil, errExpiredTicket
	}
	if session.NfsKeys == nil {
		session.NfsKeys = make(map[[32]byte]struct{})
	}
	if _, loaded := session.NfsKeys[nfsKey]; loaded {
		return nil, errReplayDetected
	}
	resumeLimit := i.resumeLimit
	if resumeLimit <= 0 {
		resumeLimit = maxSessionResumes
	}
	if len(session.NfsKeys) >= resumeLimit {
		i.deleteSessionLocked(ticket)
		return nil, errTicketResumeLimit
	}
	replayLimit := i.replayLimit
	if replayLimit <= 0 {
		replayLimit = maxReplayEntries
	}
	if i.replayEntries >= replayLimit {
		i.pruneExpiredLocked(now)
		if i.replayEntries >= replayLimit {
			return nil, errGlobalReplayLimit
		}
	}
	session.NfsKeys[nfsKey] = struct{}{}
	i.replayEntries++
	return append([]byte(nil), session.PfsKey...), nil
}

func (i *ServerInstance) currentTimeLocked() time.Time {
	if i.now != nil {
		return i.now()
	}
	return time.Now()
}

func (i *ServerInstance) pruneExpiredLocked(now time.Time) {
	for ticket, session := range i.Sessions {
		if !now.Before(session.ExpireAt) {
			i.deleteSessionLocked(ticket)
		}
	}
}

func (i *ServerInstance) deleteSessionLocked(ticket [16]byte) {
	session := i.Sessions[ticket]
	if session == nil {
		return
	}
	delete(i.Sessions, ticket)
	i.replayEntries -= len(session.NfsKeys)
	if i.replayEntries < 0 {
		i.replayEntries = 0
	}
}

func (i *ServerInstance) resume(conn net.Conn, c *CommonConn, iv, nfsKey []byte, nfsAEAD *AEAD) (*CommonConn, error) {
	if i.SecondsFrom == 0 && i.SecondsTo == 0 {
		return nil, errors.New("0-RTT is not allowed")
	}
	encryptedTicket := make([]byte, 32)
	if _, err := io.ReadFull(conn, encryptedTicket); err != nil {
		return nil, err
	}
	ticket, err := nfsAEAD.Open(nil, nil, encryptedTicket, nil)
	if err != nil {
		return nil, err
	}
	ticketID := [16]byte(ticket)
	pfsKey, err := i.acceptResume(ticketID, [32]byte(nfsKey))
	if err != nil {
		if errors.Is(err, errExpiredTicket) {
			noiseLength, randomErr := randBetween(1279, 2279)
			if randomErr != nil {
				return nil, randomErr
			}
			noise := make([]byte, noiseLength)
			for {
				if _, randomErr = rand.Read(noise); randomErr != nil {
					return nil, randomErr
				}
				if _, headerErr := DecodeHeader(noise); headerErr != nil {
					break
				}
			}
			_ = writeFull(conn, noise)
		}
		return nil, err
	}
	c.UnitedKey = make([]byte, 0, len(pfsKey)+len(nfsKey))
	c.UnitedKey = append(c.UnitedKey, pfsKey...)
	c.UnitedKey = append(c.UnitedKey, nfsKey...)
	c.PreWrite = make([]byte, 16)
	if _, err := rand.Read(c.PreWrite); err != nil {
		return nil, err
	}
	c.AEAD = NewAEAD(c.PreWrite, c.UnitedKey, c.UseAES)
	c.PeerAEAD = NewAEAD(encryptedTicket, c.UnitedKey, c.UseAES)
	if i.XorMode == 2 {
		c.Conn = NewXorConn(conn, NewCTR(c.UnitedKey, c.PreWrite), NewCTR(c.UnitedKey, iv), 16, 0)
	}
	return c, nil
}

func readAndOpenPadding(conn net.Conn, aead *AEAD, encryptedLength []byte) error {
	if _, err := io.ReadFull(conn, encryptedLength); err != nil {
		return err
	}
	if _, err := aead.Open(encryptedLength[:0], nil, encryptedLength, nil); err != nil {
		return err
	}
	encryptedPadding := make([]byte, DecodeLength(encryptedLength[:2]))
	if _, err := io.ReadFull(conn, encryptedPadding); err != nil {
		return err
	}
	_, err := aead.Open(encryptedPadding[:0], nil, encryptedPadding, nil)
	return err
}
