package fence

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"
)

var (
	guestCard   = []byte{0x52, 0x54, 0x00, 0x12, 0x34, 0x56}
	networkCard = []byte{0x9a, 0x55, 0x9a, 0x55, 0x9a, 0x55}
	guestIPv4   = netip.MustParseAddr("192.168.1.5")
	guestIPv6   = netip.MustParseAddr("2001:db8::5")
)

// frame is what the guest's card sends: an Ethernet header, then data.
func frame(kind uint16, data []byte) []byte {
	out := append(append(append([]byte{}, networkCard...), guestCard...), 0, 0)
	binary.BigEndian.PutUint16(out[12:14], kind)
	return append(out, data...)
}

// segment is the start of a TCP or UDP segment, or an ICMP message, to
// this port or of this type.
func segment(protocol uint8, port uint16) []byte {
	switch protocol {
	case protoTCP:
		out := make([]byte, 20)
		binary.BigEndian.PutUint16(out[0:2], 40000)
		binary.BigEndian.PutUint16(out[2:4], port)
		out[12], out[13] = 5<<4, 0x02 // SYN
		return out
	case protoUDP:
		out := make([]byte, 8)
		binary.BigEndian.PutUint16(out[0:2], 40000)
		binary.BigEndian.PutUint16(out[2:4], port)
		binary.BigEndian.PutUint16(out[4:6], 8)
		return out
	}
	return []byte{byte(port), 0, 0, 0, 0, 0, 0, 0}
}

func packet4(protocol uint8, source, address netip.Addr, payload []byte) []byte {
	header := make([]byte, 20)
	header[0] = 4<<4 | 5
	binary.BigEndian.PutUint16(header[2:4], uint16(20+len(payload)))
	header[8], header[9] = 64, protocol
	from, to := source.As4(), address.As4()
	copy(header[12:16], from[:])
	copy(header[16:20], to[:])
	binary.BigEndian.PutUint16(header[10:12], checksum(0, header))
	return frame(etherIPv4, append(header, payload...))
}

func packet6(next uint8, source, address netip.Addr, payload []byte) []byte {
	header := make([]byte, 40)
	header[0] = 6 << 4
	binary.BigEndian.PutUint16(header[4:6], uint16(len(payload)))
	header[6], header[7] = next, 64
	from, to := source.As16(), address.As16()
	copy(header[8:24], from[:])
	copy(header[24:40], to[:])
	return frame(etherIPv6, append(header, payload...))
}

func tcp4(address string, port uint16) []byte {
	return packet4(protoTCP, guestIPv4, netip.MustParseAddr(address), segment(protoTCP, port))
}

func tcp6(address string, port uint16) []byte {
	return packet6(protoTCP, guestIPv6, netip.MustParseAddr(address), segment(protoTCP, port))
}

func TestFramesFromTheGuestAreHeldToThePolicy(t *testing.T) {
	policy := &Policy{
		Allow:     []Allowance{{Prefix: netip.MustParsePrefix("192.168.1.20/32"), Port: 5432}},
		Resolvers: []netip.Addr{netip.MustParseAddr("192.168.1.1")},
	}
	// A fragment's header, then a TCP segment: hop-by-hop options go first.
	options := append([]byte{protoTCP, 0, 0, 0, 0, 0, 0, 0}, segment(protoTCP, 22)...)
	later := append([]byte{protoTCP, 0, 0, 8, 0, 0, 0, 1}, make([]byte, 16)...)
	for _, test := range []struct {
		name    string
		frame   []byte
		pass    bool
		answers bool
	}{
		{"a connection to the internet", tcp4("140.82.112.3", 443), true, false},
		{"a connection to the internet over IPv6", tcp6("2606:4700:4700::1111", 443), true, false},
		{"a connection to the LAN", tcp4("192.168.1.30", 22), false, true},
		{"a connection to a tailnet peer", tcp4("100.101.102.103", 22), false, true},
		{"a connection to a private IPv6 address", tcp6("fd7a:115c:a1e0::1", 22), false, true},
		{"a connection to the host's loopback, by a frame made by hand", tcp4("127.0.0.1", 22), false, true},
		{"a connection to the host's loopback over IPv6", tcp6("::1", 22), false, true},
		{"a connection to the LAN written as IPv6", tcp6("::ffff:192.168.1.30", 22), false, true},
		{"the allowed address", tcp4("192.168.1.20", 5432), true, false},
		{"the allowed address on another port", tcp4("192.168.1.20", 80), false, true},
		{"a question for the guest's resolver", packet4(protoUDP, guestIPv4, netip.MustParseAddr("192.168.1.1"), segment(protoUDP, 53)), true, false},
		{"anything else for the guest's resolver", packet4(protoUDP, guestIPv4, netip.MustParseAddr("192.168.1.1"), segment(protoUDP, 161)), false, true},
		{"the cloud's metadata service", tcp4("169.254.169.254", 80), false, true},
		{"a ping to the LAN", packet4(protoICMP, guestIPv4, netip.MustParseAddr("192.168.1.30"), segment(protoICMP, 8)), false, true},
		{"an error sent to the LAN, which is not answered", packet4(protoICMP, guestIPv4, netip.MustParseAddr("192.168.1.30"), segment(protoICMP, 3)), false, false},
		{"multicast, which is dropped without a word", packet4(protoUDP, guestIPv4, netip.MustParseAddr("224.0.0.251"), segment(protoUDP, 5353)), false, false},
		{"IPv6 multicast too", packet6(protoUDP, guestIPv6, netip.MustParseAddr("ff02::fb"), segment(protoUDP, 5353)), false, false},
		{"asking for an address", packet4(protoUDP, netip.MustParseAddr("0.0.0.0"), netip.MustParseAddr("255.255.255.255"), segment(protoUDP, 67)), true, false},
		{"asking for an IPv6 address", packet6(protoUDP, netip.MustParseAddr("fe80::5"), netip.MustParseAddr("ff02::1:2"), segment(protoUDP, 547)), true, false},
		{"finding the router", packet6(protoICMPv6, netip.MustParseAddr("fe80::5"), netip.MustParseAddr("ff02::2"), segment(protoICMPv6, 133)), true, false},
		{"finding a neighbour", packet6(protoICMPv6, netip.MustParseAddr("fe80::5"), netip.MustParseAddr("fe80::1"), segment(protoICMPv6, 135)), true, false},
		{"a ping to the router's link-local address", packet6(protoICMPv6, netip.MustParseAddr("fe80::5"), netip.MustParseAddr("fe80::1"), segment(protoICMPv6, 128)), false, true},
		{"UDP to port 67 on the LAN, which is not asking for an address", packet4(protoUDP, guestIPv4, netip.MustParseAddr("192.168.1.30"), segment(protoUDP, 67)), false, true},
		{"a LAN connection behind an IPv6 extension header", packet6(hopByHop, guestIPv6, netip.MustParseAddr("fd00::9"), options), false, true},
		{"a later IPv6 fragment to the LAN, which has no port to show", packet6(fragment, guestIPv6, netip.MustParseAddr("fd00::9"), later), false, true},
		{"ARP, which the guest's network answers itself", frame(etherARP, make([]byte, 28)), true, false},
		{"a VLAN-tagged frame", frame(0x8100, make([]byte, 46)), false, false},
		{"a frame too short to be one", []byte{1, 2, 3}, false, false},
		{"an IPv4 header that says it is longer than it is", frame(etherIPv4, []byte{0x4f, 0, 0, 20, 0, 0, 0, 0, 64, 6, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8}), false, false},
	} {
		pass, reply := policy.Check(test.frame)
		if pass != test.pass || (reply != nil) != test.answers {
			t.Errorf("%s: pass %v, answered %v; want %v, %v", test.name, pass, reply != nil, test.pass, test.answers)
		}
	}
}

func TestARefusedConnectionIsAnsweredAsARouterWould(t *testing.T) {
	refused := tcp4("192.168.1.30", 22)
	_, reply := (&Policy{}).Check(refused)
	if !bytes.Equal(reply[0:6], guestCard) || !bytes.Equal(reply[6:12], networkCard) || binary.BigEndian.Uint16(reply[12:14]) != etherIPv4 {
		t.Fatalf("not addressed back to the guest: % x", reply[:14])
	}
	ip := reply[ethernetHeader:]
	if checksum(0, ip[:20]) != 0 {
		t.Error("the IPv4 header's checksum is wrong")
	}
	if got := netip.AddrFrom4([4]byte(ip[12:16])); got.String() != "192.168.1.30" {
		t.Errorf("from %s, not the address refused", got)
	}
	if got := netip.AddrFrom4([4]byte(ip[16:20])); got != guestIPv4 {
		t.Errorf("to %s, not the guest", got)
	}
	icmp := ip[20:]
	if icmp[0] != 3 || icmp[1] != 13 || checksum(0, icmp) != 0 {
		t.Errorf("ICMP type %d code %d, checksum good: %v", icmp[0], icmp[1], checksum(0, icmp) == 0)
	}
	// The guest's kernel finds its connection by the packet quoted.
	if !bytes.Equal(icmp[8:], refused[ethernetHeader:]) {
		t.Error("the refused packet is not quoted")
	}

	refused = tcp6("fd00::9", 22)
	_, reply = (&Policy{}).Check(refused)
	ip = reply[ethernetHeader:]
	if binary.BigEndian.Uint16(reply[12:14]) != etherIPv6 || ip[6] != protoICMPv6 {
		t.Fatalf("not ICMPv6: % x", reply[:ethernetHeader+8])
	}
	source, address := [16]byte(ip[8:24]), [16]byte(ip[24:40])
	if netip.AddrFrom16(source).String() != "fd00::9" || netip.AddrFrom16(address) != guestIPv6 {
		t.Errorf("from %s to %s", netip.AddrFrom16(source), netip.AddrFrom16(address))
	}
	message := ip[40:]
	if int(binary.BigEndian.Uint16(ip[4:6])) != len(message) {
		t.Error("the payload length is wrong")
	}
	if message[0] != 1 || message[1] != 1 || checksum6(source, address, message) != 0 {
		t.Errorf("ICMPv6 type %d code %d, checksum good: %v", message[0], message[1], checksum6(source, address, message) == 0)
	}
	if !bytes.Equal(message[8:], refused[ethernetHeader:]) {
		t.Error("the refused packet is not quoted")
	}
}

// stream writes frames as QEMU and passt do: each after its length.
func stream(frames ...[]byte) []byte {
	var out []byte
	for _, each := range frames {
		out = binary.BigEndian.AppendUint32(out, uint32(len(each)))
		out = append(out, each...)
	}
	return out
}

func TestTheRelayPassesWhatTheGuestMayReachAndAnswersTheRest(t *testing.T) {
	guest, guestCardEnd := net.Pipe()
	network, networkEnd := net.Pipe()
	policy := &Policy{}
	ended := make(chan error, 1)
	go func() { ended <- Relay(guest, network, func() *Policy { return policy }) }()

	allowed, refused := tcp4("140.82.112.3", 443), tcp4("192.168.1.30", 22)
	go func() { _, _ = guestCardEnd.Write(stream(refused, allowed)) }()

	// The refusal comes back to the guest; only the allowed frame goes on.
	_ = networkEnd.SetDeadline(time.Now().Add(5 * time.Second))
	_ = guestCardEnd.SetDeadline(time.Now().Add(5 * time.Second))
	_, answer := policy.Check(refused)
	back := make([]byte, len(stream(answer)))
	if _, err := readAll(guestCardEnd, back); err != nil || !bytes.Equal(back, stream(answer)) {
		t.Fatalf("the guest got % x, %v", back, err)
	}
	got := make([]byte, len(stream(allowed)))
	if _, err := readAll(networkEnd, got); err != nil || !bytes.Equal(got, stream(allowed)) {
		t.Fatalf("the network got % x, %v", got, err)
	}

	// The network's frames reach the guest as they are.
	reply := frame(etherIPv4, []byte("anything at all"))
	go func() { _, _ = networkEnd.Write(stream(reply)) }()
	back = make([]byte, len(stream(reply)))
	if _, err := readAll(guestCardEnd, back); err != nil || !bytes.Equal(back, stream(reply)) {
		t.Fatalf("the guest got % x, %v", back, err)
	}

	// The VM going hangs up on its network.
	guestCardEnd.Close()
	if err := <-ended; err != nil {
		t.Errorf("a VM that stopped is not an error: %v", err)
	}
	if _, err := networkEnd.Read(make([]byte, 1)); err == nil {
		t.Error("the network was left open")
	}
}

func TestTheRelayRefusesAFrameNoCardSends(t *testing.T) {
	guest, guestCardEnd := net.Pipe()
	network, _ := net.Pipe()
	ended := make(chan error, 1)
	go func() { ended <- Relay(guest, network, func() *Policy { return nil }) }()
	go func() { _, _ = guestCardEnd.Write([]byte{0xff, 0xff, 0xff, 0xff}) }()
	select {
	case err := <-ended:
		if err == nil {
			t.Error("a frame of 4 GB was taken")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the relay is still waiting for a frame of 4 GB")
	}
}

func readAll(conn net.Conn, into []byte) (int, error) {
	read := 0
	for read < len(into) {
		n, err := conn.Read(into[read:])
		read += n
		if err != nil {
			return read, err
		}
	}
	return read, nil
}
