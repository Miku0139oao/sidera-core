// SPDX-License-Identifier: MPL-2.0
//
// Adapted from Xray-core proxy/vless/encryption/common.go at commit
// 6e3322d219140a025285ded1114fe17a5edb74d8. Portions copyright the
// Xray-core contributors and are licensed under the Mozilla Public License 2.0.

package xrayencryption

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	N "github.com/sagernet/sing/common/network"

	"golang.org/x/crypto/chacha20poly1305"
	"lukechampine.com/blake3"
)

const (
	maxPlaintextSize = 8192
	maxRecordSize    = 16640
)

var outBytesPool = sync.Pool{
	New: func() any {
		buffer := make([]byte, 5+maxPlaintextSize+chacha20poly1305.Overhead)
		return &buffer
	},
}

type CommonConn struct {
	net.Conn
	UseAES      bool
	Client      *ClientInstance
	UnitedKey   []byte
	PreWrite    []byte
	AEAD        *AEAD
	PeerAEAD    *AEAD
	PeerPadding []byte
	rawInput    bytes.Buffer
	input       bytes.Reader
}

var (
	_ net.Conn             = (*CommonConn)(nil)
	_ N.WithUpstreamReader = (*CommonConn)(nil)
	_ N.WithUpstreamWriter = (*CommonConn)(nil)
)

func NewCommonConn(conn net.Conn, useAES bool) *CommonConn {
	return &CommonConn{Conn: conn, UseAES: useAES}
}

func (c *CommonConn) UpstreamReader() any { return c.Conn }
func (c *CommonConn) UpstreamWriter() any { return c.Conn }

func (c *CommonConn) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if c.AEAD == nil {
		return 0, errors.New("xray encryption: connection is not initialized")
	}

	written := 0
	outBytesPointer := outBytesPool.Get().(*[]byte)
	outBytes := *outBytesPointer
	defer outBytesPool.Put(outBytesPointer)
	for written < len(b) {
		plaintext := b[written:]
		if len(plaintext) > maxPlaintextSize {
			plaintext = plaintext[:maxPlaintextSize]
		}
		headerAndData := outBytes[:5+len(plaintext)+c.AEAD.Overhead()]
		EncodeHeader(headerAndData, len(plaintext)+c.AEAD.Overhead())
		maxNonce := bytes.Equal(c.AEAD.Nonce[:], MaxNonce)
		c.AEAD.Seal(headerAndData[:5], nil, plaintext, headerAndData[:5])
		if maxNonce {
			c.AEAD = NewAEAD(headerAndData, c.UnitedKey, c.UseAES)
		}
		if c.PreWrite != nil {
			headerAndData = append(c.PreWrite, headerAndData...)
			c.PreWrite = nil
		}
		if err := writeFull(c.Conn, headerAndData); err != nil {
			return written, err
		}
		written += len(plaintext)
	}
	return written, nil
}

func (c *CommonConn) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if c.PeerAEAD == nil {
		serverRandom := make([]byte, 16)
		if _, err := io.ReadFull(c.Conn, serverRandom); err != nil {
			return 0, err
		}
		c.PeerAEAD = NewAEAD(serverRandom, c.UnitedKey, c.UseAES)
		if xorConn, ok := c.Conn.(*XorConn); ok {
			xorConn.PeerCTR = NewCTR(c.UnitedKey, serverRandom)
		}
	}
	if c.PeerPadding != nil {
		if _, err := io.ReadFull(c.Conn, c.PeerPadding); err != nil {
			return 0, err
		}
		if _, err := c.PeerAEAD.Open(c.PeerPadding[:0], nil, c.PeerPadding, nil); err != nil {
			return 0, err
		}
		c.PeerPadding = nil
	}
	if c.input.Len() > 0 {
		return c.input.Read(b)
	}

	peerHeader := [5]byte{}
	if _, err := io.ReadFull(c.Conn, peerHeader[:]); err != nil {
		return 0, err
	}
	l, err := DecodeHeader(peerHeader[:])
	if err != nil {
		if c.Client != nil {
			c.Client.RWLock.Lock()
			if bytes.HasPrefix(c.UnitedKey, c.Client.PfsKey) {
				c.Client.Expire = time.Now()
			}
			c.Client.RWLock.Unlock()
			return 0, errors.New("xray encryption: new handshake needed")
		}
		return 0, err
	}
	c.Client = nil
	if c.rawInput.Cap() < l {
		c.rawInput.Grow(l)
	}
	peerData := c.rawInput.Bytes()[:l]
	if _, err := io.ReadFull(c.Conn, peerData); err != nil {
		return 0, err
	}
	dst := peerData[:l-c.PeerAEAD.Overhead()]
	if len(dst) <= len(b) {
		dst = b[:len(dst)]
	}
	var nextAEAD *AEAD
	if bytes.Equal(c.PeerAEAD.Nonce[:], MaxNonce) {
		nextAEAD = NewAEAD(append(peerHeader[:], peerData...), c.UnitedKey, c.UseAES)
	}
	_, err = c.PeerAEAD.Open(dst[:0], nil, peerData, peerHeader[:])
	if nextAEAD != nil {
		c.PeerAEAD = nextAEAD
	}
	if err != nil {
		return 0, err
	}
	if len(dst) > len(b) {
		c.input.Reset(dst[copy(b, dst):])
		dst = b
	}
	return len(dst), nil
}

type AEAD struct {
	cipher.AEAD
	Nonce [12]byte
}

func NewAEAD(context, key []byte, useAES bool) *AEAD {
	k := make([]byte, chacha20poly1305.KeySize)
	blake3.DeriveKey(k, string(context), key)
	var aead cipher.AEAD
	if useAES {
		block, err := aes.NewCipher(k)
		if err != nil {
			panic(err)
		}
		aead, err = cipher.NewGCM(block)
		if err != nil {
			panic(err)
		}
	} else {
		var err error
		aead, err = chacha20poly1305.New(k)
		if err != nil {
			panic(err)
		}
	}
	return &AEAD{AEAD: aead}
}

func (a *AEAD) Seal(dst, nonce, plaintext, additionalData []byte) []byte {
	if nonce == nil {
		nonce = IncreaseNonce(a.Nonce[:])
	}
	return a.AEAD.Seal(dst, nonce, plaintext, additionalData)
}

func (a *AEAD) Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error) {
	if nonce == nil {
		nonce = IncreaseNonce(a.Nonce[:])
	}
	return a.AEAD.Open(dst, nonce, ciphertext, additionalData)
}

func IncreaseNonce(nonce []byte) []byte {
	for i := range len(nonce) {
		nonce[len(nonce)-1-i]++
		if nonce[len(nonce)-1-i] != 0 {
			break
		}
	}
	return nonce
}

var MaxNonce = bytes.Repeat([]byte{255}, 12)

func EncodeLength(l int) []byte {
	return []byte{byte(l >> 8), byte(l)}
}

func DecodeLength(b []byte) int {
	return int(b[0])<<8 | int(b[1])
}

func EncodeHeader(header []byte, length int) {
	header[0] = 23
	header[1] = 3
	header[2] = 3
	header[3] = byte(length >> 8)
	header[4] = byte(length)
}

func DecodeHeader(header []byte) (int, error) {
	if len(header) < 5 {
		return 0, fmt.Errorf("invalid header: %v", header)
	}
	length := int(header[3])<<8 | int(header[4])
	if header[0] != 23 || header[1] != 3 || header[2] != 3 || length < 17 || length > maxRecordSize {
		return 0, fmt.Errorf("invalid header: %v", header[:5])
	}
	return length, nil
}

func ParsePadding(padding string, paddingLens, paddingGaps *[][3]int) error {
	if padding == "" {
		return nil
	}
	maxLen := 0
	for index, value := range strings.Split(padding, ".") {
		parts := strings.Split(value, "-")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return fmt.Errorf("invalid padding length/gap parameter: %s", value)
		}
		entry := [3]int{}
		for partIndex := range entry {
			parsed, err := strconv.Atoi(parts[partIndex])
			if err != nil {
				return fmt.Errorf("invalid padding length/gap parameter %q: %w", value, err)
			}
			entry[partIndex] = parsed
		}
		if entry[0] < 0 || entry[1] < 0 || entry[2] < 0 {
			return fmt.Errorf("invalid padding length/gap parameter: %s", value)
		}
		if index == 0 && (entry[0] < 100 || entry[1] < 35 || entry[2] < 35) {
			return errors.New("first padding length must not be smaller than 35 and must have 100 percent probability")
		}
		if index%2 == 0 {
			*paddingLens = append(*paddingLens, entry)
			maxLen += max(entry[1], entry[2])
		} else {
			*paddingGaps = append(*paddingGaps, entry)
		}
	}
	if maxLen > 18+65535 {
		return errors.New("total padding length must not be larger than 65553")
	}
	return nil
}

func CreatePadding(paddingLens, paddingGaps [][3]int) (length int, lens []int, gaps []time.Duration, err error) {
	if len(paddingLens) == 0 {
		paddingLens = [][3]int{{100, 111, 1111}, {50, 0, 3333}}
		paddingGaps = [][3]int{{75, 0, 111}}
	}
	for _, entry := range paddingLens {
		partLength := 0
		chance, randomErr := randBetween(0, 100)
		if randomErr != nil {
			return 0, nil, nil, randomErr
		}
		if int64(entry[0]) >= chance {
			value, randomErr := randBetween(int64(entry[1]), int64(entry[2]))
			if randomErr != nil {
				return 0, nil, nil, randomErr
			}
			partLength = int(value)
		}
		lens = append(lens, partLength)
		length += partLength
	}
	for _, entry := range paddingGaps {
		gap := int64(0)
		chance, randomErr := randBetween(0, 100)
		if randomErr != nil {
			return 0, nil, nil, randomErr
		}
		if int64(entry[0]) >= chance {
			gap, randomErr = randBetween(int64(entry[1]), int64(entry[2]))
			if randomErr != nil {
				return 0, nil, nil, randomErr
			}
		}
		gaps = append(gaps, time.Duration(gap)*time.Millisecond)
	}
	return length, lens, gaps, nil
}

// CreatPadding retains Xray's exported misspelling for source compatibility.
func CreatPadding(paddingLens, paddingGaps [][3]int) (int, []int, []time.Duration) {
	length, lens, gaps, err := CreatePadding(paddingLens, paddingGaps)
	if err != nil {
		return 0, nil, nil
	}
	return length, lens, gaps
}

func randBetween(from, to int64) (int64, error) {
	if from == to {
		return from, nil
	}
	if from > to {
		from, to = to, from
	}
	value, err := rand.Int(rand.Reader, big.NewInt(to-from))
	if err != nil {
		return 0, err
	}
	return from + value.Int64(), nil
}

func writeFull(writer io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := writer.Write(b)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(b) {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
