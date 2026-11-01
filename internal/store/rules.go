package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/ekko-studio/natpilot/internal/model"
)

const ruleCols = `id, name, host, port, ext_port, protocol, tags, note, enabled, auto_created, source, provider, created_at, updated_at, host_id, family`

// ruleJoinedCols adds the owning host's MAC and hostname for read paths. The
// join is on host_id, so a dual-stack device is one row with both families.
const ruleJoinedCols = `r.id, r.name, r.host, r.port, r.ext_port, r.protocol, r.tags, r.note,
	r.enabled, r.auto_created, r.source, r.provider, r.created_at, r.updated_at, r.host_id, r.family,
	COALESCE(h.mac,''), COALESCE(h.hostname,'')`

func scanRule(sc interface{ Scan(...any) error }) (*model.Rule, error) {
	r := &model.Rule{}
	var enabled, autoCreated int
	var createdAt, updatedAt model.Time
	var tags model.StringList
	err := sc.Scan(&r.ID, &r.Name, &r.Host, &r.Port, &r.ExtPort, &r.Protocol, &tags, &r.Note,
		&enabled, &autoCreated, &r.Source, &r.Provider, &createdAt, &updatedAt, &r.HostID, &r.Family)
	if err != nil {
		return nil, err
	}
	return finishRule(r, enabled, autoCreated, tags, createdAt, updatedAt), nil
}

func scanRuleJoined(sc interface{ Scan(...any) error }) (*model.Rule, error) {
	r := &model.Rule{}
	var enabled, autoCreated int
	var createdAt, updatedAt model.Time
	var tags model.StringList
	var mac, hostname string
	err := sc.Scan(&r.ID, &r.Name, &r.Host, &r.Port, &r.ExtPort, &r.Protocol, &tags, &r.Note,
		&enabled, &autoCreated, &r.Source, &r.Provider, &createdAt, &updatedAt, &r.HostID, &r.Family,
		&mac, &hostname)
	if err != nil {
		return nil, err
	}
	r.MAC, r.Hostname = mac, hostname
	return finishRule(r, enabled, autoCreated, tags, createdAt, updatedAt), nil
}

func finishRule(r *model.Rule, enabled, autoCreated int, tags model.StringList, createdAt, updatedAt model.Time) *model.Rule {
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
	return r
}

// CreateRule inserts a rule and returns its id.
//
// The rule is keyed by its host row when the address belongs to a known host:
// v4 and v6 rules for the same service collapse onto one row instead of hitting
// the host_id unique index. Keeping the legacy (host, port, protocol) index
// means a literal-keyed legacy row is adopted as well.
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
	hostID, family, err := s.resolveHostIdentity(r.Host)
	if err != nil {
		return 0, err
	}
	r.HostID, r.Family = hostID, family
	now := model.Time{}
	_ = now.Scan(Now())

	if existingID, existingCreated, err := s.findRuleRow(r); err != nil {
		return 0, err
	} else if existingID > 0 {
		if err := s.writeRuleRow(existingID, r, now); err != nil {
			return 0, err
		}
		r.ID = existingID
		r.UpdatedAt = now
		if existingCreated.Time.IsZero() {
			r.CreatedAt = now
		} else {
			r.CreatedAt = existingCreated
		}
		return existingID, nil
	}

	res, err := s.db.Exec(`INSERT INTO rules(name, host, port, ext_port, protocol, tags, note, enabled, auto_created, source, provider, created_at, updated_at, host_id, family)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.Name, r.Host, r.Port, r.ExtPort, r.Protocol, r.Tags, r.Note, b2i(r.Enabled), b2i(r.AutoCreated),
		orDefault(r.Source, "manual"), r.Provider, now, now, r.HostID, r.Family)
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

// findRuleRow returns the row a create/update should reuse instead of inserting
// a duplicate: the same identity when the host is known, then the literal key of
// a row written before the host existed.
func (s *Store) findRuleRow(r *model.Rule) (int64, model.Time, error) {
	var id int64
	var created model.Time
	if r.HostID > 0 {
		err := s.db.QueryRow(`SELECT id, created_at FROM rules WHERE host_id=? AND port=? AND protocol=?`,
			r.HostID, r.Port, r.Protocol).Scan(&id, &created)
		if err == nil {
			return id, created, nil
		}
		if err != sql.ErrNoRows {
			return 0, model.Time{}, err
		}
	}
	err := s.db.QueryRow(`SELECT id, created_at FROM rules WHERE host=? AND port=? AND protocol=?`,
		r.Host, r.Port, r.Protocol).Scan(&id, &created)
	if err == sql.ErrNoRows {
		return 0, model.Time{}, nil
	}
	if err != nil {
		return 0, model.Time{}, err
	}
	return id, created, nil
}

func (s *Store) writeRuleRow(id int64, r *model.Rule, now model.Time) error {
	_, err := s.db.Exec(`UPDATE rules SET name=?, host=?, port=?, ext_port=?, protocol=?, tags=?, note=?, enabled=?, auto_created=?, source=?, provider=?, updated_at=?, host_id=?, family=? WHERE id=?`,
		r.Name, r.Host, r.Port, r.ExtPort, r.Protocol, r.Tags, r.Note, b2i(r.Enabled), b2i(r.AutoCreated),
		orDefault(r.Source, "manual"), r.Provider, now, r.HostID, r.Family, id)
	return err
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// GetRule fetches a rule by id.
func (s *Store) GetRule(id int64) (*model.Rule, error) {
	row := s.db.QueryRow(`SELECT `+ruleJoinedCols+` FROM rules r LEFT JOIN hosts h ON h.id = r.host_id WHERE r.id = ?`, id)
	r, err := scanRuleJoined(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// GetRuleByKey finds a rule by its unique host/port/protocol tuple. The host
// lookup follows the host row when the address belongs to a known host, so a
// rule stored for the IPv4 address is still found when the same service is
// observed over IPv6.
func (s *Store) GetRuleByKey(host string, port int, proto string) (*model.Rule, error) {
	proto = orDefault(proto, model.ProtoTCP)
	hostID, _, err := s.resolveHostIdentity(host)
	if err != nil {
		return nil, err
	}
	if hostID > 0 {
		row := s.db.QueryRow(`SELECT `+ruleJoinedCols+` FROM rules r LEFT JOIN hosts h ON h.id = r.host_id
			WHERE r.host_id = ? AND r.port = ? AND r.protocol = ?`, hostID, port, proto)
		r, qerr := scanRuleJoined(row)
		if qerr == nil {
			return r, nil
		}
		if qerr != sql.ErrNoRows {
			return nil, qerr
		}
	}
	row := s.db.QueryRow(`SELECT `+ruleJoinedCols+` FROM rules r LEFT JOIN hosts h ON h.id = r.host_id
		WHERE r.host = ? AND r.port = ? AND r.protocol = ?`, host, port, proto)
	r, err := scanRuleJoined(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// UpdateRule writes back all mutable fields.
//
// The host identity is resolved again because an edit may repoint the rule at
// another address. If the new identity would collide with an existing rule, that
// canonical row is updated instead of returning a unique-constraint error; the
// two rows described the same service on the same host.
func (s *Store) UpdateRule(r *model.Rule) error {
	if r.Protocol == "" {
		r.Protocol = model.ProtoTCP
	}
	if r.Tags == nil {
		r.Tags = model.StringList{}
	}
	hostID, family, err := s.resolveHostIdentity(r.Host)
	if err != nil {
		return err
	}
	r.HostID, r.Family = hostID, family
	now := model.Time{}
	_ = now.Scan(Now())
	if err := s.writeRuleRow(r.ID, r, now); err != nil {
		if !isUniqueViolation(err) {
			return err
		}
		conflictID, _, ferr := s.findRuleRow(r)
		if ferr != nil {
			return ferr
		}
		if conflictID == 0 || conflictID == r.ID {
			return err
		}
		if werr := s.writeRuleRow(conflictID, r, now); werr != nil {
			return werr
		}
	}
	r.UpdatedAt = now
	return nil
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

// ListRules returns matching rules plus the total count. The owning host's MAC
// and hostname are joined in, so the rule list identifies the device without a
// second lookup and without relying on the address literal.
func (s *Store) ListRules(f RuleFilter) ([]model.Rule, int, error) {
	where, args := f.build()
	countQ := `SELECT COUNT(*) FROM rules r LEFT JOIN hosts h ON h.id = r.host_id ` + where
	var total int
	if err := s.db.QueryRow(countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "r.id DESC"
	switch f.Sort {
	case "port":
		order = "r.port " + dir(f.Order)
	case "host":
		order = "r.host " + dir(f.Order)
	case "name":
		order = "r.name " + dir(f.Order)
	case "updated":
		order = "r.updated_at " + dir(f.Order)
	case "created":
		order = "r.created_at " + dir(f.Order)
	case "enabled":
		order = "r.enabled " + dir(f.Order) + ", r.id DESC"
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT ` + ruleJoinedCols + ` FROM rules r LEFT JOIN hosts h ON h.id = r.host_id ` + where +
		` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	args = append(args, limit, f.Offset)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.Rule{}
	for rows.Next() {
		r, err := scanRuleJoined(rows)
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
		clauses = append(clauses, "(r.name LIKE ? OR r.host LIKE ? OR r.note LIKE ? OR CAST(r.port AS TEXT) LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like, like, like)
	}
	if f.Enabled != nil {
		clauses = append(clauses, "r.enabled = ?")
		args = append(args, b2i(*f.Enabled))
	}
	if f.Protocol != "" && f.Protocol != "any" {
		clauses = append(clauses, "r.protocol = ?")
		args = append(args, f.Protocol)
	}
	if f.Source != "" {
		clauses = append(clauses, "r.source = ?")
		args = append(args, f.Source)
	}
	if f.Host != "" {
		clauses = append(clauses, "r.host = ?")
		args = append(args, f.Host)
	}
	for _, t := range f.Tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		clauses = append(clauses, "r.tags LIKE ?")
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
