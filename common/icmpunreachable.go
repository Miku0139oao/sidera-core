package common

import (
	"net/netip"

	"github.com/sagernet/sing-tun/gtcpip/header"
)

// synthesizedTTL is the hop limit used for locally generated replies, matching
// the value the network stack uses for its own synthesized ICMP.
const synthesizedTTL = 64

// BuildICMPUnreachable builds the ICMP destination-unreachable reply a tunnel
// sends when it receives a packet it cannot route. The equivalent helper was
// exported by sing-tun up to v0.8.x; from v0.9 the reply builders are internal
// to the flow dispatcher, so the tunnel endpoints build it themselves.
//
// The return value has headroom bytes in front so the caller can hand it
// straight to a return path. ok is false when the packet is malformed, is a
// fragment, or already carries an ICMP error.
func BuildICMPUnreachable(packet []byte, source netip.Addr, headroom int) (reply []byte, ok bool) {
	switch header.IPVersion(packet) {
	case header.IPv4Version:
		ipHdr := header.IPv4(packet)
		if !ipHdr.IsValid(len(packet)) || ipHdr.FragmentOffset() != 0 {
			return nil, false
		}
		sourceAddr := ipHdr.SourceAddr()
		if sourceAddr.IsUnspecified() || sourceAddr.IsMulticast() {
			return nil, false
		}
		if ipHdr.TransportProtocol() == header.ICMPv4ProtocolNumber {
			if len(ipHdr.Payload()) < header.ICMPv4MinimumSize || header.ICMPv4(ipHdr.Payload()).Type() != header.ICMPv4Echo {
				return nil, false
			}
		}
		replySource := ipHdr.DestinationAddr()
		if source.Is4() {
			replySource = source
		}
		return buildICMPv4Unreachable(ipHdr, replySource, headroom), true
	case header.IPv6Version:
		ipHdr := header.IPv6(packet)
		if !ipHdr.IsValid(len(packet)) {
			return nil, false
		}
		sourceAddr := ipHdr.SourceAddr()
		if sourceAddr.IsUnspecified() || sourceAddr.IsMulticast() {
			return nil, false
		}
		if ipHdr.TransportProtocol() == header.ICMPv6ProtocolNumber {
			if len(ipHdr.Payload()) < header.ICMPv6MinimumSize || header.ICMPv6(ipHdr.Payload()).Type() != header.ICMPv6EchoRequest {
				return nil, false
			}
		}
		replySource := ipHdr.DestinationAddr()
		if source.Is6() {
			replySource = source
		}
		return buildICMPv6Unreachable(ipHdr, replySource, headroom), true
	default:
		return nil, false
	}
}

func buildICMPv4Unreachable(packet header.IPv4, source netip.Addr, headroom int) []byte {
	const maxPayloadLength = header.IPv4MinimumProcessableDatagramSize - header.IPv4MinimumSize - header.ICMPv4MinimumSize
	payloadLength := min(int(packet.TotalLength()), len(packet), maxPayloadLength)
	size := header.IPv4MinimumSize + header.ICMPv4MinimumSize + payloadLength
	buffer := make([]byte, headroom+size)
	response := header.IPv4(buffer[headroom:])
	response.Encode(&header.IPv4Fields{
		TotalLength: uint16(size),
		TTL:         synthesizedTTL,
		Protocol:    uint8(header.ICMPv4ProtocolNumber),
		SrcAddr:     source,
		DstAddr:     packet.SourceAddr(),
	})
	response.SetChecksum(^response.CalculateChecksum())
	icmpHdr := header.ICMPv4(response.Payload())
	icmpHdr.SetType(header.ICMPv4DstUnreachable)
	icmpHdr.SetCode(header.ICMPv4HostUnreachable)
	copy(icmpHdr.Payload(), packet[:payloadLength])
	icmpHdr.SetChecksum(header.ICMPv4Checksum(icmpHdr, 0))
	return buffer
}

func buildICMPv6Unreachable(packet header.IPv6, source netip.Addr, headroom int) []byte {
	const maxPayloadLength = header.IPv6MinimumMTU - header.IPv6MinimumSize - header.ICMPv6ErrorHeaderSize
	payloadLength := min(header.IPv6MinimumSize+int(packet.PayloadLength()), len(packet), maxPayloadLength)
	size := header.IPv6MinimumSize + header.ICMPv6ErrorHeaderSize + payloadLength
	buffer := make([]byte, headroom+size)
	response := header.IPv6(buffer[headroom:])
	response.Encode(&header.IPv6Fields{
		PayloadLength:     uint16(header.ICMPv6ErrorHeaderSize + payloadLength),
		TransportProtocol: header.ICMPv6ProtocolNumber,
		HopLimit:          synthesizedTTL,
		SrcAddr:           source,
		DstAddr:           packet.SourceAddr(),
	})
	icmpHdr := header.ICMPv6(response.Payload())
	icmpHdr.SetType(header.ICMPv6DstUnreachable)
	icmpHdr.SetCode(header.ICMPv6AddressUnreachable)
	copy(icmpHdr[header.ICMPv6ErrorHeaderSize:], packet[:payloadLength])
	icmpHdr.SetChecksum(header.ICMPv6Checksum(header.ICMPv6ChecksumParams{
		Header: icmpHdr,
		Src:    response.SourceAddressSlice(),
		Dst:    response.DestinationAddressSlice(),
	}))
	return buffer
}
