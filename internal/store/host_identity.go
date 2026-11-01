package store

import (
	"database/sql"
	"strings"

	"github.com/ekko-studio/natpilot/internal/model"
)

// Host identity for ports and rules.
//
// ports and rules used to be keyed by the IP literal in their host column, which
// made a dual-stack device look like two different hosts: the same service
// discovered over IPv4 and over IPv6 produced two rows (and two rules). The
// stable identity is the host row, so both tables carry a host_id that points at
// hosts.id; the address column stays as the concrete address (the router still
// needs one) and family records which family it belongs to.
//
// Resolution order matches the host discovery path:
//
//  1. host_addresses.ip (every address of every known host),
//  2. hosts.ip (legacy rows written before host_addresses existed).
//
// An address that resolves to nothing keeps host_id = 0. That is the documented
// fallback, and the legacy unique index (host, port, protocol) keeps those rows
// unique until the host is discovered and the backfill can resolve them.

// resolveHostIdentity maps an address to the host row that owns it and reports
// the address family. It never fails just because the address is unknown; a
// caller that cannot resolve the host still stores the address.
func (s *Store) resolveHostIdentity(host string) (int64, string, error) {
	return resolveHostIdentityQ(s.db, host)
}

func resolveHostIdentityQ(q sqlQueryer, host string) (int64, string, error) {
	canonical, _ := canonicalAddress(host)
	family := model.IPFamily(canonical)
	candidates := make([]string, 0, 2)
	if canonical != "" {
		candidates = append(candidates, canonical)
	}
	if raw := strings.TrimSpace(host); raw != "" && raw != canonical {
		candidates = append(candidates, raw)
	}
	for _, ip := range candidates {
		var id int64
		err := q.QueryRow(`SELECT host_id FROM host_addresses WHERE ip=? LIMIT 1`, ip).Scan(&id)
		if err == nil {
			return id, family, nil
		}
		if err != sql.ErrNoRows {
			return 0, family, err
		}
		err = q.QueryRow(`SELECT id FROM hosts WHERE ip=?`, ip).Scan(&id)
		if err == nil {
			return id, family, nil
		}
		if err != sql.ErrNoRows {
			return 0, family, err
		}
	}
	return 0, family, nil
}

// hostFamily derives the family of a concrete address. A hostname (a rule may
// point at one) has no family; the stored value is then left alone.
func hostFamily(host string, current string) string {
	canonical, _ := canonicalAddress(host)
	if f := model.IPFamily(canonical); f != "" {
		return f
	}
	return current
}

// backfillHostIdentity resolves host_id/family for every port and rule row and
// collapses rows that resolve to the same host+port+protocol. It runs after the
// columns exist and before the unique indexes that encode the new identity are
// created, so it is the one place allowed to merge duplicates.
//
// It is idempotent: rows already resolved are only rewritten when their family
// is still empty, and a second run finds no duplicates to collapse.
func (s *Store) backfillHostIdentity() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := backfillPortIdentity(tx); err != nil {
		return err
	}
	if err := backfillRuleIdentity(tx); err != nil {
		return err
	}
	return tx.Commit()
}

type portIdentityRow struct {
	id       int64
	host     string
	port     int
	protocol string
	hostID   int64
	family   string
	added    int
	ruleID   int64
	lastSeen string
	changed  bool
}

type portIdentityKey struct {
	hostID   int64
	port     int
	protocol string
}

func newPortIdentityKey(r portIdentityRow) portIdentityKey {
	return portIdentityKey{hostID: r.hostID, port: r.port, protocol: r.protocol}
}

// portIdentityPreferred reports whether a is the row to keep when several rows
// resolve to the same host+port+protocol. A managed row wins over an inert one,
// then the most recently observed row, then the newest id.
func portIdentityPreferred(a, b portIdentityRow) bool {
	if a.added != b.added {
		return a.added > b.added
	}
	if (a.ruleID > 0) != (b.ruleID > 0) {
		return a.ruleID > 0
	}
	if a.lastSeen != b.lastSeen {
		return a.lastSeen > b.lastSeen
	}
	return a.id > b.id
}

func backfillPortIdentity(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT id, host, port, protocol, host_id, family, added, rule_id, COALESCE(last_seen,'')
		FROM ports ORDER BY id`)
	if err != nil {
		return err
	}
	list := make([]portIdentityRow, 0, 64)
	for rows.Next() {
		var r portIdentityRow
		if err := rows.Scan(&r.id, &r.host, &r.port, &r.protocol, &r.hostID, &r.family,
			&r.added, &r.ruleID, &r.lastSeen); err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	groups := map[portIdentityKey][]portIdentityRow{}
	for i := range list {
		r := &list[i]
		origID, origFamily := r.hostID, r.family
		if r.hostID == 0 {
			id, _, rerr := resolveHostIdentityQ(tx, r.host)
			if rerr != nil {
				return rerr
			}
			if id > 0 {
				r.hostID = id
			}
		}
		r.family = hostFamily(r.host, r.family)
		if r.hostID != origID || r.family != origFamily {
			r.changed = true
		}
		if r.hostID > 0 {
			k := newPortIdentityKey(*r)
			groups[k] = append(groups[k], *r)
		}
	}

	// Pick the survivor of every group and mark the rest for deletion. The new
	// partial unique index only covers host_id > 0 rows, so unresolved rows are
	// left to the legacy (host, port, protocol) index.
	deleted := map[int64]bool{}
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		keep := group[0]
		for _, r := range group[1:] {
			if portIdentityPreferred(r, keep) {
				keep = r
			}
		}
		for _, r := range group {
			if r.id == keep.id {
				continue
			}
			if _, err := tx.Exec(`DELETE FROM ports WHERE id=?`, r.id); err != nil {
				return err
			}
			deleted[r.id] = true
		}
	}

	for i := range list {
		r := list[i]
		if deleted[r.id] || !r.changed {
			continue
		}
		if _, err := tx.Exec(`UPDATE ports SET host_id=?, family=? WHERE id=?`, r.hostID, r.family, r.id); err != nil {
			return err
		}
	}
	return nil
}

type ruleIdentityRow struct {
	id        int64
	host      string
	port      int
	protocol  string
	hostID    int64
	family    string
	updatedAt string
	createdAt string
	mapped    bool
	changed   bool
}

type ruleIdentityKeyT struct {
	hostID   int64
	port     int
	protocol string
}

func ruleIdentityKey(r ruleIdentityRow) ruleIdentityKeyT {
	return ruleIdentityKeyT{hostID: r.hostID, port: r.port, protocol: r.protocol}
}

// ruleIdentityPreferred keeps the rule a gateway mapping still points at, then
// the most recently edited row, so collapsing duplicates does not silently drop
// the mapping state of the surviving service.
func ruleIdentityPreferred(a, b ruleIdentityRow) bool {
	if a.mapped != b.mapped {
		return a.mapped
	}
	if a.updatedAt != b.updatedAt {
		return a.updatedAt > b.updatedAt
	}
	if a.createdAt != b.createdAt {
		return a.createdAt > b.createdAt
	}
	return a.id > b.id
}

func backfillRuleIdentity(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT r.id, r.host, r.port, r.protocol, r.host_id, r.family,
			COALESCE(r.updated_at,''), COALESCE(r.created_at,''),
			EXISTS(SELECT 1 FROM mappings m WHERE m.rule_id = r.id)
		FROM rules r ORDER BY r.id`)
	if err != nil {
		return err
	}
	list := make([]ruleIdentityRow, 0, 32)
	for rows.Next() {
		var r ruleIdentityRow
		var mapped int
		if err := rows.Scan(&r.id, &r.host, &r.port, &r.protocol, &r.hostID, &r.family,
			&r.updatedAt, &r.createdAt, &mapped); err != nil {
			rows.Close()
			return err
		}
		r.mapped = i2b(mapped)
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	groups := map[ruleIdentityKeyT][]ruleIdentityRow{}
	for i := range list {
		r := &list[i]
		origID, origFamily := r.hostID, r.family
		if r.hostID == 0 {
			id, _, rerr := resolveHostIdentityQ(tx, r.host)
			if rerr != nil {
				return rerr
			}
			if id > 0 {
				r.hostID = id
			}
		}
		r.family = hostFamily(r.host, r.family)
		if r.hostID != origID || r.family != origFamily {
			r.changed = true
		}
		if r.hostID > 0 {
			k := ruleIdentityKey(*r)
			groups[k] = append(groups[k], *r)
		}
	}

	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		keep := group[0]
		for _, r := range group[1:] {
			if ruleIdentityPreferred(r, keep) {
				keep = r
			}
		}
		for _, r := range group {
			if r.id == keep.id {
				continue
			}
			// Move what still points at the duplicate onto the survivor before
			// removing it: a mapping that would collide with one the survivor
			// already has is dropped (it described the same provider/protocol).
			if _, err := tx.Exec(`UPDATE OR IGNORE mappings SET rule_id=? WHERE rule_id=?`, keep.id, r.id); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM mappings WHERE rule_id=?`, r.id); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE ports SET rule_id=? WHERE rule_id=?`, keep.id, r.id); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM rules WHERE id=?`, r.id); err != nil {
				return err
			}
		}
	}

	for i := range list {
		r := list[i]
		if !r.changed {
			continue
		}
		// A duplicate may already have been deleted above; UPDATE on a missing
		// row is a harmless no-op.
		if _, err := tx.Exec(`UPDATE rules SET host_id=?, family=? WHERE id=?`, r.hostID, r.family, r.id); err != nil {
			return err
		}
	}
	return nil
}
