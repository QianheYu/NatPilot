package store

import (
	"path/filepath"
	"testing"

	"github.com/ekko-studio/natpilot/internal/model"
)

// oldPoliciesTable is the policies schema from before host-bound policies
// existed: no `mac` column. Upgrading such a database is what broke a real
// installation once, so it is covered explicitly.
const oldPoliciesTable = `CREATE TABLE policies (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	name           TEXT NOT NULL DEFAULT '',
	description    TEXT NOT NULL DEFAULT '',
	enabled        INTEGER NOT NULL DEFAULT 1,
	match_target   TEXT NOT NULL DEFAULT 'any',
	port_ranges    TEXT NOT NULL DEFAULT '',
	exclude_ranges TEXT NOT NULL DEFAULT '',
	protocol       TEXT NOT NULL DEFAULT 'any',
	auto_action    TEXT NOT NULL DEFAULT 'watch',
	auto_add       INTEGER NOT NULL DEFAULT 0,
	tags           TEXT NOT NULL DEFAULT '[]',
	note           TEXT NOT NULL DEFAULT '',
	min_port       INTEGER NOT NULL DEFAULT 0,
	max_port       INTEGER NOT NULL DEFAULT 0,
	require_name   TEXT NOT NULL DEFAULT '',
	priority       INTEGER NOT NULL DEFAULT 0,
	created_at     TEXT,
	updated_at     TEXT
)`

func openRaw(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")
	st, err := Open(path+"?_busy_timeout=5000&_foreign_keys=on", path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestMigrateUpgradesOldPoliciesTable(t *testing.T) {
	st := openRaw(t)

	if _, err := st.db.Exec(oldPoliciesTable); err != nil {
		t.Fatalf("create old policies table: %v", err)
	}
	// An existing row proves the upgrade keeps data, not just the schema.
	if _, err := st.db.Exec(`INSERT INTO policies(name, enabled, match_target) VALUES('legacy', 1, 'any')`); err != nil {
		t.Fatalf("seed legacy policy: %v", err)
	}

	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate from an old schema failed: %v", err)
	}

	pols, err := st.ListPolicies()
	if err != nil {
		t.Fatalf("list policies after migration: %v", err)
	}
	if len(pols) != 1 || pols[0].Name != "legacy" {
		t.Fatalf("legacy policy lost during migration: %+v", pols)
	}
	if pols[0].MAC != "" {
		t.Fatalf("legacy policy should have an empty MAC, got %q", pols[0].MAC)
	}

	// The new column is usable and the index exists.
	if _, err := st.CreatePolicy(&model.Policy{Name: "bound", MAC: "aa:bb:cc:dd:ee:99", Enabled: true}); err != nil {
		t.Fatalf("create host-bound policy after migration: %v", err)
	}
	bound, err := st.ListPoliciesByHost("AA-BB-CC-DD-EE-99")
	if err != nil {
		t.Fatalf("list by host after migration: %v", err)
	}
	if len(bound) != 1 {
		t.Fatalf("expected 1 host-bound policy, got %d", len(bound))
	}
}

// TestMigrateCollapsesDuplicateMACHosts covers the other migration: databases
// written before MAC became the host identity can hold one row per address for
// the same device, which would make the unique index impossible to create.
func TestMigrateCollapsesDuplicateMACHosts(t *testing.T) {
	st := openRaw(t)

	if _, err := st.db.Exec(`CREATE TABLE hosts (
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
	)`); err != nil {
		t.Fatalf("create old hosts table: %v", err)
	}
	for i, row := range []struct{ ip, last string }{
		{"192.168.1.10", "2026-01-01 00:00:00"},
		{"192.168.1.11", "2026-02-01 00:00:00"},
		{"192.168.1.12", "2026-03-01 00:00:00"},
	} {
		if _, err := st.db.Exec(`INSERT INTO hosts(ip, mac, last_seen) VALUES(?,?,?)`,
			row.ip, "AA:BB:CC:DD:EE:77", row.last); err != nil {
			t.Fatalf("seed host %d: %v", i, err)
		}
	}
	// A MAC-less host (loopback) must survive untouched.
	if _, err := st.db.Exec(`INSERT INTO hosts(ip, mac) VALUES('127.0.0.1','')`); err != nil {
		t.Fatalf("seed loopback: %v", err)
	}

	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate hosts: %v", err)
	}

	hosts, err := st.ListHosts(50)
	if err != nil {
		t.Fatalf("list hosts: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("expected the duplicates collapsed to 2 rows, got %d: %+v", len(hosts), hosts)
	}
	var kept *model.Host
	for i := range hosts {
		if hosts[i].MAC != "" {
			kept = &hosts[i]
		}
	}
	if kept == nil {
		t.Fatal("the MAC host disappeared")
	}
	// The most recently seen address is the one that survives.
	if kept.IP != "192.168.1.12" {
		t.Fatalf("kept the wrong address %q, want the freshest lease", kept.IP)
	}
}
