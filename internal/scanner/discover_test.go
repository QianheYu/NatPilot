package scanner

import (
	"context"
	"testing"
	"time"

	"github.com/ekko-studio/natpilot/internal/netutil"
)

func TestHostProbeNormalize(t *testing.T) {
	p := HostProbe{CIDR: " 192.168.1.0/24 "}
	if err := p.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if p.CIDR != "192.168.1.0/24" {
		t.Fatalf("cidr not trimmed: %q", p.CIDR)
	}
	if len(p.Ports) == 0 || p.Timeout != 400*time.Millisecond || p.Concurrency != 128 {
		t.Fatalf("defaults not applied: %+v", p)
	}

	bad := []HostProbe{
		{},                                  // 缺 cidr
		{CIDR: "not-a-cidr"},                // 非法 cidr
		{CIDR: "10.0.0.0/8", SourceIP: "x"}, // 非法 source ip
		{CIDR: "10.0.0.0/8", Ports: []int{0}},
		{CIDR: "10.0.0.0/8", Ports: []int{70000}},
	}
	for i := range bad {
		if err := bad[i].Normalize(); err == nil {
			t.Fatalf("case %d should fail: %+v", i, bad[i])
		}
	}
}

func TestParseProbePorts(t *testing.T) {
	if got, err := ParseProbePorts(""); err != nil || got != nil {
		t.Fatalf("empty spec = %v, %v", got, err)
	}
	got, err := ParseProbePorts(" 80, 443 ,, 22 ")
	if err != nil || len(got) != 3 || got[0] != 80 || got[2] != 22 {
		t.Fatalf("parsed %v, %v", got, err)
	}
	if _, err := ParseProbePorts("80,x"); err == nil {
		t.Fatal("invalid port should fail")
	}
}

// Unzoned link-local destinations cannot be dialled, so ProbeCIDR reports them
// as skipped instead of pretending it scanned something. No socket is opened.
func TestProbeCIDRSkipsUnzonedLinkLocal(t *testing.T) {
	alive, skipped, err := ProbeCIDR(context.Background(), HostProbe{CIDR: "fe80::/126"})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(alive) != 0 {
		t.Fatalf("alive = %v, want none (nothing was dialled)", alive)
	}
	if skipped != 3 {
		t.Fatalf("skipped = %d, want 3 (fe80::1..fe80::3)", skipped)
	}
}

func TestPickInterface(t *testing.T) {
	ifaces := []netutil.Iface{
		{Name: "以太网", IP: "192.168.1.10"},
		{Name: "eth1", IP: "10.0.0.5"},
	}
	if i, ok := PickInterface(ifaces, "以太网"); !ok || i.IP != "192.168.1.10" {
		t.Fatalf("by name: %+v %v", i, ok)
	}
	if i, ok := PickInterface(ifaces, "10.0.0.5"); !ok || i.Name != "eth1" {
		t.Fatalf("by ip: %+v %v", i, ok)
	}
	if _, ok := PickInterface(ifaces, ""); ok {
		t.Fatal("empty ref must report not found")
	}
	if _, ok := PickInterface(ifaces, "wlan9"); ok {
		t.Fatal("unknown interface must report not found")
	}
}

// A dual-stack host lists two entries with the same Name. Selecting by bare
// name must keep returning the first (IPv4) entry, while the composite
// "<name>|<ip>" key the host-scan UI sends must select the exact entry.
func TestPickInterfaceDualStack(t *testing.T) {
	ifaces := []netutil.Iface{
		{Name: "WLAN", Index: 12, IP: "192.168.1.10", Family: netutil.FamilyIPv4},
		{Name: "WLAN", Index: 12, IP: "fe80::1234", Zone: "WLAN", Scoped: true, Family: netutil.FamilyIPv6},
	}
	cases := []struct {
		name string
		ref  string
		want string // expected IP, "" means not found
	}{
		{"plain name keeps first match", "WLAN", "192.168.1.10"},
		{"plain IPv4", "192.168.1.10", "192.168.1.10"},
		{"plain IPv6 without zone", "fe80::1234", "fe80::1234"},
		{"plain IPv6 with numeric zone", "fe80::1234%12", "fe80::1234"},
		{"plain IPv6 with name zone", "fe80::1234%WLAN", "fe80::1234"},
		{"composite IPv4", "WLAN|192.168.1.10", "192.168.1.10"},
		{"composite IPv6", "WLAN|fe80::1234", "fe80::1234"},
		{"composite IPv6 with zone", "WLAN|fe80::1234%12", "fe80::1234"},
		{"composite tolerates spaces", " WLAN | fe80::1234 ", "fe80::1234"},
		{"composite without name", "|fe80::1234", "fe80::1234"},
		{"composite wrong address", "WLAN|10.9.9.9", ""},
		{"composite wrong name", "eth9|fe80::1234", ""},
		{"composite invalid address", "WLAN|not-an-ip", ""},
	}
	for _, c := range cases {
		i, ok := PickInterface(ifaces, c.ref)
		if c.want == "" {
			if ok {
				t.Errorf("%s: PickInterface(%q) = %+v, want not found", c.name, c.ref, i)
			}
			continue
		}
		if !ok || i.IP != c.want {
			t.Errorf("%s: PickInterface(%q) = (%+v, %v), want IP %q", c.name, c.ref, i, ok, c.want)
		}
	}
}

// A zone in a bare address only disambiguates entries that share the address;
// link-local addresses can legitimately repeat on different links.
func TestPickInterfaceZonePreference(t *testing.T) {
	ifaces := []netutil.Iface{
		{Name: "eth0", Index: 3, IP: "fe80::1", Zone: "eth0", Scoped: true, Family: netutil.FamilyIPv6},
		{Name: "eth1", Index: 4, IP: "fe80::1", Zone: "eth1", Scoped: true, Family: netutil.FamilyIPv6},
	}
	for _, c := range []struct{ ref, want string }{
		{"fe80::1%eth1", "eth1"},
		{"fe80::1%4", "eth1"},
		{"fe80::1", "eth0"}, // first entry without a zone hint
	} {
		i, ok := PickInterface(ifaces, c.ref)
		if !ok || i.Name != c.want {
			t.Errorf("PickInterface(%q) = (%+v, %v), want %q", c.ref, i, ok, c.want)
		}
	}
}

func TestDialHost(t *testing.T) {
	cases := []struct {
		name string
		host string
		zone string
		want string
	}{
		{"plain ipv4", "192.168.1.10", "12", "192.168.1.10"},
		{"global ipv6", "2001:db8::1", "12", "2001:db8::1"},
		{"link-local gets target zone", "fe80::1", "12", "fe80::1%12"},
		{"link-local keeps its own zone", "fe80::1%9", "12", "fe80::1%9"},
		{"link-local without any zone", "fe80::1", "", "fe80::1"},
		{"hostname", "router.local", "12", "router.local"},
		{"bracketed link-local", "[fe80::1]", "WLAN", "fe80::1%WLAN"},
		{"ipv4-mapped ipv6", "::ffff:192.168.1.10", "12", "::ffff:192.168.1.10"},
		{"zone with percent prefix", "fe80::1", "%WLAN", "fe80::1%WLAN"},
	}
	for _, c := range cases {
		if got := dialHost(c.host, c.zone); got != c.want {
			t.Errorf("%s: dialHost(%q, %q) = %q, want %q", c.name, c.host, c.zone, got, c.want)
		}
	}
}

func TestLinkLocalNeedsZone(t *testing.T) {
	cases := []struct {
		host, zone string
		want       bool
	}{
		{"fe80::1", "", true},
		{"fe80::1", "WLAN", false},
		{"fe80::1%9", "", false},
		{"2001:db8::1", "", false},
		{"192.168.1.1", "", false},
	}
	for _, c := range cases {
		if got := linkLocalNeedsZone(c.host, c.zone); got != c.want {
			t.Errorf("linkLocalNeedsZone(%q, %q) = %v, want %v", c.host, c.zone, got, c.want)
		}
	}
}

// Normalize must accept an IPv6 source (subnet scans bind to the selected v6
// interface) while still rejecting garbage, and must normalise both zones.
func TestHostProbeNormalizeZones(t *testing.T) {
	p := HostProbe{
		CIDR:       "fe80::/64",
		SourceIP:   "fe80::1234%12",
		TargetZone: "  %WLAN  ",
	}
	if err := p.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if p.SourceIP != "fe80::1234" || p.SourceZone != "12" {
		t.Fatalf("source not normalised: %+v", p)
	}
	if p.TargetZone != "WLAN" {
		t.Fatalf("target zone not normalised: %q", p.TargetZone)
	}
}
