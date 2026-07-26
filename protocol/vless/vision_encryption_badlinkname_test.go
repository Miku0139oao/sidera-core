//go:build badlinkname

package vless

import (
	"net"
	"reflect"
	"testing"

	"github.com/Miku0139oao/sidera-core/protocol/vless/xrayencryption"
	"github.com/stretchr/testify/require"
)

func TestVisionEncryptionDirectConnection(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	creator := visionTLSRegistry[len(visionTLSRegistry)-1]
	for name, transportConn := range map[string]net.Conn{
		"native": clientConn,
		"random": &xrayencryption.XorConn{Conn: clientConn},
	} {
		t.Run(name, func(t *testing.T) {
			encryptedConn := xrayencryption.NewCommonConn(transportConn, true)
			loaded, directConn, reflectType, reflectPointer := creator(encryptedConn)
			require.True(t, loaded)
			require.Same(t, transportConn, directConn)
			require.Equal(t, reflect.TypeOf(xrayencryption.CommonConn{}), reflectType)
			require.NotZero(t, reflectPointer)
		})
	}
}
