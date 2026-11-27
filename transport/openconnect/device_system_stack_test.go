//go:build with_gvisor

package openconnect

import (
	"net/netip"
	"testing"

	"github.com/sagernet/sing-tun/gtcpip/header"
)

func TestPacketDestinationRejectsTruncatedPackets(t *testing.T) {
	tests := []struct {
		name   string
		packet []byte
	}{
		{"truncated IPv4", []byte{0x45, 0x00, 0x00}},
		{"truncated IPv6", append([]byte{0x60, 0x00, 0x00, 0x00}, make([]byte, 4)...)},
		{"empty", nil},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if address := packetDestination(testCase.packet); address.IsValid() {
				t.Fatalf("expected invalid address, got %s", address)
			}
		})
	}
}

func TestPacketDestinationIPv4(t *testing.T) {
	destination := netip.MustParseAddr("192.0.2.1")
	packet := make([]byte, header.IPv4MinimumSize)
	packet[0] = 0x45
	copy(packet[16:20], destination.AsSlice())
	if address := packetDestination(packet); address != destination {
		t.Fatalf("expected %s, got %s", destination, address)
	}
}

func TestPacketDestinationIPv6(t *testing.T) {
	destination := netip.MustParseAddr("2001:db8::1")
	packet := make([]byte, header.IPv6MinimumSize)
	packet[0] = 0x60
	copy(packet[24:40], destination.AsSlice())
	if address := packetDestination(packet); address != destination {
		t.Fatalf("expected %s, got %s", destination, address)
	}
}
