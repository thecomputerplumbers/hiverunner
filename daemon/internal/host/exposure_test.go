package host

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// fake is a machine that answers the commands in said, and fails the rest.
type fake struct {
	said  map[string]string
	files map[string]string
	ran   []string
}

func (f *fake) system(sudoer bool, addresses ...address) system {
	return system{
		run: func(name string, args ...string) (string, error) {
			line := strings.Join(append([]string{name}, args...), " ")
			f.ran = append(f.ran, line)
			if out, ok := f.said[line]; ok {
				return out, nil
			}
			return "", errors.New("exit status 1")
		},
		read:      func(pattern string) string { return f.files[pattern] },
		addresses: addresses,
		user:      "ada",
		sudoer:    sudoer,
		ipv6:      true,
	}
}

func (f *fake) sudid() bool {
	for _, line := range f.ran {
		if strings.HasPrefix(line, "sudo ") {
			return true
		}
	}
	return false
}

func only(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	var found []Check
	for _, check := range checks {
		if check.Name == name {
			found = append(found, check)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d %s checks in %+v", len(found), name, checks)
	}
	return found[0]
}

// listen is a /proc/net/tcp with something listening on each port, given in
// hex, on every address.
func listen(ports ...string) string {
	table := "  sl  local_address rem_address   st\n"
	for _, port := range ports {
		table += "   0: 00000000:" + port + " 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1\n"
	}
	return table
}

func TestSSHTakesPasswordsEitherWay(t *testing.T) {
	for settings, want := range map[string]bool{
		"": false,
		"port 22\npasswordauthentication yes\nkbdinteractiveauthentication no\n": true,
		"port 22\npasswordauthentication no\nkbdinteractiveauthentication yes\n": true,
		"port 22\npasswordauthentication no\nkbdinteractiveauthentication no\n":  false,
		// What an older sshd calls keyboard-interactive.
		"passwordauthentication no\nchallengeresponseauthentication yes\n": true,
		"permitemptypasswords yes\npasswordauthentication no\n":            false,
		// Methods that all have to succeed: a password alone is not enough.
		"passwordauthentication yes\nauthenticationmethods publickey\n":                                false,
		"passwordauthentication yes\nauthenticationmethods publickey,password\n":                       false,
		"passwordauthentication yes\nauthenticationmethods any\n":                                      true,
		"passwordauthentication yes\nauthenticationmethods publickey password\n":                       true,
		"kbdinteractiveauthentication yes\nauthenticationmethods keyboard-interactive:pam\n":           true,
		"passwordauthentication no\nkbdinteractiveauthentication no\nauthenticationmethods password\n": false,
	} {
		if got := sshPasswords(settings); got != want {
			t.Errorf("%q: got %v, want %v", settings, got, want)
		}
	}
}

func TestSSHPortsAreWhatSSHDAndLaunchdSay(t *testing.T) {
	if got := fmt.Sprint(sshPorts("port 2222\nport 22\nlistenaddress [::]:2222\nlistenaddress 0.0.0.0:2200\npasswordauthentication no\n")); got != "[2222 22 2222 2200]" {
		t.Errorf("sshd: got %v", got)
	}
	job := "system/com.openssh.sshd = {\n\tsockets = {\n\t\t\"Listeners\" = {\n\t\t\ttype = stream\n\t\t\tservice name = ssh\n\t\t}\n\t\t\"Other\" = {\n\t\t\tservice name = 2222\n\t\t}\n\t}\n}\n"
	if got := fmt.Sprint(launchdPorts(job)); got != "[22 2222]" {
		t.Errorf("launchd: got %v", got)
	}
	if sshMatches("#Match User anoncvs\nPasswordAuthentication no\n") || !sshMatches("PasswordAuthentication no\n  match Address 10.0.0.0/8\n") {
		t.Error("a Match block was misread")
	}
}

func TestTheMacFirewall(t *testing.T) {
	for said, want := range map[string][2]bool{
		"Firewall is enabled. (State = 1)\n":  {true, true},
		"Firewall is disabled. (State = 0)\n": {false, true},
		// Blocking every incoming connection is on too.
		"Firewall is blocking all non-essential incoming connections. (State = 2)\n": {true, true},
		"": {false, false},
	} {
		if on, known := firewallState(said); [2]bool{on, known} != want {
			t.Errorf("%q: got on %v known %v, want %v", said, on, known, want)
		}
	}
}

func TestAFirewallDropsWhatItDoesNotAccept(t *testing.T) {
	chain := func(family, body string) string {
		return "table " + family + " filter {\n\tchain INPUT {\n" + body + "\t}\n}\n"
	}
	drop := "\t\ttype filter hook input priority filter; policy drop;\n"
	accept := "\t\ttype filter hook input priority filter; policy accept;\n"
	for name, c := range map[string]struct {
		ruleset string
		v4, v6  bool
	}{
		"nothing":                 {"", false, false},
		"ufw, both families":      {chain("ip", drop+"\t\tcounter packets 0 bytes 0 jump ufw-before-input\n") + chain("ip6", drop), true, true},
		"IPv4 alone":              {chain("ip", drop), true, false},
		"inet":                    {chain("inet", drop), true, true},
		"firewalld's last reject": {"table inet firewalld {\n\tchain filter_INPUT {\n\t\ttype filter hook input priority filter + 10; policy accept;\n\t\tct state { established, related } accept\n\t\tjump filter_INPUT_ZONES\n\t\tct state { invalid } drop\n\t\treject with icmpx admin-prohibited\n\t}\n}\n", true, true},
		"a last counted drop":     {chain("inet", accept+"\t\tiifname \"lo\" accept\n\t\tcounter packets 3 bytes 180 drop\n"), true, true},
		"Docker's":                {"table ip filter {\n\tchain DOCKER {\n\t}\n\tchain INPUT {\n" + accept + "\t}\n\tchain FORWARD {\n\t\ttype filter hook forward priority filter; policy drop;\n\t\tcounter packets 0 bytes 0 jump DOCKER\n\t}\n}\n", false, false},
		"Tailscale's":             {chain("ip", accept+"\t\tcounter packets 0 bytes 0 jump ts-input\n") + "table ip ts {\n\tchain ts-input {\n\t\tiifname != \"tailscale0\" ip saddr 100.64.0.0/10 counter packets 0 bytes 0 drop\n\t}\n}\n", false, false},
		"Debian's empty one":      {chain("inet", accept), false, false},
		// What blocks some addresses or ports, and lets the rest in.
		"fail2ban's":     {"table inet f2b-table {\n\tset addr-set-sshd {\n\t\ttype ipv4_addr\n\t}\n\tchain f2b-chain {\n\t\ttype filter hook input priority filter - 1; policy accept;\n\t\ttcp dport 22 ip saddr @addr-set-sshd reject\n\t}\n}\n", false, false},
		"CrowdSec's":     {chain("ip", accept+"\t\tip saddr @crowdsec-blacklists counter packets 9 bytes 540 drop\n"), false, false},
		"one port":       {chain("inet", accept+"\t\ttcp dport 25 reject\n"), false, false},
		"invalid states": {chain("inet", accept+"\t\tct state invalid drop\n"), false, false},
		// A map over several lines does not end the chain.
		"a vmap, then a drop": {chain("inet", accept+"\t\ttcp dport vmap {\n\t\t\t22 : accept,\n\t\t\t80 : accept\n\t\t}\n\t\tdrop\n"), true, true},
		// Logged and counted on the way is still everything.
		"a logged last drop": {chain("inet", accept+"\t\tct state established,related accept\n\t\tlog prefix \"DROP \" counter packets 0 bytes 0 drop\n"), true, true},
		"a logged port drop": {chain("inet", accept+"\t\ttcp dport 23 log prefix \"DROP \" drop\n"), false, false},
	} {
		if v4, v6 := nftFilters(c.ruleset); v4 != c.v4 || v6 != c.v6 {
			t.Errorf("%s: got %v, %v; want %v, %v", name, v4, v6, c.v4, c.v6)
		}
	}

	for rules, want := range map[string]bool{
		"-P INPUT DROP\n-A INPUT -i lo -j ACCEPT\n":                                                           true,
		"-P INPUT ACCEPT\n-A INPUT -i lo -j ACCEPT\n-A INPUT -j REJECT --reject-with icmp-port-unreachable\n": true,
		"-P INPUT ACCEPT\n-A INPUT -p tcp --dport 25 -j DROP\n":                                               false,
		"-P INPUT ACCEPT\n": false,
	} {
		if got := legacyFilters(rules); got != want {
			t.Errorf("legacy %q: got %v, want %v", rules, got, want)
		}
	}
	for name, c := range map[string]struct {
		conf, defaults string
		v4, v6         bool
	}{
		"on":        {"ENABLED=yes\n", "IPV6=yes\nDEFAULT_INPUT_POLICY=\"DROP\"\n", true, true},
		"accepting": {"ENABLED=yes\n", "IPV6=yes\nDEFAULT_INPUT_POLICY=\"ACCEPT\"\n", false, false},
		"off":       {"ENABLED=no\n", "DEFAULT_INPUT_POLICY=\"DROP\"\n", false, false},
		"IPv4 only": {"ENABLED=yes\n", "IPV6=no\nDEFAULT_INPUT_POLICY=\"REJECT\"\n", true, false},
	} {
		if v4, v6 := ufwDrops(c.conf, c.defaults); v4 != c.v4 || v6 != c.v6 {
			t.Errorf("ufw %s: got %v, %v", name, v4, v6)
		}
	}
}

func TestLinuxFirewallReadsWhatItMay(t *testing.T) {
	// ufw is said without sudo.
	f := &fake{files: map[string]string{"/etc/ufw/ufw.conf": "ENABLED=yes\n", "/etc/default/ufw": `DEFAULT_INPUT_POLICY="DROP"`}}
	if got := f.system(true).linuxFirewall(); len(got) != 1 || !got[0].OK || f.sudid() {
		t.Errorf("ufw: got %+v, ran %q", got, f.ran)
	}
	// ufw told IPV6=no leaves IPv6 open, where there is IPv6.
	f.files["/etc/default/ufw"] = "IPV6=no\n" + `DEFAULT_INPUT_POLICY="DROP"`
	if got := f.system(false).linuxFirewall(); len(got) != 1 || got[0].OK || got[0].Found != "IPv4 is filtered, and IPv6 is not" {
		t.Errorf("ufw IPV6=no: got %+v", got)
	}
	without := f.system(false)
	without.ipv6 = false
	if got := without.linuxFirewall(); len(got) != 1 || !got[0].OK {
		t.Errorf("ufw IPV6=no, no IPv6: got %+v", got)
	}
	// Without --sudo, sudo is not tried, and the rules unread are said to
	// be: unsaid, they would read as nothing to worry about.
	f = &fake{said: map[string]string{"sudo -n nft -t list ruleset": "table inet filter {\n\tchain input {\n\t\ttype filter hook input priority filter; policy drop;\n\t}\n}\n"}}
	if got := f.system(false).linuxFirewall(); len(got) != 1 || got[0].OK || !got[0].Note || got[0].Found != "could not read the rules (needs sudo)" || !strings.Contains(got[0].Fix, "hived doctor --sudo") || f.sudid() {
		t.Errorf("no sudo: got %+v, ran %q", got, f.ran)
	}
	// sudo that refuses all the same.
	if got := (&fake{}).system(true).linuxFirewall(); len(got) != 1 || got[0].OK || !got[0].Note || !strings.Contains(got[0].Found, "even with sudo") {
		t.Errorf("sudo refused: got %+v", got)
	}
	// fail2ban alone is no firewall.
	f = &fake{said: map[string]string{"sudo -n nft -t list ruleset": "table inet f2b-table {\n\tchain f2b-chain {\n\t\ttype filter hook input priority filter - 1; policy accept;\n\t\ttcp dport 22 ip saddr @addr-set-sshd reject\n\t}\n}\n"}}
	if got := f.system(true).linuxFirewall(); len(got) != 1 || got[0].OK || !got[0].Note {
		t.Errorf("fail2ban: got %+v", got)
	}
	// A legacy IPv4 policy that drops, loaded, and nothing for IPv6.
	f = &fake{
		said:  map[string]string{"sudo -n nft -t list ruleset": "", "sudo -n iptables-legacy -S INPUT": "-P INPUT DROP\n"},
		files: map[string]string{"/proc/modules": "iptable_filter 12288 1 - Live 0x0\nip_tables 32768 1 iptable_filter, Live 0x0\n"},
	}
	got := f.system(true).linuxFirewall()
	if len(got) != 1 || got[0].OK || got[0].Found != "IPv4 is filtered, and IPv6 is not" {
		t.Errorf("legacy IPv4: got %+v", got)
	}
	for _, line := range f.ran {
		if strings.Contains(line, "ip6tables-legacy") {
			t.Errorf("listed a legacy table that was not loaded: %q", line)
		}
	}
	// A machine with no IPv6 is not told to filter it.
	without = f.system(true)
	without.ipv6 = false
	if got := without.linuxFirewall(); len(got) != 1 || !got[0].OK {
		t.Errorf("legacy IPv4, no IPv6: got %+v", got)
	}
}

func TestIPv6IsMoreThanALink(t *testing.T) {
	at := func(iface, ip string) address { return address{iface: iface, ip: net.ParseIP(ip)} }
	links := []address{at("lo", "::1"), at("eth0", "fe80::1"), at("eth0", "10.0.0.2")}
	if hasIPv6(links, "1\n") || hasIPv6(links, "") {
		t.Error("link-local IPv6 with IPv6 disabled, or no Linux, read as IPv6")
	}
	if !hasIPv6(links, "0\n") || !hasIPv6(append(links, at("eth0", "2001:db8::2")), "") {
		t.Error("IPv6 not found")
	}
}

func TestACommandIsGivenUpOnWithWhatItStarted(t *testing.T) {
	defer func(limit time.Duration) { commandLimit = limit }(commandLimit)
	commandLimit = 200 * time.Millisecond
	// sleep holds the output open after sh is killed, as what sudo runs
	// would.
	start := time.Now()
	if _, err := command("sh", "-c", "sleep 9; true"); err == nil || time.Since(start) > 3*time.Second {
		t.Errorf("took %v, err %v", time.Since(start), err)
	}
}

func TestSSHIsFoundWhereverItListens(t *testing.T) {
	// On 2222, said by sshd -G with no root, and taking passwords.
	f := &fake{
		said:  map[string]string{sshd + " -G": "port 2222\npasswordauthentication yes\n"},
		files: map[string]string{"/proc/net/tcp": listen("08AE")},
	}
	if ssh := only(t, f.system(false).linux(Fence{}), "ssh"); ssh.OK || !ssh.Note || ssh.Found != "sshd is on and takes passwords" {
		t.Errorf("port 2222: got %+v", ssh)
	}
	// Nothing on its port.
	f.files["/proc/net/tcp"] = listen("0016")
	if ssh := only(t, f.system(false).linux(Fence{}), "ssh"); !ssh.OK || ssh.Found != "sshd is not listening" {
		t.Errorf("not listening: got %+v", ssh)
	}
	// Its settings unread, all that can be said is of port 22.
	if ssh := only(t, (&fake{}).system(false).linux(Fence{}), "ssh"); !ssh.OK || ssh.Found != "nothing listens on port 22" {
		t.Errorf("unread: got %+v", ssh)
	}

	// A Mac's Remote Login is on launchd's port, and with neither sshd -G
	// nor sudo it is still said.
	f = &fake{said: map[string]string{
		"/usr/sbin/netstat -an -p tcp":                 "tcp4  0  0  *.2222  *.*  LISTEN\n",
		"/bin/launchctl print system/com.openssh.sshd": "\t\t\tservice name = 2222\n",
	}, files: map[string]string{"/etc/ssh/sshd_config": "Include /etc/ssh/sshd_config.d/*\n"}}
	if ssh := only(t, f.system(true).mac(Fence{}), "ssh"); ssh.OK || !ssh.Note || !strings.Contains(ssh.Found, "could not read") || !strings.Contains(ssh.Fix, "Remote Login off") {
		t.Errorf("unread: got %+v", ssh)
	}
	f.said["/usr/sbin/netstat -an -p tcp"] = "tcp4  0  0  *.22  *.*  LISTEN\n"
	if ssh := only(t, f.system(true).mac(Fence{}), "ssh"); !ssh.OK || ssh.Found != "Remote Login is off" {
		t.Errorf("off: got %+v", ssh)
	}
	// sudo, where -G is not there.
	f.said["/usr/sbin/netstat -an -p tcp"] = "tcp4  0  0  *.2222  *.*  LISTEN\n"
	f.said["sudo -n "+sshd+" -T"] = "passwordauthentication no\nkbdinteractiveauthentication no\n"
	if ssh := only(t, f.system(true).mac(Fence{}), "ssh"); !ssh.OK || ssh.Found != "Remote Login takes keys only" {
		t.Errorf("keys only: got %+v", ssh)
	}
}

func TestAMatchBlockIsAskedAbout(t *testing.T) {
	keys := "port 22\npasswordauthentication no\nkbdinteractiveauthentication no\n"
	config := "PasswordAuthentication no\nMatch Address 10.0.0.0/8,192.168.0.0/16\n\tPasswordAuthentication yes\n"
	f := &fake{
		said: map[string]string{
			"sudo -n " + sshd + " -T": keys,
			"sudo -n " + sshd + " -T -C user=ada,host=probe,addr=192.168.1.10": "port 22\npasswordauthentication yes\n",
		},
		files: map[string]string{"/proc/net/tcp": listen("0016"), "/etc/ssh/sshd_config": config},
	}
	if ssh := only(t, f.system(true).linux(Fence{}), "ssh"); ssh.OK || !strings.Contains(ssh.Found, "from 192.168.1.10") {
		t.Errorf("LAN passwords: got %+v", ssh)
	}
	// Without root the block cannot be asked about, and the OK says so.
	f.said[sshd+" -G"] = keys
	if ssh := only(t, f.system(false).linux(Fence{}), "ssh"); !ssh.OK || ssh.Found != "sshd takes keys only, outside its Match blocks" {
		t.Errorf("unasked: got %+v", ssh)
	}
	// Root asked, and nothing found: a block it did not ask as is still
	// said to be there.
	f = &fake{
		said:  map[string]string{"sudo -n " + sshd + " -T": keys},
		files: map[string]string{"/proc/net/tcp": listen("0016"), "/etc/ssh/sshd_config": "Match User deploy\n\tPasswordAuthentication yes\n"},
	}
	if ssh := only(t, f.system(true).linux(Fence{}), "ssh"); !ssh.OK || ssh.Found != "sshd takes keys only, outside its Match blocks" {
		t.Errorf("Match User: got %+v", ssh)
	}
	f.said[sshd+" -G"] = keys
	// A Match block in an included file without .conf, as a Mac's Include
	// allows.
	f.files["/etc/ssh/sshd_config"] = "Include /etc/ssh/sshd_config.d/*\nInclude crypto.conf\n"
	f.files["/etc/ssh/sshd_config.d/*"] = "Match Address 192.168.0.0/16\n\tPasswordAuthentication yes\n"
	if ssh := only(t, f.system(false).linux(Fence{}), "ssh"); ssh.Found != "sshd takes keys only, outside its Match blocks" {
		t.Errorf("included Match: got %+v", ssh)
	}
	read := []string{}
	sshConfig(func(pattern string) string { read = append(read, pattern); return f.files[pattern] })
	if fmt.Sprint(read) != "[/etc/ssh/sshd_config /etc/ssh/sshd_config.d/* /etc/ssh/crypto.conf]" {
		t.Errorf("read %v", read)
	}
	// Fedora's configuration is root's to read: it may have a block, so
	// root asks, and keys only is said of the rest.
	delete(f.files, "/etc/ssh/sshd_config")
	f.ran = nil
	if ssh := only(t, f.system(true).linux(Fence{}), "ssh"); ssh.Found != "sshd takes keys only, outside its Match blocks" || !strings.Contains(strings.Join(f.ran, "\n"), " -T -C ") {
		t.Errorf("unreadable config: got %+v, ran %q", ssh, f.ran)
	}
	// No Match block, nothing to ask.
	f.files["/etc/ssh/sshd_config"] = "PasswordAuthentication no\n"
	if ssh := only(t, f.system(false).linux(Fence{}), "ssh"); ssh.Found != "sshd takes keys only" {
		t.Errorf("plain: got %+v", ssh)
	}
}

func TestOnlyWhatListensForOtherMachinesIsShared(t *testing.T) {
	netstat := `Active Internet connections (including servers)
Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)
tcp4       0      0  10.0.0.5.51785         17.253.23.201.443      ESTABLISHED
tcp4       0      0  127.0.0.1.5037         *.*                    LISTEN
tcp6       0      0  ::1.631                *.*                    LISTEN
tcp6       0      0  fe80::1%lo0.8021       *.*                    LISTEN
tcp4       0      0  *.22                   *.*                    LISTEN
tcp6       0      0  *.7000                 *.*                    LISTEN
tcp4       0      0  10.0.0.5.445           *.*                    LISTEN
`
	if got := netstatListening(netstat); len(got) != 3 || !got[22] || !got[7000] || !got[445] {
		t.Errorf("netstat: got %v, want 22, 445 and 7000", got)
	}

	tcp := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0000000000000000 100 0 0 10 0
   1: 0100007F:0277 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2 1 0000000000000000 100 0 0 10 0
   2: 0500000A:01BD 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 3 1 0000000000000000 100 0 0 10 0
   3: 0500000A:D2A4 0100A8C0:01BB 01 00000000:00000000 00:00000000 00000000  1000        0 4 1 0000000000000000 100 0 0 10 0
`
	if got := procListening(tcp); len(got) != 2 || !got[22] || !got[445] {
		t.Errorf("tcp: got %v, want 22 and 445", got)
	}
	tcp6 := `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:0019 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 5 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000000000000:170C 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 6 1 0000000000000000 100 0 0 10 0
`
	if got := procListening(tcp6); len(got) != 1 || !got[5900] {
		t.Errorf("tcp6: got %v, want 5900", got)
	}

	services := []service{{"Screen Sharing", 5900}, {"File Sharing", 445}}
	if got := sharing(map[int]bool{22: true}, services, "fix"); len(got) != 1 || !got[0].OK {
		t.Errorf("only ssh listening: got %+v", got)
	}
	got := sharing(map[int]bool{445: true, 5900: true}, services, "fix")
	if len(got) != 1 || got[0].OK || !got[0].Note || got[0].Found != "Screen Sharing, File Sharing listening" || got[0].Fix != "fix" {
		t.Errorf("both listening: got %+v", got)
	}
	// A Mac's Remote Management, Remote Apple Events and Printer Sharing.
	f := &fake{said: map[string]string{"/usr/sbin/netstat -an -p tcp": "tcp4  0  0  *.3283  *.*  LISTEN\ntcp4  0  0  *.3031  *.*  LISTEN\ntcp6  0  0  *.631  *.*  LISTEN\n"}}
	if shared := only(t, f.system(false).mac(Fence{}), "sharing"); shared.Found != "Remote Management, Printer Sharing, Remote Apple Events listening" {
		t.Errorf("Mac sharing: got %+v", shared)
	}
}

func TestANetworkWithOtherMachinesOnIsSaid(t *testing.T) {
	// An address given with its prefix is on a subnet of that length.
	at := func(iface, ip string) address {
		if addr, subnet, err := net.ParseCIDR(ip); err == nil {
			bits, _ := subnet.Mask.Size()
			return address{iface, addr, bits}
		}
		return address{iface: iface, ip: net.ParseIP(ip)}
	}

	got := network([]address{at("lo0", "127.0.0.1"), at("utun4", "100.104.79.38"), at("utun4", "fd7a:115c:a1e0::1"), at("en0", "192.168.1.20"), at("en0", "fe80::1")}, Fence{})
	if len(got) != 1 || got[0].OK || !got[0].Note || !strings.Contains(got[0].Found, "a CGNAT or tailnet address (100.104.79.38 on utun4, fd7a:115c:a1e0::1 on utun4) and a private network (192.168.1.20 on en0)") ||
		!strings.Contains(got[0].Fix, "tailnet's policy") || !strings.Contains(got[0].Fix, "network of its own") {
		t.Errorf("tailnet and LAN: got %+v", got)
	}
	// A server with a public address on its way out and a private one on
	// the side.
	if got := network([]address{at("eth0", "203.0.113.7"), at("eth1", "10.0.0.2")}, Fence{}); len(got) != 1 || got[0].OK || !strings.Contains(got[0].Found, "10.0.0.2 on eth1") {
		t.Errorf("VPC: got %+v", got)
	}
	// An IPv6 LAN of unique local addresses.
	if got := network([]address{at("eth0", "fd12:3456::7")}, Fence{}); len(got) != 1 || got[0].OK {
		t.Errorf("ULA: got %+v", got)
	}
	if got := network([]address{at("eth0", "203.0.113.7")}, Fence{}); len(got) != 1 || !got[0].OK {
		t.Errorf("public address: got %+v", got)
	}
	if got := network([]address{at("lo", "127.0.0.1")}, Fence{}); len(got) != 0 {
		t.Errorf("no network at all: got %+v", got)
	}
	// Containers' and VMs' bridges are said as what they are.
	got = network([]address{at("eth0", "203.0.113.7"), at("docker0", "172.17.0.1"), at("virbr0", "192.168.122.1")}, Fence{})
	if len(got) != 1 || got[0].OK || !strings.Contains(got[0].Found, "local bridges (172.17.0.1 on docker0, 192.168.122.1 on virbr0)") ||
		strings.Contains(got[0].Found, "private network") || strings.Contains(got[0].Fix, "Wi-Fi") {
		t.Errorf("bridges: got %+v", got)
	}
	// A Mac's own VMs are behind vmnet's bridge, which is not a LAN; its
	// Thunderbolt Bridge can be.
	if network := only(t, (&fake{}).system(false, at("bridge100", "192.168.64.1"), at("en0", "203.0.113.7")).mac(Fence{}), "network"); !network.OK {
		t.Errorf("VM bridge: got %+v", network)
	}
	if network := only(t, (&fake{}).system(false, at("bridge0", "169.254.3.4"), at("bridge0", "10.1.1.2")).mac(Fence{}), "network"); network.OK {
		t.Errorf("Thunderbolt Bridge: got %+v", network)
	}
}

func TestAPublicSubnetHasNeighboursToo(t *testing.T) {
	at := func(iface, cidr string) address {
		ip, subnet, _ := net.ParseCIDR(cidr)
		bits, _ := subnet.Mask.Size()
		return address{iface, ip, bits}
	}
	for name, c := range map[string]struct {
		addresses []address
		said      string
	}{
		"a public IPv4 /24":            {[]address{at("eth0", "203.0.113.7/24")}, "a public subnet shared with other machines (203.0.113.0/24 on eth0)"},
		"a global IPv6 /64, twice":     {[]address{at("eth0", "2001:db8:1:2::7/64"), at("eth0", "2001:db8:1:2:a::9/64")}, "(2001:db8:1:2::/64 on eth0)"},
		"a LAN and a global IPv6 /64":  {[]address{at("en0", "192.168.1.20/24"), at("en0", "2001:db8:1:2::7/64")}, "a private network (192.168.1.20 on en0) and a public subnet"},
		"an IPv4 mapped into IPv6 too": {[]address{{"eth0", net.ParseIP("203.0.113.7"), 24}}, "(203.0.113.0/24 on eth0)"},
	} {
		got := network(c.addresses, Fence{})
		if len(got) != 1 || got[0].OK || !got[0].Note || !strings.Contains(got[0].Found, c.said) || !strings.Contains(got[0].Fix, "subnet of its own") {
			t.Errorf("%s: got %+v", name, got)
		}
		if strings.Count(got[0].Found, "2001:db8:1:2::/64") > 1 {
			t.Errorf("%s: a subnet said twice: %q", name, got[0].Found)
		}
	}
	// A point-to-point link or a lone address has nobody beside it.
	for _, alone := range []address{at("eth0", "203.0.113.7/32"), at("eth0", "203.0.113.7/31"), at("eth0", "2001:db8::7/128")} {
		if got := network([]address{alone}, Fence{}); len(got) != 1 || !got[0].OK || strings.Contains(got[0].Found, "shared with") {
			t.Errorf("%s/%d: got %+v", alone.ip, alone.bits, got)
		}
	}
}

func TestTheNetworkRowSaysWhatTheFenceKeepsJobsOff(t *testing.T) {
	lan := []address{{"en0", net.ParseIP("192.168.1.20"), 24}}
	if got := network(lan, Fence{On: true}); len(got) != 1 || !got[0].OK || !strings.HasSuffix(got[0].Found, "and jobs are kept off them") {
		t.Errorf("fenced: got %+v", got)
	}
	if got := network(lan, Fence{On: true, Allow: []string{"192.168.1.5:5432"}}); len(got) != 1 || !got[0].OK || !strings.Contains(got[0].Found, "kept off them but for 192.168.1.5:5432") {
		t.Errorf("fenced, with an allowance: got %+v", got)
	}
	open := Fence{Why: "private-network is allow", Fix: "hived config set private-network block"}
	if got := network(lan, open); len(got) != 1 || got[0].OK || !got[0].Note || !strings.Contains(got[0].Found, "whatever this machine can there, as private-network is allow") || got[0].Fix != open.Fix {
		t.Errorf("opened by the owner: got %+v", got)
	}
	// Nothing to fence is said the same either way.
	if got := network([]address{{"eth0", net.ParseIP("203.0.113.7"), 32}}, Fence{On: true}); len(got) != 1 || !got[0].OK || !strings.Contains(got[0].Found, "no tailnet") {
		t.Errorf("nothing to fence: got %+v", got)
	}
}

func TestNothingIsRunWithSudoUnlessAskedFor(t *testing.T) {
	f := &fake{
		said: map[string]string{
			"sudo -n nft -t list ruleset": "",
			"sudo -n " + sshd + " -T":     "port 22\npasswordauthentication no\nkbdinteractiveauthentication no\n",
		},
		files: map[string]string{"/proc/net/tcp": listen("0016"), "/etc/ssh/sshd_config": "Match Address 10.0.0.0/8\n"},
	}
	checks := f.system(false).linux(Fence{})
	if f.sudid() {
		t.Errorf("ran sudo without --sudo: %q", f.ran)
	}
	if ssh := only(t, checks, "ssh"); !strings.Contains(ssh.Fix, "hived doctor --sudo") {
		t.Errorf("ssh unread said %+v", ssh)
	}
	only(t, checks, "firewall")

	// A user name that would be more keys to -C is not asked about.
	f.ran = nil
	s := f.system(true)
	s.user = "ada,addr=1.2.3.4"
	s.linux(Fence{})
	for _, line := range f.ran {
		if strings.Contains(line, " -C ") {
			t.Errorf("asked sshd with a user name of more keys: %q", line)
		}
	}
}
