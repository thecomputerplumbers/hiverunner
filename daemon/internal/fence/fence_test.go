package fence

import (
	"net/netip"
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
		{"fec0::1", false},
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
	router, host := netip.MustParseAddr("192.168.1.1"), netip.MustParseAddr("203.0.113.7")
	policy := &Policy{Allow: allowed, Resolvers: []netip.Addr{router}, Host: []netip.Addr{host, netip.MustParseAddr("192.168.1.5")}}
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
		{"the internet", protoTCP, "140.82.112.3", 443, true},
	} {
		if got := policy.Reaches(test.protocol, netip.MustParseAddr(test.address), test.port); got != test.want {
			t.Errorf("%s (%s:%d): reached %v, want %v", test.name, test.address, test.port, got, test.want)
		}
	}
	// The owner's own allowance covers the host too: it is how a job is
	// let at something the machine serves on purpose.
	policy.Allow = append(policy.Allow, Allowance{Prefix: netip.MustParsePrefix("192.168.1.5/32"), Port: 5432})
	if !policy.Reaches(protoTCP, netip.MustParseAddr("192.168.1.5"), 5432) {
		t.Error("an allowance for the host's own address was not honoured")
	}
	open := &Policy{Open: true, Host: []netip.Addr{host}}
	for _, address := range []string{"192.168.1.20", "100.100.100.100", "203.0.113.7", "169.254.169.254"} {
		if !open.Reaches(protoTCP, netip.MustParseAddr(address), 22) {
			t.Errorf("private-network allow did not reach %s", address)
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
	for _, bad := range []string{"lan", "192.168.1.300", "192.168.1.20:0", "192.168.1.20:65536", "192.168.1.20:http", "10.0.0.0/33", "fd00::5:80:", "[fd00::5]", "[fd00::5]80", "fe80::1%eth0", "printer.local"} {
		if _, err := ParseAllowances(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
