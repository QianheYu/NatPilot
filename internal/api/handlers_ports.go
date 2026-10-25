package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/store"
)

func (s *Server) handleListPorts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.PortStatusFilter{
		Host:       strings.TrimSpace(q.Get("host")),
		Query:      strings.TrimSpace(q.Get("q")),
		TargetKind: strings.TrimSpace(q.Get("target_kind")),
		Sort:       q.Get("sort"),
		Order:      q.Get("order"),
		Limit:      atoiDefault(q.Get("limit"), 500),
		Offset:     atoiDefault(q.Get("offset"), 0),
		ScanID:     int64(atoiDefault(q.Get("scan_id"), 0)),
	}
	if v := strings.TrimSpace(q.Get("status")); v != "" {
		f.Statuses = splitCSV(v)
	}
	if v, ok := queryBool(r, "open_only"); ok {
		f.OnlyOpen = v
	}
	if v, ok := queryBool(r, "added"); ok {
		f.OnlyAdded = &v
	}
	if f.Limit <= 0 || f.Limit > 5000 {
		f.Limit = 500
	}
	ports, total, err := s.app.Store.ListPorts(f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	counts, _ := s.app.Store.CountPortsByStatus()
	writeJSON(w, http.StatusOK, map[string]any{
		"ports":  ports,
		"total":  total,
		"counts": counts,
		"limit":  f.Limit,
		"offset": f.Offset,
	})
}

func splitCSV(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// portActionRequest drives a decision on a discovered port.
type portActionRequest struct {
	Decision string   `json:"decision"` // add|ignore|watch|delete
	Tags     []string `json:"tags"`
	Note     string   `json:"note"`
	Enabled  *bool    `json:"enabled"`
	Host     string   `json:"host"`
	Name     string   `json:"name"`
	ExtPort  int      `json:"ext_port"`
}

func (s *Server) handlePortAction(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid port id")
		return
	}
	port, err := s.app.Store.GetPort(id)
	if err != nil || port == nil {
		writeError(w, http.StatusNotFound, "discovered port not found")
		return
	}
	var req portActionRequest
	if err := decodeJSONLoose(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	decision := strings.ToLower(strings.TrimSpace(req.Decision))
	if decision == "" {
		decision = model.ActionAdd
	}

	switch decision {
	case model.ActionAdd:
		rule, created, aerr := s.addRuleForPort(port, req)
		if aerr != nil {
			writeError(w, http.StatusBadRequest, aerr.Error())
			return
		}
		_ = s.app.Store.MarkPortAdded(port.ID, rule.ID)
		var sum any
		var applyErr string
		if rule.Enabled {
			sm, err2 := s.app.Mapper.ApplyRule(r.Context(), *rule)
			sum = sm
			if err2 != nil {
				applyErr = err2.Error()
			}
		}
		s.audit(r, "port.add", fmt.Sprintf("%s:%d", port.Host, port.Port), fmt.Sprintf("rule=%d", rule.ID), applyErr == "")
		out := map[string]any{"ok": true, "rule": rule, "created": created, "status": model.StatusAdded}
		if sum != nil {
			out["result"] = sum
		}
		if applyErr != "" {
			out["apply_error"] = applyErr
		}
		writeJSON(w, http.StatusOK, out)

	case model.ActionIgnore:
		if err := s.app.Store.MarkPortIgnored(port.ID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.audit(r, "port.ignore", fmt.Sprintf("%s:%d", port.Host, port.Port), "", true)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": model.StatusClosed, "ignored": true})

	case model.ActionWatch:
		if err := s.app.Store.SetPortStatus(port.ID, model.StatusNotAdded, model.ActionWatch); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": model.StatusNotAdded})

	case model.ActionDelete:
		// Deleting from the list also removes the managed rule when one exists.
		removedRule := int64(0)
		if port.RuleID > 0 {
			if rule, _ := s.app.Store.GetRule(port.RuleID); rule != nil {
				_ = s.app.Mapper.RemoveMapping(r.Context(), *rule)
				_ = s.app.Store.DeleteRule(rule.ID)
				_ = s.app.Store.DeleteMappingsForRule(rule.ID)
				removedRule = rule.ID
			}
		}
		if err := s.app.Store.DeletePort(port.ID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.audit(r, "port.delete", fmt.Sprintf("%s:%d", port.Host, port.Port), "", true)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": port.ID, "removed_rule": removedRule})

	default:
		writeError(w, http.StatusBadRequest, "decision must be one of add, ignore, watch, delete")
	}
}

// addRuleForPort creates (or reuses) a rule for a discovered port.
func (s *Server) addRuleForPort(port *model.DiscoveredPort, req portActionRequest) (*model.Rule, bool, error) {
	// Reuse the rule linked to the port, or any rule with the same key.
	if port.RuleID > 0 {
		if rule, _ := s.app.Store.GetRule(port.RuleID); rule != nil {
			return rule, false, nil
		}
	}
	if existing, _ := s.app.Store.GetRuleByKey(port.Host, port.Port, port.Protocol); existing != nil {
		return existing, false, nil
	}
	// A rule on the same port but a different host: reuse that too, since the
	// gateway only needs one mapping per external port.
	rules, _ := s.app.Store.AllRules()
	for i := range rules {
		if rules[i].Port == port.Port && strings.EqualFold(rules[i].Protocol, port.Protocol) {
			return &rules[i], false, nil
		}
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		svc := port.Service
		if svc == "" {
			svc = guessService(port.Port)
		}
		if svc != "" {
			name = fmt.Sprintf("%s %s:%d", strings.ToUpper(port.Protocol), port.Host, port.Port)
			if svc != "" {
				name = fmt.Sprintf("%s (%s)", name, svc)
			}
		} else {
			name = fmt.Sprintf("%s %s:%d", strings.ToUpper(port.Protocol), port.Host, port.Port)
		}
	}
	host := strings.TrimSpace(req.Host)
	if host == "" {
		host = port.Host
	}
	rule := &model.Rule{
		Name:     name,
		Host:     host,
		Port:     port.Port,
		ExtPort:  req.ExtPort,
		Protocol: port.Protocol,
		Tags:     model.StringList(req.Tags),
		Note:     req.Note,
		Enabled:  enabled,
		Source:   "scan",
	}
	if rule.Tags == nil {
		rule.Tags = model.StringList{}
	}
	id, err := s.app.Store.CreateRule(rule)
	if err != nil {
		return nil, false, err
	}
	rule.ID = id
	return rule, true, nil
}

// handlePortsAdd adds several discovered ports to the management list at once.
func (s *Server) handlePortsAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs     []int64  `json:"ids"`
		Tags    []string `json:"tags"`
		Note    string   `json:"note"`
		Enabled *bool    `json:"enabled"`
	}
	if err := decodeJSONLoose(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "no port ids supplied")
		return
	}
	added, failed := 0, 0
	applied := 0
	ruleIDs := []int64{}
	for _, id := range req.IDs {
		port, err := s.app.Store.GetPort(id)
		if err != nil || port == nil {
			failed++
			continue
		}
		rule, _, aerr := s.addRuleForPort(port, portActionRequest{Tags: req.Tags, Note: req.Note, Enabled: req.Enabled})
		if aerr != nil {
			failed++
			continue
		}
		_ = s.app.Store.MarkPortAdded(port.ID, rule.ID)
		ruleIDs = append(ruleIDs, rule.ID)
		added++
		if rule.Enabled {
			if _, err := s.app.Mapper.ApplyRule(r.Context(), *rule); err == nil {
				applied++
			}
		}
	}
	s.audit(r, "port.bulk_add", fmt.Sprintf("%d ports", len(req.IDs)), fmt.Sprintf("added=%d applied=%d", added, applied), failed == 0)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": added, "failed": failed, "applied": applied, "rule_ids": ruleIDs})
}

func (s *Server) handlePatchPort(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid port id")
		return
	}
	port, err := s.app.Store.GetPort(id)
	if err != nil || port == nil {
		writeError(w, http.StatusNotFound, "discovered port not found")
		return
	}
	var req struct {
		Tags []string `json:"tags"`
		Note *string  `json:"note"`
	}
	if err := decodeJSONLoose(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	note := port.Note
	if req.Note != nil {
		note = *req.Note
	}
	tags := port.Tags
	if req.Tags != nil {
		tags = model.StringList(req.Tags)
	}
	if err := s.app.Store.SetPortNote(id, tags, note); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "tags": tags, "note": note})
}

func (s *Server) handleDeletePort(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid port id")
		return
	}
	ruleRemoved := int64(0)
	if port, _ := s.app.Store.GetPort(id); port != nil && port.RuleID > 0 {
		if rule, _ := s.app.Store.GetRule(port.RuleID); rule != nil {
			_ = s.app.Mapper.RemoveMapping(r.Context(), *rule)
			_ = s.app.Store.DeleteRule(rule.ID)
			_ = s.app.Store.DeleteMappingsForRule(rule.ID)
			ruleRemoved = rule.ID
		}
	}
	if err := s.app.Store.DeletePort(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "port.delete", fmt.Sprintf("port %d", id), "", true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": id, "removed_rule": ruleRemoved})
}

// handleBulkPorts applies an action to many discovered ports.
func (s *Server) handleBulkPorts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs      []int64  `json:"ids"`
		Action   string   `json:"action"`
		Statuses []string `json:"statuses"`
		Host     string   `json:"host"`
		Tags     []string `json:"tags"`
	}
	if err := decodeJSONLoose(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	result := map[string]any{}

	// Bulk-by-criteria cleanup uses statuses/host instead of ids.
	if len(req.IDs) == 0 {
		switch action {
		case "clear", "delete", "clear_closed":
			statuses := req.Statuses
			if len(statuses) == 0 {
				statuses = []string{model.StatusClosed, model.StatusSourceGone}
			}
			n, err := s.app.Store.DeletePortsWhere(statuses, req.Host)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			s.audit(r, "port.bulk_clear", strings.Join(statuses, ","), "", true)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": n})
			return
		default:
			writeError(w, http.StatusBadRequest, "no port ids supplied")
			return
		}
	}

	switch action {
	case "delete", "":
		n := 0
		for _, id := range req.IDs {
			port, _ := s.app.Store.GetPort(id)
			if port == nil {
				continue
			}
			if port.RuleID > 0 {
				if rule, _ := s.app.Store.GetRule(port.RuleID); rule != nil {
					_ = s.app.Mapper.RemoveMapping(r.Context(), *rule)
					_ = s.app.Store.DeleteRule(rule.ID)
					_ = s.app.Store.DeleteMappingsForRule(rule.ID)
				}
			}
			if err := s.app.Store.DeletePort(id); err == nil {
				n++
			}
		}
		result["deleted"] = n
	case "ignore":
		n := 0
		for _, id := range req.IDs {
			if err := s.app.Store.MarkPortIgnored(id); err == nil {
				n++
			}
		}
		result["ignored"] = n
	case "tag":
		n := 0
		for _, id := range req.IDs {
			port, _ := s.app.Store.GetPort(id)
			if port == nil {
				continue
			}
			tags := model.StringList(req.Tags)
			if err := s.app.Store.SetPortNote(id, tags, port.Note); err == nil {
				n++
			}
		}
		result["tagged"] = n
	default:
		writeError(w, http.StatusBadRequest, "unknown bulk action")
		return
	}
	s.audit(r, "port.bulk", action, fmt.Sprintf("%d ids", len(req.IDs)), true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

// handlePromoteAll adds every open, not-yet-managed port that matches a filter.
func (s *Server) handlePromoteAll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Statuses   []string `json:"statuses"`
		Host       string   `json:"host"`
		TargetKind string   `json:"target_kind"`
		Tags       []string `json:"tags"`
		PortsSpec  string   `json:"ports_spec"`
	}
	if err := decodeJSONLoose(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	statuses := req.Statuses
	if len(statuses) == 0 {
		statuses = []string{model.StatusOpen, model.StatusNotAdded}
	}
	ports, _, err := s.app.Store.ListPorts(store.PortStatusFilter{
		Statuses:   statuses,
		Host:       req.Host,
		TargetKind: req.TargetKind,
		Limit:      5000,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	allowed := map[int]bool{}
	if strings.TrimSpace(req.PortsSpec) != "" {
		list, perr := s.parsePorts(req.PortsSpec)
		if perr != nil {
			writeError(w, http.StatusBadRequest, perr.Error())
			return
		}
		for _, p := range list {
			allowed[p] = true
		}
	}
	added, applied, failed := 0, 0, 0
	for i := range ports {
		p := ports[i]
		if len(allowed) > 0 && !allowed[p.Port] {
			continue
		}
		rule, _, aerr := s.addRuleForPort(&p, portActionRequest{Tags: req.Tags})
		if aerr != nil {
			failed++
			continue
		}
		_ = s.app.Store.MarkPortAdded(p.ID, rule.ID)
		added++
		if _, err := s.app.Mapper.ApplyRule(r.Context(), *rule); err == nil {
			applied++
		}
	}
	s.audit(r, "port.promote", fmt.Sprintf("%d ports", len(ports)), fmt.Sprintf("added=%d", added), true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": added, "applied": applied, "failed": failed})
}

var _ = store.PortStatusFilter{}
