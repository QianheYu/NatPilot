package netutil

import (
	"net"
	"strings"
	"testing"
)

// IPv6 support tests for the pure logic in netutil.go: address-family tagging,
// CIDR expansion, primary address selection and host:port formatting. They run
// on every platform because none of them touches the OS.

func TestJoinHostPort(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"192.168.1.1", 80, "192.168.1.1:80"},
		{"10.0.0.5", 65535, "10.0.0.5:65535"},
		{"::1", 80, "[::1]:80"},
		{"::", 8080, "[::]:8080"},
		{"2001:db8::1", 443, "[2001:db8::1]:443"},
		{"fe80::1%eth0", 80, "[fe80::1%eth0]:80"}, // zone stays inside the brackets
		{"[2001:db8::1]", 53, "[2001:db8::1]:53"},
		{"[fe80::1%eth0]", 22, "[fe80::1%eth0]:22"},
		{"router.lan", 8000, "router.lan:8000"},
		{"", 22, ":22"},                   // matches net.JoinHostPort
		{" 10.0.0.5 ", 80, "10.0.0.5:80"}, // surrounding spaces are trimmed
	}
	for _, c := range cases {
		if got := JoinHostPort(c.host, c.port); got != c.want {
			t.Errorf("JoinHostPort(%q, %d) = %q, want %q", c.host, c.port, got, c.want)
		}
	}
}

func TestIfaceIsV6(t *testing.T) {
	cases := []struct {
		ifc  Iface
		want bool
	}{
		{Iface{IP: "192.168.1.8", Family: FamilyIPv4}, false},
		{Iface{IP: "2001:db8::8", Family: FamilyIPv6}, true},
		{Iface{IP: "fe80::1", Family: FamilyIPv6}, true},
		// Family escalates a hand-built entry even when IP is missing.
		{Iface{Family: FamilyIPv6}, true},
		// Without Family the address decides, so older callers keep working.
		{Iface{IP: "192.168.1.8"}, false},
		{Iface{IP: "2001:db8::8"}, true},
		{Iface{IP: "::ffff:192.168.1.8"}, false}, // 4-in-6 is an IPv4 address
		{Iface{IP: "not-an-ip"}, false},
		{Iface{}, false},
	}
	for _, c := range cases {
		if got := c.ifc.IsV6(); got != c.want {
			t.Errorf("Iface{%q, family=%q}.IsV6() = %v, want %v", c.ifc.IP, c.ifc.Family, got, c.want)
		}
	}
}

func TestExpandCIDRIPv4Unchanged(t *testing.T) {
	// The historical /30 behaviour must not change.
	hosts, err := ExpandCIDR("192.168.1.0/30", 16)
	if err != nil {
		t.Fatalf("ExpandCIDR(/30): %v", err)
	}
	if len(hosts) != 2 || hosts[0] != "192.168.1.1" || hosts[1] != "192.168.1.2" {
		t.Errorf("/30 hosts = %v, want [192.168.1.1 192.168.1.2]", hosts)
	}
	// /32 and a bare IPv4 literal are a single host.
	for _, in := range []string{"192.168.1.7/32", "192.168.1.7"} {
		hosts, err = ExpandCIDR(in, 8)
		if err != nil {
			t.Fatalf("ExpandCIDR(%q): %v", in, err)
		}
		if len(hosts) != 1 || hosts[0] != "192.168.1.7" {
			t.Errorf("ExpandCIDR(%q) = %v", in, hosts)
		}
	}
	// A block larger than maxHosts is still an error for IPv4.
	if _, err := ExpandCIDR("10.0.0.0/16", 100); err == nil {
		t.Errorf("ExpandCIDR(/16, 100) should still refuse an oversized block")
	}
}

func TestExpandCIDRIPv6(t *testing.T) {
	// /126 has four addresses; the all-zero host part is the subnet-router
	// anycast address and is skipped, and IPv6 has no broadcast to skip.
	hosts, err := ExpandCIDR("2001:db8::/126", 16)
	if err != nil {
		t.Fatalf("ExpandCIDR(/126): %v", err)
	}
	want := []string{"2001:db8::1", "2001:db8::2", "2001:db8::3"}
	if strings.Join(hosts, ",") != strings.Join(want, ",") {
		t.Errorf("/126 hosts = %v, want %v", hosts, want)
	}

	// A /64 cannot be enumerated, so it yields exactly maxHosts addresses with
	// network+1 first and network+2 second, without an error.
	hosts, err = ExpandCIDR("2001:db8:1::/64", 8)
	if err != nil {
		t.Fatalf("ExpandCIDR(/64): %v", err)
	}
	if len(hosts) != 8 {
		t.Fatalf("/64 with maxHosts 8 yielded %d hosts: %v", len(hosts), hosts)
	}
	if hosts[0] != "2001:db8:1::1" || hosts[1] != "2001:db8:1::2" {
		t.Errorf("/64 first two hosts = %q, %q, want 2001:db8:1::1, 2001:db8:1::2", hosts[0], hosts[1])
	}

	// maxHosts is a hard ceiling even when the caller asks for "everything".
	hosts, err = ExpandCIDR("2001:db8:1::/64", 1)
	if err != nil {
		t.Fatalf("ExpandCIDR(/64, 1): %v", err)
	}
	if len(hosts) != 1 {
		t.Errorf("/64 with maxHosts 1 yielded %d hosts, want 1", len(hosts))
	}

	// The default limit applies when maxHosts is not positive.
	hosts, err = ExpandCIDR("2001:db8:1::/64", 0)
	if err != nil {
		t.Fatalf("ExpandCIDR(/64, 0): %v", err)
	}
	if len(hosts) != 4096 {
		t.Errorf("default maxHosts yielded %d hosts, want 4096", len(hosts))
	}

	// /127 and /128 hold no separate network address: every address is a host,
	// mirroring how IPv4 /31 and /32 are treated.
	hosts, err = ExpandCIDR("2001:db8::/127", 16)
	if err != nil {
		t.Fatalf("ExpandCIDR(/127): %v", err)
	}
	if len(hosts) != 2 || hosts[0] != "2001:db8::" || hosts[1] != "2001:db8::1" {
		t.Errorf("/127 hosts = %v, want [2001:db8:: 2001:db8::1]", hosts)
	}
	hosts, err = ExpandCIDR("2001:db8::1/128", 16)
	if err != nil {
		t.Fatalf("ExpandCIDR(/128): %v", err)
	}
	if len(hosts) != 1 || hosts[0] != "2001:db8::1" {
		t.Errorf("/128 hosts = %v, want [2001:db8::1]", hosts)
	}

	// A bare IPv6 literal is a single host, like a bare IPv4 literal.
	hosts, err = ExpandCIDR("2001:db8::5", 16)
	if err != nil {
		t.Fatalf("ExpandCIDR(single v6): %v", err)
	}
	if len(hosts) != 1 || hosts[0] != "2001:db8::5" {
		t.Errorf("single v6 host = %v", hosts)
	}

	// A zone suffix is accepted and dropped: the returned literals are bare.
	hosts, err = ExpandCIDR("fe80::1%eth0/64", 3)
	if err != nil {
		t.Fatalf("ExpandCIDR(zoned /64): %v", err)
	}
	if strings.Join(hosts, ",") != "fe80::1,fe80::2,fe80::3" {
		t.Errorf("zoned /64 hosts = %v, want [fe80::1 fe80::2 fe80::3]", hosts)
	}
	hosts, err = ExpandCIDR("fe80::1%eth0", 3)
	if err != nil {
		t.Fatalf("ExpandCIDR(zoned single): %v", err)
	}
	if len(hosts) != 1 || hosts[0] != "fe80::1" {
		t.Errorf("zoned single host = %v, want [fe80::1]", hosts)
	}
}

// TestExpandCIDRIPv6NeverWraps drives the top of the address space: the
// increment must stop instead of wrapping to :: and re-entering the prefix.
func TestExpandCIDRIPv6NeverWraps(t *testing.T) {
	hosts, err := ExpandCIDR("ffff:ffff:ffff:ffff:ffff:ffff:ffff:fffc/126", 100)
	if err != nil {
		t.Fatalf("ExpandCIDR(top of space): %v", err)
	}
	want := []string{
		"ffff:ffff:ffff:ffff:ffff:ffff:ffff:fffd",
		"ffff:ffff:ffff:ffff:ffff:ffff:ffff:fffe",
		"ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
	}
	if strings.Join(hosts, ",") != strings.Join(want, ",") {
		t.Errorf("top-of-space /126 = %v, want %v", hosts, want)
	}

	// ::/0 must also terminate at maxHosts and start after the unspecified
	// address, not loop forever.
	hosts, err = ExpandCIDR("::/0", 5)
	if err != nil {
		t.Fatalf("ExpandCIDR(::/0): %v", err)
	}
	if strings.Join(hosts, ",") != "::1,::2,::3,::4,::5" {
		t.Errorf("::/0 hosts = %v", hosts)
	}
}

func TestPickPrimaryIPv6(t *testing.T) {
	// A real dual-stack desktop: the interface that owns the IPv6 default route
	// wins even though another adapter has a ULA.
	dualStack := []Iface{
		{Name: "Mihomo", IP: "198.18.0.1", Family: FamilyIPv4, Up: true},
		{Name: "Tailscale", IP: "fd7a:115c:a1e0::1", Network: "fd7a:115c:a1e0::/48", Up: true, Family: FamilyIPv6},
		{Name: "WLAN", IP: "2001:db8:1::8", Network: "2001:db8:1::/64", Gateway: "fe80::1", Up: true, Family: FamilyIPv6},
		{Name: "WLAN", IP: "fe80::1234:5678:9abc:def0", Network: "fe80::/64", Zone: "WLAN", Scoped: true, Up: true, Family: FamilyIPv6},
		{Name: "Loopback Pseudo-Interface 1", IP: "::1", Network: "::1/128", Loopback: true, Up: true, Family: FamilyIPv6},
	}
	if got := PickPrimaryIPv6(dualStack); got != "2001:db8:1::8" {
		t.Errorf("PickPrimaryIPv6(dual stack) = %q, want 2001:db8:1::8", got)
	}

	// Without a default route, ULA beats global unicast beats link-local.
	noRoute := []Iface{
		{Name: "eth0", IP: "fe80::1", Network: "fe80::/64", Zone: "eth0", Scoped: true, Up: true, Family: FamilyIPv6},
		{Name: "eth0", IP: "2001:db8:1::8", Network: "2001:db8:1::/64", Up: true, Family: FamilyIPv6},
		{Name: "eth0", IP: "fd00::8", Network: "fd00::/64", Up: true, Family: FamilyIPv6},
	}
	if got := PickPrimaryIPv6(noRoute); got != "fd00::8" {
		t.Errorf("PickPrimaryIPv6(no route) = %q, want the ULA fd00::8", got)
	}

	// A link-local address is still better than nothing.
	linkLocalOnly := []Iface{
		{Name: "eth0", IP: "fe80::1", Network: "fe80::/64", Zone: "eth0", Scoped: true, Up: true, Family: FamilyIPv6},
	}
	if got := PickPrimaryIPv6(linkLocalOnly); got != "fe80::1" {
		t.Errorf("PickPrimaryIPv6(link-local only) = %q", got)
	}

	// Loopback, multicast and 4-in-6 addresses never count.
	junk := []Iface{
		{Name: "lo", IP: "::1", Loopback: true, Up: true, Family: FamilyIPv6},
		{Name: "eth0", IP: "ff02::1", Up: true, Family: FamilyIPv6},
		{Name: "eth0", IP: "::ffff:192.168.1.8", Up: true, Family: FamilyIPv6},
		{Name: "eth0", IP: "::", Up: true, Family: FamilyIPv6},
	}
	if got := PickPrimaryIPv6(junk); got != "" {
		t.Errorf("PickPrimaryIPv6(junk) = %q, want empty", got)
	}
	if got := PickPrimaryIPv6(nil); got != "" {
		t.Errorf("PickPrimaryIPv6(nil) = %q, want empty", got)
	}
}

// TestInterfacesDualStack checks the invariants of live enumeration. It cannot
// assert which addresses exist, but it can assert that every entry is tagged and
// that IPv6 entries look like IPv6 entries.
func TestInterfacesDualStack(t *testing.T) {
	list, err := Interfaces()
	if err != nil {
		t.Fatalf("Interfaces: %v", err)
	}
	v4, v6 := 0, 0
	seenV6 := false
	for _, i := range list {
		if i.Family != FamilyIPv4 && i.Family != FamilyIPv6 {
			t.Errorf("entry %q has Family %q", i.IP, i.Family)
		}
		if strings.Contains(i.IP, "%") {
			t.Errorf("entry IP %q carries a zone suffix; the zone belongs in Zone", i.IP)
		}
		if i.IsV6() != (i.Family == FamilyIPv6) {
			t.Errorf("entry %q Family=%q IsV6=%v disagree", i.IP, i.Family, i.IsV6())
		}
		if i.Scoped && i.Zone == "" {
			t.Errorf("scoped entry %q has no Zone", i.IP)
		}
		if !i.Scoped && i.Zone != "" {
			t.Errorf("global entry %q has Zone %q", i.IP, i.Zone)
		}
		if !i.IsV6() {
			if seenV6 {
				t.Errorf("IPv4 entry %q sorts after an IPv6 entry; IPv4 order must come first", i.IP)
			}
			v4++
			ip := net.ParseIP(i.IP)
			if ip == nil || ip.To4() == nil {
				t.Errorf("IPv4 entry with unparsable IP %q", i.IP)
			}
			continue
		}
		seenV6 = true
		v6++
		ip := net.ParseIP(i.IP)
		if ip == nil || ip.To4() != nil {
			t.Errorf("IPv6 entry with non-IPv6 IP %q", i.IP)
			continue
		}
		if i.Broadcast != "" {
			t.Errorf("IPv6 entry %q has Broadcast %q; IPv6 has no broadcast", i.IP, i.Broadcast)
		}
		if i.Network == "" {
			t.Errorf("IPv6 entry %q has no Network CIDR", i.IP)
		} else if _, _, err := net.ParseCIDR(i.Network); err != nil {
			t.Errorf("IPv6 entry %q has unparsable Network %q: %v", i.IP, i.Network, err)
		}
		if i.Scoped != ip.IsLinkLocalUnicast() {
			t.Errorf("IPv6 entry %q Scoped=%v, want %v", i.IP, i.Scoped, ip.IsLinkLocalUnicast())
		}
		if i.Scoped && i.Zone != i.Name {
			t.Errorf("scoped entry %q Zone=%q, want the interface name %q", i.IP, i.Zone, i.Name)
		}
	}
	t.Logf("enumerated %d IPv4 and %d IPv6 addresses", v4, v6)
}
