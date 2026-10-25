// Package store implements the SQLite persistence layer.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	// Pure Go SQLite driver: no CGO, which is what allows a single code base to
	// cross-compile for linux/darwin/windows without a host toolchain.
	_ "modernc.org/sqlite"

	"github.com/ekko-studio/natpilot/internal/model"
)

// Store wraps the SQLite database.
type Store struct {
	db   *sql.DB
	path string
	mu   sync.Mutex
}

// Open opens (and pings) the database at the given DSN.
func Open(dsn, path string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite writer serialization; WAL readers still work
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(0)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return &Store{db: db, path: path}, nil
}

// DB exposes the raw handle for advanced queries.
func (s *Store) DB() *sql.DB { return s.db }

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping verifies connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Now returns the current UTC time formatted for storage.
func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// schema is the full DDL, applied idempotently.
const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version    INTEGER PRIMARY KEY,
	applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
	key        TEXT PRIMARY KEY,
	value      TEXT NOT NULL DEFAULT '',
	updated_at TEXT
);

CREATE TABLE IF NOT EXISTS rules (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	name         TEXT NOT NULL DEFAULT '',
	host         TEXT NOT NULL DEFAULT '127.0.0.1',
	port         INTEGER NOT NULL,
	ext_port     INTEGER NOT NULL DEFAULT 0,
	protocol     TEXT NOT NULL DEFAULT 'tcp',
	tags         TEXT NOT NULL DEFAULT '[]',
	note         TEXT NOT NULL DEFAULT '',
	enabled      INTEGER NOT NULL DEFAULT 1,
	auto_created INTEGER NOT NULL DEFAULT 0,
	source       TEXT NOT NULL DEFAULT 'manual',
	created_at   TEXT,
	updated_at   TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_rules_host_port_proto ON rules(host, port, protocol);
CREATE INDEX IF NOT EXISTS ix_rules_enabled ON rules(enabled);
CREATE INDEX IF NOT EXISTS ix_rules_source ON rules(source);

CREATE TABLE IF NOT EXISTS mappings (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	rule_id       INTEGER NOT NULL REFERENCES rules(id) ON DELETE CASCADE,
	provider_name TEXT NOT NULL DEFAULT '',
	protocol      TEXT NOT NULL DEFAULT 'tcp',
	status        TEXT NOT NULL DEFAULT 'pending',
	external_ip   TEXT NOT NULL DEFAULT '',
	external_port INTEGER NOT NULL DEFAULT 0,
	internal_host TEXT NOT NULL DEFAULT '',
	internal_port INTEGER NOT NULL DEFAULT 0,
	lease_seconds INTEGER NOT NULL DEFAULT 0,
	description   TEXT NOT NULL DEFAULT '',
	error         TEXT NOT NULL DEFAULT '',
	created_at    TEXT,
	updated_at    TEXT,
	expires_at    TEXT
);
CREATE INDEX IF NOT EXISTS ix_mappings_rule ON mappings(rule_id);

CREATE TABLE IF NOT EXISTS gateways (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	name          TEXT NOT NULL DEFAULT '',
	kind          TEXT NOT NULL DEFAULT '',
	address       TEXT NOT NULL DEFAULT '',
	port          INTEGER NOT NULL DEFAULT 0,
	location      TEXT NOT NULL DEFAULT '',
	control_url   TEXT NOT NULL DEFAULT '',
	service_type  TEXT NOT NULL DEFAULT '',
	external_ip   TEXT NOT NULL DEFAULT '',
	model_name    TEXT NOT NULL DEFAULT '',
	manufacturer  TEXT NOT NULL DEFAULT '',
	preferred     INTEGER NOT NULL DEFAULT 0,
	enabled       INTEGER NOT NULL DEFAULT 1,
	last_seen     TEXT,
	last_error    TEXT NOT NULL DEFAULT '',
	capabilities  TEXT NOT NULL DEFAULT '[]'
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_gateways_addr ON gateways(address, port, kind);

CREATE TABLE IF NOT EXISTS hosts (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	ip         TEXT NOT NULL,
	mac        TEXT NOT NULL DEFAULT '',
	hostname   TEXT NOT NULL DEFAULT '',
	vendor     TEXT NOT NULL DEFAULT '',
	kind       TEXT NOT NULL DEFAULT '',
	iface      TEXT NOT NULL DEFAULT '',
	is_local   INTEGER NOT NULL DEFAULT 0,
	seen_count INTEGER NOT NULL DEFAULT 1,
	open_ports TEXT NOT NULL DEFAULT '[]',
	tags       TEXT NOT NULL DEFAULT '[]',
	note       TEXT NOT NULL DEFAULT '',
	first_seen TEXT,
	last_seen  TEXT,
	source     TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_hosts_ip ON hosts(ip);

CREATE TABLE IF NOT EXISTS scans (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	name         TEXT NOT NULL DEFAULT '',
	target_kind  TEXT NOT NULL DEFAULT 'local',
	target_ref   TEXT NOT NULL DEFAULT '',
	ports_spec   TEXT NOT NULL DEFAULT '',
	trigger      TEXT NOT NULL DEFAULT 'manual',
	status       TEXT NOT NULL DEFAULT 'running',
	progress     INTEGER NOT NULL DEFAULT 0,
	total        INTEGER NOT NULL DEFAULT 0,
	open_count   INTEGER NOT NULL DEFAULT 0,
	new_count    INTEGER NOT NULL DEFAULT 0,
	error_count  INTEGER NOT NULL DEFAULT 0,
	message      TEXT NOT NULL DEFAULT '',
	started_at   TEXT,
	finished_at  TEXT,
	duration_ms  INTEGER NOT NULL DEFAULT 0,
	policy_id    INTEGER NOT NULL DEFAULT 0,
	hosts_count  INTEGER NOT NULL DEFAULT 0,
	engine       TEXT NOT NULL DEFAULT '',
	results_note TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_scans_started ON scans(started_at DESC);

CREATE TABLE IF NOT EXISTS ports (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	scan_id     INTEGER NOT NULL DEFAULT 0,
	rule_id     INTEGER NOT NULL DEFAULT 0,
	host        TEXT NOT NULL,
	port        INTEGER NOT NULL,
	protocol    TEXT NOT NULL DEFAULT 'tcp',
	status      TEXT NOT NULL DEFAULT 'not_added',
	action      TEXT NOT NULL DEFAULT '',
	added       INTEGER NOT NULL DEFAULT 0,
	ignored     INTEGER NOT NULL DEFAULT 0,
	process     TEXT NOT NULL DEFAULT '',
	pid         INTEGER NOT NULL DEFAULT 0,
	banner      TEXT NOT NULL DEFAULT '',
	service     TEXT NOT NULL DEFAULT '',
	latency_ms  INTEGER NOT NULL DEFAULT 0,
	tags        TEXT NOT NULL DEFAULT '[]',
	note        TEXT NOT NULL DEFAULT '',
	target_kind TEXT NOT NULL DEFAULT '',
	target_ref  TEXT NOT NULL DEFAULT '',
	first_seen  TEXT,
	last_seen   TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_ports_host_port_proto ON ports(host, port, protocol);
CREATE INDEX IF NOT EXISTS ix_ports_status ON ports(status);
CREATE INDEX IF NOT EXISTS ix_ports_scan ON ports(scan_id);

CREATE TABLE IF NOT EXISTS policies (
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
	updated_at     TEXT,
	-- Binds the policy to one host by hardware address. DHCP moves the IP, the
	-- MAC stays, so host-bound policies resolve their target at match time.
	mac            TEXT NOT NULL DEFAULT '',
	-- Matches a group of devices by hardware address (full address or OUI
	-- prefix, comma separated): the "MAC" match kind.
	mac_patterns   TEXT NOT NULL DEFAULT ''
);
-- NOTE: no index on policies(mac) here. This schema block runs before the
-- ALTER TABLE migrations, so on a database created by an older build the column
-- does not exist yet and an index here would abort the whole migration. The
-- index is created in postMigrations, after the column is added.

CREATE TABLE IF NOT EXISTS schedules (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL DEFAULT '',
	kind        TEXT NOT NULL DEFAULT 'scan',
	enabled     INTEGER NOT NULL DEFAULT 1,
	cron        TEXT NOT NULL DEFAULT '',
	target_kind TEXT NOT NULL DEFAULT 'local',
	target_ref  TEXT NOT NULL DEFAULT '',
	ports_spec  TEXT NOT NULL DEFAULT '',
	policy_id   INTEGER NOT NULL DEFAULT 0,
	last_run_at TEXT,
	next_run_at TEXT,
	last_status TEXT NOT NULL DEFAULT '',
	run_count   INTEGER NOT NULL DEFAULT 0,
	created_at  TEXT,
	updated_at  TEXT
);

CREATE TABLE IF NOT EXISTS users (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	username      TEXT NOT NULL UNIQUE,
	display_name  TEXT NOT NULL DEFAULT '',
	email         TEXT NOT NULL DEFAULT '',
	role          TEXT NOT NULL DEFAULT 'admin',
	enabled       INTEGER NOT NULL DEFAULT 1,
	password_hash TEXT NOT NULL DEFAULT '',
	created_at    TEXT,
	updated_at    TEXT,
	last_login_at TEXT
);

CREATE TABLE IF NOT EXISTS sessions (
	token      TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL,
	created_at TEXT,
	expires_at TEXT,
	ip         TEXT NOT NULL DEFAULT '',
	user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_sessions_expires ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS audit (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	at         TEXT,
	actor      TEXT NOT NULL DEFAULT '',
	action     TEXT NOT NULL DEFAULT '',
	object     TEXT NOT NULL DEFAULT '',
	detail     TEXT NOT NULL DEFAULT '',
	ip         TEXT NOT NULL DEFAULT '',
	user_agent TEXT NOT NULL DEFAULT '',
	success    INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS ix_audit_at ON audit(at DESC);

CREATE TABLE IF NOT EXISTS events (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	at       TEXT,
	level    TEXT NOT NULL DEFAULT 'info',
	category TEXT NOT NULL DEFAULT '',
	message  TEXT NOT NULL DEFAULT '',
	meta     TEXT NOT NULL DEFAULT '{}',
	read     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_events_at ON events(at DESC);
`

// Migrate applies the schema and any extra migrations.
func (s *Store) Migrate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	for _, stmt := range postMigrations {
		if _, err := s.db.Exec(stmt); err != nil {
			// Column already exists is fine; anything else is a real error.
			if isDuplicateColumn(err) {
				continue
			}
			return fmt.Errorf("migration %q: %w", firstLine(stmt), err)
		}
	}
	_, _ = s.db.Exec(`INSERT OR REPLACE INTO schema_migrations(version, applied_at) VALUES (1, ?)`, Now())
	return nil
}

var postMigrations = []string{
	// Collapse the duplicate mapping rows produced by the old SELECT-then-INSERT
	// path (keeping the most recent row) before uniqueness is enforced.
	`DELETE FROM mappings WHERE id NOT IN (SELECT MAX(id) FROM mappings GROUP BY rule_id, provider_name, protocol)`,
	// One mapping record per rule/provider/protocol. Without this, a racing
	// reconcile could store two rows and later updates would only touch one.
	`CREATE UNIQUE INDEX IF NOT EXISTS ux_mappings_rule_provider ON mappings(rule_id, provider_name, protocol)`,
	// Host-bound policies (keyed by MAC) need a column on an existing database.
	`ALTER TABLE policies ADD COLUMN mac TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX IF NOT EXISTS ix_policies_mac ON policies(mac)`,
	// Policies that match a group of devices by hardware address.
	`ALTER TABLE policies ADD COLUMN mac_patterns TEXT NOT NULL DEFAULT ''`,
	// A device that moved to a new DHCP lease used to be stored twice. Keep the
	// most recently seen row per MAC before the address becomes its own identity,
	// otherwise the unique index below cannot be created.
	`DELETE FROM hosts WHERE mac <> '' AND id NOT IN (
		SELECT id FROM (
			SELECT id, ROW_NUMBER() OVER (PARTITION BY mac ORDER BY COALESCE(last_seen,'') DESC, id DESC) AS rn
			FROM hosts WHERE mac <> ''
		) WHERE rn = 1
	)`,
	// Identity by hardware address; hosts without one (loopback, virtual) are
	// left alone by using a partial index.
	`CREATE UNIQUE INDEX IF NOT EXISTS ux_hosts_mac ON hosts(mac) WHERE mac <> ''`,
}

func isDuplicateColumn(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "duplicate column name") || contains(msg, "already exists")
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ---- settings ----

// GetSetting returns a persisted setting value.
func (s *Store) GetSetting(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetSetting upserts a setting value.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key, value, updated_at) VALUES(?,?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		key, value, Now())
	return err
}

// AllSettings returns every persisted setting.
func (s *Store) AllSettings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// DeleteSetting removes a setting.
func (s *Store) DeleteSetting(key string) error {
	_, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, key)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func i2b(i int) bool { return i != 0 }

func (s *Store) event(level, category, message string, meta model.JSONMap) {
	_, _ = s.db.Exec(`INSERT INTO events(at, level, category, message, meta) VALUES(?,?,?,?,?)`,
		Now(), level, category, message, meta)
}
