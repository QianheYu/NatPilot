package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/ekko-studio/natpilot/internal/model"
)

const ruleCols = `id, name, host, port, ext_port, protocol, tags, note, enabled, auto_created, source, provider, created_at, updated_at`

func scanRule(sc interface{ Scan(...any) error }) (*model.Rule, error) {
	r := &model.Rule{}
	var enabled, autoCreated int
	var createdAt, updatedAt model.Time
	var tags model.StringList
	err := sc.Scan(&r.ID, &r.Name, &r.Host, &r.Port, &r.ExtPort, &r.Protocol, &tags, &r.Note,
		&enabled, &autoCreated, &r.Source, &r.Provider, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	r.Enabled = i2b(enabled)
	r.AutoCreated = i2b(autoCreated)
	r.CreatedAt = createdAt
	r.UpdatedAt = updatedAt
	r.Tags = tags
	if r.Tags == nil {
		r.Tags = model.StringList{}
	}
	if r.Protocol == "" {
		r.Protocol = model.ProtoTCP
	}
	return r, nil
}

// CreateRule inserts a rule and returns its id.
func (s *Store) CreateRule(r *model.Rule) (int64, error) {
	if r.Protocol == "" {
		r.Protocol = model.ProtoTCP
	}
	if r.Host == "" {
		r.Host = "127.0.0.1"
	}
	if r.Tags == nil {
		r.Tags = model.StringList{}
	}
	now := model.Time{}
	_ = now.Scan(Now())
	res, err := s.db.Exec(`INSERT INTO rules(name, host, port, ext_port, protocol, tags, note, enabled, auto_created, source, provider, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.Name, r.Host, r.Port, r.ExtPort, r.Protocol, r.Tags, r.Note, b2i(r.Enabled), b2i(r.AutoCreated), orDefault(r.Source, "manual"), r.Provider, now, now)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	r.ID = id
	r.CreatedAt = now
	r.UpdatedAt = now
	return id, nil
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// GetRule fetches a rule by id.
func (s *Store) GetRule(id int64) (*model.Rule, error) {
	row := s.db.QueryRow(`SELECT `+ruleCols+` FROM rules WHERE id = ?`, id)
	r, err := scanRule(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// GetRuleByKey finds a rule by its unique host/port/protocol tuple.
func (s *Store) GetRuleByKey(host string, port int, proto string) (*model.Rule, error) {
	row := s.db.QueryRow(`SELECT `+ruleCols+` FROM rules WHERE host = ? AND port = ? AND protocol = ?`, host, port, orDefault(proto, model.ProtoTCP))
	r, err := scanRule(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// UpdateRule writes back all mutable fields.
func (s *Store) UpdateRule(r *model.Rule) error {
	if r.Protocol == "" {
		r.Protocol = model.ProtoTCP
	}
	if r.Tags == nil {
		r.Tags = model.StringList{}
	}
	now := model.Time{}
	_ = now.Scan(Now())
	_, err := s.db.Exec(`UPDATE rules SET name=?, host=?, port=?, ext_port=?, protocol=?, tags=?, note=?, enabled=?, auto_created=?, source=?, provider=?, updated_at=? WHERE id=?`,
		r.Name, r.Host, r.Port, r.ExtPort, r.Protocol, r.Tags, r.Note, b2i(r.Enabled), b2i(r.AutoCreated), r.Source, r.Provider, now, r.ID)
	if err == nil {
		r.UpdatedAt = now
	}
	return err
}

// DeleteRule removes a rule by id.
func (s *Store) DeleteRule(id int64) error {
	_, err := s.db.Exec(`DELETE FROM rules WHERE id = ?`, id)
	return err
}

// BulkDeleteRules removes several rules.
func (s *Store) BulkDeleteRules(ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	q, args := inClause("id", ids)
	res, err := s.db.Exec(`DELETE FROM rules WHERE `+q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetRulesEnabled toggles the enabled flag for a set of rules.
func (s *Store) SetRulesEnabled(ids []int64, enabled bool) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	q, args := inClause("id", ids)
	args = append([]any{b2i(enabled), Now()}, args...)
	res, err := s.db.Exec(`UPDATE rules SET enabled=?, updated_at=? WHERE `+q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// BulkedRulesEnabled sets enabled plus tags/note for several rules.
func (s *Store) TagRules(ids []int64, add []string, remove []string) (int64, error) {
	var affected int64
	for _, id := range ids {
		r, err := s.GetRule(id)
		if err != nil || r == nil {
			continue
		}
		set := map[string]bool{}
		for _, t := range r.Tags {
			set[t] = true
		}
		for _, t := range add {
			t = strings.TrimSpace(t)
			if t != "" {
				set[t] = true
			}
		}
		for _, t := range remove {
			delete(set, strings.TrimSpace(t))
		}
		tags := model.StringList{}
		for t := range set {
			tags = append(tags, t)
		}
		sortStrings(tags)
		r.Tags = tags
		if err := s.UpdateRule(r); err == nil {
			affected++
		}
	}
	return affected, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// RuleFilter narrows a rule listing.
type RuleFilter struct {
	Query    string
	Tags     []string
	Enabled  *bool
	Status   string
	Protocol string
	Source   string
	Host     string
	Sort     string
	Order    string
	Limit    int
	Offset   int
}

// ListRules returns matching rules plus the total count.
func (s *Store) ListRules(f RuleFilter) ([]model.Rule, int, error) {
	where, args := f.build()
	countQ := `SELECT COUNT(*) FROM rules ` + where
	var total int
	if err := s.db.QueryRow(countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "id DESC"
	switch f.Sort {
	case "port":
		order = "port " + dir(f.Order)
	case "host":
		order = "host " + dir(f.Order)
	case "name":
		order = "name " + dir(f.Order)
	case "updated":
		order = "updated_at " + dir(f.Order)
	case "created":
		order = "created_at " + dir(f.Order)
	case "enabled":
		order = "enabled " + dir(f.Order) + ", id DESC"
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT ` + ruleCols + ` FROM rules ` + where + ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	args = append(args, limit, f.Offset)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *r)
	}
	return out, total, rows.Err()
}

func dir(o string) string {
	if strings.EqualFold(o, "asc") {
		return "ASC"
	}
	return "DESC"
}

func (f RuleFilter) build() (string, []any) {
	clauses := []string{}
	args := []any{}
	if q := strings.TrimSpace(f.Query); q != "" {
		clauses = append(clauses, "(name LIKE ? OR host LIKE ? OR note LIKE ? OR CAST(port AS TEXT) LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like, like, like)
	}
	if f.Enabled != nil {
		clauses = append(clauses, "enabled = ?")
		args = append(args, b2i(*f.Enabled))
	}
	if f.Protocol != "" && f.Protocol != "any" {
		clauses = append(clauses, "protocol = ?")
		args = append(args, f.Protocol)
	}
	if f.Source != "" {
		clauses = append(clauses, "source = ?")
		args = append(args, f.Source)
	}
	if f.Host != "" {
		clauses = append(clauses, "host = ?")
		args = append(args, f.Host)
	}
	for _, t := range f.Tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		clauses = append(clauses, "tags LIKE ?")
		args = append(args, "%\""+t+"\"%")
	}
	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// EnabledRules returns every enabled rule (the mapping working set).
func (s *Store) EnabledRules() ([]model.Rule, error) {
	rows, err := s.db.Query(`SELECT ` + ruleCols + ` FROM rules WHERE enabled = 1 ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// AllRules returns every rule regardless of state.
func (s *Store) AllRules() ([]model.Rule, error) {
	rows, err := s.db.Query(`SELECT ` + ruleCols + ` FROM rules ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// DistinctTags lists every tag in use with its rule count.
func (s *Store) DistinctTags() (map[string]int, error) {
	rules, err := s.AllRules()
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, r := range rules {
		for _, t := range r.Tags {
			out[t]++
		}
	}
	return out, nil
}

// inClause builds "col IN (?,?,...)" with args.
func inClause(col string, ids []int64) (string, []any) {
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	return fmt.Sprintf("%s IN (%s)", col, strings.Join(ph, ",")), args
}

// ---- mappings ----

const mappingCols = `id, rule_id, provider_name, protocol, status, external_ip, external_port, internal_host, internal_port, lease_seconds, description, error, created_at, updated_at, expires_at`

func scanMapping(sc interface{ Scan(...any) error }) (*model.MappingRecord, error) {
	m := &model.MappingRecord{}
	var createdAt, updatedAt model.Time
	var expiresAt model.NullTime
	err := sc.Scan(&m.ID, &m.RuleID, &m.ProviderName, &m.Protocol, &m.Status, &m.ExternalIP, &m.ExternalPort,
		&m.InternalHost, &m.InternalPort, &m.LeaseSeconds, &m.Description, &m.Error, &createdAt, &updatedAt, &expiresAt)
	if err != nil {
		return nil, err
	}
	m.CreatedAt, m.UpdatedAt, m.ExpiresAt = createdAt, updatedAt, expiresAt
	return m, nil
}

// UpsertMapping stores the latest mapping attempt for a rule/provider.
//
// This is a single UPSERT statement on purpose: the previous SELECT-then-INSERT
// sequence let two concurrent reconcile passes both observe "no row" and insert
// duplicates for the same rule/provider/protocol, after which updates only ever
// touched the first row and the stale copy stayed behind.
func (s *Store) UpsertMapping(m *model.MappingRecord) error {
	now := model.Time{}
	_ = now.Scan(Now())
	_, err := s.db.Exec(`INSERT INTO mappings(rule_id, provider_name, protocol, status, external_ip, external_port, internal_host, internal_port, lease_seconds, description, error, created_at, updated_at, expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(rule_id, provider_name, protocol) DO UPDATE SET
			status=excluded.status,
			external_ip=excluded.external_ip,
			external_port=excluded.external_port,
			internal_host=excluded.internal_host,
			internal_port=excluded.internal_port,
			lease_seconds=excluded.lease_seconds,
			description=excluded.description,
			error=excluded.error,
			updated_at=excluded.updated_at,
			expires_at=excluded.expires_at`,
		m.RuleID, m.ProviderName, m.Protocol, m.Status, m.ExternalIP, m.ExternalPort, m.InternalHost, m.InternalPort,
		m.LeaseSeconds, m.Description, m.Error, now, now, m.ExpiresAt)
	if err != nil {
		return err
	}
	var id int64
	if qerr := s.db.QueryRow(`SELECT id FROM mappings WHERE rule_id = ? AND provider_name = ? AND protocol = ?`,
		m.RuleID, m.ProviderName, m.Protocol).Scan(&id); qerr == nil {
		m.ID = id
	}
	m.UpdatedAt = now
	return nil
}

// ListMappings returns mapping records for the given rule ids (empty = all).
func (s *Store) ListMappings(ruleIDs []int64) ([]model.MappingRecord, error) {
	q := `SELECT ` + mappingCols + ` FROM mappings`
	args := []any{}
	if len(ruleIDs) > 0 {
		c, a := inClause("rule_id", ruleIDs)
		q += ` WHERE ` + c
		args = append(args, a...)
	}
	q += ` ORDER BY updated_at DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.MappingRecord{}
	for rows.Next() {
		m, err := scanMapping(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// DeleteMappingsForRule removes every mapping record of a rule.
func (s *Store) DeleteMappingsForRule(ruleID int64) error {
	_, err := s.db.Exec(`DELETE FROM mappings WHERE rule_id = ?`, ruleID)
	return err
}

// MappingsByRule returns a map ruleID -> latest mapping.
func (s *Store) MappingsByRule() (map[int64]model.MappingRecord, error) {
	list, err := s.ListMappings(nil)
	if err != nil {
		return nil, err
	}
	out := map[int64]model.MappingRecord{}
	for _, m := range list {
		if prev, ok := out[m.RuleID]; !ok || m.UpdatedAt.Time.After(prev.UpdatedAt.Time) {
			out[m.RuleID] = m
		}
	}
	return out, nil
}
