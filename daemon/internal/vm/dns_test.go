package vm

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestAGuestIsNotGivenAResolverOnlyTheHostCanUse(t *testing.T) {
	public := []string{"1.1.1.1", "8.8.8.8"}
	public6 := []string{"2606:4700:4700::1111", "2001:4860:4860::8888"}
	for _, test := range []struct {
		name string
		// The host's resolv.conf, and what systemd-resolved keeps behind
		// its stub. "-" is a file that is not there.
		resolv, resolved string
		// nil is "the host's resolv.conf will do as it is".
		want []string
	}{
		{"a resolver on the LAN is left alone",
			"nameserver 192.168.0.1\nsearch lan\n", "-", nil},
		{"systemd-resolved's stub gives way to its real servers",
			"nameserver 127.0.0.53\noptions edns0 trust-ad\n", "nameserver 192.168.0.1\nnameserver 9.9.9.9\n", []string{"192.168.0.1", "9.9.9.9"}},
		{"a stub with nothing behind it gives way to public resolvers",
			"nameserver 127.0.0.53\n", "-", public},
		{"Tailscale's resolver alone gives way to public resolvers",
			"nameserver 100.100.100.100\nsearch example.ts.net lan\n", "-", public},
		// passt would read the IPv6 one from resolv.conf for itself.
		{"Tailscale's resolver in both families gives way in both",
			"nameserver 100.100.100.100\nnameserver fd7a:115c:a1e0::53\nsearch example.ts.net lan\n", "-", slices.Concat(public, public6)},
		{"Tailscale's resolver is dropped from beside a LAN one",
			"nameserver 100.100.100.100\nnameserver 192.168.0.1\n", "-", []string{"192.168.0.1"}},
		{"Tailscale's IPv6 resolver beside a LAN one gives way to public IPv6",
			"nameserver 100.100.100.100\nnameserver 192.168.0.1\nnameserver fd7a:115c:a1e0::53\n", "-", slices.Concat([]string{"192.168.0.1"}, public6)},
		{"Tailscale's resolver is dropped from behind the stub too",
			"nameserver 127.0.0.53\n", "nameserver 100.100.100.100\nnameserver 192.168.0.1\n", []string{"192.168.0.1"}},
		{"a stub over nothing but Tailscale gives way to public resolvers",
			"nameserver 127.0.0.53\n", "nameserver 100.100.100.100\n", public},
		{"another address in Tailscale's range is a resolver like any other",
			"nameserver 100.101.102.103\n", "-", nil},
		{"a loopback resolver is dropped from beside a LAN one",
			"nameserver ::1\nnameserver 192.168.0.1\n", "-", []string{"192.168.0.1"}},
		{"an IPv6 resolver's zone is not part of its address",
			"nameserver ::1\nnameserver fe80::1%eth0\n", "-", []string{"fe80::1"}},
		{"no resolv.conf at all gives public resolvers",
			"-", "-", public},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, content string) string {
				path := filepath.Join(dir, name)
				if content != "-" {
					if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return path
			}
			got := guestDNS(write("resolv.conf", test.resolv), write("resolved.conf", test.resolved))
			if !slices.Equal(got, test.want) || (got == nil) != (test.want == nil) {
				t.Errorf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestAGuestMayAskTheResolversItWasGiven(t *testing.T) {
	resolv := filepath.Join(t.TempDir(), "resolv.conf")
	if err := os.WriteFile(resolv, []byte("nameserver 127.0.0.53\nnameserver 192.168.0.1\nnameserver fe80::1%eth0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := []netip.Addr{netip.MustParseAddr("192.168.0.1"), netip.MustParseAddr("fe80::1")}
	if got := guestResolvers(nil, resolv); !slices.Equal(got, want) {
		t.Errorf("from resolv.conf: got %v, want %v", got, want)
	}
	want = []netip.Addr{netip.MustParseAddr("1.1.1.1")}
	if got := guestResolvers([]string{"1.1.1.1"}, resolv); !slices.Equal(got, want) {
		t.Errorf("chosen: got %v, want %v", got, want)
	}
}

func TestPublicDNSLeavesTheHostsResolversOut(t *testing.T) {
	resolv := filepath.Join(t.TempDir(), "resolv.conf")
	if err := os.WriteFile(resolv, []byte("nameserver 192.168.0.1\nnameserver fd00::1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := guestResolvers(publicResolvers(), resolv)
	if len(got) != 4 || !slices.ContainsFunc(got, netip.Addr.Is4) || !slices.ContainsFunc(got, netip.Addr.Is6) {
		t.Fatalf("public resolvers: %v", got)
	}
	for _, resolver := range got {
		if resolver.IsPrivate() {
			t.Errorf("a LAN resolver was given: %v", resolver)
		}
	}
}
