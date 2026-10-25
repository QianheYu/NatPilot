package netutil

import (
	"reflect"
	"testing"
)

func TestParsePortSpec(t *testing.T) {
	got, err := ParsePortSpec("80,443,8000-8003", 1000)
	if err != nil {
		t.Fatalf("ParsePortSpec: %v", err)
	}
	want := []int{80, 443, 8000, 8001, 8002, 8003}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParsePortSpec = %v, want %v", got, want)
	}

	// Ranges are normalised and de-duplicated.
	got, err = ParsePortSpec("8080,8080,9000-9000", 100)
	if err != nil {
		t.Fatalf("ParsePortSpec: %v", err)
	}
	if !reflect.DeepEqual(got, []int{8080, 9000}) {
		t.Errorf("ParsePortSpec dedupe = %v", got)
	}

	for _, bad := range []string{"", "0", "70000", "abc", "10-", "8000-9000"} {
		limit := 65535
		if bad == "8000-9000" {
			limit = 10 // exceed the configured maximum
		}
		if _, err := ParsePortSpec(bad, limit); err == nil {
			t.Errorf("ParsePortSpec(%q) expected an error", bad)
		}
	}
}

func TestExpandCIDR(t *testing.T) {
	hosts, err := ExpandCIDR("192.168.1.0/30", 16)
	if err != nil {
		t.Fatalf("ExpandCIDR: %v", err)
	}
	// /30 => 2 usable hosts, network and broadcast excluded.
	if len(hosts) != 2 {
		t.Fatalf("/30 yielded %d hosts (%v), want 2", len(hosts), hosts)
	}
	if hosts[0] != "192.168.1.1" || hosts[1] != "192.168.1.2" {
		t.Errorf("/30 hosts = %v, want [192.168.1.1 192.168.1.2]", hosts)
	}

	// A single address is accepted as /32.
	hosts, err = ExpandCIDR("10.0.0.5", 8)
	if err != nil {
		t.Fatalf("ExpandCIDR single: %v", err)
	}
	if len(hosts) != 1 || hosts[0] != "10.0.0.5" {
		t.Errorf("single host = %v", hosts)
	}

	// Exceeding the host limit is an error rather than a partial scan.
	if _, err := ExpandCIDR("10.0.0.0/16", 100); err == nil {
		t.Errorf("ExpandCIDR should refuse a block larger than maxHosts")
	}
}

func TestParsePortsInRange(t *testing.T) {
	ports, err := ParsePortsInRange("1-1024,8000-8100", 20, 100)
	if err != nil {
		t.Fatalf("ParsePortsInRange: %v", err)
	}
	for _, p := range ports {
		if p < 20 || p > 100 {
			t.Errorf("port %d escaped the [20,100] window", p)
		}
	}
	if len(ports) != 81 {
		t.Errorf("got %d ports, want 81", len(ports))
	}
}

func TestGuessService(t *testing.T) {
	cases := map[int]string{22: "ssh", 80: "http", 443: "https", 5432: "postgres", 6379: "redis"}
	for port, want := range cases {
		if got := GuessService(port); got != want {
			t.Errorf("GuessService(%d) = %q, want %q", port, got, want)
		}
	}
	if got := GuessService(65000); got != "" {
		t.Errorf("GuessService(65000) = %q, want empty", got)
	}
}

func TestMACVendor(t *testing.T) {
	if v := MACVendor("b8:27:eb:12:34:56"); v != "Raspberry Pi" {
		t.Errorf("MACVendor(Raspberry Pi) = %q", v)
	}
	if v := MACVendor("52:54:00:aa:bb:cc"); v != "QEMU" {
		t.Errorf("MACVendor(QEMU) = %q", v)
	}
	if v := MACVendor("not-a-mac"); v != "" {
		t.Errorf("MACVendor(invalid) = %q, want empty", v)
	}
}
