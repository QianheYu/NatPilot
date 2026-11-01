package api

import (
	"testing"

	"github.com/ekko-studio/natpilot/internal/netutil"
)

var (
	testIfaceV4 = netutil.Iface{
		Name: "以太网", IP: "192.168.1.10", IPNet: "192.168.1.10/24",
		Network: "192.168.1.0/24", Family: netutil.FamilyIPv4,
	}
	testIfaceV4IPNetOnly = netutil.Iface{
		Name: "eth1", IP: "10.0.0.5", IPNet: "10.0.0.5/24",
		Family: netutil.FamilyIPv4,
	}
	testIfaceV6 = netutil.Iface{
		Name: "WLAN", IP: "fe80::1234", IPNet: "fe80::1234/64",
		Network: "fe80::/64", Zone: "WLAN", Scoped: true, Family: netutil.FamilyIPv6,
	}
	testIfaceNoSubnet = netutil.Iface{
		Name: "eth9", IP: "169.254.1.2", Family: netutil.FamilyIPv4,
	}
)

// resolveScanMode is the pure policy behind handleHostScan: the UI can leave the
// subnet empty (scan the selected interface's own subnet), ask for the passive
// neighbour pass, or pin an explicit CIDR, and every combination has to land on
// exactly one of those three before any socket is opened.
func TestResolveScanMode(t *testing.T) {
	cases := []struct {
		name     string
		cidr     string
		iface    string
		mode     string
		ifc      netutil.Iface
		hasIface bool
		wantKind string
		wantCIDR string
		wantErr  bool
	}{
		// Nothing given: backwards-compatible passive pass (old `{}` body).
		{name: "empty body is passive", wantKind: scanModePassive},
		{name: "auto with nothing is passive", mode: "auto", wantKind: scanModePassive},
		{name: "whitespace cidr and iface are ignored", cidr: "  ", iface: "  ", wantKind: scanModePassive},

		// Explicit CIDR sweeps the CIDR.
		{name: "explicit cidr", cidr: "192.168.1.0/24", wantKind: scanModeCIDR, wantCIDR: "192.168.1.0/24"},
		{name: "explicit cidr is trimmed", cidr: " 10.0.0.0/24 ", wantKind: scanModeCIDR, wantCIDR: "10.0.0.0/24"},
		{name: "mode cidr with cidr", cidr: "10.0.0.0/24", mode: "cidr", wantKind: scanModeCIDR, wantCIDR: "10.0.0.0/24"},
		{name: "mode CIDR is case-insensitive", cidr: "10.0.0.0/24", mode: "CIDR", wantKind: scanModeCIDR, wantCIDR: "10.0.0.0/24"},
		{name: "cidr beats iface", cidr: "10.0.0.0/24", iface: "WLAN", ifc: testIfaceV6, hasIface: true, wantKind: scanModeCIDR, wantCIDR: "10.0.0.0/24"},
		{name: "cidr beats subnet mode", cidr: "10.0.0.0/24", iface: "WLAN", mode: "subnet", ifc: testIfaceV6, hasIface: true, wantKind: scanModeCIDR, wantCIDR: "10.0.0.0/24"},

		// Empty CIDR plus an interface means "sweep this interface's subnet".
		{name: "iface v4 subnet", iface: "以太网", ifc: testIfaceV4, hasIface: true, wantKind: scanModeSubnet, wantCIDR: "192.168.1.0/24"},
		{name: "iface subnet falls back to ip_net", iface: "eth1", ifc: testIfaceV4IPNetOnly, hasIface: true, wantKind: scanModeSubnet, wantCIDR: "10.0.0.5/24"},
		{name: "iface v6 subnet", iface: "WLAN|fe80::1234", ifc: testIfaceV6, hasIface: true, wantKind: scanModeSubnet, wantCIDR: "fe80::/64"},
		{name: "subnet mode with iface", iface: "WLAN", mode: "subnet", ifc: testIfaceV6, hasIface: true, wantKind: scanModeSubnet, wantCIDR: "fe80::/64"},
		{name: "empty mode with iface is subnet", iface: "以太网", mode: "", ifc: testIfaceV4, hasIface: true, wantKind: scanModeSubnet, wantCIDR: "192.168.1.0/24"},

		// Passive wins over everything.
		{name: "passive ignores cidr and iface", cidr: "192.168.1.0/24", iface: "以太网", mode: "passive", ifc: testIfaceV4, hasIface: true, wantKind: scanModePassive},
		{name: "passive without iface", mode: "passive", wantKind: scanModePassive},
		{name: "passive ignores subnet mode inputs", iface: "以太网", mode: "PASSIVE", ifc: testIfaceV4, hasIface: true, wantKind: scanModePassive},

		// Error cases.
		{name: "mode cidr without cidr", mode: "cidr", wantErr: true},
		{name: "subnet mode without iface", mode: "subnet", wantErr: true},
		{name: "iface without usable subnet", iface: "eth9", ifc: testIfaceNoSubnet, hasIface: true, wantErr: true},
		{name: "unknown iface", iface: "eth9", wantErr: true},
		{name: "invalid mode", mode: "turbo", wantErr: true},
		{name: "invalid mode with cidr", cidr: "10.0.0.0/24", mode: "turbo", wantErr: true},
		{name: "invalid mode with iface", iface: "以太网", mode: "turbo", ifc: testIfaceV4, hasIface: true, wantErr: true},
	}

	for _, c := range cases {
		kind, cidrOut, err := resolveScanMode(c.cidr, c.iface, c.mode, c.ifc, c.hasIface)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: resolveScanMode(%q, %q, %q) = (%q, %q, nil), want an error",
					c.name, c.cidr, c.iface, c.mode, kind, cidrOut)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: resolveScanMode(%q, %q, %q) returned error: %v", c.name, c.cidr, c.iface, c.mode, err)
			continue
		}
		if kind != c.wantKind || cidrOut != c.wantCIDR {
			t.Errorf("%s: resolveScanMode(%q, %q, %q) = (%q, %q), want (%q, %q)",
				c.name, c.cidr, c.iface, c.mode, kind, cidrOut, c.wantKind, c.wantCIDR)
		}
	}
}

// An interface of the other address family must not be used as the bind source:
// pinning a v6 source address onto a v4 sweep makes every connect fail at bind
// time, which used to turn "scan this CIDR on this NIC" into a silent empty scan.
func TestBindSourceFamily(t *testing.T) {
	cases := []struct {
		name string
		ifc  netutil.Iface
		cidr string
		want bool
	}{
		{"v4 iface v4 cidr", testIfaceV4, "192.168.1.0/24", true},
		{"v6 iface v6 cidr", testIfaceV6, "fe80::/64", true},
		{"v6 iface v4 cidr", testIfaceV6, "192.168.1.0/24", false},
		{"v4 iface v6 cidr", testIfaceV4, "fe80::/64", false},
		{"invalid cidr leaves it to ProbeCIDR", testIfaceV4, "not-a-cidr", false},
	}
	for _, c := range cases {
		if got := bindSourceFamily(c.ifc, c.cidr); got != c.want {
			t.Errorf("%s: bindSourceFamily(%+v, %q) = %v, want %v", c.name, c.ifc, c.cidr, got, c.want)
		}
	}
}
