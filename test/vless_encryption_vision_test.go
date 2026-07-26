//go:build badlinkname

package main

import (
	"crypto/mlkem"
	"encoding/base64"
	"net/netip"
	"testing"

	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/gofrs/uuid/v5"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/json/badoption"
)

func TestVLESSEncryptionVisionTLS(t *testing.T) {
	const (
		vlessServerPort uint16 = 21100 + iota
		mixedPort
		echoPort
	)
	user, err := uuid.NewV4()
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := mlkem.GenerateKey768()
	if err != nil {
		t.Fatal(err)
	}
	decryption := "mlkem768x25519plus.native.60s." + base64.RawURLEncoding.EncodeToString(privateKey.Bytes())
	encryption := "mlkem768x25519plus.native.0rtt." + base64.RawURLEncoding.EncodeToString(privateKey.EncapsulationKey().Bytes())
	const flow = "xtls-rprx-vision"
	_, certificatePath, keyPath := createSelfSignedCertificate(t, "example.org")

	startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeMixed,
				Tag:  "mixed-in",
				Options: &option.HTTPMixedInboundOptions{ListenOptions: option.ListenOptions{
					Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
					ListenPort: mixedPort,
				}},
			},
			{
				Type: C.TypeVLESS,
				Options: &option.VLESSInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: vlessServerPort,
					},
					Users:      []option.VLESSUser{{UUID: user.String(), Flow: flow}},
					Decryption: decryption,
					InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{TLS: &option.InboundTLSOptions{
						Enabled:         true,
						ServerName:      "example.org",
						CertificatePath: certificatePath,
						KeyPath:         keyPath,
					}},
				},
			},
		},
		Outbounds: []option.Outbound{
			{Type: C.TypeDirect},
			{
				Type: C.TypeVLESS,
				Tag:  "vless-out",
				Options: &option.VLESSOutboundOptions{
					ServerOptions: option.ServerOptions{Server: "127.0.0.1", ServerPort: vlessServerPort},
					UUID:          user.String(),
					Flow:          flow,
					Encryption:    encryption,
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{TLS: &option.OutboundTLSOptions{
						Enabled:         true,
						ServerName:      "example.org",
						CertificatePath: certificatePath,
					}},
				},
			},
		},
		Route: routeInboundTo("mixed-in", "vless-out"),
	})
	testSuit(t, mixedPort, echoPort)
}
