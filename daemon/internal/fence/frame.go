package fence

import (
	"encoding/binary"
	"net/netip"
	"slices"
)

// Ethernet frame types the guest's network passes on. Anything else, a VLAN
// tag included, is dropped: nothing a job needs travels in it.
const (
	etherIPv4 = 0x0800
	etherARP  = 0x0806
	etherIPv6 = 0x86dd
)

const (
	ethernetHeader = 14
	protoICMP      = 1
	protoICMPv6    = 58
)

// Where a guest asks for an address: everyone on IPv4, and every DHCP
// server on IPv6.
var (
	broadcast   = netip.AddrFrom4([4]byte{255, 255, 255, 255})
	dhcpServers = netip.MustParseAddr("ff02::1:2")
)

// packet is what Check needs to know of a frame from the guest.
type packet struct {
	v6       bool
	protocol uint8
	source   netip.Addr
	address  netip.Addr
	// port is the destination port of a TCP or UDP packet, and zero for
	// any other.
	port uint16
	// icmp is the message type of an ICMP or ICMPv6 packet.
	icmp int
	// hops is an IPv6 packet's hop limit.
	hops uint8
	// datagram is the IP packet itself, header and all.
	datagram []byte
}

// Check says whether a frame from the guest may go on to its network. A
// frame that may not is answered, when it is the kind a router answers, by
// reply: an ICMP message saying the destination is administratively
// prohibited, which the guest's kernel takes as a connection refused at
// once rather than one left to time out.
func (p *Policy) Check(frame []byte) (pass bool, reply []byte) {
	if len(frame) < ethernetHeader {
		return false, nil
	}
	kind := binary.BigEndian.Uint16(frame[12:14])
	switch kind {
	case etherARP:
		// Asked of the guest's network, and answered by it, for every
		// address: no ARP goes past it.
		return true, nil
	case etherIPv4, etherIPv6:
	default:
		return false, nil
	}
	parsed, ok := parse(kind, frame[ethernetHeader:])
	if !ok {
		return false, nil
	}
	if answeredLocally(parsed) || p.Reaches(parsed.protocol, parsed.address, parsed.port) {
		return true, nil
	}
	if !answerable(parsed) {
		return false, nil
	}
	return false, prohibited(frame, parsed)
}

// answeredLocally is what the guest's network answers itself and never
// sends on: asking for an address, and finding the router. The guest needs
// those to have a network at all.
func answeredLocally(parsed packet) bool {
	switch {
	case !parsed.v6 && parsed.protocol == protoUDP && parsed.port == 67 && parsed.address == broadcast:
		return true // DHCP
	case parsed.v6 && parsed.protocol == protoUDP && parsed.port == 547 && parsed.address == dhcpServers:
		return true // DHCPv6
	case parsed.v6 && parsed.protocol == protoICMPv6 && parsed.icmp >= 133 && parsed.icmp <= 136 && parsed.hops == 255 && onLink(parsed.address):
		// Router and neighbour discovery, which never leaves the link:
		// a hop limit of 255 is how a receiver knows (RFC 4861).
		return true
	}
	return false
}

// linkLocal and linkMulticast are the destinations discovery is sent to.
var (
	linkLocal     = netip.MustParsePrefix("fe80::/10")
	linkMulticast = netip.MustParsePrefix("ff02::/16")
)

func onLink(address netip.Addr) bool {
	return linkLocal.Contains(address) || linkMulticast.Contains(address)
}

// answerable is whether a router would say why it dropped this packet. It
// never answers an error with another, nor a packet meant for many.
func answerable(parsed packet) bool {
	if parsed.address.IsMulticast() || parsed.address == broadcast || !parsed.source.IsValid() || parsed.source.IsUnspecified() || parsed.source.IsMulticast() {
		return false
	}
	switch {
	case !parsed.v6 && parsed.protocol == protoICMP:
		// Only a query is answered: echo, timestamp, information, mask.
		return parsed.icmp == 8 || parsed.icmp == 13 || parsed.icmp == 15 || parsed.icmp == 17
	case parsed.v6 && parsed.protocol == protoICMPv6:
		return parsed.icmp >= 128
	}
	return true
}

// parse reads an IP packet's header, and the start of what it carries.
// passt goes by the frame's type and the fence by the packet's version, so
// a packet whose two disagree is dropped.
func parse(kind uint16, datagram []byte) (packet, bool) {
	if len(datagram) < 1 {
		return packet{}, false
	}
	switch {
	case kind == etherIPv4 && datagram[0]>>4 == 4:
		return parse4(datagram)
	case kind == etherIPv6 && datagram[0]>>4 == 6:
		return parse6(datagram)
	}
	return packet{}, false
}

func parse4(datagram []byte) (packet, bool) {
	if len(datagram) < 20 {
		return packet{}, false
	}
	header := int(datagram[0]&0x0f) * 4
	total := int(binary.BigEndian.Uint16(datagram[2:4]))
	if header < 20 || total < header || len(datagram) < header {
		return packet{}, false
	}
	// A fragment is dropped, as passt drops it (tap4_is_fragment): the
	// port the policy looks at is in the first one only, and passt
	// reassembles none. Only "don't fragment" may be set.
	if binary.BigEndian.Uint16(datagram[6:8])&^0x4000 != 0 {
		return packet{}, false
	}
	datagram = datagram[:min(total, len(datagram))]
	parsed := packet{
		protocol: datagram[9],
		source:   netip.AddrFrom4([4]byte(datagram[12:16])),
		address:  netip.AddrFrom4([4]byte(datagram[16:20])),
		icmp:     -1,
		datagram: datagram,
	}
	parsed.port, parsed.icmp = upper(parsed.protocol, datagram[header:])
	return parsed, true
}

// passtSkips is every IPv6 next header passt takes for an extension header
// and walks past to find what the packet carries: IPV6_NH_OPT in its
// tap.c. A guest's packet with any of them is dropped. Its walk stops one
// header short of the upper-layer one, so what passt would take for the
// port is not the port, and the policy cannot judge such a packet by the
// one passt acts on. Nothing a job needs to send has them.
var passtSkips = []uint8{0, 43, 44, 50, 51, 60, 135, 139, 140, 253, 254}

func parse6(datagram []byte) (packet, bool) {
	if len(datagram) < 40 {
		return packet{}, false
	}
	datagram = datagram[:min(40+int(binary.BigEndian.Uint16(datagram[4:6])), len(datagram))]
	parsed := packet{
		v6:       true,
		source:   netip.AddrFrom16([16]byte(datagram[8:24])),
		address:  netip.AddrFrom16([16]byte(datagram[24:40])),
		protocol: datagram[6],
		hops:     datagram[7],
		datagram: datagram,
	}
	if slices.Contains(passtSkips, parsed.protocol) {
		return packet{}, false
	}
	parsed.port, parsed.icmp = upper(parsed.protocol, datagram[40:])
	return parsed, true
}

// upper is the destination port of a TCP or UDP segment, and the type of
// an ICMP message, from the start of what a packet carries.
func upper(protocol uint8, payload []byte) (port uint16, icmp int) {
	icmp = -1
	switch protocol {
	case protoTCP, protoUDP:
		if len(payload) >= 4 {
			port = binary.BigEndian.Uint16(payload[2:4])
		}
	case protoICMP, protoICMPv6:
		if len(payload) >= 1 {
			icmp = int(payload[0])
		}
	}
	return port, icmp
}

// How much of the refused packet an answer quotes: as much as fits in the
// smallest datagram every host takes, 576 bytes for IPv4 and 1280 for IPv6.
const (
	quote4 = 576 - 20 - 8
	quote6 = 1280 - 40 - 8
)

// prohibited is the answer to a refused packet, as a frame to the guest:
// destination unreachable, administratively prohibited, from the address
// the packet was for.
func prohibited(frame []byte, parsed packet) []byte {
	var datagram []byte
	if parsed.v6 {
		quoted := parsed.datagram[:min(len(parsed.datagram), quote6)]
		message := make([]byte, 8+len(quoted))
		message[0], message[1] = 1, 1 // unreachable, administratively prohibited
		copy(message[8:], quoted)
		datagram = make([]byte, 40, 40+len(message))
		datagram[0] = 6 << 4
		binary.BigEndian.PutUint16(datagram[4:6], uint16(len(message)))
		datagram[6], datagram[7] = protoICMPv6, 64
		source, address := parsed.address.As16(), parsed.source.As16()
		copy(datagram[8:24], source[:])
		copy(datagram[24:40], address[:])
		binary.BigEndian.PutUint16(message[2:4], checksum6(source, address, message))
		datagram = append(datagram, message...)
	} else {
		quoted := parsed.datagram[:min(len(parsed.datagram), quote4)]
		message := make([]byte, 8+len(quoted))
		message[0], message[1] = 3, 13 // unreachable, administratively prohibited
		copy(message[8:], quoted)
		binary.BigEndian.PutUint16(message[2:4], checksum(0, message))
		datagram = make([]byte, 20, 20+len(message))
		datagram[0] = 4<<4 | 5
		binary.BigEndian.PutUint16(datagram[2:4], uint16(20+len(message)))
		datagram[8], datagram[9] = 64, protoICMP
		source, address := parsed.address.As4(), parsed.source.As4()
		copy(datagram[12:16], source[:])
		copy(datagram[16:20], address[:])
		binary.BigEndian.PutUint16(datagram[10:12], checksum(0, datagram))
		datagram = append(datagram, message...)
	}
	// Back the way it came: to the guest's card, from the one it sent to.
	reply := make([]byte, ethernetHeader, ethernetHeader+len(datagram))
	copy(reply[0:6], frame[6:12])
	copy(reply[6:12], frame[0:6])
	copy(reply[12:14], frame[12:14])
	return append(reply, datagram...)
}

// checksum is the internet checksum of data, carrying on from sum.
func checksum(sum uint32, data []byte) uint16 {
	for len(data) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(data))
		data = data[2:]
	}
	if len(data) == 1 {
		sum += uint32(data[0]) << 8
	}
	for sum > 0xffff {
		sum = sum>>16 + sum&0xffff
	}
	return ^uint16(sum)
}

// checksum6 is an ICMPv6 message's checksum, which covers the addresses
// it travels between as well as the message.
func checksum6(source, address [16]byte, message []byte) uint16 {
	var sum uint32
	for _, half := range [][]byte{source[:], address[:]} {
		for i := 0; i < len(half); i += 2 {
			sum += uint32(binary.BigEndian.Uint16(half[i:]))
		}
	}
	sum += uint32(len(message)) + protoICMPv6
	return checksum(sum, message)
}
