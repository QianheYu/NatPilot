package store

import (
	"database/sql"
	"time"

	"github.com/ekko-studio/natpilot/internal/model"
)

const userCols = `id, username, display_name, email, role, enabled, password_hash, created_at, updated_at, last_login_at`

func scanUser(sc interface{ Scan(...any) error }) (*model.User, error) {
	u := &model.User{}
	var enabled int
	var createdAt, updatedAt model.Time
	var lastLogin model.NullTime
	err := sc.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &enabled, &u.PasswordHash, &createdAt, &updatedAt, &lastLogin)
	if err != nil {
		return nil, err
	}
	u.Enabled = i2b(enabled)
	u.CreatedAt, u.UpdatedAt, u.LastLoginAt = createdAt, updatedAt, lastLogin
	return u, nil
}

// CreateUser inserts a user.
func (s *Store) CreateUser(u *model.User) (int64, error) {
	now := model.Time{}
	_ = now.Scan(Now())
	if u.Role == "" {
		u.Role = "admin"
	}
	res, err := s.db.Exec(`INSERT INTO users(username, display_name, email, role, enabled, password_hash, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		u.Username, u.DisplayName, u.Email, u.Role, b2i(u.Enabled), u.PasswordHash, now, now)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	u.ID = id
	u.CreatedAt, u.UpdatedAt = now, now
	return id, nil
}

// UpdateUser writes back user fields (password hash updated only when non-empty).
func (s *Store) UpdateUser(u *model.User) error {
	now := model.Time{}
	_ = now.Scan(Now())
	if u.PasswordHash != "" {
		_, err := s.db.Exec(`UPDATE users SET display_name=?, email=?, role=?, enabled=?, password_hash=?, updated_at=? WHERE id=?`,
			u.DisplayName, u.Email, u.Role, b2i(u.Enabled), u.PasswordHash, now, u.ID)
		return err
	}
	_, err := s.db.Exec(`UPDATE users SET display_name=?, email=?, role=?, enabled=?, updated_at=? WHERE id=?`,
		u.DisplayName, u.Email, u.Role, b2i(u.Enabled), now, u.ID)
	return err
}

// GetUserByName finds a user by login name.
func (s *Store) GetUserByName(name string) (*model.User, error) {
	row := s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE username=?`, name)
	u, err := scanUser(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

// GetUser fetches a user by id.
func (s *Store) GetUser(id int64) (*model.User, error) {
	row := s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id)
	u, err := scanUser(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

// ListUsers returns every user.
func (s *Store) ListUsers() ([]model.User, error) {
	rows, err := s.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// DeleteUser removes a user.
func (s *Store) DeleteUser(id int64) error {
	_, err := s.db.Exec(`DELETE FROM users WHERE id=?`, id)
	return err
}

// CountUsers returns the number of user accounts.
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// TouchLogin records a successful login timestamp.
func (s *Store) TouchLogin(id int64) error {
	_, err := s.db.Exec(`UPDATE users SET last_login_at=? WHERE id=?`, Now(), id)
	return err
}

// ---- sessions ----

// CreateSession stores a session row.
func (s *Store) CreateSession(token string, userID int64, ttl time.Duration, ip, ua string) error {
	exp := time.Now().UTC().Add(ttl)
	_, err := s.db.Exec(`INSERT INTO sessions(token, user_id, created_at, expires_at, ip, user_agent) VALUES(?,?,?,?,?,?)`,
		token, userID, Now(), exp.Format(time.RFC3339Nano), ip, ua)
	return err
}

// GetSession returns the user id for a non-expired session token.
func (s *Store) GetSession(token string) (int64, bool, error) {
	var uid int64
	var exp string
	err := s.db.QueryRow(`SELECT user_id, expires_at FROM sessions WHERE token=?`, token).Scan(&uid, &exp)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	t, perr := time.Parse(time.RFC3339Nano, exp)
	if perr != nil {
		return uid, true, nil
	}
	if time.Now().UTC().After(t) {
		_ = s.DeleteSession(token)
		return 0, false, nil
	}
	return uid, true, nil
}

// DeleteSession removes a session token.
func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token=?`, token)
	return err
}

// DeleteUserSessions removes every session for a user.
func (s *Store) DeleteUserSessions(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id=?`, userID)
	return err
}

// PurgeExpiredSessions removes stale session rows.
func (s *Store) PurgeExpiredSessions() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, Now())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- audit ----

// AddAudit appends an audit entry.
func (s *Store) AddAudit(a *model.AuditEntry) error {
	now := model.Time{}
	_ = now.Scan(Now())
	a.At = now
	_, err := s.db.Exec(`INSERT INTO audit(at, actor, action, object, detail, ip, user_agent, success) VALUES(?,?,?,?,?,?,?,?)`,
		now, a.Actor, a.Action, a.Object, a.Detail, a.IP, a.UserAgent, b2i(a.Success))
	return err
}

// ListAudit returns recent audit entries.
func (s *Store) ListAudit(limit, offset int) ([]model.AuditEntry, int, error) {
	if limit <= 0 {
		limit = 100
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(`SELECT id, at, actor, action, object, detail, ip, user_agent, success FROM audit ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.AuditEntry{}
	for rows.Next() {
		var a model.AuditEntry
		var at model.Time
		var success int
		if err := rows.Scan(&a.ID, &at, &a.Actor, &a.Action, &a.Object, &a.Detail, &a.IP, &a.UserAgent, &success); err != nil {
			return nil, 0, err
		}
		a.At = at
		a.Success = i2b(success)
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// ---- events ----

// AddEvent appends a system event.
func (s *Store) AddEvent(level, category, message string, meta model.JSONMap) error {
	if meta == nil {
		meta = model.JSONMap{}
	}
	_, err := s.db.Exec(`INSERT INTO events(at, level, category, message, meta, read) VALUES(?,?,?,?,?,0)`,
		Now(), level, category, message, meta)
	return err
}

// EventFilter narrows an event listing.
type EventFilter struct {
	Level    string
	Category string
	Query    string
	Unread   bool
	Limit    int
	Offset   int
}

// ListEvents returns events matching the filter.
func (s *Store) ListEvents(f EventFilter) ([]model.SystemEvent, int, error) {
	clauses := []string{}
	args := []any{}
	if f.Level != "" {
		clauses = append(clauses, "level = ?")
		args = append(args, f.Level)
	}
	if f.Category != "" {
		clauses = append(clauses, "category = ?")
		args = append(args, f.Category)
	}
	if f.Query != "" {
		clauses = append(clauses, "(message LIKE ? OR category LIKE ?)")
		args = append(args, "%"+f.Query+"%", "%"+f.Query+"%")
	}
	if f.Unread {
		clauses = append(clauses, "read = 0")
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + joinAnd(clauses)
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT id, at, level, category, message, meta, read FROM events ` + where + ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, f.Offset)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.SystemEvent{}
	for rows.Next() {
		var e model.SystemEvent
		var at model.Time
		var read int
		if err := rows.Scan(&e.ID, &at, &e.Level, &e.Category, &e.Message, &e.Meta, &read); err != nil {
			return nil, 0, err
		}
		e.At = at
		e.Read = i2b(read)
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// CountUnreadEvents returns the unread event count.
func (s *Store) CountUnreadEvents() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE read=0`).Scan(&n)
	return n, err
}

// MarkEventsRead marks the given events read (empty = all).
func (s *Store) MarkEventsRead(ids []int64) error {
	if len(ids) == 0 {
		_, err := s.db.Exec(`UPDATE events SET read=1 WHERE read=0`)
		return err
	}
	q, args := inClause("id", ids)
	_, err := s.db.Exec(`UPDATE events SET read=1 WHERE `+q, args...)
	return err
}

// ClearEvents deletes events, keeping at most keep recent rows.
func (s *Store) ClearEvents(keep int) (int64, error) {
	if keep <= 0 {
		res, err := s.db.Exec(`DELETE FROM events`)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	}
	res, err := s.db.Exec(`DELETE FROM events WHERE id NOT IN (SELECT id FROM events ORDER BY id DESC LIMIT ?)`, keep)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TrimEvents keeps the newest n events.
func (s *Store) TrimEvents(n int) (int64, error) {
	if n <= 0 {
		return 0, nil
	}
	res, err := s.db.Exec(`DELETE FROM events WHERE id NOT IN (SELECT id FROM events ORDER BY id DESC LIMIT ?)`, n)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func joinAnd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " AND "
		}
		out += p
	}
	return out
}

func nowMinusDays(days int) string {
	return time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339Nano)
}
