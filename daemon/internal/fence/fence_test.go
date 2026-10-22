package fence

import (
	"net"
	"net/netip"
	"strings"
	"testing"
)

func TestTheGuestIsKeptOffWhatIsNotTheInternet(t *testing.T) {
	for _, test := range []struct {
		address string
		fenced  bool
	}{
		// The owner's LAN, in each of its usual shapes.
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.254", true},
		{"192.168.1.20", true},
		// A tailnet, and a carrier's NAT, share one range.
		{"100.64.0.1", true},
		{"100.100.100.100", true},
		{"100.127.255.255", true},
		// A cloud's metadata service, and the rest of link-local.
		{"169.254.169.254", true},
		{"224.0.0.251", true},
		{"239.255.255.250", true},
		{"255.255.255.255", true},
		// Addresses that mean the host itself.
		{"0.0.0.0", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"::", true},
		{"fd7a:115c:a1e0::1", true},
		{"fc00::1", true},
		{"fe80::1", true},
		{"ff02::fb", true},
		// IPv4 written as IPv6 is the IPv4 address it is.
		{"::ffff:192.168.1.1", true},
		{"64:ff9b::c0a8:0101", true},
		{"64:ff9b::808:808", false},
		// A network's own NAT64 prefix is read at every place the IPv4
		// address may be, and fenced if any of them is private: here
		// 8.8.8.8 at /96 but 10.0.0.1 at /56, or 0.0.0.0 at /64.
		{"64:ff9b:1:808:8:808:808:808", false},
		{"64:ff9b:1:80a:0:1:808:808", true},
		{"64:ff9b:1:abcd::808:808", true},
		{"64:ff9b:1::c0a8:101", true},
		// 8.8.8.8 at /96, and 0.0.0.0 at /64: refused, as the README says.
		{"64:ff9b:1::808:808", true},
		{"64:ff9b:2::c0a8:101", false},
		{"::ffff:8.8.8.8", false},
		// The internet, right up to each range's edges.
		{"8.8.8.8", false},
		{"9.255.255.255", false},
		{"11.0.0.0", false},
		{"172.15.255.255", false},
		{"172.32.0.0", false},
		{"192.167.255.255", false},
		{"192.169.0.0", false},
		{"100.63.255.255", false},
		{"100.128.0.0", false},
		{"169.253.255.255", false},
		{"223.255.255.255", false},
		{"2606:4700:4700::1111", false},
		{"2001:4860:4860::8888", false},
		{"fbff:ffff::1", false},
		{"fec0::1", true},
		{"feff:ffff::1", true},
		// Azure's host service, in public-looking space, and its neighbours.
		{"168.63.129.16", true},
		{"168.63.129.15", false},
		{"168.63.129.17", false},
	} {
		_, fenced := Fenced(netip.MustParseAddr(test.address))
		if fenced != test.fenced {
			t.Errorf("%s: fenced %v, want %v", test.address, fenced, test.fenced)
		}
		policy := &Policy{}
		if reaches := policy.Reaches(protoTCP, netip.MustParseAddr(test.address), 443); reaches == test.fenced {
			t.Errorf("%s: reached %v by default", test.address, reaches)
		}
	}
}

func TestTheOwnerSaysWhatElseAJobMayReach(t *testing.T) {
	allowed, err := ParseAllowances("192.168.1.20:5432, 10.0.0.0/24,[fd00::5]:80,fd12::/16 ")
	if err != nil {
		t.Fatal(err)
	}
	router := netip.MustParseAddr("192.168.1.1")
	host := []netip.Prefix{
		netip.MustParsePrefix("203.0.113.7/32"),
		netip.MustParsePrefix("192.168.1.5/24"),
		netip.MustParsePrefix("198.51.100.5/24"),
		netip.MustParsePrefix("2001:db8:1:1::5/64"),
	}
	policy := &Policy{Allow: allowed, Resolvers: []netip.Addr{router}, Host: host}
	for _, test := range []struct {
		name     string
		protocol uint8
		address  string
		port     uint16
		want     bool
	}{
		{"an allowed address on its port", protoTCP, "192.168.1.20", 5432, true},
		{"an allowed address on its port, by UDP", protoUDP, "192.168.1.20", 5432, true},
		{"an allowed address on another port", protoTCP, "192.168.1.20", 22, false},
		{"an allowed address pinged, which has no port", protoICMP, "192.168.1.20", 0, false},
		{"anywhere in an allowed range", protoTCP, "10.0.0.200", 8080, true},
		{"a ping in an allowed range", protoICMP, "10.0.0.200", 0, true},
		{"just past an allowed range", protoTCP, "10.0.1.1", 8080, false},
		{"an allowed IPv6 address on its port", protoTCP, "fd00::5", 80, true},
		{"an allowed IPv6 address on another", protoTCP, "fd00::5", 443, false},
		{"an allowed IPv6 range", protoUDP, "fd12:3456::9", 9999, true},
		{"the resolver the guest was given", protoUDP, "192.168.1.1", 53, true},
		{"the resolver by TCP", protoTCP, "192.168.1.1", 53, true},
		{"the resolver's admin page", protoTCP, "192.168.1.1", 80, false},
		{"the host's own public address", protoTCP, "203.0.113.7", 22, false},
		{"the host's own LAN address", protoTCP, "192.168.1.5", 22, false},
		{"the host's own address written as IPv6", protoTCP, "::ffff:203.0.113.7", 22, false},
		// The machine's neighbours on a public network are its LAN all the
		// same, and it reaches them past the router.
		{"a neighbour on the host's public IPv4 subnet", protoTCP, "198.51.100.50", 445, false},
		{"a neighbour in the host's global IPv6 /64", protoTCP, "2001:db8:1:1::50", 445, false},
		// The rest of the host's IPv6 site, a /56 unless the owner says.
		{"another /64 of the host's site, such as guest Wi-Fi", protoTCP, "2001:db8:1:2::50", 445, false},
		{"the last /64 of the host's site", protoTCP, "2001:db8:1:ff::50", 445, false},
		{"the next site along, which is the internet", protoTCP, "2001:db8:1:100::50", 443, true},
		{"the next IPv4 subnet along", protoTCP, "198.51.101.50", 443, true},
		{"the internet", protoTCP, "140.82.112.3", 443, true},
	} {
		if got := policy.Reaches(test.protocol, netip.MustParseAddr(test.address), test.port); got != test.want {
			t.Errorf("%s (%s:%d): reached %v, want %v", test.name, test.address, test.port, got, test.want)
		}
	}
	// The owner's own allowance covers the host too: it is how a job is
	// let at something the machine serves on purpose.
	policy.Allow = append(policy.Allow,
		Allowance{Prefix: netip.MustParsePrefix("192.168.1.5/32"), Port: 5432},
		Allowance{Prefix: netip.MustParsePrefix("2001:db8:1:1::50/128"), Port: 631})
	if !policy.Reaches(protoTCP, netip.MustParseAddr("192.168.1.5"), 5432) {
		t.Error("an allowance for the host's own address was not honoured")
	}
	if !policy.Reaches(protoTCP, netip.MustParseAddr("2001:db8:1:1::50"), 631) {
		t.Error("an allowance for a neighbour on the host's /64 was not honoured")
	}
	open := &Policy{Open: true, Host: host}
	for _, address := range []string{"192.168.1.20", "100.100.100.100", "203.0.113.7", "169.254.169.254"} {
		if !open.Reaches(protoTCP, netip.MustParseAddr(address), 22) {
			t.Errorf("network-isolation off did not reach %s", address)
		}
	}
	if (*Policy)(nil).Reaches(protoTCP, netip.MustParseAddr("10.0.0.1"), 80) {
		t.Error("no policy at all reached a private address")
	}
}

func TestAllowancesAreReadAsTypedAndWrittenTheOneWay(t *testing.T) {
	for _, test := range []struct{ typed, stored string }{
		{"", ""},
		{"192.168.1.20:5432,10.0.0.0/24", "192.168.1.20:5432,10.0.0.0/24"},
		{" 192.168.1.20 , 10.0.0.9/24 ", "192.168.1.20,10.0.0.0/24"},
		{"10.0.0.0/24:8080", "10.0.0.0/24:8080"},
		{"[fd00::5]:80,fd00::/64", "[fd00::5]:80,fd00::/64"},
		{"[fd00::/64]:443", "[fd00::/64]:443"},
		{"::ffff:192.168.1.20", "192.168.1.20"},
		{"::ffff:10.0.0.0/104", "10.0.0.0/8"},
		// NAT64 is the IPv4 address it stands for, as a packet's is.
		{"64:ff9b::a00:5", "10.0.0.5"},
		{"[64:ff9b::a00:5]:443", "10.0.0.5:443"},
		{"1.2.3.4:65535", "1.2.3.4:65535"},
	} {
		allowed, err := ParseAllowances(test.typed)
		if err != nil {
			t.Errorf("%q: %v", test.typed, err)
			continue
		}
		if got := FormatAllowances(allowed); got != test.stored {
			t.Errorf("%q stored as %q, want %q", test.typed, got, test.stored)
		}
		again, err := ParseAllowances(test.stored)
		if err != nil || FormatAllowances(again) != test.stored {
			t.Errorf("%q does not read back: %q %v", test.stored, FormatAllowances(again), err)
		}
	}
	for _, bad := range []struct{ typed, says string }{
		{"lan", "allow-network takes"},
		{"192.168.1.300", "allow-network takes"},
		{"192.168.1.20:0", "from 1 to 65535"},
		{"192.168.1.20:65536", "from 1 to 65535"},
		{"192.168.1.20:http", "nothing before it"},
		{"10.0.0.0/33", "allow-network takes"},
		{"fd00::5:80:", "allow-network takes"},
		{"[fd00::5]", "allow-network takes"},
		{"[fd00::5]80", "allow-network takes"},
		{"fe80::1%eth0", "allow-network takes"},
		{"printer.local", "allow-network takes"},
		// A port left off is not every port.
		{"10.0.0.1:", "port after the colon is missing"},
		{"[fd00::5]:", "port after the colon is missing"},
		{"10.0.0.0/24:", "port after the colon is missing"},
		{"10.0.0.1:+80", "nothing before it"},
		{"10.0.0.1:-80", "nothing before it"},
		{"10.0.0.1:053", "no leading zero"},
		{"10.0.0.1: 80", "nothing before it"},
		// IPv4 written as IPv6 has 96 bits before the IPv4 part.
		{"::ffff:10.0.0.0/8", "write it as IPv4"},
		{"64:ff9b::/64", "write it as IPv4"},
		// Which IPv4 address a network's own NAT64 address means is its
		// network's business.
		{"64:ff9b:1::a00:5", "write the IPv4 address"},
		{"64:ff9b:1::a00:0/120", "write the IPv4 address"},
	} {
		_, err := ParseAllowances(bad.typed)
		if err == nil || !strings.Contains(err.Error(), bad.says) {
			t.Errorf("%q: got %v, want an error saying %q", bad.typed, err, bad.says)
		}
	}
}

func TestTheHostsNetworksAreItsNeighboursUnlessItHasNone(t *testing.T) {
	up := net.FlagUp | net.FlagBroadcast | net.FlagMulticast
	for _, test := range []struct {
		name    string
		flags   net.Flags
		address string
		bits    int
		want    string
	}{
		{"a LAN", up, "192.168.1.5", 24, "192.168.1.5/24"},
		{"a public IPv4 LAN", up, "198.51.100.5", 24, "198.51.100.5/24"},
		{"a SLAAC address", up, "2001:db8:1:1::5", 64, "2001:db8:1:1::5/64"},
		{"a DHCPv6 address, which says /128 and sits on a /64", up, "2001:db8:1:1::5", 128, "2001:db8:1:1::5/64"},
		{"an IPv6 /56 on the link", up, "2001:db8:1:100::5", 56, "2001:db8:1:100::5/56"},
		{"an IPv6 mask wider than any link's", up, "2001:db8:1:1::5", 32, "2001:db8:1:1::5/64"},
		{"an IPv4 mask wider than any LAN's, which is taken as a /16", up, "203.0.113.7", 8, "203.0.113.7/16"},
		{"a /16 public LAN", up, "203.0.113.7", 16, "203.0.113.7/16"},
		{"a tunnel to one peer", net.FlagUp | net.FlagPointToPoint, "100.101.102.103", 10, "100.101.102.103/32"},
		{"a loopback", net.FlagUp | net.FlagLoopback, "127.0.0.1", 8, "127.0.0.1/32"},
		{"an interface that is down", net.FlagBroadcast, "198.51.100.5", 24, "198.51.100.5/32"},
	} {
		got := hostNetwork(test.flags, netip.MustParseAddr(test.address), test.bits)
		if got.String() != test.want {
			t.Errorf("%s: %s, want %s", test.name, got, test.want)
		}
	}
	found, err := HostNetworks()
	if err != nil {
		t.Fatal(err)
	}
	for _, network := range found {
		if !network.IsValid() {
			t.Errorf("an invalid network: %v", network)
		}
	}
}

func TestTheOwnerSaysHowBigTheHostsIPv6SiteIs(t *testing.T) {
	host := []netip.Prefix{
		netip.MustParsePrefix("2001:db8:1:1::5/64"),
		// A tunnel's address is alone, and stays alone.
		netip.MustParsePrefix("2001:db8:9:9::9/128"),
	}
	for _, test := range []struct {
		site    int
		address string
		want    bool
	}{
		{0, "2001:db8:1:2::50", false},
		{0, "2001:db8:1:100::50", true},
		{56, "2001:db8:1:2::50", false},
		{64, "2001:db8:1:1::50", false},
		{64, "2001:db8:1:2::50", true},
		{48, "2001:db8:1:100::50", false},
		{48, "2001:db8:1:ffff::50", false},
		{48, "2001:db8:2::50", true},
		{48, "2001:db8:9:9::8", true},
		{0, "2001:db8:9:9::9", false},
	} {
		policy := &Policy{Host: host, Site: test.site}
		if got := policy.Reaches(protoTCP, netip.MustParseAddr(test.address), 445); got != test.want {
			t.Errorf("site /%d, %s: reached %v, want %v", test.site, test.address, got, test.want)
		}
	}
}

func TestLANAllowKeepsTheGuestOffTheMachineAndItsTailnetAlone(t *testing.T) {
	policy := &Policy{LAN: true, Host: []netip.Prefix{netip.MustParsePrefix("192.168.1.10/24"), netip.MustParsePrefix("2001:db8:1:1::10/64"), netip.MustParsePrefix("100.101.102.103/32")}}
	for address, want := range map[string]bool{
		"192.168.1.30":        true, // a registry on the LAN
		"10.20.0.5":           true, // another private network
		"2001:db8:1:1::5":     true, // a neighbour on the machine's /64
		"2001:db8:1:2::5":     true, // the rest of its site
		"fd00::5":             true,
		"140.82.112.3":        true,
		"192.168.1.10":        false, // the machine itself
		"2001:db8:1:1::10":    false,
		"100.101.102.103":     false, // its tailnet address
		"100.90.1.1":          false, // the tailnet
		"fd7a:115c:a1e0::9":   false,
		"169.254.169.254":     false, // a cloud's metadata service
		"127.0.0.1":           false,
		"::ffff:192.168.1.10": false,
	} {
		if got := policy.Reaches(protoTCP, netip.MustParseAddr(address), 443); got != want {
			t.Errorf("lan-allow, %s: reaches %v, want %v", address, got, want)
		}
	}
}

func TestEverythingIsNoAllowance(t *testing.T) {
	for _, everything := range []string{"0.0.0.0/0", "::/0", "[::/0]:443", "0.0.0.0/0:53", "::ffff:0.0.0.0/96"} {
		if _, err := ParseAllowances(everything); err == nil {
			t.Errorf("%s was taken", everything)
		}
	}
	allowed, err := ParseAllowances("10.0.0.0/8,0.0.0.0/1,2001:db8::/32,2000::/3,192.168.1.5")
	if err != nil {
		t.Fatal(err)
	}
	var wide []string
	for _, allowance := range allowed {
		if allowance.Wide() {
			wide = append(wide, allowance.String())
		}
	}
	if strings.Join(wide, ",") != "0.0.0.0/1,2000::/3" {
		t.Errorf("wide: %v", wide)
	}
}
