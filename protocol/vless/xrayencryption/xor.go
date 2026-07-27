// SPDX-License-Identifier: MPL-2.0
//
// Adapted from Xray-core proxy/vless/encryption/xor.go at commit
// 6e3322d219140a025285ded1114fe17a5edb74d8. Portions copyright the
// Xray-core contributors and are licensed under the Mozilla Public License 2.0.

package xrayencryption

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"net"

	N "github.com/sagernet/sing/common/network"

	"lukechampine.com/blake3"
)

func NewCTR(key, iv []byte) cipher.Stream {
	k := make([]byte, 32)
	blake3.DeriveKey(k, "VLESS", key)
	block, err := aes.NewCipher(k)
	if err != nil {
		panic(err)
	}
	return cipher.NewCTR(block, iv)
}

type XorConn struct {
	net.Conn
	CTR       cipher.Stream
	PeerCTR   cipher.Stream
	OutSkip   int
	OutHeader []byte
	InSkip    int
	InHeader  []byte
}

var (
	_ net.Conn             = (*XorConn)(nil)
	_ N.WithUpstreamReader = (*XorConn)(nil)
	_ N.WithUpstreamWriter = (*XorConn)(nil)
)

func NewXorConn(conn net.Conn, ctr, peerCTR cipher.Stream, outSkip, inSkip int) *XorConn {
	return &XorConn{
		Conn:      conn,
		CTR:       ctr,
		PeerCTR:   peerCTR,
		OutSkip:   outSkip,
		OutHeader: make([]byte, 0, 5),
		InSkip:    inSkip,
		InHeader:  make([]byte, 0, 5),
	}
}

func (c *XorConn) UpstreamReader() any { return c.Conn }
func (c *XorConn) UpstreamWriter() any { return c.Conn }

func (c *XorConn) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if c.CTR == nil {
		return 0, errors.New("xray encryption: missing outbound XOR stream")
	}
	for remaining := b; ; {
		if len(remaining) <= c.OutSkip {
			c.OutSkip -= len(remaining)
			break
		}
		remaining = remaining[c.OutSkip:]
		c.OutSkip = 0
		need := 5 - len(c.OutHeader)
		if len(remaining) < need {
			c.OutHeader = append(c.OutHeader, remaining...)
			c.CTR.XORKeyStream(remaining, remaining)
			break
		}
		c.OutSkip, _ = DecodeHeader(append(c.OutHeader, remaining[:need]...))
		c.OutHeader = c.OutHeader[:0]
		c.CTR.XORKeyStream(remaining[:need], remaining[:need])
		remaining = remaining[need:]
	}
	if err := writeFull(c.Conn, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *XorConn) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	n, err := c.Conn.Read(b)
	for remaining := b[:n]; len(remaining) > 0; {
		if len(remaining) <= c.InSkip {
			c.InSkip -= len(remaining)
			break
		}
		remaining = remaining[c.InSkip:]
		c.InSkip = 0
		if c.PeerCTR == nil {
			return 0, errors.New("xray encryption: missing inbound XOR stream")
		}
		need := 5 - len(c.InHeader)
		if len(remaining) < need {
			c.PeerCTR.XORKeyStream(remaining, remaining)
			c.InHeader = append(c.InHeader, remaining...)
			break
		}
		c.PeerCTR.XORKeyStream(remaining[:need], remaining[:need])
		c.InSkip, _ = DecodeHeader(append(c.InHeader, remaining[:need]...))
		c.InHeader = c.InHeader[:0]
		remaining = remaining[need:]
	}
	return n, err
}
