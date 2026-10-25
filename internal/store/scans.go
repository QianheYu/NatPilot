package store

import (
	"database/sql"

	"github.com/ekko-studio/natpilot/internal/model"
)

const scanCols = `id, name, target_kind, target_ref, ports_spec, trigger, status, progress, total, open_count, new_count, error_count, message, started_at, finished_at, duration_ms, policy_id, hosts_count, engine, results_note`

func scanRun(sc interface{ Scan(...any) error }) (*model.ScanRun, error) {
	r := &model.ScanRun{}
	var startedAt model.Time
	var finishedAt model.NullTime
	err := sc.Scan(&r.ID, &r.Name, &r.TargetKind, &r.TargetRef, &r.PortsSpec, &r.Trigger, &r.Status, &r.Progress,
		&r.Total, &r.Open, &r.NewCount, &r.ErrorCount, &r.Message, &startedAt, &finishedAt, &r.DurationMS,
		&r.PolicyID, &r.HostsCount, &r.Engine, &r.ResultsNote)
	if err != nil {
		return nil, err
	}
	r.StartedAt = startedAt
	r.FinishedAt = finishedAt
	return r, nil
}

// CreateScan inserts a scan run record.
func (s *Store) CreateScan(r *model.ScanRun) (int64, error) {
	now := model.Time{}
	_ = now.Scan(Now())
	if r.Status == "" {
		r.Status = model.ScanStatusRunning
	}
	res, err := s.db.Exec(`INSERT INTO scans(name, target_kind, target_ref, ports_spec, trigger, status, progress, total, open_count, new_count, error_count, message, started_at, duration_ms, policy_id, hosts_count, engine)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.Name, r.TargetKind, r.TargetRef, r.PortsSpec, r.Trigger, r.Status, r.Progress, r.Total, r.Open, r.NewCount,
		r.ErrorCount, r.Message, now, 0, r.PolicyID, r.HostsCount, r.Engine)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	r.ID = id
	r.StartedAt = now
	return id, nil
}

// UpdateScanProgress refreshes live counters of a running scan.
func (s *Store) UpdateScanProgress(id int64, progress, total, openC, newC, errC int, message string) error {
	_, err := s.db.Exec(`UPDATE scans SET progress=?, total=?, open_count=?, new_count=?, error_count=?, message=? WHERE id=?`,
		progress, total, openC, newC, errC, message, id)
	return err
}

// FinishScan marks a scan finished.
func (s *Store) FinishScan(id int64, status string, durationMS int64, openC, newC, errC int, message string) error {
	_, err := s.db.Exec(`UPDATE scans SET status=?, finished_at=?, duration_ms=?, progress=100, open_count=?, new_count=?, error_count=?, message=? WHERE id=?`,
		status, Now(), durationMS, openC, newC, errC, message, id)
	return err
}

// GetScan fetches a scan run.
func (s *Store) GetScan(id int64) (*model.ScanRun, error) {
	row := s.db.QueryRow(`SELECT `+scanCols+` FROM scans WHERE id=?`, id)
	r, err := scanRun(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// ListScans returns recent scans.
func (s *Store) ListScans(limit, offset int) ([]model.ScanRun, int, error) {
	if limit <= 0 {
		limit = 50
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM scans`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(`SELECT `+scanCols+` FROM scans ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.ScanRun{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *r)
	}
	return out, total, rows.Err()
}

// DeleteScan removes a scan and its ports.
func (s *Store) DeleteScan(id int64) error {
	if _, err := s.db.Exec(`DELETE FROM ports WHERE scan_id=?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM scans WHERE id=?`, id)
	return err
}

// PurgeScans deletes scans older than the given number of days and their ports.
func (s *Store) PurgeScans(days int) (int64, error) {
	if days <= 0 {
		days = 30
	}
	cutoff := nowMinusDays(days)
	if _, err := s.db.Exec(`DELETE FROM ports WHERE scan_id IN (SELECT id FROM scans WHERE started_at < ?)`, cutoff); err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`DELETE FROM scans WHERE started_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountScansSince counts scans started after a timestamp.
func (s *Store) CountScansSince(ts string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM scans WHERE started_at >= ?`, ts).Scan(&n)
	return n, err
}

// ---- policies ----

const policyCols = `id, name, description, enabled, match_target, port_ranges, exclude_ranges, protocol, auto_action, auto_add, tags, note, min_port, max_port, require_name, priority, created_at, updated_at, mac`

func scanPolicy(sc interface{ Scan(...any) error }) (*model.Policy, error) {
	p := &model.Policy{}
	var enabled, autoAdd int
	var createdAt, updatedAt model.Time
	var tags model.StringList
	err := sc.Scan(&p.ID, &p.Name, &p.Description, &enabled, &p.MatchTarget, &p.PortRanges, &p.ExcludeRanges,
		&p.Protocol, &p.AutoAction, &autoAdd, &tags, &p.Note, &p.MinPort, &p.MaxPort, &p.RequireName, &p.Priority,
		&createdAt, &updatedAt, &p.MAC)
	if err != nil {
		return nil, err
	}
	p.Enabled, p.AutoAdd = i2b(enabled), i2b(autoAdd)
	p.CreatedAt, p.UpdatedAt = createdAt, updatedAt
	p.Tags = tags
	if p.Tags == nil {
		p.Tags = model.StringList{}
	}
	return p, nil
}

// CreatePolicy inserts a policy.
func (s *Store) CreatePolicy(p *model.Policy) (int64, error) {
	now := model.Time{}
	_ = now.Scan(Now())
	if p.Tags == nil {
		p.Tags = model.StringList{}
	}
	if p.MatchTarget == "" {
		p.MatchTarget = "any"
	}
	if p.AutoAction == "" {
		p.AutoAction = "watch"
	}
	res, err := s.db.Exec(`INSERT INTO policies(name, description, enabled, match_target, port_ranges, exclude_ranges, protocol, auto_action, auto_add, tags, note, min_port, max_port, require_name, priority, created_at, updated_at, mac)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.Name, p.Description, b2i(p.Enabled), p.MatchTarget, p.PortRanges, p.ExcludeRanges, p.Protocol, p.AutoAction,
		b2i(p.AutoAdd), p.Tags, p.Note, p.MinPort, p.MaxPort, p.RequireName, p.Priority, now, now, normalizeMAC(p.MAC))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	p.ID = id
	p.CreatedAt, p.UpdatedAt = now, now
	return id, nil
}

// UpdatePolicy writes back a policy.
func (s *Store) UpdatePolicy(p *model.Policy) error {
	now := model.Time{}
	_ = now.Scan(Now())
	if p.Tags == nil {
		p.Tags = model.StringList{}
	}
	_, err := s.db.Exec(`UPDATE policies SET name=?, description=?, enabled=?, match_target=?, port_ranges=?, exclude_ranges=?, protocol=?, auto_action=?, auto_add=?, tags=?, note=?, min_port=?, max_port=?, require_name=?, priority=?, updated_at=?, mac=? WHERE id=?`,
		p.Name, p.Description, b2i(p.Enabled), p.MatchTarget, p.PortRanges, p.ExcludeRanges, p.Protocol, p.AutoAction,
		b2i(p.AutoAdd), p.Tags, p.Note, p.MinPort, p.MaxPort, p.RequireName, p.Priority, now, normalizeMAC(p.MAC), p.ID)
	if err == nil {
		p.UpdatedAt = now
	}
	return err
}

// GetPolicy fetches a policy.
func (s *Store) GetPolicy(id int64) (*model.Policy, error) {
	row := s.db.QueryRow(`SELECT `+policyCols+` FROM policies WHERE id=?`, id)
	p, err := scanPolicy(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

// ListPolicies returns all policies ordered by priority.
func (s *Store) ListPolicies() ([]model.Policy, error) {
	rows, err := s.db.Query(`SELECT ` + policyCols + ` FROM policies ORDER BY priority DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Policy{}
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The host lookup runs after the result set is closed: SQLite hands out a
	// single connection here, so querying while iterating rows would deadlock.
	rows.Close()
	for i := range out {
		s.attachPolicyHost(&out[i])
	}
	return out, nil
}

// EnabledPolicies returns only enabled policies.
func (s *Store) EnabledPolicies() ([]model.Policy, error) {
	rows, err := s.db.Query(`SELECT ` + policyCols + ` FROM policies WHERE enabled=1 ORDER BY priority DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Policy{}
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// DeletePolicy removes a policy.
func (s *Store) DeletePolicy(id int64) error {
	_, err := s.db.Exec(`DELETE FROM policies WHERE id=?`, id)
	return err
}

// normalizeMAC canonicalises a hardware address (see model.NormalizeMAC). DHCP
// makes MAC the only stable host identity, so the comparison must not depend on
// how the address was spelled by whichever source discovered it.
func normalizeMAC(mac string) string { return model.NormalizeMAC(mac) }

// attachPolicyHost fills in the current address of a host-bound policy so the UI
// can show "this policy follows AA:BB:..." together with where that host is now.
func (s *Store) attachPolicyHost(p *model.Policy) {
	if p == nil || p.MAC == "" {
		return
	}
	h, err := s.GetHostByMAC(p.MAC)
	if err != nil || h == nil {
		return
	}
	p.HostIP, p.HostName = h.IP, h.Hostname
}

// ListPoliciesByHost returns the policies bound to one hardware address.
func (s *Store) ListPoliciesByHost(mac string) ([]model.Policy, error) {
	mac = normalizeMAC(mac)
	if mac == "" {
		return []model.Policy{}, nil
	}
	rows, err := s.db.Query(`SELECT `+policyCols+` FROM policies WHERE mac=? ORDER BY priority DESC, id ASC`, mac)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Policy{}
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Close before the host lookups (single SQLite connection, see ListPolicies).
	rows.Close()
	for i := range out {
		s.attachPolicyHost(&out[i])
	}
	return out, nil
}

// ---- schedules ----

const schedCols = `id, name, kind, enabled, cron, target_kind, target_ref, ports_spec, policy_id, last_run_at, next_run_at, last_status, run_count, created_at, updated_at`

func scanSchedule(sc interface{ Scan(...any) error }) (*model.Schedule, error) {
	sc2 := &model.Schedule{}
	var enabled int
	var lastRun, nextRun model.NullTime
	var createdAt, updatedAt model.Time
	err := sc.Scan(&sc2.ID, &sc2.Name, &sc2.Kind, &enabled, &sc2.Cron, &sc2.TargetKind, &sc2.TargetRef,
		&sc2.PortsSpec, &sc2.PolicyID, &lastRun, &nextRun, &sc2.LastStatus, &sc2.RunCount, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	sc2.Enabled = i2b(enabled)
	sc2.LastRunAt, sc2.NextRunAt = lastRun, nextRun
	sc2.CreatedAt, sc2.UpdatedAt = createdAt, updatedAt
	return sc2, nil
}

// CreateSchedule inserts a schedule.
func (s *Store) CreateSchedule(sc *model.Schedule) (int64, error) {
	now := model.Time{}
	_ = now.Scan(Now())
	if sc.Kind == "" {
		sc.Kind = "scan"
	}
	if sc.TargetKind == "" {
		sc.TargetKind = model.ScanTargetLocal
	}
	res, err := s.db.Exec(`INSERT INTO schedules(name, kind, enabled, cron, target_kind, target_ref, ports_spec, policy_id, last_status, run_count, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		sc.Name, sc.Kind, b2i(sc.Enabled), sc.Cron, sc.TargetKind, sc.TargetRef, sc.PortsSpec, sc.PolicyID, "", 0, now, now)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	sc.ID = id
	sc.CreatedAt, sc.UpdatedAt = now, now
	return id, nil
}

// UpdateSchedule writes back a schedule.
func (s *Store) UpdateSchedule(sc *model.Schedule) error {
	now := model.Time{}
	_ = now.Scan(Now())
	_, err := s.db.Exec(`UPDATE schedules SET name=?, kind=?, enabled=?, cron=?, target_kind=?, target_ref=?, ports_spec=?, policy_id=?, next_run_at=?, updated_at=? WHERE id=?`,
		sc.Name, sc.Kind, b2i(sc.Enabled), sc.Cron, sc.TargetKind, sc.TargetRef, sc.PortsSpec, sc.PolicyID, sc.NextRunAt, now, sc.ID)
	if err == nil {
		sc.UpdatedAt = now
	}
	return err
}

// MarkScheduleRun records a run outcome.
func (s *Store) MarkScheduleRun(id int64, status string, nextRun model.NullTime) error {
	_, err := s.db.Exec(`UPDATE schedules SET last_run_at=?, next_run_at=?, last_status=?, run_count=run_count+1 WHERE id=?`,
		Now(), nextRun, status, id)
	return err
}

// SetScheduleNextRun stores a freshly computed next fire time. It deliberately
// leaves run_count and last_run_at alone: merely working out when a schedule will
// fire next is not a run, and counting it made a never-executed schedule report
// "last run <now>" with run_count 1.
func (s *Store) SetScheduleNextRun(id int64, nextRun model.NullTime) error {
	_, err := s.db.Exec(`UPDATE schedules SET next_run_at=? WHERE id=?`, nextRun, id)
	return err
}

// GetSchedule fetches a schedule.
func (s *Store) GetSchedule(id int64) (*model.Schedule, error) {
	row := s.db.QueryRow(`SELECT `+schedCols+` FROM schedules WHERE id=?`, id)
	sc, err := scanSchedule(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return sc, err
}

// ListSchedules returns all schedules.
func (s *Store) ListSchedules() ([]model.Schedule, error) {
	rows, err := s.db.Query(`SELECT ` + schedCols + ` FROM schedules ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Schedule{}
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sc)
	}
	return out, rows.Err()
}

// EnabledSchedules returns only enabled schedules.
func (s *Store) EnabledSchedules() ([]model.Schedule, error) {
	rows, err := s.db.Query(`SELECT ` + schedCols + ` FROM schedules WHERE enabled=1 ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Schedule{}
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sc)
	}
	return out, rows.Err()
}

// DeleteSchedule removes a schedule.
func (s *Store) DeleteSchedule(id int64) error {
	_, err := s.db.Exec(`DELETE FROM schedules WHERE id=?`, id)
	return err
}
