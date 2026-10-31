package api

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/netutil"
	"github.com/ekko-studio/natpilot/internal/store"
)

// storeRuleFilter translates URL query parameters into a store filter.
func storeRuleFilter(q url.Values) store.RuleFilter {
	f := store.RuleFilter{
		Query:    strings.TrimSpace(q.Get("q")),
		Protocol: strings.TrimSpace(q.Get("protocol")),
		Source:   strings.TrimSpace(q.Get("source")),
		Host:     strings.TrimSpace(q.Get("host")),
		Status:   strings.TrimSpace(q.Get("status")),
		Sort:     q.Get("sort"),
		Order:    q.Get("order"),
		Limit:    atoiDefault(q.Get("limit"), 200),
		Offset:   atoiDefault(q.Get("offset"), 0),
	}
	if v, ok := q["tag"]; ok {
		f.Tags = v
	}
	if v := strings.TrimSpace(q.Get("tags")); v != "" {
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				f.Tags = append(f.Tags, t)
			}
		}
	}
	if raw, ok := q["enabled"]; ok && len(raw) > 0 {
		switch strings.ToLower(raw[0]) {
		case "1", "true", "yes":
			v := true
			f.Enabled = &v
		case "0", "false", "no":
			v := false
			f.Enabled = &v
		}
	}
	if f.Limit <= 0 || f.Limit > 2000 {
		f.Limit = 200
	}
	return f
}

// portStatusFilterFor resolves the discovered-port filter for a rule.
func portStatusFilterFor(r model.Rule) store.PortStatusFilter {
	return store.PortStatusFilter{
		Host:  r.Host,
		Query: strconv.Itoa(r.Port),
		Limit: 5,
	}
}

// guessService maps a port to its well known service name.
func guessService(port int) string { return netutil.GuessService(port) }

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// pageArgs reads the standard limit/offset pagination query parameters and
// clamps them to a sane range so a hostile value cannot ask for everything.
func pageArgs(q url.Values) (limit, offset int) {
	limit = 50
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 1000 {
		limit = 1000
	}
	if v := strings.TrimSpace(q.Get("offset")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}
	return limit, offset
}
