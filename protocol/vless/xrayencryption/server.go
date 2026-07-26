// SPDX-License-Identifier: MPL-2.0
//
// Adapted from Xray-core proxy/vless/encryption/server.go at commit
// 6e3322d219140a025285ded1114fe17a5edb74d8. Portions copyright the
// Xray-core contributors and are licensed under the Mozilla Public License 2.0.

package xrayencryption

import (
	"bytes"
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

type ServerSession struct {
	PfsKey  []byte
	NfsKeys sync.Map
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

	RWLock   sync.RWMutex
	Closed   bool
	done     chan struct{}
	Lasts    map[int64][16]byte
	Tickets  [][16]byte
	Sessions map[[16]byte]*ServerSession
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
	if secondsFrom > 0 || secondsTo > 0 {
		i.Lasts = make(map[int64][16]byte)
		i.Tickets = make([][16]byte, 0, 1024)
		i.Sessions = make(map[[16]byte]*ServerSession)
		i.done = make(chan struct{})
		go i.expireSessions()
	}
	return nil
}

func (i *ServerInstance) expireSessions() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			i.RWLock.Lock()
			minute := time.Now().Unix() / 60
			last := i.Lasts[minute]
			delete(i.Lasts, minute)
			delete(i.Lasts, minute-1)
			if last != [16]byte{} {
				for index, ticket := range i.Tickets {
					delete(i.Sessions, ticket)
					if ticket == last {
						i.Tickets = i.Tickets[index+1:]
						break
					}
				}
			}
			i.RWLock.Unlock()
		case <-i.done:
			return
		}
	}
}

func (i *ServerInstance) Close() error {
	i.RWLock.Lock()
	if !i.Closed {
		i.Closed = true
		if i.done != nil {
			close(i.done)
		}
	}
	i.RWLock.Unlock()
	return nil
}

func (i *ServerInstance) Handshake(conn net.Conn, fallback *[]byte) (*CommonConn, error) {
	if i.NfsSKeys == nil {
		return nil, errors.New("uninitialized")
	}
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
	copy(ticket[:], EncodeLength(int(seconds)))
	if seconds > 0 {
		i.RWLock.Lock()
		i.Lasts[(time.Now().Unix()+max(i.SecondsFrom, i.SecondsTo))/60+2] = ticket
		i.Tickets = append(i.Tickets, ticket)
		i.Sessions[ticket] = &ServerSession{PfsKey: pfsKey}
		i.RWLock.Unlock()
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
			time.Sleep(paddingGaps[index])
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
	i.RWLock.RLock()
	session := i.Sessions[[16]byte(ticket)]
	i.RWLock.RUnlock()
	if session == nil {
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
		return nil, errors.New("expired ticket")
	}
	if _, loaded := session.NfsKeys.LoadOrStore([32]byte(nfsKey), true); loaded {
		return nil, errors.New("replay detected")
	}
	c.UnitedKey = append(session.PfsKey, nfsKey...)
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
