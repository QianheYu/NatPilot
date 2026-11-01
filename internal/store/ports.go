package store

import (
	"database/sql"
	"strings"

	"github.com/ekko-studio/natpilot/internal/model"
)

// portCols is the read projection. The host columns are joined so a caller sees
// which device the port belongs to without a second query; the ports table keeps
// host (the concrete address) for the mapping path.
const portCols = `p.id, p.scan_id, p.rule_id, p.host, p.host_id, p.family, p.port, p.protocol,
	p.status, p.action, p.added, p.ignored, p.process, p.pid, p.banner, p.service, p.latency_ms,
	p.tags, p.note, p.target_kind, p.target_ref, p.first_seen, p.last_seen,
	COALESCE(h.mac,''), COALESCE(h.hostname,'')`

func scanPort(sc interface{ Scan(...any) error }) (*model.DiscoveredPort, error) {
	p := &model.DiscoveredPort{}
	var added, ignored int
	var firstSeen, lastSeen model.Time
	var tags model.StringList
	var mac, hostname string
	err := sc.Scan(&p.ID, &p.ScanID, &p.RuleID, &p.Host, &p.HostID, &p.Family, &p.Port, &p.Protocol, &p.Status, &p.Action,
		&added, &ignored, &p.Process, &p.PID, &p.Banner, &p.Service, &p.LatencyMS, &tags, &p.Note,
		&p.TargetKind, &p.TargetRef, &firstSeen, &lastSeen, &mac, &hostname)
	if err != nil {
		return nil, err
	}
	p.Added, p.Ignored = i2b(added), i2b(ignored)
	p.FirstSeen, p.LastSeen = firstSeen, lastSeen
	p.Tags = tags
	p.MAC, p.Hostname = mac, hostname
	if p.Tags == nil {
		p.Tags = model.StringList{}
	}
	return p, nil
}

// PortStatusFilter narrows port listings.
type PortStatusFilter struct {
	Statuses   []string
	Host       string
	HostID     int64
	Query      string
	OnlyAdded  *bool
	OnlyOpen   bool
	TargetKind string
	ScanID     int64
	Limit      int
	Offset     int
	Sort       string
	Order      string
}

// ListPorts returns discovered ports matching the filter.
func (s *Store) ListPorts(f PortStatusFilter) ([]model.DiscoveredPort, int, error) {
	clauses := []string{}
	args := []any{}
	if len(f.Statuses) > 0 {
		ph := make([]string, len(f.Statuses))
		for i, st := range f.Statuses {
			ph[i] = "?"
			args = append(args, st)
		}
		clauses = append(clauses, "p.status IN ("+strings.Join(ph, ",")+")")
	}
	if f.Host != "" {
		clauses = append(clauses, "p.host = ?")
		args = append(args, f.Host)
	}
	if f.HostID > 0 {
		clauses = append(clauses, "p.host_id = ?")
		args = append(args, f.HostID)
	}
	if f.Query != "" {
		clauses = append(clauses, "(p.host LIKE ? OR p.process LIKE ? OR p.service LIKE ? OR CAST(p.port AS TEXT) LIKE ? OR p.note LIKE ?)")
		like := "%" + f.Query + "%"
		args = append(args, like, like, like, like, like)
	}
	if f.OnlyAdded != nil {
		clauses = append(clauses, "p.added = ?")
		args = append(args, b2i(*f.OnlyAdded))
	}
	if f.OnlyOpen {
		clauses = append(clauses, "p.status IN ('open','not_added','added')")
	}
	if f.TargetKind != "" {
		clauses = append(clauses, "p.target_kind = ?")
		args = append(args, f.TargetKind)
	}
	if f.ScanID > 0 {
		clauses = append(clauses, "p.scan_id = ?")
		args = append(args, f.ScanID)
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ports p `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "p.last_seen DESC"
	switch f.Sort {
	case "port":
		order = "p.port " + dir(f.Order) + ", p.host ASC"
	case "host":
		order = "p.host " + dir(f.Order) + ", p.port ASC"
	case "status":
		order = "p.status " + dir(f.Order) + ", p.port ASC"
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	q := `SELECT ` + portCols + ` FROM ports p LEFT JOIN hosts h ON h.id = p.host_id ` + where +
		` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	args = append(args, limit, f.Offset)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.DiscoveredPort{}
	for rows.Next() {
		p, err := scanPort(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *p)
	}
	return out, total, rows.Err()
}

// UpsertPort inserts or refreshes a discovered port, returning the row id and
// whether it was newly created.
//
// Lookup is by host row when the address belongs to a known host, so the same
// service discovered over IPv4 and over IPv6 updates one row instead of creating
// a second one. The address (and its family) is refreshed from the observation.
func (s *Store) UpsertPort(p *model.DiscoveredPort) (int64, bool, error) {
	now := model.Time{}
	_ = now.Scan(Now())
	if p.Protocol == "" {
		p.Protocol = model.ProtoTCP
	}
	if p.Tags == nil {
		p.Tags = model.StringList{}
	}
	hostID, family, err := s.resolveHostIdentity(p.Host)
	if err != nil {
		return 0, false, err
	}
	p.HostID, p.Family = hostID, family

	var existingID int64
	var firstSeen model.Time
	if hostID > 0 {
		err = s.db.QueryRow(`SELECT id, first_seen FROM ports WHERE host_id = ? AND port = ? AND protocol = ?`,
			hostID, p.Port, p.Protocol).Scan(&existingID, &firstSeen)
	} else {
		err = s.db.QueryRow(`SELECT id, first_seen FROM ports WHERE host = ? AND port = ? AND protocol = ?`,
			p.Host, p.Port, p.Protocol).Scan(&existingID, &firstSeen)
	}
	if err == sql.ErrNoRows && hostID > 0 {
		// A legacy row may still be keyed by the literal address with host_id=0
		// (it was written before the host was discovered). Adopt it instead of
		// inserting a row that would violate the new partial unique index.
		err = s.db.QueryRow(`SELECT id, first_seen FROM ports WHERE host = ? AND port = ? AND protocol = ?`,
			p.Host, p.Port, p.Protocol).Scan(&existingID, &firstSeen)
	}
	if err == nil {
		_, err = s.db.Exec(`UPDATE ports SET host=?, host_id=?, family=?, scan_id=?, status=?, process=?, pid=?, banner=?, service=?, latency_ms=?,
			target_kind=?, target_ref=?, last_seen=?, added=?, rule_id=?, note=?, tags=? WHERE id=?`,
			p.Host, p.HostID, p.Family, p.ScanID, p.Status, p.Process, p.PID, p.Banner, p.Service, p.LatencyMS, p.TargetKind, p.TargetRef, now,
			b2i(p.Added), p.RuleID, p.Note, p.Tags, existingID)
		p.ID = existingID
		p.FirstSeen = firstSeen
		p.LastSeen = now
		return existingID, false, err
	}
	if err != sql.ErrNoRows {
		return 0, false, err
	}
	res, err := s.db.Exec(`INSERT INTO ports(scan_id, rule_id, host, host_id, family, port, protocol, status, action, added, ignored,
		process, pid, banner, service, latency_ms, tags, note, target_kind, target_ref, first_seen, last_seen)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ScanID, p.RuleID, p.Host, p.HostID, p.Family, p.Port, p.Protocol, p.Status, p.Action, b2i(p.Added), b2i(p.Ignored),
		p.Process, p.PID, p.Banner, p.Service, p.LatencyMS, p.Tags, p.Note, p.TargetKind, p.TargetRef, now, now)
	if err != nil {
		return 0, false, err
	}
	id, _ := res.LastInsertId()
	p.ID = id
	p.FirstSeen, p.LastSeen = now, now
	return id, true, nil
}

// GetPort fetches one discovered port.
func (s *Store) GetPort(id int64) (*model.DiscoveredPort, error) {
	row := s.db.QueryRow(`SELECT `+portCols+` FROM ports p LEFT JOIN hosts h ON h.id = p.host_id WHERE p.id = ?`, id)
	p, err := scanPort(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

// SetPortStatus updates the lifecycle status/action of a port.
func (s *Store) SetPortStatus(id int64, status, action string) error {
	_, err := s.db.Exec(`UPDATE ports SET status=?, action=? WHERE id=?`, status, action, id)
	return err
}

// MarkPortAdded links a discovered port to a rule.
func (s *Store) MarkPortAdded(id, ruleID int64) error {
	_, err := s.db.Exec(`UPDATE ports SET added=1, rule_id=?, status=? WHERE id=?`, ruleID, model.StatusAdded, id)
	return err
}

// MarkPortIgnored flags a port so policies never auto-add it again.
func (s *Store) MarkPortIgnored(id int64) error {
	_, err := s.db.Exec(`UPDATE ports SET ignored=1, status=? WHERE id=?`, model.StatusClosed, id)
	return err
}

// MarkPortRemoved clears the added link after a rule was deleted or disabled.
//
// The port returns to "not added" rather than "open": an open port was merely
// discovered and never handled, while this one was deliberately managed and now
// needs a decision again. Keeping those states apart is what lets the port list
// be trusted after a rule is removed.
func (s *Store) MarkPortRemoved(id int64) error {
	_, err := s.db.Exec(`UPDATE ports SET added=0, rule_id=0, status=? WHERE id=?`, model.StatusNotAdded, id)
	return err
}

// SetPortNote updates tags/note for a port.
func (s *Store) SetPortNote(id int64, tags model.StringList, note string) error {
	if tags == nil {
		tags = model.StringList{}
	}
	_, err := s.db.Exec(`UPDATE ports SET tags=?, note=? WHERE id=?`, tags, note, id)
	return err
}

// DeletePort removes a discovered port row.
func (s *Store) DeletePort(id int64) error {
	_, err := s.db.Exec(`DELETE FROM ports WHERE id=?`, id)
	return err
}

// DeletePortsWhere removes ports matching a status filter (bulk cleanup).
func (s *Store) DeletePortsWhere(statuses []string, host string) (int64, error) {
	clauses := []string{}
	args := []any{}
	if len(statuses) > 0 {
		ph := make([]string, len(statuses))
		for i, st := range statuses {
			ph[i] = "?"
			args = append(args, st)
		}
		clauses = append(clauses, "status IN ("+strings.Join(ph, ",")+")")
	}
	if host != "" {
		clauses = append(clauses, "host = ?")
		args = append(args, host)
	}
	if len(clauses) == 0 {
		return 0, nil
	}
	res, err := s.db.Exec(`DELETE FROM ports WHERE `+strings.Join(clauses, " AND "), args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ReconcilePortStatuses keeps the discovered-port lifecycle in sync with the
// rule table:
//
//	rule exists + enabled  -> added
//	rule exists + disabled -> added (still known, but inert)
//	rule missing, seen     -> open
//
// It also flags rules whose observed source port disappeared as source_gone.
func (s *Store) ReconcilePortStatuses() error {
	rows, err := s.db.Query(`SELECT id, host, host_id, port, protocol FROM rules`)
	if err != nil {
		return err
	}
	type key struct {
		hostID int64
		host   string
		port   int
		proto  string
	}
	// Identity is the host row when known, so a rule created for the IPv4
	// address still matches a port discovered over IPv6 (and vice versa).
	keyOf := func(host string, hostID int64, port int, proto string) key {
		if hostID > 0 {
			return key{hostID: hostID, port: port, proto: proto}
		}
		return key{host: host, port: port, proto: proto}
	}
	ruleByKey := map[key]int64{}
	for rows.Next() {
		var id, hostID int64
		var host, proto string
		var port int
		if err := rows.Scan(&id, &host, &hostID, &port, &proto); err != nil {
			rows.Close()
			return err
		}
		ruleByKey[keyOf(host, hostID, port, proto)] = id
	}
	rows.Close()

	prs, err := s.db.Query(`SELECT id, host, host_id, port, protocol, status FROM ports`)
	if err != nil {
		return err
	}
	type upd struct {
		id     int64
		status string
		rule   int64
	}
	updates := []upd{}
	for prs.Next() {
		var id, rule, hostID int64
		var host, proto, status string
		var port int
		if err := prs.Scan(&id, &host, &hostID, &port, &proto, &status); err != nil {
			prs.Close()
			return err
		}
		if rid, ok := ruleByKey[keyOf(host, hostID, port, proto)]; ok {
			if rule != rid || status != model.StatusAdded {
				updates = append(updates, upd{id, model.StatusAdded, rid})
			}
		} else if status == model.StatusAdded {
			updates = append(updates, upd{id, model.StatusOpen, 0})
		}
	}
	prs.Close()
	for _, u := range updates {
		if _, err := s.db.Exec(`UPDATE ports SET status=?, rule_id=?, added=? WHERE id=?`, u.status, u.rule, b2i(u.rule > 0), u.id); err != nil {
			return err
		}
	}
	return nil
}

// CountPortsByStatus returns a status histogram.
func (s *Store) CountPortsByStatus() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM ports GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}
