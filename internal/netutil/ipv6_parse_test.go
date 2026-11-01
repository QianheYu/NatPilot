package netutil

import (
	"net"
	"strings"
	"testing"
)

// Parser tests for the IPv6 neighbour (NDP) and default-route output. The
// samples are captured shapes of the real tools; the parsers themselves are pure
// so these run on every platform without the tools installed.

func TestSplitIPv6Zone(t *testing.T) {
	cases := []struct {
		in, ip, zone string
	}{
		{"fe80::1%eth0", "fe80::1", "eth0"},
		{"[fe80::1%eth0]", "fe80::1", "eth0"},
		{"fe80::1", "fe80::1", ""},
		{"2001:db8::1", "2001:db8::1", ""},
		{"2001:0db8:0000::0001", "2001:db8::1", ""}, // canonicalised
		{"10.0.0.1", "", ""},
		{"::ffff:10.0.0.1", "", ""},
		{"not-an-ip", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		ip, zone := splitIPv6Zone(c.in)
		if ip != c.ip || zone != c.zone {
			t.Errorf("splitIPv6Zone(%q) = (%q, %q), want (%q, %q)", c.in, ip, zone, c.ip, c.zone)
		}
	}
}

func TestStripZoneSuffix(t *testing.T) {
	cases := []struct {
		in, out, zone string
	}{
		{"fe80::1%eth0/64", "fe80::1/64", "eth0"},
		{"fe80::1%eth0", "fe80::1", "eth0"},
		{"2001:db8::/32", "2001:db8::/32", ""},
		{"192.168.1.0/24", "192.168.1.0/24", ""},
	}
	for _, c := range cases {
		out, zone := stripZoneSuffix(c.in)
		if out != c.out || zone != c.zone {
			t.Errorf("stripZoneSuffix(%q) = (%q, %q), want (%q, %q)", c.in, out, zone, c.out, c.zone)
		}
	}
}

// TestParseWinNetshNeighbors covers the English table. The "router" and "Stale"
// rows are real neighbours and must be kept; the placeholder rows (dash MAC,
// "(incomplete)", all-zero MAC) and the multicast row must be dropped so a failed
// NDP probe never becomes a fake host.
func TestParseWinNetshNeighbors(t *testing.T) {
	out := `Interface 12: WLAN

Internet Address                              Physical Address   Type
--------------------------------------------  -----------------  -----------
fe80::1                                       34-66-ee-78-a2-d1  Reachable
2001:db8:1::20                                b8-27-eb-11-22-33  Stale
2001:db8:1::21                                8c-16-45-aa-bb-cc  router
ff02::1                                       33-33-00-00-00-01  Permanent
fe80::9999                                    --                 Unreachable
fe80::aaaa                                    (incomplete)       Incomplete
::1                                           00-00-00-00-00-00  Permanent

Interface 20: 以太网 2

Internet Address                              Physical Address   Type
--------------------------------------------  -----------------  -----------
2001:db8:5::1                                 52-54-00-aa-bb-cc  Reachable
`
	ns := parseWinNetshNeighbors(out)
	if len(ns) != 4 {
		t.Fatalf("got %d neighbours, want 4: %+v", len(ns), ns)
	}
	if ns[0].IP != "fe80::1" || ns[0].MAC != "34:66:ee:78:a2:d1" || ns[0].Iface != "WLAN" {
		t.Errorf("first neighbour = %+v", ns[0])
	}
	if ns[1].IP != "2001:db8:1::20" || ns[1].MAC != "b8:27:eb:11:22:33" {
		t.Errorf("stale neighbour = %+v", ns[1])
	}
	if ns[2].IP != "2001:db8:1::21" || ns[2].MAC != "8c:16:45:aa:bb:cc" {
		t.Errorf("router neighbour = %+v", ns[2])
	}
	// The section header names change mid-output and must be tracked per row,
	// including names with spaces.
	if ns[3].IP != "2001:db8:5::1" || ns[3].Iface != "以太网 2" {
		t.Errorf("last neighbour = %+v", ns[3])
	}
	for _, n := range ns {
		if n.Family != FamilyIPv6 {
			t.Errorf("neighbour %+v has Family %q", n, n.Family)
		}
		if n.MAC == "" {
			t.Errorf("neighbour %+v has an empty MAC", n)
		}
		if strings.Contains(n.IP, "%") {
			t.Errorf("neighbour IP %q carries a zone suffix", n.IP)
		}
		if _, p := splitIPv6Zone(n.IP); p != "" {
			t.Errorf("neighbour IP %q still has a zone after parsing", n.IP)
		}
	}
}

// TestParseWinNetshNeighborsChineseLocale uses the localised table shipped by
// Chinese Windows ("接口 12:", "Internet 地址", "可访问"/"陈旧"/"无法访问"). The
// parser must not depend on the English header or state words.
func TestParseWinNetshNeighborsChineseLocale(t *testing.T) {
	out := `接口 12: WLAN

Internet 地址                              物理地址           类型
------------------------------------------  -----------------  -----------
fe80::1                                     36-66-ee-78-a2-d1  可访问
2001:db8:1::52                              a4-50-46-11-22-33  陈旧
fe80::abcd                                  00-00-00-00-00-00  无法访问
fe80::dead                                  --                 探测中
`
	ns := parseWinNetshNeighbors(out)
	if len(ns) != 2 {
		t.Fatalf("got %d neighbours, want 2: %+v", len(ns), ns)
	}
	if ns[0].IP != "fe80::1" || ns[0].MAC != "36:66:ee:78:a2:d1" || ns[0].Iface != "WLAN" {
		t.Errorf("first neighbour = %+v", ns[0])
	}
	if ns[1].IP != "2001:db8:1::52" || ns[1].MAC != "a4:50:46:11:22:33" {
		t.Errorf("second neighbour = %+v", ns[1])
	}
}

func TestParseIPNeigh6(t *testing.T) {
	out := `2001:db8::1 dev eth0 lladdr 00:11:22:33:44:55 REACHABLE
fe80::1 dev eth0 lladdr 00:11:22:33:44:55 router STALE
fe80::2 dev eth0  FAILED
fe80::3 dev eth0  INCOMPLETE
fe80::4 dev eth0 lladdr 00:11:22:33:44:55 DELAY
ff02::1 dev eth0  NOARP
2001:db8::9 dev wlan0  probe
fe80::5%eth0 dev eth0 lladdr b8:27:eb:11:22:33 STALE
`
	ns := parseIPNeigh6(out)
	if len(ns) != 4 {
		t.Fatalf("got %d neighbours, want 4: %+v", len(ns), ns)
	}
	if ns[0].IP != "2001:db8::1" || ns[0].MAC != "00:11:22:33:44:55" || ns[0].Iface != "eth0" {
		t.Errorf("first neighbour = %+v", ns[0])
	}
	if ns[1].IP != "fe80::1" || ns[1].Iface != "eth0" {
		t.Errorf("router neighbour = %+v", ns[1])
	}
	if ns[2].IP != "fe80::4" {
		t.Errorf("delay neighbour = %+v", ns[2])
	}
	// A zone on the address is split off and the bare address is returned.
	if ns[3].IP != "fe80::5" || ns[3].Iface != "eth0" {
		t.Errorf("zoned neighbour = %+v", ns[3])
	}
	for _, n := range ns {
		if n.Family != FamilyIPv6 || n.MAC == "" || strings.Contains(n.IP, "%") {
			t.Errorf("neighbour = %+v", n)
		}
	}
}

func TestParseNdpAn(t *testing.T) {
	out := `Neighbor                             Linklayer Address  Netif Expire    S Flags
fe80::1%en0                          0:11:22:33:44:55   en0   23h59m59s S R
2001:db8:1::20                       b8:27:eb:11:22:33  en0   23h58m11s   R
2001:db8:1::99                       (incomplete)       en0   expired   I
ff02::1                              33:33:00:00:00:01  en0   23h59m28s S R
fe80::2%en1                          52:54:00:aa:bb:cc  ---   expired   I
`
	ns := parseNdpAn(out)
	if len(ns) != 3 {
		t.Fatalf("got %d neighbours, want 3: %+v", len(ns), ns)
	}
	if ns[0].IP != "fe80::1" || ns[0].MAC != "00:11:22:33:44:55" || ns[0].Iface != "en0" {
		t.Errorf("first neighbour = %+v", ns[0])
	}
	if ns[1].IP != "2001:db8:1::20" || ns[1].Iface != "en0" {
		t.Errorf("global neighbour = %+v", ns[1])
	}
	// The Netif column was "---", so the zone supplies the interface.
	if ns[2].IP != "fe80::2" || ns[2].Iface != "en1" {
		t.Errorf("zoned neighbour = %+v", ns[2])
	}
}

func TestParseProcIPv6Route(t *testing.T) {
	out := `00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000400 00000000 00000000 00000003     eth0
00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00001000 00000000 00000000 00000003     wlan0
20010db8000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000000 00000000 00000003     eth0
fe800000000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000000 00000000 00000003     eth0
`
	// The lowest metric (0x400) wins; the on-link default with a worse metric is
	// not a gateway.
	got := parseProcIPv6Route(out)
	if got.Gateway != "fe80::1" || got.Iface != "eth0" {
		t.Errorf("parseProcIPv6Route = %+v, want fe80::1 via eth0", got)
	}
	if got.IfIndex != 0 {
		t.Errorf("IfIndex should be unknown for /proc output, got %d", got.IfIndex)
	}

	// An on-link default has no next hop, and non-default rows are ignored.
	onlink := `00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000400 00000000 00000000 00000003     wlan0
20010db8000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000000 00000000 00000003     eth0
`
	got = parseProcIPv6Route(onlink)
	if got.Gateway != "" || got.Iface != "wlan0" {
		t.Errorf("on-link default = %+v, want no gateway via wlan0", got)
	}
	if got := parseProcIPv6Route("garbage\n"); (got != route6Info{}) {
		t.Errorf("garbage output = %+v, want the zero value", got)
	}
}

func TestParseRoutePrintWindows6(t *testing.T) {
	out := `===========================================================================
IPv6 Route Table
===========================================================================
Active Routes:
 If Metric Network Destination      Gateway
  1    281 ::/0                     fe80::1
 12     25 ::/0                     fe80::2
 12     41 2001:db8:1::/64          On-link
  1    281 fe80::/64                On-link
===========================================================================
Persistent Routes:
  None
`
	// The lowest metric wins, and the interface index is kept so the link-local
	// next hop can be attributed later.
	got := parseRoutePrintWindows6(out)
	if got.Gateway != "fe80::2" || got.IfIndex != 12 {
		t.Errorf("parseRoutePrintWindows6 = %+v, want fe80::2 on index 12", got)
	}

	// Persistent-route rows print "none" instead of an index.
	persistent := `  none  16  ::/0                     fe80::9
`
	if got := parseRoutePrintWindows6(persistent); (got != route6Info{}) {
		t.Errorf("persistent row = %+v, want the zero value", got)
	}

	// "On-link" is localised ("在链路上") and must never become a gateway.
	onlink := `  1    281 ::/0                     在链路上
 12     25 ::/0                     On-link
`
	if got := parseRoutePrintWindows6(onlink); (got != route6Info{}) {
		t.Errorf("on-link rows = %+v, want the zero value", got)
	}
}

func TestParseRouteGetDarwin6(t *testing.T) {
	out := `   route to: ::
destination: ::
       mask: default
    gateway: fe80::1%en0
  interface: en0
      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING,GLOBAL>
 recvpipe  sendpipe  ssthresh  rtt,msec    rttvar  hopcount      mtu     expire
       0         0         0         0         0         0      1500         0
`
	got := parseRouteGetDarwin6(out)
	if got.Gateway != "fe80::1" || got.Iface != "en0" {
		t.Errorf("parseRouteGetDarwin6 = %+v, want fe80::1 via en0", got)
	}

	// A global next hop needs no zone.
	global := `   route to: ::
destination: ::
       mask: default
    gateway: 2001:db8:1::1
  interface: en0
`
	got = parseRouteGetDarwin6(global)
	if got.Gateway != "2001:db8:1::1" || got.Iface != "en0" {
		t.Errorf("global default = %+v", got)
	}
	if got := parseRouteGetDarwin6("no gateway here\n"); (got != route6Info{}) {
		t.Errorf("output without a gateway = %+v, want the zero value", got)
	}
}

func TestParseNetstatDarwin6(t *testing.T) {
	out := `Routing tables

Internet6:
Destination                             Gateway                                 Flags         Netif Expire
default                                 fe80::1%en0                             UGcg            en0
::1                                     ::1                                     UHL             lo0
2001:db8:1::/64                         link#12                                 UC              en0
`
	got := parseNetstatDarwin6(out)
	if got.Gateway != "fe80::1" || got.Iface != "en0" {
		t.Errorf("parseNetstatDarwin6 = %+v, want fe80::1 via en0", got)
	}
}

// TestIPv4ParsersKeepFamily locks the one intentional change to the existing
// IPv4 parsers: every row is now tagged with its family.
func TestIPv4ParsersKeepFamily(t *testing.T) {
	darwin := parseARPOutputDarwin("? (192.168.1.1) at 0:11:22:33:44:55 on en0 ifscope [ethernet]\n")
	if len(darwin) != 1 || darwin[0].Family != FamilyIPv4 {
		t.Errorf("darwin IPv4 neighbours = %+v", darwin)
	}
	windows := parseARPOutputWindows("Interface: 192.168.1.100 --- 0xc\n  192.168.1.1  00-11-22-33-44-55  dynamic\n")
	if len(windows) != 1 || windows[0].Family != FamilyIPv4 {
		t.Errorf("windows IPv4 neighbours = %+v", windows)
	}
}

// TestNeighborZoneIsNotInIP is a guard for the contract that the zone is always
// reported separately: joining the neighbours of a link-local table must never
// produce an address that net.ParseIP rejects.
func TestNeighborZoneIsNotInIP(t *testing.T) {
	out := `fe80::1 dev eth0 lladdr 00:11:22:33:44:55 REACHABLE
fe80::2%eth0 dev eth0 lladdr B8:27:EB:11:22:33 STALE
`
	for _, n := range parseIPNeigh6(out) {
		if ip := net.ParseIP(n.IP); ip == nil || ip.To4() != nil {
			t.Errorf("neighbour IP %q is not a bare IPv6 literal", n.IP)
		}
		if n.Iface != "eth0" {
			t.Errorf("neighbour %+v lost its zone", n)
		}
	}
}
