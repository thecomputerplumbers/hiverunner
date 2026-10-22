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
	"cmp"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// A Range is a block of addresses a guest is kept from, and what it is.
// LAN is whether it is somebody's LAN, which lan-allow lets a guest reach.
type Range struct {
	Prefix netip.Prefix
	What   string
	LAN    bool
}

// Private is every range a guest is kept from unless the owner says
// otherwise. These are the networks that are not the internet: someone's
// LAN, a tailnet or VPN, a cloud's metadata service, and addresses that
// mean the host itself.
var Private = []Range{
	{netip.MustParsePrefix("10.0.0.0/8"), "a private network", true},
	{netip.MustParsePrefix("172.16.0.0/12"), "a private network", true},
	{netip.MustParsePrefix("192.168.0.0/16"), "a private network", true},
	{netip.MustParsePrefix("100.64.0.0/10"), "a tailnet, or a carrier's NAT", false},
	{netip.MustParsePrefix("169.254.0.0/16"), "link-local, where a cloud keeps its metadata service", false},
	{netip.MustParsePrefix("168.63.129.16/32"), "Azure's host service: metadata, DNS and health probes", false},
	{netip.MustParsePrefix("224.0.0.0/4"), "multicast", false},
	{netip.MustParsePrefix("255.255.255.255/32"), "broadcast", false},
	{netip.MustParsePrefix("0.0.0.0/8"), "this host", false},
	{netip.MustParsePrefix("127.0.0.0/8"), "this host's loopback", false},
	{netip.MustParsePrefix("fc00::/7"), "a private IPv6 network, a tailnet's included", true},
	{netip.MustParsePrefix("fe80::/10"), "link-local IPv6", false},
	{netip.MustParsePrefix("fec0::/10"), "site-local IPv6, deprecated and still routed by some networks", false},
	{netip.MustParsePrefix("ff00::/8"), "IPv6 multicast", false},
	{netip.MustParsePrefix("::1/128"), "this host's loopback", false},
	{netip.MustParsePrefix("::/128"), "this host", false},
}

// The prefixes a NAT64 gateway reaches IPv4 through. The well-known one
// is a /96, with the IPv4 address in its last four bytes. The one set
// aside for a network's own gateway is a /48 that the network may carve a
// longer prefix from, so where in it the IPv4 address sits depends on a
// prefix length the guest's packet does not say.
var (
	nat64      = netip.MustParsePrefix("64:ff9b::/96")
	nat64Local = netip.MustParsePrefix("64:ff9b:1::/48")
)

// tailnet6 is Tailscale's IPv6 range, inside fc00::/7: never a LAN.
var tailnet6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")

// translated is whether an IPv6 address stands for an IPv4 one.
func translated(address netip.Addr) bool {
	return address.Is4In6() || nat64.Contains(address) || nat64Local.Contains(address)
}

// Fenced is the range that keeps a guest from this address, if one does.
// An address that may stand for several IPv4 addresses is fenced if any of
// them is.
func Fenced(address netip.Addr) (Range, bool) {
	for _, form := range standsFor(address) {
		for _, r := range Private {
			if r.Prefix.Contains(form) {
				return r, true
			}
		}
	}
	return Range{}, false
}

// standsFor is the addresses a packet to this one may actually reach. An
// IPv4 address written as IPv6 is that IPv4 address. In a network's own
// NAT64 prefix it is the IPv4 address at each place RFC 6052 puts one in a
// prefix of /48, /56, /64 or /96, since which is meant cannot be told. Its
// /32 and /40 places lie in the /48's own fixed bits, which no prefix
// carved from it can use: read there, every address in it would be
// 0.1.x.x, which is fenced, or 1.x.x.x.
func standsFor(address netip.Addr) []netip.Addr {
	address = address.WithZone("")
	if !address.Is6() {
		return []netip.Addr{address}
	}
	raw := address.As16()
	four := func(a, b, c, d byte) netip.Addr { return netip.AddrFrom4([4]byte{a, b, c, d}) }
	switch {
	case address.Is4In6(), nat64.Contains(address):
		return []netip.Addr{four(raw[12], raw[13], raw[14], raw[15])}
	case nat64Local.Contains(address):
		// Bits 64 to 71 are always zero and never part of the address.
		return []netip.Addr{
			four(raw[6], raw[7], raw[9], raw[10]),   // /48
			four(raw[7], raw[9], raw[10], raw[11]),  // /56
			four(raw[9], raw[10], raw[11], raw[12]), // /64
			four(raw[12], raw[13], raw[14], raw[15]),
		}
	}
	return []netip.Addr{address}
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

// Wide reports whether an allowance takes in so much that it all but opens
// the fence: wider than an IPv4 /8 or an IPv6 /32.
func (a Allowance) Wide() bool {
	if a.Prefix.Addr().Is4() {
		return a.Prefix.Bits() < 8
	}
	return a.Prefix.Bits() < 32
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
	place, port, hasPort := entry, "", false
	switch {
	case strings.HasPrefix(entry, "["):
		// IPv6 with a port, which needs the brackets to be told apart.
		inside, rest, ok := strings.Cut(entry[1:], "]")
		if !ok || !strings.HasPrefix(rest, ":") {
			return Allowance{}, fmt.Errorf("%q: %w", entry, errAllowance)
		}
		place, port, hasPort = inside, rest[1:], true
	case strings.Count(entry, ":") == 1:
		place, port, hasPort = strings.Cut(entry, ":")
	}
	var allowance Allowance
	if strings.Contains(place, "/") {
		prefix, err := netip.ParsePrefix(place)
		if err != nil {
			return Allowance{}, fmt.Errorf("%q: %w", entry, errAllowance)
		}
		// IPv4 written as IPv6 is the IPv4 it stands for, as a packet's
		// address is: only then can the two match.
		if translated(prefix.Addr()) {
			forms := standsFor(prefix.Addr())
			if len(forms) != 1 {
				return Allowance{}, fmt.Errorf("%q: %w", entry, errNAT64Local)
			}
			if prefix.Bits() < 96 {
				return Allowance{}, fmt.Errorf("%q: a range of IPv4 written as IPv6 must be /96 or narrower; write it as IPv4, like 10.0.0.0/8", entry)
			}
			prefix = netip.PrefixFrom(forms[0], prefix.Bits()-96)
		}
		allowance.Prefix = prefix.Masked()
		// Everything, written as a range, is no fence at all, and would
		// open the machine itself while the setting still said block.
		if allowance.Prefix.Bits() == 0 {
			return Allowance{}, fmt.Errorf("%q is every address: to let jobs reach everything, hived config set network-isolation off", entry)
		}
	} else {
		address, err := netip.ParseAddr(place)
		if err != nil || address.Zone() != "" {
			return Allowance{}, fmt.Errorf("%q: %w", entry, errAllowance)
		}
		forms := standsFor(address)
		if len(forms) != 1 {
			return Allowance{}, fmt.Errorf("%q: %w", entry, errNAT64Local)
		}
		allowance.Prefix = netip.PrefixFrom(forms[0], forms[0].BitLen())
	}
	// A port left off after the colon is a mistake, not every port.
	if hasPort {
		number, err := parsePort(port)
		if err != nil {
			return Allowance{}, fmt.Errorf("%q: %w", entry, err)
		}
		allowance.Port = number
	}
	return allowance, nil
}

var errNAT64Local = errors.New("which IPv4 address one in 64:ff9b:1::/48 stands for depends on its network's prefix; write the IPv4 address")

// parsePort reads a port as it is written: digits alone, with no sign and
// no leading zero.
func parsePort(port string) (uint16, error) {
	switch {
	case port == "":
		return 0, errors.New("the port after the colon is missing")
	case strings.Trim(port, "0123456789") != "":
		return 0, errors.New("a port is a number from 1 to 65535, with nothing before it")
	case port[0] == '0':
		return 0, errors.New("a port is a number from 1 to 65535, with no leading zero")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number > 65535 {
		return 0, errors.New("a port is a number from 1 to 65535")
	}
	return uint16(number), nil
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
	// before there was a fence: the owner's network-isolation off.
	Open bool
	// LAN lets the guest reach the private networks around the machine,
	// and its neighbours on a public subnet, but not the machine itself,
	// its tailnet, link-local addresses or a cloud's metadata service: the
	// owner's network-isolation lan-allow.
	LAN bool
	// Allow is somewhere private the owner lets jobs reach all the same.
	Allow []Allowance
	// Resolvers are the name servers the guest was given. They are asked
	// on port 53 wherever they are, a router on the LAN included.
	Resolvers []netip.Addr
	// Host is the machine's own addresses, each with the network it is on
	// (see HostNetworks), kept from the guest whatever network that is:
	// what the machine and its neighbours serve is not the job's.
	Host []netip.Prefix
	// Site is how much of the IPv6 address space around the machine is its
	// site: the /64s beside its own, such as a home's guest Wi-Fi or an
	// office's other floors, which it reaches from inside the site's
	// firewall. Zero means DefaultSite. It widens a network in Host, and
	// never a lone address.
	Site int
}

// DefaultSite is the IPv6 site a machine is taken to be on unless its
// owner says otherwise: a /56, what a provider usually delegates to a
// home. An office's /48 needs the owner to say so.
const DefaultSite = 56

// Sites are the site prefixes an owner can choose.
var Sites = []int{48, 56, 64}

// site is the network a guest is kept from for one of the machine's: the
// machine's IPv6 site, for an address on a link.
func (p *Policy) site(network netip.Prefix) netip.Prefix {
	length := cmp.Or(p.Site, DefaultSite)
	if network.Addr().Is6() && network.Bits() <= 64 && length < network.Bits() {
		return netip.PrefixFrom(network.Addr(), length)
	}
	return network
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
	if protocol != protoTCP && protocol != protoUDP {
		port = 0
	}
	// An address that may stand for several is reached only if each of
	// them may be.
	for _, form := range standsFor(address) {
		if !p.reaches(form, port) {
			return false
		}
	}
	return true
}

func (p *Policy) reaches(address netip.Addr, port uint16) bool {
	if slices.ContainsFunc(p.Allow, func(a Allowance) bool { return a.covers(address, port) }) {
		return true
	}
	if port == 53 && slices.ContainsFunc(p.Resolvers, func(r netip.Addr) bool { return r.Unmap().WithZone("") == address }) {
		return true
	}
	if slices.ContainsFunc(p.Host, func(h netip.Prefix) bool { return p.hostContains(h, address) }) {
		return false
	}
	r, fenced := Fenced(address)
	return !fenced || p.LAN && r.LAN && !tailnet6.Contains(address)
}

// hostContains is whether one of the machine's own networks keeps the
// guest from an address: the whole network, or under LAN the machine's
// own address alone, its neighbours being the LAN.
func (p *Policy) hostContains(network netip.Prefix, address netip.Addr) bool {
	if p.LAN {
		return network.Addr() == address
	}
	return p.site(network).Contains(address)
}

// HostNetworks is every address this machine has, each with the network
// it is on. The machine reaches its neighbours on those networks directly,
// past the router and whatever it keeps out, so a guest is kept from them
// as from the machine itself: a printer with a global IPv6 address beside
// the machine's is still on its LAN.
//
// Nothing the machine is on is left out. The guest's own network is a copy
// of the machine's, made by passt, and fencing it off keeps the guest from
// its neighbours and nothing else: its packets for the internet go to
// addresses outside it. A bridge for other VMs on the machine is as much
// the machine's as its LAN is.
func HostNetworks() ([]netip.Prefix, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var found []netip.Prefix
	for _, each := range interfaces {
		addresses, err := each.Addrs()
		if err != nil {
			return nil, err
		}
		for _, address := range addresses {
			network, ok := address.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(network.IP)
			if !ok {
				continue
			}
			ip = ip.Unmap()
			bits, all := network.Mask.Size()
			if ip.Is4() && all == 128 {
				bits -= 96
			}
			found = append(found, hostNetwork(each.Flags, ip, bits))
		}
	}
	return found, nil
}

// hostNetwork is the network an interface's address is on, as far as the
// fence goes. It is the address alone where the interface has no
// neighbours: a loopback, a tunnel to a single peer, an interface that is
// down. An IPv4 mask wider than a /16 is taken as a /16, the widest LAN
// there is, so that it fences the machine's neighbours and not a share of
// the internet. An IPv6 address is on a /64 whatever its mask says, as one
// from DHCPv6 is; Policy.Site widens that to the machine's site.
func hostNetwork(flags net.Flags, address netip.Addr, bits int) netip.Prefix {
	alone := netip.PrefixFrom(address, address.BitLen())
	if flags&net.FlagUp == 0 || flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
		return alone
	}
	if address.Is4() {
		if bits > 32 {
			return alone
		}
		return netip.PrefixFrom(address, max(bits, 16))
	}
	if bits < 48 || bits > 64 {
		bits = 64
	}
	return netip.PrefixFrom(address, bits)
}
