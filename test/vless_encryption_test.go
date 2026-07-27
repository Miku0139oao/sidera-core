package main

import (
	"crypto/mlkem"
	"encoding/base64"
	"net/netip"
	"testing"

	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/gofrs/uuid/v5"
)

func TestVLESSEncryption(t *testing.T) {
	const (
		vlessServerPort uint16 = 21000 + iota
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
					Users:      []option.VLESSUser{{UUID: user.String()}},
					Decryption: decryption,
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
					Encryption:    encryption,
				},
			},
		},
		Route: routeInboundTo("mixed-in", "vless-out"),
	})
	testSuit(t, mixedPort, echoPort)
}

func routeInboundTo(inbound, outbound string) *option.RouteOptions {
	return &option.RouteOptions{Rules: []option.Rule{{
		Type: C.RuleTypeDefault,
		DefaultOptions: option.DefaultRule{
			RawDefaultRule: option.RawDefaultRule{Inbound: []string{inbound}},
			RuleAction: option.RuleAction{
				Action:       C.RuleActionTypeRoute,
				RouteOptions: option.RouteActionOptions{Outbound: outbound},
			},
		},
	}}}
}
