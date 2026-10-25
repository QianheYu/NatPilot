package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Time is a nullable-friendly time.Time wrapper that persists as RFC3339 text
// and marshals to JSON as RFC3339 (or null when zero).
type Time struct{ time.Time }

// MarshalJSON implements json.Marshaler.
func (t Time) MarshalJSON() ([]byte, error) {
	if t.Time.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(t.Time.UTC().Format(time.RFC3339))
}

// UnmarshalJSON implements json.Unmarshaler.
func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		if string(b) == "null" {
			t.Time = time.Time{}
			return nil
		}
		return err
	}
	if s == "" {
		t.Time = time.Time{}
		return nil
	}
	return t.parse(s)
}

func (t *Time) parse(s string) error {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05", "2006-01-02"} {
		if v, err := time.Parse(layout, s); err == nil {
			t.Time = v
			return nil
		}
	}
	return fmt.Errorf("cannot parse time %q", s)
}

// Scan implements sql.Scanner.
func (t *Time) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		t.Time = time.Time{}
	case time.Time:
		t.Time = v
	case string:
		if v == "" {
			t.Time = time.Time{}
			return nil
		}
		return t.parse(v)
	case []byte:
		if len(v) == 0 {
			t.Time = time.Time{}
			return nil
		}
		return t.parse(string(v))
	case int64:
		t.Time = time.Unix(v, 0).UTC()
	case float64:
		t.Time = time.Unix(int64(v), 0).UTC()
	default:
		return fmt.Errorf("unsupported time source %T", src)
	}
	return nil
}

// Value implements driver.Valuer.
func (t Time) Value() (driver.Value, error) {
	if t.Time.IsZero() {
		return nil, nil
	}
	return t.Time.UTC().Format(time.RFC3339Nano), nil
}

// NullTime mirrors sql.NullTime using the Time wrapper.
type NullTime struct {
	Time  Time
	Valid bool
}

// Scan implements sql.Scanner.
func (n *NullTime) Scan(src any) error {
	if src == nil {
		n.Valid = false
		n.Time = Time{}
		return nil
	}
	if err := n.Time.Scan(src); err != nil {
		return err
	}
	n.Valid = !n.Time.Time.IsZero()
	return nil
}

// Value implements driver.Valuer.
func (n NullTime) Value() (driver.Value, error) {
	if !n.Valid || n.Time.Time.IsZero() {
		return nil, nil
	}
	return n.Time.Time.UTC().Format(time.RFC3339Nano), nil
}

// MarshalJSON implements json.Marshaler.
func (n NullTime) MarshalJSON() ([]byte, error) {
	if !n.Valid || n.Time.Time.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(n.Time.Time.UTC().Format(time.RFC3339))
}

// UnmarshalJSON implements json.Unmarshaler.
func (n *NullTime) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		n.Valid = false
		return nil
	}
	if err := n.Time.UnmarshalJSON(b); err != nil {
		return err
	}
	n.Valid = !n.Time.Time.IsZero()
	return nil
}

// Ptr returns a NullTime pointing at t.
func Ptr(t time.Time) NullTime { return NullTime{Time: Time{t}, Valid: true} }

// StringList is a string slice persisted as a JSON array (or a comma separated
// string when the data comes from an older/hand-written row).
type StringList []string

// Value implements driver.Valuer.
func (l StringList) Value() (driver.Value, error) {
	if l == nil {
		l = StringList{}
	}
	b, err := json.Marshal([]string(l))
	return string(b), err
}

// Scan implements sql.Scanner.
func (l *StringList) Scan(src any) error {
	var raw string
	switch v := src.(type) {
	case nil:
		*l = StringList{}
		return nil
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		return fmt.Errorf("unsupported StringList source %T", src)
	}
	raw = trimSpace(raw)
	if raw == "" {
		*l = StringList{}
		return nil
	}
	if raw[0] == '[' {
		var out []string
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return err
		}
		*l = StringList(out)
		return nil
	}
	out := []string{}
	for _, part := range splitComma(raw) {
		if p := trimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	*l = StringList(out)
	return nil
}

// JSONMap is a string-keyed map persisted as a JSON object.
type JSONMap map[string]any

// Value implements driver.Valuer.
func (m JSONMap) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(map[string]any(m))
	return string(b), err
}

// Scan implements sql.Scanner.
func (m *JSONMap) Scan(src any) error {
	var raw string
	switch v := src.(type) {
	case nil:
		*m = nil
		return nil
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		return fmt.Errorf("unsupported JSONMap source %T", src)
	}
	if trimSpace(raw) == "" {
		*m = nil
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return err
	}
	*m = JSONMap(out)
	return nil
}
