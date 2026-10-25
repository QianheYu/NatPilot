package store

import (
	"database/sql"
	"strings"

	"github.com/ekko-studio/natpilot/internal/model"
)

const portCols = `id, scan_id, rule_id, host, port, protocol, status, action, added, ignored, process, pid, banner, service, latency_ms, tags, note, target_kind, target_ref, first_seen, last_seen`

func scanPort(sc interface{ Scan(...any) error }) (*model.DiscoveredPort, error) {
	p := &model.DiscoveredPort{}
	var added, ignored int
	var firstSeen, lastSeen model.Time
	var tags model.StringList
	err := sc.Scan(&p.ID, &p.ScanID, &p.RuleID, &p.Host, &p.Port, &p.Protocol, &p.Status, &p.Action,
		&added, &ignored, &p.Process, &p.PID, &p.Banner, &p.Service, &p.LatencyMS, &tags, &p.Note,
		&p.TargetKind, &p.TargetRef, &firstSeen, &lastSeen)
	if err != nil {
		return nil, err
	}
	p.Added, p.Ignored = i2b(added), i2b(ignored)
	p.FirstSeen, p.LastSeen = firstSeen, lastSeen
	p.Tags = tags
	if p.Tags == nil {
		p.Tags = model.StringList{}
	}
	return p, nil
}

// PortStatusFilter narrows port listings.
type PortStatusFilter struct {
	Statuses   []string
	Host       string
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
		clauses = append(clauses, "status IN ("+strings.Join(ph, ",")+")")
	}
	if f.Host != "" {
		clauses = append(clauses, "host = ?")
		args = append(args, f.Host)
	}
	if f.Query != "" {
		clauses = append(clauses, "(host LIKE ? OR process LIKE ? OR service LIKE ? OR CAST(port AS TEXT) LIKE ? OR note LIKE ?)")
		like := "%" + f.Query + "%"
		args = append(args, like, like, like, like, like)
	}
	if f.OnlyAdded != nil {
		clauses = append(clauses, "added = ?")
		args = append(args, b2i(*f.OnlyAdded))
	}
	if f.OnlyOpen {
		clauses = append(clauses, "status IN ('open','not_added','added')")
	}
	if f.TargetKind != "" {
		clauses = append(clauses, "target_kind = ?")
		args = append(args, f.TargetKind)
	}
	if f.ScanID > 0 {
		clauses = append(clauses, "scan_id = ?")
		args = append(args, f.ScanID)
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ports `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "last_seen DESC"
	switch f.Sort {
	case "port":
		order = "port " + dir(f.Order) + ", host ASC"
	case "host":
		order = "host " + dir(f.Order) + ", port ASC"
	case "status":
		order = "status " + dir(f.Order) + ", port ASC"
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	q := `SELECT ` + portCols + ` FROM ports ` + where + ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
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
func (s *Store) UpsertPort(p *model.DiscoveredPort) (int64, bool, error) {
	now := model.Time{}
	_ = now.Scan(Now())
	if p.Protocol == "" {
		p.Protocol = model.ProtoTCP
	}
	if p.Tags == nil {
		p.Tags = model.StringList{}
	}
	var existingID int64
	var firstSeen model.Time
	err := s.db.QueryRow(`SELECT id, first_seen FROM ports WHERE host = ? AND port = ? AND protocol = ?`,
		p.Host, p.Port, p.Protocol).Scan(&existingID, &firstSeen)
	if err == nil {
		_, err = s.db.Exec(`UPDATE ports SET scan_id=?, status=?, process=?, pid=?, banner=?, service=?, latency_ms=?,
			target_kind=?, target_ref=?, last_seen=?, added=?, rule_id=?, note=?, tags=? WHERE id=?`,
			p.ScanID, p.Status, p.Process, p.PID, p.Banner, p.Service, p.LatencyMS, p.TargetKind, p.TargetRef, now,
			b2i(p.Added), p.RuleID, p.Note, p.Tags, existingID)
		p.ID = existingID
		p.FirstSeen = firstSeen
		p.LastSeen = now
		return existingID, false, err
	}
	if err != sql.ErrNoRows {
		return 0, false, err
	}
	res, err := s.db.Exec(`INSERT INTO ports(scan_id, rule_id, host, port, protocol, status, action, added, ignored,
		process, pid, banner, service, latency_ms, tags, note, target_kind, target_ref, first_seen, last_seen)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ScanID, p.RuleID, p.Host, p.Port, p.Protocol, p.Status, p.Action, b2i(p.Added), b2i(p.Ignored),
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
	row := s.db.QueryRow(`SELECT `+portCols+` FROM ports WHERE id = ?`, id)
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

// MarkPortRemoved clears the added link after a rule was deleted.
func (s *Store) MarkPortRemoved(id int64) error {
	_, err := s.db.Exec(`UPDATE ports SET added=0, rule_id=0, status=? WHERE id=?`, model.StatusOpen, id)
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
	rows, err := s.db.Query(`SELECT id, host, port, protocol FROM rules`)
	if err != nil {
		return err
	}
	type key struct {
		host  string
		port  int
		proto string
	}
	ruleByKey := map[key]int64{}
	for rows.Next() {
		var id int64
		var host, proto string
		var port int
		if err := rows.Scan(&id, &host, &port, &proto); err != nil {
			rows.Close()
			return err
		}
		ruleByKey[key{host, port, proto}] = id
	}
	rows.Close()

	prs, err := s.db.Query(`SELECT id, host, port, protocol, status FROM ports`)
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
		var id, rule int64
		var host, proto, status string
		var port int
		if err := prs.Scan(&id, &host, &port, &proto, &status); err != nil {
			prs.Close()
			return err
		}
		if rid, ok := ruleByKey[key{host, port, proto}]; ok {
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
