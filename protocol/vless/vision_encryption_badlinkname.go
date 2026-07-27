//go:build badlinkname

package vless

import (
	"net"
	"reflect"
	"unsafe"

	"github.com/Miku0139oao/sidera-core/protocol/vless/xrayencryption"
)

type visionTLSCreator func(conn net.Conn) (loaded bool, netConn net.Conn, reflectType reflect.Type, reflectPointer uintptr)

//go:linkname visionTLSRegistry github.com/sagernet/sing-vmess/vless.tlsRegistry
var visionTLSRegistry []visionTLSCreator

func init() {
	visionTLSRegistry = append(visionTLSRegistry, func(conn net.Conn) (loaded bool, netConn net.Conn, reflectType reflect.Type, reflectPointer uintptr) {
		encryptedConn, loaded := conn.(*xrayencryption.CommonConn)
		if !loaded {
			return false, nil, nil, 0
		}
		// Xray removes the AEAD layer in Vision direct mode while retaining the
		// immediate transport wrapper (including XorConn for random mode).
		return true, encryptedConn.Conn, reflect.TypeFor[xrayencryption.CommonConn](), uintptr(unsafe.Pointer(encryptedConn))
	})
}

func xrayEncryptionVisionSupported() bool {
	return true
}
