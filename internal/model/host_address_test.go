package model

import "testing"

func TestIPFamily(t *testing.T) {
	cases := []struct{ ip, want string }{
		{"192.168.1.1", FamilyIPv4},
		{"10.0.0.1", FamilyIPv4},
		{"::1", FamilyIPv6},
		{"2001:db8::1", FamilyIPv6},
		{"fe80::1%eth0", FamilyIPv6},
		{"[2001:db8::1]", FamilyIPv6},
		{"", ""},
		{"not-an-ip", ""},
		{"192.168.1", ""},
	}
	for _, c := range cases {
		if got := IPFamily(c.ip); got != c.want {
			t.Errorf("IPFamily(%q) = %q, want %q", c.ip, got, c.want)
		}
	}
}

func TestHostAddressOf(t *testing.T) {
	h := &Host{
		IP: "192.168.1.5",
		Addresses: []HostAddress{
			{IP: "2001:db8::5", Family: FamilyIPv6},
			{IP: "192.168.1.5", Family: FamilyIPv4, Primary: true},
		},
	}
	a, ok := h.AddressOf(FamilyIPv4)
	if !ok || a.IP != "192.168.1.5" || !a.Primary {
		t.Fatalf("AddressOf(ipv4) = %+v/%v", a, ok)
	}
	a, ok = h.AddressOf("IPv6")
	if !ok || a.IP != "2001:db8::5" {
		t.Fatalf("AddressOf(ipv6) = %+v/%v", a, ok)
	}
	if _, ok := h.AddressOf("ipx"); ok {
		t.Fatalf("unknown family must not match")
	}

	// Addresses not populated: fall back to the legacy IP field.
	legacy := &Host{IP: "2001:db8::9"}
	if a, ok := legacy.AddressOf(FamilyIPv6); !ok || a.IP != "2001:db8::9" {
		t.Fatalf("legacy AddressOf(ipv6) = %+v/%v", a, ok)
	}
	if _, ok := legacy.AddressOf(FamilyIPv4); ok {
		t.Fatalf("legacy IPv6 host must not report an IPv4 address")
	}
}
