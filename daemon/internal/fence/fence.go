// Package fence keeps a job's guest off the networks around the machine
// that lends it: the owner's LAN, the tailnet the machine is on, and the
// machine itself. The guest keeps the internet and the resolvers hived gave
// it. The owner can let it reach particular places as well, or everything,
// as it could before there was a fence.
//
// It works on the guest's own frames as they leave its network card, before
// anything on the host acts on them. A job is root in its VM and can send
// any frame it likes; what is fenced off is fenced off for those too.
package fence

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// A Range is a block of addresses a guest is kept from, and what it is.
type Range struct {
	Prefix netip.Prefix
	What   string
}

// Private is every range a guest is kept from unless the owner says
// otherwise. These are the networks that are not the internet: someone's
// LAN, a tailnet or VPN, a cloud's metadata service, and addresses that
// mean the host itself.
var Private = []Range{
	{netip.MustParsePrefix("10.0.0.0/8"), "a private network"},
	{netip.MustParsePrefix("172.16.0.0/12"), "a private network"},
	{netip.MustParsePrefix("192.168.0.0/16"), "a private network"},
	{netip.MustParsePrefix("100.64.0.0/10"), "a tailnet, or a carrier's NAT"},
	{netip.MustParsePrefix("169.254.0.0/16"), "link-local, where a cloud keeps its metadata service"},
	{netip.MustParsePrefix("224.0.0.0/4"), "multicast"},
	{netip.MustParsePrefix("255.255.255.255/32"), "broadcast"},
	{netip.MustParsePrefix("0.0.0.0/8"), "this host"},
	{netip.MustParsePrefix("127.0.0.0/8"), "this host's loopback"},
	{netip.MustParsePrefix("fc00::/7"), "a private IPv6 network, a tailnet's included"},
	{netip.MustParsePrefix("fe80::/10"), "link-local IPv6"},
	{netip.MustParsePrefix("ff00::/8"), "IPv6 multicast"},
	{netip.MustParsePrefix("::1/128"), "this host's loopback"},
	{netip.MustParsePrefix("::/128"), "this host"},
}

// nat64 is the well-known prefix a NAT64 gateway reaches IPv4 through. An
// address in it is the IPv4 address in its last four bytes.
var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// Fenced is the range that keeps a guest from this address, if one does.
func Fenced(address netip.Addr) (Range, bool) {
	address = plain(address)
	for _, r := range Private {
		if r.Prefix.Contains(address) {
			return r, true
		}
	}
	return Range{}, false
}

// plain is the address a packet to this one actually reaches: an IPv4
// address written as IPv6, either way, is that IPv4 address.
func plain(address netip.Addr) netip.Addr {
	address = address.Unmap().WithZone("")
	if address.Is6() && nat64.Contains(address) {
		raw := address.As16()
		return netip.AddrFrom4([4]byte(raw[12:]))
	}
	return address
}

// An Allowance is somewhere private the owner lets jobs reach: an address
// or a range, and on it one port or, with Port zero, all of them.
type Allowance struct {
	Prefix netip.Prefix
	Port   uint16
}

func (a Allowance) String() string {
	place := a.Prefix.String()
	if a.Prefix.IsSingleIP() {
		place = a.Prefix.Addr().String()
	}
	if a.Port == 0 {
		return place
	}
	if a.Prefix.Addr().Is6() {
		place = "[" + place + "]"
	}
	return place + ":" + strconv.Itoa(int(a.Port))
}

// covers reports whether this allowance lets a packet through. A port is
// only TCP's or UDP's: a packet of any other kind has none to match.
func (a Allowance) covers(address netip.Addr, port uint16) bool {
	return a.Prefix.Contains(address) && (a.Port == 0 || a.Port == port)
}

var errAllowance = errors.New("allow-network takes addresses and ranges, each with a port if you like, separated by commas: 192.168.1.20:5432,10.0.0.0/24,[fd00::5]:80; or none")

// ParseAllowances reads what `hived config set allow-network` was given:
// "192.168.1.20:5432,10.0.0.0/24,[fd00::5]:80", or "" for none.
func ParseAllowances(text string) ([]Allowance, error) {
	var allowed []Allowance
	for _, entry := range strings.Split(text, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		allowance, err := parseAllowance(entry)
		if err != nil {
			return nil, err
		}
		allowed = append(allowed, allowance)
	}
	return allowed, nil
}

func parseAllowance(entry string) (Allowance, error) {
	place, port := entry, ""
	switch {
	case strings.HasPrefix(entry, "["):
		// IPv6 with a port, which needs the brackets to be told apart.
		inside, rest, ok := strings.Cut(entry[1:], "]")
		if !ok || !strings.HasPrefix(rest, ":") {
			return Allowance{}, fmt.Errorf("%q: %w", entry, errAllowance)
		}
		place, port = inside, rest[1:]
	case strings.Count(entry, ":") == 1:
		place, port, _ = strings.Cut(entry, ":")
	}
	var allowance Allowance
	if strings.Contains(place, "/") {
		prefix, err := netip.ParsePrefix(place)
		if err != nil {
			return Allowance{}, fmt.Errorf("%q: %w", entry, errAllowance)
		}
		allowance.Prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-unmappedBits(prefix)).Masked()
	} else {
		address, err := netip.ParseAddr(place)
		if err != nil || address.Zone() != "" {
			return Allowance{}, fmt.Errorf("%q: %w", entry, errAllowance)
		}
		address = address.Unmap()
		allowance.Prefix = netip.PrefixFrom(address, address.BitLen())
	}
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return Allowance{}, fmt.Errorf("%q: a port is from 1 to 65535", entry)
		}
		allowance.Port = uint16(number)
	}
	return allowance, nil
}

// unmappedBits is what a range written as IPv4-in-IPv6 loses when it is
// written as the IPv4 range it is.
func unmappedBits(prefix netip.Prefix) int {
	if prefix.Addr().Is4In6() {
		return 96
	}
	return 0
}

// FormatAllowances is the allowances as ParseAllowances reads them.
func FormatAllowances(allowed []Allowance) string {
	parts := make([]string, len(allowed))
	for i, allowance := range allowed {
		parts[i] = allowance.String()
	}
	return strings.Join(parts, ",")
}

// Policy is what one guest may reach.
type Policy struct {
	// Open lets the guest reach everything the machine can, as it could
	// before there was a fence: the owner's private-network allow.
	Open bool
	// Allow is somewhere private the owner lets jobs reach all the same.
	Allow []Allowance
	// Resolvers are the name servers the guest was given. They are asked
	// on port 53 wherever they are, a router on the LAN included.
	Resolvers []netip.Addr
	// Host is the machine's own addresses, kept from the guest whatever
	// network they are on: what the machine serves is not the job's.
	Host []netip.Addr
}

// Protocols a port is matched for.
const (
	protoTCP = 6
	protoUDP = 17
)

// Reaches reports whether the guest may send a packet of this protocol to
// this address and port. Port is zero for a packet that has none, which
// only an allowance of every port lets through.
func (p *Policy) Reaches(protocol uint8, address netip.Addr, port uint16) bool {
	if p == nil {
		p = &Policy{}
	}
	if p.Open {
		return true
	}
	address = plain(address)
	if protocol != protoTCP && protocol != protoUDP {
		port = 0
	}
	if slices.ContainsFunc(p.Allow, func(a Allowance) bool { return a.covers(address, port) }) {
		return true
	}
	if port == 53 && slices.ContainsFunc(p.Resolvers, func(r netip.Addr) bool { return plain(r) == address }) {
		return true
	}
	if slices.ContainsFunc(p.Host, func(h netip.Addr) bool { return plain(h) == address }) {
		return false
	}
	_, fenced := Fenced(address)
	return !fenced
}

// HostAddresses is every address this machine has, on any interface.
func HostAddresses() []netip.Addr {
	found, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var addresses []netip.Addr
	for _, each := range found {
		if network, ok := each.(*net.IPNet); ok {
			if address, ok := netip.AddrFromSlice(network.IP); ok {
				addresses = append(addresses, address.Unmap())
			}
		}
	}
	return addresses
}
