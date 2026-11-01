package scanner

import (
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
