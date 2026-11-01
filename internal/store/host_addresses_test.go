package store

import (
	"testing"

	"github.com/ekko-studio/natpilot/internal/model"
)

// hostAddrByIP returns one of the host's stored addresses by IP.
func hostAddrByIP(t *testing.T, h model.Host, ip string) model.HostAddress {
	t.Helper()
	for _, a := range h.Addresses {
		if a.IP == ip {
			return a
		}
	}
	t.Fatalf("host %d has no address %s (addresses: %+v)", h.ID, ip, h.Addresses)
	return model.HostAddress{}
}

// TestUpsertHostMergesDualStack is the regression this feature exists for: the
// same device is first seen over ARP (IPv4) and then over NDP (IPv6). It must
// stay one host row that remembers both families, with the IPv4 address as the
// primary one.
func TestUpsertHostMergesDualStack(t *testing.T) {
	st := newTestStore(t)

	v4 := &model.Host{IP: "192.168.1.50", MAC: "aa:bb:cc:dd:ee:50", Hostname: "nas", Source: "arp"}
	if err := st.UpsertHost(v4); err != nil {
		t.Fatalf("upsert IPv4 observation: %v", err)
	}
	v6 := &model.Host{IP: "2001:db8::50", MAC: "AA-BB-CC-DD-EE-50", Hostname: "nas", Source: "ndp"}
	if err := st.UpsertHost(v6); err != nil {
		t.Fatalf("upsert IPv6 observation: %v", err)
	}
	if v6.ID != v4.ID {
		t.Fatalf("dual-stack observation created a second host row: %d vs %d", v6.ID, v4.ID)
	}

	hosts, err := st.ListHosts(50)
	if err != nil {
		t.Fatalf("list hosts: %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("expected one host row, got %d: %+v", len(hosts), hosts)
	}
	h := hosts[0]
	if len(h.Addresses) != 2 {
		t.Fatalf("expected both addresses, got %+v", h.Addresses)
	}
	if h.IP != "192.168.1.50" {
		t.Fatalf("primary address = %q, want the global IPv4 address", h.IP)
	}

	v4Addr := hostAddrByIP(t, h, "192.168.1.50")
	if v4Addr.Family != model.FamilyIPv4 || !v4Addr.Primary {
		t.Fatalf("IPv4 address = %+v, want family ipv4 and primary", v4Addr)
	}
	v6Addr := hostAddrByIP(t, h, "2001:db8::50")
	if v6Addr.Family != model.FamilyIPv6 || v6Addr.Primary {
		t.Fatalf("IPv6 address = %+v, want family ipv6 and not primary", v6Addr)
	}

	byFamily, ok := h.AddressOf(model.FamilyIPv6)
	if !ok || byFamily.IP != "2001:db8::50" {
		t.Fatalf("AddressOf(ipv6) = %+v/%v", byFamily, ok)
	}
}

// A device seen only over IPv6 keeps its IPv6 address as primary; when it later
// shows up over IPv4 as well, both are kept and the IPv4 address is promoted
// (the address services are usually reachable on).
func TestUpsertHostPromotesIPv4(t *testing.T) {
	st := newTestStore(t)

	v6 := &model.Host{IP: "2001:db8::60", MAC: "aa:bb:cc:dd:ee:60", Source: "ndp"}
	if err := st.UpsertHost(v6); err != nil {
		t.Fatalf("upsert IPv6-only host: %v", err)
	}
	hosts, err := st.ListHosts(50)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("list hosts: %v %+v", err, hosts)
	}
	if hosts[0].IP != "2001:db8::60" {
		t.Fatalf("IPv6-only host primary = %q, want the IPv6 address", hosts[0].IP)
	}
	if len(hosts[0].Addresses) != 1 || hosts[0].Addresses[0].Family != model.FamilyIPv6 {
		t.Fatalf("IPv6-only host addresses = %+v", hosts[0].Addresses)
	}

	v4 := &model.Host{IP: "192.168.1.60", MAC: v6.MAC, Source: "arp"}
	if err := st.UpsertHost(v4); err != nil {
		t.Fatalf("upsert IPv4 observation: %v", err)
	}
	if v4.ID != v6.ID {
		t.Fatalf("IPv4 discovery created a new row instead of merging: %d vs %d", v4.ID, v6.ID)
	}

	hosts, err = st.ListHosts(50)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("list hosts after merge: %v %+v", err, hosts)
	}
	h := hosts[0]
	if h.IP != "192.168.1.60" {
		t.Fatalf("primary address = %q, want the promoted IPv4 address", h.IP)
	}
	if len(h.Addresses) != 2 {
		t.Fatalf("expected both addresses after merge, got %+v", h.Addresses)
	}
	if a := hostAddrByIP(t, h, "192.168.1.60"); !a.Primary || a.Family != model.FamilyIPv4 {
		t.Fatalf("IPv4 address = %+v, want primary ipv4", a)
	}
	if a := hostAddrByIP(t, h, "2001:db8::60"); a.Primary || a.Family != model.FamilyIPv6 {
		t.Fatalf("IPv6 address = %+v, want non-primary ipv6", a)
	}
}

// Link-local IPv6 addresses need their interface scope (zone) to be usable, and
// must not win over a global/ULA address just because they were observed last.
func TestUpsertHostStoresLinkLocalZone(t *testing.T) {
	st := newTestStore(t)

	h := &model.Host{
		IP: "fd00::70", MAC: "aa:bb:cc:dd:ee:70", Source: "ndp",
		Addresses: []model.HostAddress{
			{IP: "fd00::70", Family: model.FamilyIPv6, Source: "ndp"},
			{IP: "fe80::70", Family: model.FamilyIPv6, Zone: "eth0", Source: "ndp"},
		},
	}
	if err := st.UpsertHost(h); err != nil {
		t.Fatalf("upsert dual IPv6 host: %v", err)
	}

	hosts, err := st.ListHosts(50)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("list hosts: %v %+v", err, hosts)
	}
	got := hosts[0]
	if got.IP != "fd00::70" {
		t.Fatalf("primary address = %q, want the ULA address over the link-local one", got.IP)
	}
	linkLocal := hostAddrByIP(t, got, "fe80::70")
	if linkLocal.Zone != "eth0" {
		t.Fatalf("link-local zone = %q, want eth0", linkLocal.Zone)
	}
	if linkLocal.Primary || linkLocal.Family != model.FamilyIPv6 {
		t.Fatalf("link-local address = %+v, want non-primary ipv6", linkLocal)
	}

	// A zone embedded in the address string is accepted and split off too.
	embedded := &model.Host{IP: "fe80::71%eth1", MAC: "aa:bb:cc:dd:ee:71", Source: "ndp"}
	if err := st.UpsertHost(embedded); err != nil {
		t.Fatalf("upsert embedded-zone host: %v", err)
	}
	byIP, err := st.GetHostByIP("fe80::71")
	if err != nil || byIP == nil {
		t.Fatalf("GetHostByIP(link-local) = %+v/%v", byIP, err)
	}
	if a := hostAddrByIP(t, *byIP, "fe80::71"); a.Zone != "eth1" {
		t.Fatalf("embedded zone = %q, want eth1", a.Zone)
	}
	if byIP.IP != "fe80::71" {
		t.Fatalf("link-local-only host primary = %q, want the bare IPv6 address", byIP.IP)
	}
}

// TestMigrateSeedsHostAddressesOnce upgrades a database written before
// host_addresses existed and checks that running Migrate again neither loses nor
// duplicates the seeded addresses. NULL first_seen/last_seen columns (legal on
// old databases) must not abort the migration.
func TestMigrateSeedsHostAddressesOnce(t *testing.T) {
	st := newTestStore(t)

	if _, err := st.db.Exec(`DROP TABLE host_addresses`); err != nil {
		t.Fatalf("drop host_addresses to emulate an old database: %v", err)
	}
	if _, err := st.db.Exec(`INSERT INTO hosts(ip, mac, source) VALUES('192.168.1.80','aa:bb:cc:dd:ee:80','arp')`); err != nil {
		t.Fatalf("seed legacy IPv4 host: %v", err)
	}
	if _, err := st.db.Exec(`INSERT INTO hosts(ip, mac, source) VALUES('2001:db8::81','aa:bb:cc:dd:ee:81','ndp')`); err != nil {
		t.Fatalf("seed legacy IPv6 host: %v", err)
	}

	for run := 1; run <= 2; run++ {
		if err := st.Migrate(); err != nil {
			t.Fatalf("migrate run %d: %v", run, err)
		}
		var n int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM host_addresses`).Scan(&n); err != nil {
			t.Fatalf("count host_addresses after run %d: %v", run, err)
		}
		if n != 2 {
			t.Fatalf("run %d seeded %d address rows, want exactly 2 (idempotent migration)", run, n)
		}
	}

	var family string
	if err := st.db.QueryRow(`SELECT family FROM host_addresses WHERE ip='2001:db8::81'`).Scan(&family); err != nil {
		t.Fatalf("read seeded IPv6 family: %v", err)
	}
	if family != model.FamilyIPv6 {
		t.Fatalf("seeded IPv6 family = %q, want ipv6", family)
	}
	if err := st.db.QueryRow(`SELECT family FROM host_addresses WHERE ip='192.168.1.80'`).Scan(&family); err != nil {
		t.Fatalf("read seeded IPv4 family: %v", err)
	}
	if family != model.FamilyIPv4 {
		t.Fatalf("seeded IPv4 family = %q, want ipv4", family)
	}

	// The migrated rows expose their seeded address through the normal read path.
	hosts, err := st.ListHosts(50)
	if err != nil {
		t.Fatalf("list hosts after migration: %v", err)
	}
	for _, h := range hosts {
		if len(h.Addresses) != 1 || h.Addresses[0].IP != h.IP {
			t.Fatalf("host %d addresses after migration = %+v (ip %q)", h.ID, h.Addresses, h.IP)
		}
	}
}

// DeleteHost must remove the address rows explicitly: relying on ON DELETE
// CASCADE is not safe because foreign key enforcement is off by default in
// SQLite and the store's DSN does not always enable it.
func TestDeleteHostRemovesAddresses(t *testing.T) {
	st := newTestStore(t)

	h := &model.Host{IP: "192.168.1.90", MAC: "aa:bb:cc:dd:ee:90", Source: "arp"}
	if err := st.UpsertHost(h); err != nil {
		t.Fatalf("insert host: %v", err)
	}
	if err := st.UpsertHost(&model.Host{IP: "2001:db8::90", MAC: h.MAC, Source: "ndp"}); err != nil {
		t.Fatalf("merge IPv6 address: %v", err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM host_addresses WHERE host_id=?`, h.ID).Scan(&n); err != nil {
		t.Fatalf("count addresses: %v", err)
	}
	if n != 2 {
		t.Fatalf("address rows before delete = %d, want 2", n)
	}

	if err := st.DeleteHost(h.ID); err != nil {
		t.Fatalf("delete host: %v", err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM host_addresses WHERE host_id=?`, h.ID).Scan(&n); err != nil {
		t.Fatalf("count addresses after delete: %v", err)
	}
	if n != 0 {
		t.Fatalf("address rows survived DeleteHost: %d", n)
	}
	hosts, err := st.ListHosts(50)
	if err != nil {
		t.Fatalf("list hosts after delete: %v", err)
	}
	if len(hosts) != 0 {
		t.Fatalf("host survived DeleteHost: %+v", hosts)
	}
}

// ListHosts must fill Addresses for every host in the result (the implementation
// groups one batched query, but the test asserts the observable data).
func TestListHostsLoadsAddresses(t *testing.T) {
	st := newTestStore(t)

	dual := &model.Host{IP: "192.168.1.100", MAC: "aa:bb:cc:dd:ee:10", Source: "arp"}
	if err := st.UpsertHost(dual); err != nil {
		t.Fatalf("insert dual-stack host: %v", err)
	}
	if err := st.UpsertHost(&model.Host{IP: "2001:db8::100", MAC: dual.MAC, Source: "ndp"}); err != nil {
		t.Fatalf("merge dual-stack address: %v", err)
	}
	v6only := &model.Host{IP: "fd00::101", MAC: "aa:bb:cc:dd:ee:11", Source: "ndp"}
	if err := st.UpsertHost(v6only); err != nil {
		t.Fatalf("insert IPv6-only host: %v", err)
	}
	plain := &model.Host{IP: "127.0.0.1", MAC: "", Source: "interface"}
	if err := st.UpsertHost(plain); err != nil {
		t.Fatalf("insert MAC-less host: %v", err)
	}

	hosts, err := st.ListHosts(50)
	if err != nil {
		t.Fatalf("list hosts: %v", err)
	}
	if len(hosts) != 3 {
		t.Fatalf("expected 3 hosts, got %d: %+v", len(hosts), hosts)
	}
	want := map[int64]int{dual.ID: 2, v6only.ID: 1, plain.ID: 1}
	for _, h := range hosts {
		n, ok := want[h.ID]
		if !ok {
			t.Fatalf("unexpected host %+v", h)
		}
		if len(h.Addresses) != n {
			t.Fatalf("host %d addresses = %+v, want %d", h.ID, h.Addresses, n)
		}
		primary := 0
		for _, a := range h.Addresses {
			if a.Family != model.FamilyIPv4 && a.Family != model.FamilyIPv6 {
				t.Fatalf("host %d address has no family: %+v", h.ID, a)
			}
			if a.Primary {
				primary++
				if a.IP != h.IP {
					t.Fatalf("host %d primary address %q != Host.IP %q", h.ID, a.IP, h.IP)
				}
			}
		}
		if primary != 1 {
			t.Fatalf("host %d has %d primary addresses, want 1: %+v", h.ID, primary, h.Addresses)
		}
	}
}

// An address already owned by another host row must stay with that row: two
// devices cannot share one hosts.ip because of the legacy unique index, and the
// observation must still succeed instead of failing.
func TestUpsertHostKeepsAddressWithItsOwner(t *testing.T) {
	st := newTestStore(t)

	owner := &model.Host{IP: "192.168.1.110", MAC: "aa:bb:cc:dd:ee:21", Source: "arp"}
	if err := st.UpsertHost(owner); err != nil {
		t.Fatalf("insert owner: %v", err)
	}
	newcomer := &model.Host{
		IP: "192.168.1.111", MAC: "aa:bb:cc:dd:ee:22", Source: "arp",
		Addresses: []model.HostAddress{
			{IP: "192.168.1.110", Family: model.FamilyIPv4, Source: "arp"},
			{IP: "192.168.1.111", Family: model.FamilyIPv4, Source: "arp"},
		},
	}
	if err := st.UpsertHost(newcomer); err != nil {
		t.Fatalf("upsert newcomer with a conflicting address: %v", err)
	}
	if newcomer.ID == owner.ID {
		t.Fatalf("different MACs merged into one row")
	}
	if newcomer.IP != "192.168.1.111" {
		t.Fatalf("newcomer primary = %q, want its own address", newcomer.IP)
	}

	hosts, err := st.ListHosts(50)
	if err != nil {
		t.Fatalf("list hosts: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("expected 2 host rows, got %d: %+v", len(hosts), hosts)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM host_addresses WHERE ip='192.168.1.110'`).Scan(&n); err != nil {
		t.Fatalf("count conflicting address: %v", err)
	}
	if n != 1 {
		t.Fatalf("conflicting address stored %d times, want 1", n)
	}
	var ownerID int64
	if err := st.db.QueryRow(`SELECT host_id FROM host_addresses WHERE ip='192.168.1.110'`).Scan(&ownerID); err != nil {
		t.Fatalf("read conflicting address owner: %v", err)
	}
	if ownerID != owner.ID {
		t.Fatalf("conflicting address moved from host %d to %d", owner.ID, ownerID)
	}
}
