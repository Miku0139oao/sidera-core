// SPDX-License-Identifier: MPL-2.0
//
// Adapted from Xray-core proxy/vless/encryption/client.go at commit
// 6e3322d219140a025285ded1114fe17a5edb74d8. Portions copyright the
// Xray-core contributors and are licensed under the Mozilla Public License 2.0.

package xrayencryption

import (
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/mlkem"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sys/cpu"
	"lukechampine.com/blake3"
)

type ClientInstance struct {
	NfsPKeys      []any
	NfsPKeysBytes [][]byte
	Hash32s       [][32]byte
	RelaysLength  int
	XorMode       uint32
	Seconds       uint32
	PaddingLens   [][3]int
	PaddingGaps   [][3]int

	RWLock sync.RWMutex
	Expire time.Time
	PfsKey []byte
	Ticket []byte
}

func (i *ClientInstance) Init(nfsPKeysBytes [][]byte, xorMode, seconds uint32, padding string) error {
	if i.NfsPKeys != nil {
		return errors.New("already initialized")
	}
	if len(nfsPKeysBytes) == 0 {
		return errors.New("empty client key list")
	}
	if xorMode > 2 {
		return fmt.Errorf("invalid XOR mode %d", xorMode)
	}
	i.NfsPKeys = make([]any, len(nfsPKeysBytes))
	i.NfsPKeysBytes = make([][]byte, len(nfsPKeysBytes))
	i.Hash32s = make([][32]byte, len(nfsPKeysBytes))
	for index, key := range nfsPKeysBytes {
		i.NfsPKeysBytes[index] = append([]byte(nil), key...)
		if len(key) == 32 {
			publicKey, err := ecdh.X25519().NewPublicKey(key)
			if err != nil {
				return err
			}
			i.NfsPKeys[index] = publicKey
			i.RelaysLength += 64
		} else {
			publicKey, err := mlkem.NewEncapsulationKey768(key)
			if err != nil {
				return err
			}
			i.NfsPKeys[index] = publicKey
			i.RelaysLength += 1120
		}
		i.Hash32s[index] = blake3.Sum256(key)
	}
	i.RelaysLength -= 32
	i.XorMode = xorMode
	i.Seconds = seconds
	return ParsePadding(padding, &i.PaddingLens, &i.PaddingGaps)
}

func (i *ClientInstance) Handshake(conn net.Conn) (*CommonConn, error) {
	if i.NfsPKeys == nil {
		return nil, errors.New("uninitialized")
	}
	c := NewCommonConn(conn, hasAESGCMHardwareSupport())

	ivAndRelaysLength := 16 + i.RelaysLength
	const pfsKeyExchangeLength = 18 + 1184 + 32 + 16
	paddingLength, paddingLens, paddingGaps, err := CreatePadding(i.PaddingLens, i.PaddingGaps)
	if err != nil {
		return nil, err
	}
	clientHello := make([]byte, ivAndRelaysLength+pfsKeyExchangeLength+paddingLength)
	iv := clientHello[:16]
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	relays := clientHello[16:ivAndRelaysLength]
	var nfsKey []byte
	var lastCTR cipher.Stream
	for index, key := range i.NfsPKeys {
		keyLength := 32
		switch publicKey := key.(type) {
		case *ecdh.PublicKey:
			privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				return nil, err
			}
			copy(relays, privateKey.PublicKey().Bytes())
			nfsKey, err = privateKey.ECDH(publicKey)
			if err != nil {
				return nil, err
			}
		case *mlkem.EncapsulationKey768:
			var ciphertext []byte
			nfsKey, ciphertext = publicKey.Encapsulate()
			copy(relays, ciphertext)
			keyLength = 1088
		}
		if i.XorMode > 0 {
			NewCTR(i.NfsPKeysBytes[index], iv).XORKeyStream(relays, relays[:keyLength])
		}
		if lastCTR != nil {
			lastCTR.XORKeyStream(relays, relays[:32])
		}
		if index == len(i.NfsPKeys)-1 {
			break
		}
		lastCTR = NewCTR(nfsKey, iv)
		lastCTR.XORKeyStream(relays[keyLength:], i.Hash32s[index+1][:])
		relays = relays[keyLength+32:]
	}
	nfsAEAD := NewAEAD(iv, nfsKey, c.UseAES)

	if i.Seconds > 0 {
		i.RWLock.RLock()
		if time.Now().Before(i.Expire) {
			c.Client = i
			c.UnitedKey = append(i.PfsKey, nfsKey...)
			nfsAEAD.Seal(clientHello[:ivAndRelaysLength], nil, EncodeLength(32), nil)
			nfsAEAD.Seal(clientHello[:ivAndRelaysLength+18], nil, i.Ticket, nil)
			i.RWLock.RUnlock()
			c.PreWrite = clientHello[:ivAndRelaysLength+18+32]
			c.AEAD = NewAEAD(clientHello[ivAndRelaysLength+18:ivAndRelaysLength+18+32], c.UnitedKey, c.UseAES)
			if i.XorMode == 2 {
				c.Conn = NewXorConn(conn, NewCTR(c.UnitedKey, iv), nil, len(c.PreWrite), 16)
			}
			return c, nil
		}
		i.RWLock.RUnlock()
	}

	pfsKeyExchange := clientHello[ivAndRelaysLength : ivAndRelaysLength+pfsKeyExchangeLength]
	nfsAEAD.Seal(pfsKeyExchange[:0], nil, EncodeLength(pfsKeyExchangeLength-18), nil)
	mlkemPrivateKey, err := mlkem.GenerateKey768()
	if err != nil {
		return nil, err
	}
	x25519PrivateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	pfsPublicKey := append(mlkemPrivateKey.EncapsulationKey().Bytes(), x25519PrivateKey.PublicKey().Bytes()...)
	nfsAEAD.Seal(pfsKeyExchange[:18], nil, pfsPublicKey, nil)

	padding := clientHello[ivAndRelaysLength+pfsKeyExchangeLength:]
	nfsAEAD.Seal(padding[:0], nil, EncodeLength(paddingLength-18), nil)
	nfsAEAD.Seal(padding[:18], nil, padding[18:paddingLength-16], nil)

	paddingLens[0] += ivAndRelaysLength + pfsKeyExchangeLength
	for index, fragmentLength := range paddingLens {
		if fragmentLength > 0 {
			if err := writeFull(conn, clientHello[:fragmentLength]); err != nil {
				return nil, err
			}
			clientHello = clientHello[fragmentLength:]
		}
		if len(paddingGaps) > index {
			time.Sleep(paddingGaps[index])
		}
	}

	encryptedPfsPublicKey := make([]byte, 1088+32+16)
	if _, err := io.ReadFull(conn, encryptedPfsPublicKey); err != nil {
		return nil, err
	}
	if _, err := nfsAEAD.Open(encryptedPfsPublicKey[:0], MaxNonce, encryptedPfsPublicKey, nil); err != nil {
		return nil, err
	}
	mlkemKey, err := mlkemPrivateKey.Decapsulate(encryptedPfsPublicKey[:1088])
	if err != nil {
		return nil, err
	}
	peerX25519PublicKey, err := ecdh.X25519().NewPublicKey(encryptedPfsPublicKey[1088 : 1088+32])
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
	c.UnitedKey = append(pfsKey, nfsKey...)
	c.AEAD = NewAEAD(pfsPublicKey, c.UnitedKey, c.UseAES)
	c.PeerAEAD = NewAEAD(encryptedPfsPublicKey[:1088+32], c.UnitedKey, c.UseAES)

	encryptedTicket := make([]byte, 32)
	if _, err := io.ReadFull(conn, encryptedTicket); err != nil {
		return nil, err
	}
	if _, err := c.PeerAEAD.Open(encryptedTicket[:0], nil, encryptedTicket, nil); err != nil {
		return nil, err
	}
	seconds := DecodeLength(encryptedTicket)
	if i.Seconds > 0 && seconds > 0 {
		i.RWLock.Lock()
		i.Expire = time.Now().Add(time.Duration(seconds) * time.Second)
		i.PfsKey = pfsKey
		i.Ticket = append([]byte(nil), encryptedTicket[:16]...)
		i.RWLock.Unlock()
	}

	encryptedLength := make([]byte, 18)
	if _, err := io.ReadFull(conn, encryptedLength); err != nil {
		return nil, err
	}
	if _, err := c.PeerAEAD.Open(encryptedLength[:0], nil, encryptedLength, nil); err != nil {
		return nil, err
	}
	length := DecodeLength(encryptedLength[:2])
	c.PeerPadding = make([]byte, length)
	if i.XorMode == 2 {
		c.Conn = NewXorConn(conn, NewCTR(c.UnitedKey, iv), NewCTR(c.UnitedKey, encryptedTicket[:16]), 0, length)
	}
	return c, nil
}

func hasAESGCMHardwareSupport() bool {
	hasAMD64 := cpu.X86.HasAES && cpu.X86.HasPCLMULQDQ && cpu.X86.HasSSE41 && cpu.X86.HasSSSE3
	hasARM64 := (cpu.ARM64.HasAES && cpu.ARM64.HasPMULL) || runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
	hasS390X := cpu.S390X.HasAES && cpu.S390X.HasAESCTR && cpu.S390X.HasGHASH
	hasPPC64 := runtime.GOARCH == "ppc64" || runtime.GOARCH == "ppc64le"
	return hasAMD64 || hasARM64 || hasS390X || hasPPC64
}
