package store

import (
	"database/sql"
	"net"
	"strings"

	"github.com/ekko-studio/natpilot/internal/model"
)

// The host_addresses table keeps every address a device was seen at, while
// hosts.ip stays the single "primary" address used by the legacy unique index
// and by clients that only understand one address.
//
// Why a separate table: the only stable host identity is the MAC address, so
// UpsertHost finds the row by MAC and refreshes it. A device reachable over both
// IPv4 (ARP) and IPv6 (NDP) would otherwise have its address overwritten by
// whichever discovery ran last, silently dropping the other family. Addresses
// are per-host rows, so both survive.

const hostAddressCols = `host_id, ip, family, zone, source, last_seen`

// hostAddressDDL is applied from postMigrations (after the hosts table and the
// legacy duplicate-MAC cleanup) so it is safe on an existing database.
const hostAddressDDL = `CREATE TABLE IF NOT EXISTS host_addresses (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  host_id INTEGER NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  ip TEXT NOT NULL,
  family TEXT NOT NULL DEFAULT '',
  zone TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  first_seen TEXT NOT NULL DEFAULT '',
  last_seen TEXT NOT NULL DEFAULT '',
  UNIQUE(host_id, ip)
)`

const hostAddressIPIndexDDL = `CREATE UNIQUE INDEX IF NOT EXISTS ux_host_addresses_ip ON host_addresses(ip)`

const hostAddressHostIndexDDL = `CREATE INDEX IF NOT EXISTS ix_host_addresses_host ON host_addresses(host_id)`

// hostAddressSeedDDL copies the existing one-address-per-host data into the new
// table. It runs after the table/indexes exist and is idempotent: INSERT OR
// IGNORE makes a second Migrate a no-op (UNIQUE(host_id, ip) / UNIQUE(ip)).
// COALESCE is needed because hosts.first_seen/last_seen are nullable columns on
// databases written by older builds, while host_addresses declares them NOT NULL.
const hostAddressSeedDDL = `INSERT OR IGNORE INTO host_addresses(host_id, ip, family, source, first_seen, last_seen)
  SELECT id, ip, CASE WHEN instr(ip, ':') > 0 THEN 'ipv6' ELSE 'ipv4' END,
         COALESCE(source, ''), COALESCE(first_seen, ''), COALESCE(last_seen, '')
  FROM hosts WHERE ip <> ''`

// sqlQueryer is satisfied by both *sql.DB and *sql.Tx, so the address helpers
// work inside the UpsertHost transaction and on the read paths.
type sqlQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// canonicalAddress normalises an observed address. The optional IPv6 zone is
// split off into its own value (the schema keeps ip and zone apart so the
// UNIQUE(ip) index identifies one address independent of the interface scope),
// and the address itself is rendered in net.IP's canonical form so the same
// address spelled differently does not create a second row.
func canonicalAddress(raw string) (ip, zone string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		raw = raw[1 : len(raw)-1]
	}
	if i := strings.IndexByte(raw, '%'); i >= 0 {
		zone = strings.TrimSpace(raw[i+1:])
		raw = strings.TrimSpace(raw[:i])
	}
	parsed := net.ParseIP(raw)
	if parsed == nil {
		return raw, zone
	}
	return parsed.String(), zone
}

// hostAddressFamily trusts a caller-supplied family only when it is one of the
// two valid values, and otherwise derives it from the address.
func hostAddressFamily(ip, provided string) string {
	switch f := strings.ToLower(strings.TrimSpace(provided)); f {
	case model.FamilyIPv4, model.FamilyIPv6:
		return f
	}
	return model.IPFamily(ip)
}

// hostCandidates collects every address carried by an observation: Addresses
// first, then the legacy single IP field, de-duplicated. h.IP is inserted first
// so lookups prefer the address the caller considers current.
func hostCandidates(h *model.Host) []model.HostAddress {
	if h == nil {
		return nil
	}
	out := make([]model.HostAddress, 0, len(h.Addresses)+1)
	seen := make(map[string]struct{}, len(h.Addresses)+1)
	add := func(a model.HostAddress, fallbackSource string) {
		ip, zone := canonicalAddress(a.IP)
		if ip == "" {
			return
		}
		if _, ok := seen[ip]; ok {
			return
		}
		seen[ip] = struct{}{}
		a.IP = ip
		if a.Zone == "" {
			a.Zone = zone
		}
		if a.Family == "" {
			a.Family = hostAddressFamily(ip, "")
		}
		if a.Source == "" {
			a.Source = fallbackSource
		}
		out = append(out, a)
	}
	add(model.HostAddress{IP: h.IP, Source: h.Source, LastSeen: h.LastSeen}, h.Source)
	for _, a := range h.Addresses {
		add(a, h.Source)
	}
	return out
}

// findHostRow resolves the host row an observation belongs to:
//
//  1. the MAC address (the stable identity across DHCP moves),
//  2. any known address of the observation, first in host_addresses and then
//     in the legacy hosts.ip column,
//  3. otherwise no row exists yet.
//
// An address match is only accepted when the stored row has the same MAC (or no
// MAC at all). Otherwise the address was reused by a different device and the
// row is skipped; merging would silently change the other device's identity.
func findHostRow(tx *sql.Tx, mac string, cands []model.HostAddress) (id int64, seen int, firstSeen model.Time, err error) {
	if mac != "" {
		err = tx.QueryRow(`SELECT id, seen_count, first_seen FROM hosts WHERE mac=?`, mac).Scan(&id, &seen, &firstSeen)
		if err == nil || err != sql.ErrNoRows {
			return id, seen, firstSeen, err
		}
	}
	for _, a := range cands {
		var rowMAC string
		err = tx.QueryRow(`SELECT h.id, h.seen_count, h.first_seen, h.mac
			FROM host_addresses a JOIN hosts h ON h.id = a.host_id WHERE a.ip=?`, a.IP).
			Scan(&id, &seen, &firstSeen, &rowMAC)
		if err == nil {
			if mac != "" && rowMAC != "" && rowMAC != mac {
				continue
			}
			return id, seen, firstSeen, nil
		}
		if err != sql.ErrNoRows {
			return 0, 0, model.Time{}, err
		}
		err = tx.QueryRow(`SELECT id, seen_count, first_seen, mac FROM hosts WHERE ip=?`, a.IP).
			Scan(&id, &seen, &firstSeen, &rowMAC)
		if err == nil {
			if mac != "" && rowMAC != "" && rowMAC != mac {
				continue
			}
			return id, seen, firstSeen, nil
		}
		if err != sql.ErrNoRows {
			return 0, 0, model.Time{}, err
		}
	}
	return 0, 0, model.Time{}, sql.ErrNoRows
}

// findHostRowByAddress is the legacy fallback used only when every observed
// address is already owned by a different host: refreshing that row is better
// than failing the observation (which is what the pre-address-table code did).
func findHostRowByAddress(tx *sql.Tx, cands []model.HostAddress) (id int64, seen int, firstSeen model.Time, err error) {
	for _, a := range cands {
		err = tx.QueryRow(`SELECT h.id, h.seen_count, h.first_seen
			FROM host_addresses a JOIN hosts h ON h.id = a.host_id WHERE a.ip=?`, a.IP).
			Scan(&id, &seen, &firstSeen)
		if err == nil || err != sql.ErrNoRows {
			return id, seen, firstSeen, err
		}
		err = tx.QueryRow(`SELECT id, seen_count, first_seen FROM hosts WHERE ip=?`, a.IP).Scan(&id, &seen, &firstSeen)
		if err == nil || err != sql.ErrNoRows {
			return id, seen, firstSeen, err
		}
	}
	return 0, 0, model.Time{}, sql.ErrNoRows
}

// firstUnclaimedAddress returns an observed address that no host row claims yet,
// which is the address a new host row can safely use as its primary. It returns
// "" when every candidate is already owned by another host.
func firstUnclaimedAddress(tx *sql.Tx, cands []model.HostAddress) (string, error) {
	for _, a := range cands {
		if a.IP == "" {
			continue
		}
		var one int
		err := tx.QueryRow(`SELECT 1 FROM host_addresses WHERE ip=? LIMIT 1`, a.IP).Scan(&one)
		if err == nil {
			continue
		}
		if err != sql.ErrNoRows {
			return "", err
		}
		err = tx.QueryRow(`SELECT 1 FROM hosts WHERE ip=? LIMIT 1`, a.IP).Scan(&one)
		if err == nil {
			continue
		}
		if err != sql.ErrNoRows {
			return "", err
		}
		return a.IP, nil
	}
	return "", nil
}

// mergeHostAddresses upserts every observed address for one host. An address
// that already belongs to a different host row is left where it is: hosts.ip and
// host_addresses.ip are unique, and moving an address between devices would
// silently steal it from the row that owns it. The observation still succeeds;
// the host simply does not gain that address.
func mergeHostAddresses(tx *sql.Tx, hostID int64, cands []model.HostAddress, now model.Time) error {
	for _, a := range cands {
		if a.IP == "" {
			continue
		}
		observed := a.LastSeen
		if observed.Time.IsZero() {
			observed = now
		}
		var owner int64
		var firstSeen model.Time
		err := tx.QueryRow(`SELECT host_id, first_seen FROM host_addresses WHERE ip=?`, a.IP).
			Scan(&owner, &firstSeen)
		switch {
		case err == nil:
			if owner != hostID {
				continue
			}
			_, err = tx.Exec(`UPDATE host_addresses SET
					family = CASE WHEN ? <> '' THEN ? ELSE family END,
					zone   = CASE WHEN ? <> '' THEN ? ELSE zone END,
					source = CASE WHEN ? <> '' THEN ? ELSE source END,
					first_seen = CASE WHEN first_seen = '' THEN ? ELSE first_seen END,
					last_seen = ?
				WHERE host_id=? AND ip=?`,
				a.Family, a.Family, a.Zone, a.Zone, a.Source, a.Source, observed, observed, hostID, a.IP)
			if err != nil {
				return err
			}
		case err == sql.ErrNoRows:
			// A legacy row may still claim this address in hosts.ip without an
			// address-table entry. Treat that row as the owner too.
			var otherID int64
			lerr := tx.QueryRow(`SELECT id FROM hosts WHERE ip=? AND id<>?`, a.IP, hostID).Scan(&otherID)
			if lerr == nil {
				continue
			}
			if lerr != sql.ErrNoRows {
				return lerr
			}
			_, err = tx.Exec(`INSERT INTO host_addresses(host_id, ip, family, zone, source, first_seen, last_seen)
				VALUES(?,?,?,?,?,?,?)`,
				hostID, a.IP, a.Family, a.Zone, a.Source, observed, observed)
			if err != nil {
				if isUniqueViolation(err) {
					continue
				}
				return err
			}
		default:
			return err
		}
	}
	return nil
}

// setPrimaryIP writes the chosen primary address to hosts.ip, which carries the
// legacy unique index. If another row still claims that address as its own
// primary (a stale row from before the address table existed), that row is moved
// to one of its own addresses first. When no safe move exists the update is
// skipped rather than failing the whole observation.
func setPrimaryIP(tx *sql.Tx, hostID int64, ip string) error {
	if ip == "" {
		return nil
	}
	var current string
	if err := tx.QueryRow(`SELECT ip FROM hosts WHERE id=?`, hostID).Scan(&current); err != nil {
		return err
	}
	if current == ip {
		return nil
	}
	var otherID int64
	err := tx.QueryRow(`SELECT id FROM hosts WHERE ip=? AND id<>?`, ip, hostID).Scan(&otherID)
	switch {
	case err == nil:
		moved, merr := demoteHostPrimary(tx, otherID, ip)
		if merr != nil {
			return merr
		}
		if !moved {
			return nil
		}
	case err != sql.ErrNoRows:
		return err
	}
	if _, err := tx.Exec(`UPDATE hosts SET ip=? WHERE id=?`, ip, hostID); err != nil {
		if isUniqueViolation(err) {
			return nil
		}
		return err
	}
	return nil
}

// demoteHostPrimary moves a host row off an address it no longer owns, using its
// own best remaining address. Reports false when the row has no alternative
// (then the caller must not take the address over).
func demoteHostPrimary(tx *sql.Tx, hostID int64, claimed string) (bool, error) {
	addrs, err := loadHostAddresses(tx, hostID)
	if err != nil {
		return false, err
	}
	remaining := make([]model.HostAddress, 0, len(addrs))
	for _, a := range addrs {
		if a.IP != claimed {
			remaining = append(remaining, a)
		}
	}
	best, ok := pickPrimary(remaining)
	if !ok {
		return false, nil
	}
	if _, err := tx.Exec(`UPDATE hosts SET ip=? WHERE id=?`, best.IP, hostID); err != nil {
		if isUniqueViolation(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// loadHostAddresses reads the stored addresses of one host.
func loadHostAddresses(q sqlQueryer, hostID int64) ([]model.HostAddress, error) {
	rows, err := q.Query(`SELECT ip, family, zone, source, last_seen FROM host_addresses WHERE host_id=? ORDER BY id`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.HostAddress{}
	for rows.Next() {
		a, err := scanHostAddress(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// attachHostAddresses fills Host.Addresses for a batch of hosts with a single
// query (no N+1) and marks the primary address of each row.
func (s *Store) attachHostAddresses(hosts []*model.Host) error {
	if len(hosts) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(hosts))
	for _, h := range hosts {
		if h != nil {
			ids = append(ids, h.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	byHost, err := s.loadAddressesByHost(ids)
	if err != nil {
		return err
	}
	for _, h := range hosts {
		if h == nil {
			continue
		}
		h.Addresses = byHost[h.ID]
		annotatePrimaryAddress(h)
	}
	return nil
}

// loadAddressesByHost fetches the addresses of many hosts in one query and
// groups them in Go, so listing hosts stays O(1) queries.
func (s *Store) loadAddressesByHost(ids []int64) (map[int64][]model.HostAddress, error) {
	var b strings.Builder
	b.WriteString(`SELECT `)
	b.WriteString(hostAddressCols)
	b.WriteString(` FROM host_addresses WHERE host_id IN (`)
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("?")
		args = append(args, id)
	}
	b.WriteString(`) ORDER BY host_id, id`)
	rows, err := s.db.Query(b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]model.HostAddress, len(ids))
	for rows.Next() {
		var hostID int64
		a, err := scanHostAddressWithHost(rows, &hostID)
		if err != nil {
			return nil, err
		}
		out[hostID] = append(out[hostID], a)
	}
	return out, rows.Err()
}

// annotatePrimaryAddress marks the address that Host.IP points at. If the
// primary value does not match any stored address (legacy/corrupt row), the
// best address is promoted so callers always see a coherent picture.
func annotatePrimaryAddress(h *model.Host) {
	if h == nil || len(h.Addresses) == 0 {
		return
	}
	matched := false
	for i := range h.Addresses {
		h.Addresses[i].Primary = h.Addresses[i].IP != "" && h.Addresses[i].IP == h.IP
		if h.Addresses[i].Primary {
			matched = true
		}
	}
	if matched {
		return
	}
	best, ok := pickPrimary(h.Addresses)
	if !ok {
		return
	}
	h.IP = best.IP
	for i := range h.Addresses {
		h.Addresses[i].Primary = h.Addresses[i].IP == best.IP
	}
}

func scanHostAddress(sc interface{ Scan(...any) error }) (model.HostAddress, error) {
	var a model.HostAddress
	var lastSeen model.Time
	if err := sc.Scan(&a.IP, &a.Family, &a.Zone, &a.Source, &lastSeen); err != nil {
		return a, err
	}
	a.LastSeen = lastSeen
	if a.Family == "" {
		a.Family = model.IPFamily(a.IP)
	}
	return a, nil
}

func scanHostAddressWithHost(sc interface{ Scan(...any) error }, hostID *int64) (model.HostAddress, error) {
	var a model.HostAddress
	var lastSeen model.Time
	if err := sc.Scan(hostID, &a.IP, &a.Family, &a.Zone, &a.Source, &lastSeen); err != nil {
		return a, err
	}
	a.LastSeen = lastSeen
	if a.Family == "" {
		a.Family = model.IPFamily(a.IP)
	}
	return a, nil
}

// pickPrimary chooses the address that should be written to Host.IP. The
// preference order is:
//
//  1. global IPv4 (the LAN address most services are reachable on),
//  2. ULA IPv6 (fc00::/7),
//  3. global IPv6,
//  4. link-local IPv6 (fe80::/10),
//  5. IPv4 link-local (169.254/16),
//  6. anything else (loopback, unspecified, multicast, unparseable).
//
// Equal ranks are broken by the freshest LastSeen and then by address text, so
// the result is deterministic.
func pickPrimary(addrs []model.HostAddress) (model.HostAddress, bool) {
	if len(addrs) == 0 {
		return model.HostAddress{}, false
	}
	best := addrs[0]
	for _, a := range addrs[1:] {
		if addressPreferredOver(a, best) {
			best = a
		}
	}
	return best, true
}

func addressPreferredOver(a, b model.HostAddress) bool {
	ra, rb := addressRank(a), addressRank(b)
	if ra != rb {
		return ra < rb
	}
	if !a.LastSeen.Equal(b.LastSeen.Time) {
		return a.LastSeen.After(b.LastSeen.Time)
	}
	return a.IP < b.IP
}

func addressRank(a model.HostAddress) int {
	ip, _ := canonicalAddress(a.IP)
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return 5
	}
	if v4 := parsed.To4(); v4 != nil {
		switch {
		case v4[0] == 169 && v4[1] == 254:
			// IPv4 link-local is still usable for NAT rules and needs no zone, so it
			// outranks an IPv6 link-local address (which always needs a scope id).
			return 3
		case parsed.IsLoopback(), parsed.IsUnspecified(), parsed.IsMulticast():
			return 5
		default:
			return 0 // global IPv4 (private LAN ranges included)
		}
	}
	switch {
	case parsed.IsLinkLocalUnicast():
		return 4
	case parsed.IsLoopback(), parsed.IsUnspecified(), parsed.IsMulticast():
		return 5
	case isULA(parsed):
		return 1
	default:
		return 2
	}
}

// isULA reports whether ip is in the IPv6 unique-local range fc00::/7.
func isULA(ip net.IP) bool {
	return len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc
}

// isUniqueViolation recognises the SQLite unique-constraint error. It is used to
// degrade gracefully (skip the conflicting address) instead of failing an
// observation when an address is already owned by another host row.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "constraint failed")
}
