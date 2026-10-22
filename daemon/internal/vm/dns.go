package vm

import (
	"bufio"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
)

// guestDNS chooses the guest's resolvers from the host's. A resolver only
// the host can use is no use to a guest: one on the host's loopback, which
// the guest cannot reach, and Tailscale's, which answers while the guest
// borrows the host's place on the tailnet and stops the moment a job starts
// a Tailscale of its own, whose interface then owns that address. Public
// names stop resolving in the middle of the job.
//
// If some of the host's resolvers are usable the guest is given those and
// not the rest, so it never waits on a dead one. If none are, it is given
// the servers systemd-resolved keeps behind its stub, and failing those two
// public ones: a guest that cannot resolve names cannot run a job at all.
//
// passt takes the two families separately. Told of an IPv4 resolver and no
// IPv6 one, it still reads resolv.conf for IPv6 and hands the guest what it
// finds there, Tailscale's included; a loopback one it leaves out by itself.
// So a family in which the host has Tailscale's resolver and nothing the
// guest can use is given public ones, which is the only way to keep
// Tailscale's out.
func guestDNS(resolvConf, resolvedConf string) []string {
	servers := nameservers(resolvConf)
	usable := guestUsable(servers)
	if len(usable) > 0 && len(usable) == len(servers) {
		return nil
	}
	if len(usable) == 0 {
		usable = guestUsable(nameservers(resolvedConf))
	}
	if len(usable) == 0 {
		usable = slices.Clone(publicDNS4)
	}
	tailscale4 := func(server string) bool { return isIPv4(server) && isTailscaleDNS(server) }
	tailscale6 := func(server string) bool { return isIPv6(server) && isTailscaleDNS(server) }
	if !slices.ContainsFunc(usable, isIPv4) && slices.ContainsFunc(servers, tailscale4) {
		usable = append(usable, publicDNS4...)
	}
	if !slices.ContainsFunc(usable, isIPv6) && slices.ContainsFunc(servers, tailscale6) {
		usable = append(usable, publicDNS6...)
	}
	return usable
}

// Cloudflare's and Google's, in each family.
var (
	publicDNS4 = []string{"1.1.1.1", "8.8.8.8"}
	publicDNS6 = []string{"2606:4700:4700::1111", "2001:4860:4860::8888"}
)

func isIPv4(server string) bool {
	ip := net.ParseIP(server)
	return ip != nil && ip.To4() != nil
}

func isIPv6(server string) bool {
	ip := net.ParseIP(server)
	return ip != nil && ip.To4() == nil
}

// Tailscale's resolver, at the one address it has in each family. The rest
// of 100.64.0.0/10 is ordinary: a tailnet peer that serves DNS is a
// resolver like any other for as long as the guest can reach it.
var tailscaleDNS = []net.IP{net.ParseIP("100.100.100.100"), net.ParseIP("fd7a:115c:a1e0::53")}

func isTailscaleDNS(server string) bool {
	ip := net.ParseIP(server)
	return ip != nil && slices.ContainsFunc(tailscaleDNS, ip.Equal)
}

// guestUsable is the resolvers among these that a guest can rely on.
func guestUsable(servers []string) []string {
	var usable []string
	for _, server := range servers {
		ip := net.ParseIP(server)
		if ip == nil || ip.IsLoopback() || isTailscaleDNS(server) {
			continue
		}
		usable = append(usable, server)
	}
	return usable
}

// guestResolvers is the resolvers a guest is given: those guestDNS chose,
// or, where it chose none, the usable ones in the host's resolv.conf, which
// is what passt then hands the guest. A guest may ask these on port 53
// wherever they are, which is what keeps names resolving for a guest kept
// off the LAN its router is on.
func guestResolvers(chosen []string, resolvConf string) []netip.Addr {
	if chosen == nil {
		chosen = guestUsable(nameservers(resolvConf))
	}
	var resolvers []netip.Addr
	for _, server := range chosen {
		if address, err := netip.ParseAddr(server); err == nil {
			resolvers = append(resolvers, address.WithZone(""))
		}
	}
	return resolvers
}

func nameservers(file string) []string {
	handle, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer handle.Close()
	var servers []string
	scanner := bufio.NewScanner(handle)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "nameserver" {
			// An IPv6 server may carry a zone, which is not part of it.
			servers = append(servers, strings.SplitN(fields[1], "%", 2)[0])
		}
	}
	return servers
}
