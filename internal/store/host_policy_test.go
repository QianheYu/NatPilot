package store

import (
	"testing"

	"github.com/ekko-studio/natpilot/internal/model"
)

func TestNormalizeMAC(t *testing.T) {
	want := "aa:bb:cc:dd:ee:ff"
	for _, raw := range []string{
		"AA-BB-CC-DD-EE-FF",
		"aa:bb:cc:dd:ee:ff",
		"aabb.ccdd.eeff",
		"AA:BB:CC:DD:EE:FF",
		"  aa-bb-cc-dd-ee-ff  ",
	} {
		if got := normalizeMAC(raw); got != want {
			t.Fatalf("normalizeMAC(%q) = %q, want %q", raw, got, want)
		}
	}
	if got := normalizeMAC(""); got != "" {
		t.Fatalf("empty MAC must stay empty, got %q", got)
	}
	if got := normalizeMAC("not-a-mac"); got != "not-a-mac" {
		t.Fatalf("unrecognised MAC should be kept verbatim, got %q", got)
	}
}

// TestHostIdentityFollowsMAC is the DHCP case: the same device is observed at a
// new address, and it must update the existing row instead of creating a second
// one — otherwise a policy bound to the MAC would point at a stale entry.
func TestHostIdentityFollowsMAC(t *testing.T) {
	st := newTestStore(t)

	first := &model.Host{IP: "192.168.1.20", MAC: "AA-BB-CC-DD-EE-01", Hostname: "nas", Source: "arp"}
	if err := st.UpsertHost(first); err != nil {
		t.Fatalf("insert host: %v", err)
	}
	if first.ID == 0 {
		t.Fatal("host id was not assigned")
	}

	// DHCP hands out a different address for the very same NIC.
	moved := &model.Host{IP: "192.168.1.77", MAC: "aa:bb:cc:dd:ee:01", Hostname: "nas", Source: "arp"}
	if err := st.UpsertHost(moved); err != nil {
		t.Fatalf("update host: %v", err)
	}
	if moved.ID != first.ID {
		t.Fatalf("host id changed on IP move: got %d, want %d", moved.ID, first.ID)
	}

	hosts, err := st.ListHosts(50)
	if err != nil {
		t.Fatalf("list hosts: %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("expected a single row for one MAC, got %d: %+v", len(hosts), hosts)
	}
	if hosts[0].IP != "192.168.1.77" {
		t.Fatalf("row still points at the old address: %q", hosts[0].IP)
	}
	if hosts[0].MAC != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("MAC not normalised: %q", hosts[0].MAC)
	}

	byMAC, err := st.GetHostByMAC("AA-BB-CC-DD-EE-01")
	if err != nil || byMAC == nil {
		t.Fatalf("GetHostByMAC failed: %v %v", byMAC, err)
	}
	if byMAC.IP != "192.168.1.77" {
		t.Fatalf("lookup by MAC returned the stale address %q", byMAC.IP)
	}
}

// TestHostWithoutMACIsMatchedByIP keeps loopback/virtual adapters working: they
// have no MAC, so the IP remains their identity.
func TestHostWithoutMACIsMatchedByIP(t *testing.T) {
	st := newTestStore(t)
	a := &model.Host{IP: "127.0.0.1", IsLocal: true, Source: "interface"}
	if err := st.UpsertHost(a); err != nil {
		t.Fatalf("insert: %v", err)
	}
	b := &model.Host{IP: "127.0.0.1", IsLocal: true, Source: "interface"}
	if err := st.UpsertHost(b); err != nil {
		t.Fatalf("update: %v", err)
	}
	if b.ID != a.ID {
		t.Fatalf("MAC-less host duplicated: %d vs %d", b.ID, a.ID)
	}
	if b.SeenCount != 2 {
		t.Fatalf("seen_count = %d, want 2", b.SeenCount)
	}
}

// TestHostPoliciesByMAC covers the whole point of the feature: several policies
// can follow one host, they survive an address change, and each reports where
// the host currently is.
func TestHostPoliciesByMAC(t *testing.T) {
	st := newTestStore(t)

	host := &model.Host{IP: "192.168.1.30", MAC: "aa:bb:cc:dd:ee:30", Hostname: "camera", Source: "arp"}
	if err := st.UpsertHost(host); err != nil {
		t.Fatalf("insert host: %v", err)
	}

	ssh, err := st.CreatePolicy(&model.Policy{
		Name: "camera ssh", MAC: host.MAC, MatchTarget: "host",
		PortRanges: "22", Protocol: "tcp", AutoAction: model.ActionAdd, Priority: 5, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create policy 1: %v", err)
	}
	web, err := st.CreatePolicy(&model.Policy{
		Name: "camera web", MAC: host.MAC, MatchTarget: "host",
		PortRanges: "80,443", Protocol: "tcp", AutoAction: model.ActionWatch, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create policy 2: %v", err)
	}
	if ssh == web {
		t.Fatal("two policies must be distinct rows")
	}

	got, err := st.ListPoliciesByHost("AA-BB-CC-DD-EE-30")
	if err != nil {
		t.Fatalf("list by host: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 policies for the host, got %d", len(got))
	}
	// Priority ordering: the ssh policy has priority 5.
	if got[0].Name != "camera ssh" {
		t.Fatalf("policies not ordered by priority: %+v", got)
	}
	if got[0].HostIP != "192.168.1.30" {
		t.Fatalf("host address not filled in: %+v", got[0])
	}

	// The device moves; the policies must now report the new address.
	moved := &model.Host{IP: "192.168.1.99", MAC: host.MAC, Hostname: "camera", Source: "arp"}
	if err := st.UpsertHost(moved); err != nil {
		t.Fatalf("move host: %v", err)
	}
	again, err := st.ListPoliciesByHost(host.MAC)
	if err != nil {
		t.Fatalf("list after move: %v", err)
	}
	if len(again) != 2 {
		t.Fatalf("policies lost after DHCP move: %d", len(again))
	}
	if again[0].HostIP != "192.168.1.99" {
		t.Fatalf("policy did not follow the host to its new address: %q", again[0].HostIP)
	}

	// A global policy must not be attributed to the host.
	if _, err := st.CreatePolicy(&model.Policy{Name: "global", MatchTarget: "any", Enabled: true}); err != nil {
		t.Fatalf("create global policy: %v", err)
	}
	still, _ := st.ListPoliciesByHost(host.MAC)
	if len(still) != 2 {
		t.Fatalf("global policy leaked into the host list: %d", len(still))
	}
}
