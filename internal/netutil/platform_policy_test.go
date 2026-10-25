package netutil

// Localisation and multi-homing regression tests.
//
// Every sample here was captured from a real machine (Chinese Windows 11 with
// WLAN + VMware + ZeroTier + Tailscale + a Clash TUN adapter) or is the output
// shape documented for another locale. The parsers must not depend on the
// English column headers, on the row order, or on the English state names.

import (
	"net"
	"testing"
)

// TestParseRoutePrintWindowsPicksLowestMetric covers the case that broke
// mapping on multi-homed Windows hosts: three default routes, where the first
// row in the table is not the one the kernel uses.
func TestParseRoutePrintWindowsPicksLowestMetric(t *testing.T) {
	out := `===========================================================================
IPv4 路由表
===========================================================================
活动路由:
网络目标        网络掩码          网关       接口   跃点数
          0.0.0.0          0.0.0.0      192.168.1.1      192.168.1.8     35
          0.0.0.0          0.0.0.0   25.255.255.254    10.121.20.206  10034
          0.0.0.0          0.0.0.0       198.18.0.2       198.18.0.1      0
      192.168.1.0    255.255.255.0             On-link       192.168.1.8    291
`
	if got := parseRoutePrintWindows(out); got != "198.18.0.2" {
		t.Errorf("parseRoutePrintWindows = %q, want the lowest-metric route 198.18.0.2", got)
	}
}

// TestParseRoutePrintWindowsNetstatShape checks the `netstat -rn` spelling of
// the same table, which also ends with the metric column.
func TestParseRoutePrintWindowsNetstatShape(t *testing.T) {
	out := `===========================================================================
接口列表
 10...00 50 56 c0 00 08 ......VMware Virtual Ethernet Adapter for VMnet8
===========================================================================
IPv4 路由表
===========================================================================
          0.0.0.0          0.0.0.0      192.168.1.1     192.168.1.100     25
          0.0.0.0          0.0.0.0       10.0.0.1        10.0.0.5         5
`
	if got := parseRoutePrintWindows(out); got != "10.0.0.1" {
		t.Errorf("parseRoutePrintWindows = %q, want 10.0.0.1 (metric 5)", got)
	}
}

// TestParseRoutePrintWindowsIgnoresLocalisedOnLink makes sure a translated
// "on-link" placeholder ("在链路上", "Auf Verbindung", ...) is not mistaken for a
// gateway.
func TestParseRoutePrintWindowsIgnoresLocalisedOnLink(t *testing.T) {
	out := `          0.0.0.0          0.0.0.0            在链路上       192.168.1.8    291
          0.0.0.0          0.0.0.0      192.168.1.1       192.168.1.8     35
`
	if got := parseRoutePrintWindows(out); got != "192.168.1.1" {
		t.Errorf("parseRoutePrintWindows = %q, want 192.168.1.1", got)
	}
}

// TestParseARPOutputWindowsChineseLocale uses the real header of a Chinese
// Windows ("接口:" instead of "Interface:"). The interface must still be tracked.
func TestParseARPOutputWindowsChineseLocale(t *testing.T) {
	out := `接口: 192.168.1.8 --- 0x1a
  Internet 地址         物理地址              类型
  192.168.1.1           36-66-ee-78-a2-d1     动态
  192.168.1.2           42-c9-a7-66-54-6f     动态
  192.168.1.255         ff-ff-ff-ff-ff-ff     静态
  224.0.0.22            01-00-5e-00-00-16     静态

接口: 10.121.20.206 --- 0x19
  Internet 地址         物理地址              类型
  10.121.20.52          8a-de-50-09-ae-86     动态
`
	ns := parseARPOutputWindows(out)
	if len(ns) != 3 {
		t.Fatalf("got %d neighbours, want 3: %+v", len(ns), ns)
	}
	if ns[0].Iface != "192.168.1.8" || ns[0].MAC != "36:66:ee:78:a2:d1" {
		t.Errorf("first neighbour = %+v", ns[0])
	}
	if ns[2].IP != "10.121.20.52" || ns[2].Iface != "10.121.20.206" {
		t.Errorf("third neighbour = %+v", ns[2])
	}
}

// TestParseWinARPInterfaceHeader covers the labels shipped by other locales.
func TestParseWinARPInterfaceHeader(t *testing.T) {
	cases := []struct {
		line string
		want string
		ok   bool
	}{
		{"Interface: 192.168.1.100 --- 0xb", "192.168.1.100", true},
		{"接口: 192.168.1.8 --- 0x1a", "192.168.1.8", true},
		{"Schnittstelle: 10.0.0.5 --- 0x5", "10.0.0.5", true},
		// MAC rows never contain three consecutive dashes.
		{"192.168.1.1           36-66-ee-78-a2-d1     动态", "", false},
		{"  192.168.1.255         ff-ff-ff-ff-ff-ff     statisch", "", false},
	}
	for _, c := range cases {
		got, ok := parseWinARPInterfaceHeader(c.line)
		if ok != c.ok || got != c.want {
			t.Errorf("parseWinARPInterfaceHeader(%q) = (%q,%v), want (%q,%v)", c.line, got, ok, c.want, c.ok)
		}
	}
}

// TestPickPrimaryIPv4 mirrors the interface set of a real Windows desktop where
// the TUN adapter sorts first and the LAN adapter is not the first entry.
func TestPickPrimaryIPv4(t *testing.T) {
	realDesktop := []Iface{
		{Name: "Mihomo", IP: "198.18.0.1", Network: "198.18.0.0/30", Up: true},
		{Name: "Tailscale", IP: "169.254.83.107", Network: "169.254.0.0/16", Up: true},
		{Name: "VMware Network Adapter VMnet1", IP: "192.168.15.1", Network: "192.168.15.0/24", Up: true},
		{Name: "VMware Network Adapter VMnet8", IP: "192.168.6.1", Network: "192.168.6.0/24", Up: true},
		{Name: "WLAN", IP: "192.168.1.8", Network: "192.168.1.0/24", Gateway: "192.168.1.1", Up: true},
		{Name: "ZeroTier One", IP: "10.121.20.206", Network: "10.121.20.0/24", Up: true},
		{Name: "Loopback Pseudo-Interface 1", IP: "127.0.0.1", Network: "127.0.0.0/8", Up: true, Loopback: true},
	}
	if got := PickPrimaryIPv4(realDesktop); got != "192.168.1.8" {
		t.Errorf("PickPrimaryIPv4(real desktop) = %q, want the LAN address 192.168.1.8", got)
	}

	// A single-homed host still works.
	single := []Iface{{Name: "eth0", IP: "192.168.0.10", Network: "192.168.0.0/24", Gateway: "192.168.0.1", Up: true}}
	if got := PickPrimaryIPv4(single); got != "192.168.0.10" {
		t.Errorf("PickPrimaryIPv4(single) = %q", got)
	}

	// Without any gateway (a container on a bridge), the private address wins
	// over the link-local one.
	noGateway := []Iface{
		{Name: "dummy", IP: "169.254.10.10", Network: "169.254.0.0/16", Up: true},
		{Name: "eth0", IP: "172.17.0.2", Network: "172.17.0.0/16", Up: true},
	}
	if got := PickPrimaryIPv4(noGateway); got != "172.17.0.2" {
		t.Errorf("PickPrimaryIPv4(no gateway) = %q, want 172.17.0.2", got)
	}

	// Loopback is the last resort, never preferred over a routable address.
	onlyLoopback := []Iface{{Name: "lo", IP: "127.0.0.1", Network: "127.0.0.0/8", Up: true, Loopback: true}}
	if got := PickPrimaryIPv4(onlyLoopback); got != "127.0.0.1" {
		t.Errorf("PickPrimaryIPv4(loopback only) = %q", got)
	}

	if got := PickPrimaryIPv4(nil); got != "" {
		t.Errorf("PickPrimaryIPv4(nil) = %q, want empty", got)
	}
}

// TestIsPrivateIPv4 documents which ranges count as "LAN-like".
func TestIsPrivateIPv4(t *testing.T) {
	cases := map[string]bool{
		"10.0.0.1":       true,
		"172.16.0.1":     true,
		"172.31.255.1":   true,
		"172.32.0.1":     false,
		"192.168.1.1":    true,
		"100.64.0.1":     true, // carrier grade NAT
		"169.254.83.107": false,
		"8.8.8.8":        false,
	}
	for ip, want := range cases {
		if got := isPrivateIPv4(net.ParseIP(ip)); got != want {
			t.Errorf("isPrivateIPv4(%s) = %v, want %v", ip, got, want)
		}
	}
}
