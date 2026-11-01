package store

import (
	"testing"

	"github.com/ekko-studio/natpilot/internal/model"
)

// The legacy tables mirror the pre-host_id schema: no host_id/family columns.
const legacyIdentityHostsTable = `CREATE TABLE hosts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ip TEXT NOT NULL,
	mac TEXT NOT NULL DEFAULT '',
	hostname TEXT NOT NULL DEFAULT '',
	vendor TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL DEFAULT '',
	iface TEXT NOT NULL DEFAULT '',
	is_local INTEGER NOT NULL DEFAULT 0,
	seen_count INTEGER NOT NULL DEFAULT 1,
	open_ports TEXT NOT NULL DEFAULT '[]',
	tags TEXT NOT NULL DEFAULT '[]',
	note TEXT NOT NULL DEFAULT '',
	first_seen TEXT,
	last_seen TEXT,
	source TEXT NOT NULL DEFAULT ''
)`

const legacyIdentityPortsTable = `CREATE TABLE ports (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	scan_id INTEGER NOT NULL DEFAULT 0,
	rule_id INTEGER NOT NULL DEFAULT 0,
	host TEXT NOT NULL,
	port INTEGER NOT NULL,
	protocol TEXT NOT NULL DEFAULT 'tcp',
	status TEXT NOT NULL DEFAULT 'not_added',
	action TEXT NOT NULL DEFAULT '',
	added INTEGER NOT NULL DEFAULT 0,
	ignored INTEGER NOT NULL DEFAULT 0,
	process TEXT NOT NULL DEFAULT '',
	pid INTEGER NOT NULL DEFAULT 0,
	banner TEXT NOT NULL DEFAULT '',
	service TEXT NOT NULL DEFAULT '',
	latency_ms INTEGER NOT NULL DEFAULT 0,
	tags TEXT NOT NULL DEFAULT '[]',
	note TEXT NOT NULL DEFAULT '',
	target_kind TEXT NOT NULL DEFAULT '',
	target_ref TEXT NOT NULL DEFAULT '',
	first_seen TEXT,
	last_seen TEXT
)`

const legacyIdentityRulesTable = `CREATE TABLE rules (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL DEFAULT '',
	host TEXT NOT NULL DEFAULT '127.0.0.1',
	port INTEGER NOT NULL,
	ext_port INTEGER NOT NULL DEFAULT 0,
	protocol TEXT NOT NULL DEFAULT 'tcp',
	tags TEXT NOT NULL DEFAULT '[]',
	note TEXT NOT NULL DEFAULT '',
	enabled INTEGER NOT NULL DEFAULT 1,
	auto_created INTEGER NOT NULL DEFAULT 0,
	source TEXT NOT NULL DEFAULT 'manual',
	created_at TEXT,
	updated_at TEXT
)`

// TestMigrateBackfillsHostIDAndFamily upgrades a database written before the
// host_id columns existed: rows keyed by a known IPv4 or IPv6 address resolve to
// the owning host and get the address family; an address no host claims stays at
// host_id 0.
func TestMigrateBackfillsHostIDAndFamily(t *testing.T) {
	st := openRaw(t)
	for _, ddl := range []string{legacyIdentityHostsTable, legacyIdentityPortsTable, legacyIdentityRulesTable} {
		if _, err := st.db.Exec(ddl); err != nil {
			t.Fatalf("create legacy table: %v", err)
		}
	}
	if _, err := st.db.Exec(`INSERT INTO hosts(ip, mac, hostname) VALUES
		('192.168.1.10', 'aa:bb:cc:dd:ee:10', 'v4-device'),
		('2001:db8::20', 'bb:cc:dd:ee:ff:20', 'v6-device')`); err != nil {
		t.Fatalf("seed hosts: %v", err)
	}
	if _, err := st.db.Exec(`INSERT INTO ports(host, port, protocol, status) VALUES
		('192.168.1.10', 80, 'tcp', 'open'),
		('2001:db8::20', 443, 'tcp', 'open'),
		('203.0.113.99', 999, 'tcp', 'open')`); err != nil {
		t.Fatalf("seed ports: %v", err)
	}
	if _, err := st.db.Exec(`INSERT INTO rules(host, port, protocol) VALUES
		('2001:db8::20', 8080, 'tcp'),
		('198.51.100.7', 22, 'tcp')`); err != nil {
		t.Fatalf("seed rules: %v", err)
	}

	hostID := func(ip string) int64 {
		t.Helper()
		var id int64
		if err := st.db.QueryRow(`SELECT id FROM hosts WHERE ip=?`, ip).Scan(&id); err != nil {
			t.Fatalf("read host %s: %v", ip, err)
		}
		return id
	}
	v4ID, v6ID := hostID("192.168.1.10"), hostID("2001:db8::20")

	for run := 1; run <= 2; run++ {
		if err := st.Migrate(); err != nil {
			t.Fatalf("migrate run %d: %v", run, err)
		}
		cases := []struct {
			table   string
			port    int
			wantID  int64
			wantFam string
		}{
			{"ports", 80, v4ID, model.FamilyIPv4},
			{"ports", 443, v6ID, model.FamilyIPv6},
			{"ports", 999, 0, model.FamilyIPv4},
			{"rules", 8080, v6ID, model.FamilyIPv6},
			{"rules", 22, 0, model.FamilyIPv4},
		}
		for _, c := range cases {
			var gotID int64
			var gotFam string
			if err := st.db.QueryRow(`SELECT host_id, family FROM `+c.table+` WHERE port=?`, c.port).
				Scan(&gotID, &gotFam); err != nil {
				t.Fatalf("run %d: read %s port %d: %v", run, c.table, c.port, err)
			}
			if gotID != c.wantID || gotFam != c.wantFam {
				t.Fatalf("run %d: %s port %d = host_id %d family %q, want %d/%q",
					run, c.table, c.port, gotID, gotFam, c.wantID, c.wantFam)
			}
		}
	}

	// Both identity indexes exist after the backfill.
	for _, idx := range []string{"ux_ports_hostid_port_proto", "ux_rules_hostid_port_proto"} {
		var n int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n); err != nil {
			t.Fatalf("look up index %s: %v", idx, err)
		}
		if n != 1 {
			t.Fatalf("index %s missing after migration", idx)
		}
	}
}

// TestMigrateCollapsesIdentityDuplicates simulates a database from the release
// that already had host_addresses: the same service was recorded once per
// address family. After the upgrade it must be one row per host+port+protocol,
// not a failed migration caused by the new unique index.
func TestMigrateCollapsesIdentityDuplicates(t *testing.T) {
	st := newTestStore(t)

	host := &model.Host{
		IP: "192.168.1.30", MAC: "aa:bb:cc:dd:ee:30", Source: "arp",
		Addresses: []model.HostAddress{
			{IP: "192.168.1.30", Family: model.FamilyIPv4, Source: "arp"},
			{IP: "2001:db8::30", Family: model.FamilyIPv6, Source: "ndp"},
		},
	}
	if err := st.UpsertHost(host); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	for i, ip := range []string{"192.168.1.30", "2001:db8::30"} {
		if _, err := st.db.Exec(`INSERT INTO ports(host, port, protocol, status, added) VALUES(?,?,?,?,?)`,
			ip, 8080, "tcp", model.StatusOpen, i); err != nil {
			t.Fatalf("seed port %s: %v", ip, err)
		}
		if _, err := st.db.Exec(`INSERT INTO rules(host, port, protocol, name) VALUES(?,?,?,?)`,
			ip, 8080, "tcp", "web-"+ip); err != nil {
			t.Fatalf("seed rule %s: %v", ip, err)
		}
	}
	// Rewind to the pre-upgrade shape: no identity columns populated and no new
	// indexes. The legacy indexes stay and accept both address literals.
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS ux_ports_hostid_port_proto`,
		`DROP INDEX IF EXISTS ux_rules_hostid_port_proto`,
		`UPDATE ports SET host_id=0, family=''`,
		`UPDATE rules SET host_id=0, family=''`,
	} {
		if _, err := st.db.Exec(stmt); err != nil {
			t.Fatalf("rewind (%s): %v", stmt, err)
		}
	}

	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate with dual-stack duplicates: %v", err)
	}

	var ports, rules int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM ports WHERE port=8080`).Scan(&ports); err != nil {
		t.Fatalf("count ports: %v", err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM rules WHERE port=8080`).Scan(&rules); err != nil {
		t.Fatalf("count rules: %v", err)
	}
	if ports != 1 || rules != 1 {
		t.Fatalf("duplicates survived migration: ports=%d rules=%d, want 1/1", ports, rules)
	}
	var hostID int64
	var family string
	if err := st.db.QueryRow(`SELECT host_id, family FROM ports WHERE port=8080`).Scan(&hostID, &family); err != nil {
		t.Fatalf("read collapsed port: %v", err)
	}
	if hostID != host.ID {
		t.Fatalf("collapsed port host_id = %d, want %d", hostID, host.ID)
	}
	if family != model.FamilyIPv4 && family != model.FamilyIPv6 {
		t.Fatalf("collapsed port family = %q", family)
	}
}

// TestPortsOfDualStackHostShareHostID is the behaviour the change exists for:
// the same device discovered over IPv4 and IPv6 is one host, and a port observed
// on either family belongs to that one host row.
func TestPortsOfDualStackHostShareHostID(t *testing.T) {
	st := newTestStore(t)

	v4 := &model.Host{IP: "192.168.1.50", MAC: "aa:bb:cc:dd:ee:50", Hostname: "nas", Source: "arp"}
	if err := st.UpsertHost(v4); err != nil {
		t.Fatalf("upsert IPv4 observation: %v", err)
	}
	v6 := &model.Host{IP: "2001:db8::50", MAC: v4.MAC, Source: "ndp"}
	if err := st.UpsertHost(v6); err != nil {
		t.Fatalf("upsert IPv6 observation: %v", err)
	}
	if v6.ID != v4.ID {
		t.Fatalf("dual-stack discovery created two hosts: %d vs %d", v4.ID, v6.ID)
	}

	port4 := &model.DiscoveredPort{
		Host: "192.168.1.50", Port: 443, Protocol: model.ProtoTCP,
		Status: model.StatusNotAdded, TargetKind: model.ScanTargetCIDR,
	}
	id4, created, err := st.UpsertPort(port4)
	if err != nil || !created {
		t.Fatalf("upsert IPv4 port: id=%d created=%v err=%v", id4, created, err)
	}
	port6 := &model.DiscoveredPort{
		Host: "2001:db8::50", Port: 443, Protocol: model.ProtoTCP,
		Status: model.StatusNotAdded, TargetKind: model.ScanTargetCIDR,
	}
	id6, createdAgain, err := st.UpsertPort(port6)
	if err != nil {
		t.Fatalf("upsert IPv6 port: %v", err)
	}
	if createdAgain {
		t.Fatalf("the same service on the other family created a second port row")
	}
	if id6 != id4 {
		t.Fatalf("IPv6 observation id = %d, want the IPv4 row %d", id6, id4)
	}
	if port4.HostID != v4.ID || port6.HostID != v4.ID {
		t.Fatalf("port host_id = %d/%d, want the shared host %d", port4.HostID, port6.HostID, v4.ID)
	}
	if port6.Family != model.FamilyIPv6 {
		t.Fatalf("IPv6 observation family = %q", port6.Family)
	}

	// A different port discovered over IPv6 is a second row with the same host_id.
	v6Only := &model.DiscoveredPort{
		Host: "2001:db8::50", Port: 8443, Protocol: model.ProtoTCP,
		Status: model.StatusOpen, TargetKind: model.ScanTargetCIDR,
	}
	if _, _, err := st.UpsertPort(v6Only); err != nil {
		t.Fatalf("upsert second IPv6 port: %v", err)
	}
	if v6Only.HostID != v4.ID {
		t.Fatalf("second port host_id = %d, want %d", v6Only.HostID, v4.ID)
	}

	ports, total, err := st.ListPorts(PortStatusFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list ports: %v", err)
	}
	if total != 2 || len(ports) != 2 {
		t.Fatalf("port rows = %d/%d, want 2", len(ports), total)
	}
	for _, p := range ports {
		if p.HostID != v4.ID || p.MAC != "aa:bb:cc:dd:ee:50" || p.Hostname != "nas" {
			t.Fatalf("listed port identity = host_id %d mac %q hostname %q", p.HostID, p.MAC, p.Hostname)
		}
	}
}

// TestCreateRuleResolvesSecondaryIPv6Address checks that a rule for the device's
// IPv6 address (not its primary IPv4 one) is attached to the same host row.
func TestCreateRuleResolvesSecondaryIPv6Address(t *testing.T) {
	st := newTestStore(t)

	host := &model.Host{
		IP: "192.168.1.60", MAC: "aa:bb:cc:dd:ee:60", Hostname: "media", Source: "arp",
		Addresses: []model.HostAddress{
			{IP: "192.168.1.60", Family: model.FamilyIPv4, Source: "arp"},
			{IP: "2001:db8::60", Family: model.FamilyIPv6, Source: "ndp"},
		},
	}
	if err := st.UpsertHost(host); err != nil {
		t.Fatalf("seed host: %v", err)
	}

	rule := &model.Rule{
		Name: "media", Host: "2001:db8::60", Port: 8443, Protocol: model.ProtoTCP, Enabled: true,
	}
	id, err := st.CreateRule(rule)
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	got, err := st.GetRule(id)
	if err != nil || got == nil {
		t.Fatalf("GetRule(%d): %v", id, err)
	}
	if got.HostID != host.ID {
		t.Fatalf("rule host_id = %d, want %d", got.HostID, host.ID)
	}
	if got.Family != model.FamilyIPv6 || got.Host != "2001:db8::60" {
		t.Fatalf("rule identity = family %q host %q", got.Family, got.Host)
	}
	if got.MAC != "aa:bb:cc:dd:ee:60" || got.Hostname != "media" {
		t.Fatalf("joined host = mac %q hostname %q", got.MAC, got.Hostname)
	}
	if byKey, _ := st.GetRuleByKey("192.168.1.60", 8443, model.ProtoTCP); byKey == nil || byKey.ID != id {
		t.Fatalf("GetRuleByKey over the other family did not find rule %d: %+v", id, byKey)
	}

	// Creating the same service for the primary IPv4 address must collapse onto
	// the same row instead of violating the host_id unique index.
	dupID, err := st.CreateRule(&model.Rule{
		Name: "media v4", Host: "192.168.1.60", Port: 8443, Protocol: model.ProtoTCP, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateRule over the other family: %v", err)
	}
	if dupID != id {
		t.Fatalf("same service on the other family created rule %d, want %d", dupID, id)
	}
}

// TestUpsertHostKeepsLearnedHostname: discovery without a hostname (a PTR lookup
// that timed out, or an observation from a source that has no name) must not
// erase the name learned earlier.
func TestUpsertHostKeepsLearnedHostname(t *testing.T) {
	st := newTestStore(t)

	first := &model.Host{
		IP: "192.168.1.70", MAC: "aa:bb:cc:dd:ee:70", Hostname: "printer", Note: "office", Source: "arp",
		Tags: model.StringList{"lab"},
	}
	if err := st.UpsertHost(first); err != nil {
		t.Fatalf("first observation: %v", err)
	}
	later := &model.Host{IP: "192.168.1.70", MAC: first.MAC, Source: "arp"}
	if err := st.UpsertHost(later); err != nil {
		t.Fatalf("second observation without a hostname: %v", err)
	}

	got, err := st.GetHostByID(first.ID)
	if err != nil || got == nil {
		t.Fatalf("GetHostByID: %v", err)
	}
	if got.Hostname != "printer" {
		t.Fatalf("hostname = %q, want the learned name", got.Hostname)
	}
	if got.Note != "office" || len(got.Tags) != 1 || got.Tags[0] != "lab" {
		t.Fatalf("identity fields were erased: note=%q tags=%v", got.Note, got.Tags)
	}

	// An explicit SetHostIdentity wins and also survives a later observation
	// that carries nothing.
	if err := st.SetHostIdentity(first.ID, "office-printer", "new note", []string{"edge"}); err != nil {
		t.Fatalf("SetHostIdentity: %v", err)
	}
	if err := st.UpsertHost(&model.Host{IP: "192.168.1.70", MAC: first.MAC, Source: "arp"}); err != nil {
		t.Fatalf("observation after SetHostIdentity: %v", err)
	}
	got, _ = st.GetHostByID(first.ID)
	if got.Hostname != "office-printer" || got.Note != "new note" || len(got.Tags) != 1 || got.Tags[0] != "edge" {
		t.Fatalf("user identity did not survive discovery: %+v", got)
	}

	// A genuinely new hostname still replaces the old one.
	if err := st.UpsertHost(&model.Host{IP: "192.168.1.70", MAC: first.MAC, Hostname: "printer-2", Source: "arp"}); err != nil {
		t.Fatalf("observation with a new hostname: %v", err)
	}
	got, _ = st.GetHostByID(first.ID)
	if got.Hostname != "printer-2" {
		t.Fatalf("hostname = %q, want the newer observed name", got.Hostname)
	}
}
