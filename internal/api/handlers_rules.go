package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ekko-studio/natpilot/internal/logging"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/store"
)

// rulesRequest is the create/update payload for a managed port mapping rule.
type rulesRequest struct {
	Name     string   `json:"name"`
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	ExtPort  int      `json:"ext_port"`
	Protocol string   `json:"protocol"`
	Tags     []string `json:"tags"`
	Note     string   `json:"note"`
	Enabled  *bool    `json:"enabled"`
	Source   string   `json:"source"`
	// Gateway pins the rule to one gateway (kind|address|port); empty = automatic.
	Gateway string   `json:"gateway"`
	TagsAdd []string `json:"tags_add"`
	TagsDel []string `json:"tags_remove"`
}

// ruleView is a rule enriched with live mapping state.
type ruleView struct {
	model.Rule
	MappingStatus string `json:"mapping_status"`
	ObservedOpen  bool   `json:"observed_open"`
	SourceGone    bool   `json:"source_gone"`
	ConflictWith  int64  `json:"conflict_with,omitempty"`
}

func (s *Server) enrichRules(rules []model.Rule, verbose bool) []ruleView {
	records, _ := s.app.Store.MappingsByRule()
	conflicts := s.app.Mapper.Conflicts()
	out := make([]ruleView, 0, len(rules))
	for i := range rules {
		r := rules[i]
		vw := ruleView{Rule: r, MappingStatus: "unmapped"}
		if !r.Enabled {
			vw.MappingStatus = "disabled"
		}
		if rec, ok := records[r.ID]; ok {
			vw.MappingStatus = rec.Status
			vw.ExternalIP = rec.ExternalIP
			if rec.ExternalPort > 0 {
				vw.ExternalPort = rec.ExternalPort
			}
			vw.LastError = rec.Error
			vw.ProviderName = rec.ProviderName
			vw.LeaseSeconds = rec.LeaseSeconds
			if rec.ExpiresAt.Valid && !rec.ExpiresAt.Time.Time.IsZero() {
				vw.LeaseExpires = int(rec.ExpiresAt.Time.Time.Unix())
			}
		}
		if other, bad := conflicts[r.ID]; bad {
			vw.ConflictWith = other
			if vw.MappingStatus != "active" {
				vw.MappingStatus = "conflict"
			}
		}
		if verbose {
			// Observe whether the rule's internal port is actually listening.
			ports, _, err := s.app.Store.ListPorts(portStatusFilterFor(r))
			if err == nil && len(ports) > 0 {
				p := ports[0]
				vw.ObservedOpen = p.Status == model.StatusOpen || p.Status == model.StatusAdded || p.Status == model.StatusNotAdded
				if !vw.ObservedOpen {
					vw.SourceGone = true
					if vw.MappingStatus == "active" {
						vw.MappingStatus = "source_gone"
					}
				}
				vw.Rule.Status = p.Status
			}
		}
		out = append(out, vw)
	}
	return out
}

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := storeRuleFilter(q)
	rules, total, err := s.app.Store.ListRules(f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	verbose := q.Get("verbose") == "1" || q.Get("verbose") == "true" || q.Get("verbose") == ""
	views := s.enrichRules(rules, verbose)
	tags, _ := s.app.Store.DistinctTags()
	writeJSON(w, http.StatusOK, map[string]any{
		"rules":    views,
		"total":    total,
		"limit":    f.Limit,
		"offset":   f.Offset,
		"all_tags": tags,
	})
}

func (s *Server) handleGetRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	rule, err := s.app.Store.GetRule(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rule == nil {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	views := s.enrichRules([]model.Rule{*rule}, true)
	mappings, _ := s.app.Store.ListMappings([]int64{id})
	writeJSON(w, http.StatusOK, map[string]any{
		"rule":     views[0],
		"mappings": mappings,
	})
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	var req rulesRequest
	if err := decodeJSONLoose(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := validateRuleRequest(&req, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if existing, err := s.app.Store.GetRuleByKey(req.Host, req.Port, req.Protocol); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	} else if existing != nil {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("a rule for %s/%d (%s) already exists (id %d)", req.Host, req.Port, req.Protocol, existing.ID))
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	rule := &model.Rule{
		Name:     req.Name,
		Host:     req.Host,
		Port:     req.Port,
		ExtPort:  req.ExtPort,
		Protocol: req.Protocol,
		Tags:     model.StringList(req.Tags),
		Note:     req.Note,
		Enabled:  enabled,
		Provider: normalizeGatewayPin(req.Gateway),
		Source:   orDef(req.Source, "manual"),
	}
	id, err := s.app.Store.CreateRule(rule)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "rule.create", fmt.Sprintf("%s/%d/%s", rule.Host, rule.Port, rule.Protocol), "created", true)

	// Link a matching discovered port if one exists.
	if p, _ := s.app.Store.GetRuleByKey(rule.Host, rule.Port, rule.Protocol); p != nil {
		_ = p
	}
	s.linkPortToRule(rule)

	// Provision immediately when enabled, so the UI reflects the real state.
	summary := map[string]any{}
	if enabled {
		sum, aerr := s.app.Mapper.ApplyRule(r.Context(), *rule)
		summary["apply"] = sum
		if aerr != nil {
			summary["apply_error"] = aerr.Error()
		}
	}
	fresh, _ := s.app.Store.GetRule(id)
	views := s.enrichRules([]model.Rule{*fresh}, true)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "rule": views[0], "result": summary})
}

func validateRuleRequest(req *rulesRequest, requirePort bool) error {
	req.Host = strings.TrimSpace(req.Host)
	if req.Host == "" {
		req.Host = "127.0.0.1"
	}
	if requirePort && (req.Port < 1 || req.Port > 65535) {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if !requirePort && req.Port != 0 && (req.Port < 1 || req.Port > 65535) {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if req.ExtPort < 0 || req.ExtPort > 65535 {
		return fmt.Errorf("external port must be between 0 and 65535")
	}
	switch strings.ToLower(req.Protocol) {
	case "", "tcp":
		req.Protocol = model.ProtoTCP
	case "udp":
		req.Protocol = model.ProtoUDP
	case "any":
		req.Protocol = model.ProtoAny
	default:
		return fmt.Errorf("protocol must be tcp, udp or any")
	}
	return nil
}

func orDef(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// linkPortToRule marks a discovered port as managed by a rule.
func (s *Server) linkPortToRule(rule *model.Rule) {
	ports, _, err := s.app.Store.ListPorts(store.PortStatusFilter{Host: rule.Host, Query: strconv.Itoa(rule.Port), Limit: 5})
	if err != nil {
		return
	}
	for _, p := range ports {
		if p.Port == rule.Port && strings.EqualFold(p.Protocol, rule.Protocol) {
			_ = s.app.Store.MarkPortAdded(p.ID, rule.ID)
			return
		}
	}
	// Also record a synthetic port entry so the rule has an observation trail.
	if _, _, uerr := s.app.Store.UpsertPort(&model.DiscoveredPort{
		Host: rule.Host, Port: rule.Port, Protocol: rule.Protocol,
		Status: model.StatusAdded, RuleID: rule.ID, Added: true,
		TargetKind: model.ScanTargetLocal, TargetRef: "rule",
		Service: serviceFor(rule.Port),
	}); uerr != nil {
		return
	}
}

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	rule, err := s.app.Store.GetRule(id)
	if err != nil || rule == nil {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	var req rulesRequest
	if err := decodeJSONLoose(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	before := rule.Clone()
	if err := applyRuleRequest(rule, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Enforce uniqueness on the key tuple.
	if other, _ := s.app.Store.GetRuleByKey(rule.Host, rule.Port, rule.Protocol); other != nil && other.ID != rule.ID {
		writeError(w, http.StatusConflict, fmt.Sprintf("another rule already manages %s/%d (%s)", rule.Host, rule.Port, rule.Protocol))
		return
	}
	if err := s.app.Store.UpdateRule(rule); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "rule.update", fmt.Sprintf("%s/%d", rule.Host, rule.Port), "", true)

	needsReprovision := before.Port != rule.Port || before.Host != rule.Host ||
		before.Protocol != rule.Protocol || before.ExtPort != rule.ExtPort
	summary := map[string]any{}
	if !rule.Enabled {
		if err := s.app.Mapper.RemoveMapping(r.Context(), *rule); err != nil {
			summary["remove_error"] = err.Error()
		} else {
			summary["removed"] = true
		}
	} else if needsReprovision || before.Enabled != rule.Enabled {
		// Tear the old mapping down first when the target changed.
		if needsReprovision {
			_ = s.app.Mapper.RemoveMapping(r.Context(), before)
		}
		sum, aerr := s.app.Mapper.ApplyRule(r.Context(), *rule)
		summary["apply"] = sum
		if aerr != nil {
			summary["apply_error"] = aerr.Error()
		}
	}
	// The target may have changed: drop links that point at a host/port this rule
	// no longer manages, then link the port it manages now.
	s.unlinkStalePortsForRule(rule)
	s.linkPortToRule(rule)
	fresh, _ := s.app.Store.GetRule(id)
	views := s.enrichRules([]model.Rule{*fresh}, true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rule": views[0], "result": summary})
}

func applyRuleRequest(rule *model.Rule, req *rulesRequest) error {
	if err := validateRuleRequest(req, true); err != nil {
		return err
	}
	if strings.TrimSpace(req.Name) != "" {
		rule.Name = req.Name
	}
	rule.Host = req.Host
	rule.Port = req.Port
	rule.ExtPort = req.ExtPort
	rule.Protocol = req.Protocol
	// Always take the pin from the request: empty string means "back to automatic",
	// so clearing it in the editor must actually unpin the rule.
	rule.Provider = normalizeGatewayPin(req.Gateway)
	if req.Tags != nil {
		rule.Tags = model.StringList(req.Tags)
	}
	if len(req.TagsAdd) > 0 || len(req.TagsDel) > 0 {
		set := map[string]bool{}
		for _, t := range rule.Tags {
			set[t] = true
		}
		for _, t := range req.TagsAdd {
			t = strings.TrimSpace(t)
			if t != "" {
				set[t] = true
			}
		}
		for _, t := range req.TagsDel {
			delete(set, strings.TrimSpace(t))
		}
		tags := model.StringList{}
		for t := range set {
			tags = append(tags, t)
		}
		rule.Tags = tags
	}
	if strings.TrimSpace(req.Note) != "" {
		rule.Note = req.Note
	}
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
	if strings.TrimSpace(req.Source) != "" {
		rule.Source = req.Source
	}
	if rule.Tags == nil {
		rule.Tags = model.StringList{}
	}
	return nil
}

func (s *Server) handlePatchRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	rule, err := s.app.Store.GetRule(id)
	if err != nil || rule == nil {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	var req struct {
		Enabled *bool     `json:"enabled"`
		Name    *string   `json:"name"`
		Note    *string   `json:"note"`
		Tags    *[]string `json:"tags"`
		ExtPort *int      `json:"ext_port"`
	}
	if err := decodeJSONLoose(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name != nil {
		rule.Name = *req.Name
	}
	if req.Note != nil {
		rule.Note = *req.Note
	}
	if req.Tags != nil {
		rule.Tags = model.StringList(*req.Tags)
	}
	toggled := false
	if req.Enabled != nil && *req.Enabled != rule.Enabled {
		rule.Enabled = *req.Enabled
		toggled = true
	}
	if req.ExtPort != nil {
		rule.ExtPort = *req.ExtPort
	}
	if err := s.app.Store.UpdateRule(rule); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	summary := map[string]any{}
	if toggled {
		sum, aerr := s.app.Mapper.ApplyRule(r.Context(), *rule)
		summary["apply"] = sum
		if aerr != nil {
			summary["apply_error"] = aerr.Error()
		}
	}
	s.audit(r, "rule.patch", fmt.Sprintf("%s/%d", rule.Host, rule.Port), "", true)
	views := s.enrichRules([]model.Rule{*rule}, true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rule": views[0], "result": summary})
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	rule, err := s.app.Store.GetRule(id)
	if err != nil || rule == nil {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	var removeErr error
	if rule.Enabled {
		removeErr = s.app.Mapper.RemoveMapping(r.Context(), *rule)
	}
	if err := s.app.Store.DeleteRule(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.app.Store.DeleteMappingsForRule(id)
	s.unlinkPortsFromRule(id)
	s.audit(r, "rule.delete", fmt.Sprintf("%s/%d", rule.Host, rule.Port), "", true)
	out := map[string]any{"ok": true, "deleted": id}
	if removeErr != nil {
		out["gateway_error"] = removeErr.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

// unlinkPortsFromRule releases every port that was managed by this rule, so the
// port list goes back to "not added" instead of claiming a rule that no longer
// exists (or is disabled).
func (s *Server) unlinkPortsFromRule(ruleID int64) {
	ports, _, err := s.app.Store.ListPorts(store.PortStatusFilter{Limit: 5000})
	if err != nil {
		return
	}
	for _, p := range ports {
		if p.RuleID == ruleID {
			_ = s.app.Store.MarkPortRemoved(p.ID)
		}
	}
}

// unlinkStalePortsForRule clears links whose observed host/port no longer match
// the rule after an edit. Without it a rule repointed from 8080 to 9090 would
// leave 8080 permanently marked as "added".
func (s *Server) unlinkStalePortsForRule(rule *model.Rule) {
	ports, _, err := s.app.Store.ListPorts(store.PortStatusFilter{Limit: 5000})
	if err != nil {
		return
	}
	for _, p := range ports {
		if p.RuleID != rule.ID {
			continue
		}
		if p.Host == rule.Host && p.Port == rule.Port && strings.EqualFold(p.Protocol, rule.Protocol) {
			continue
		}
		_ = s.app.Store.MarkPortRemoved(p.ID)
	}
}

func (s *Server) handleToggleRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	rule, err := s.app.Store.GetRule(id)
	if err != nil || rule == nil {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	_ = decodeJSONLoose(r, &req)
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	} else {
		rule.Enabled = !rule.Enabled
	}
	if err := s.app.Store.UpdateRule(rule); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sum, aerr := s.app.Mapper.ApplyRule(r.Context(), *rule)
	// Keep the port list in step with the switch: a disabled rule no longer owns
	// its port, so it must not stay marked as "added".
	if rule.Enabled {
		s.linkPortToRule(rule)
	} else {
		s.unlinkPortsFromRule(rule.ID)
	}
	s.audit(r, "rule.toggle", fmt.Sprintf("%s/%d", rule.Host, rule.Port), strconv.FormatBool(rule.Enabled), aerr == nil)
	out := map[string]any{"ok": true, "enabled": rule.Enabled, "result": sum}
	if aerr != nil {
		out["error"] = aerr.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleApplyRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	rule, err := s.app.Store.GetRule(id)
	if err != nil || rule == nil {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	sum, aerr := s.app.Mapper.ApplyRule(r.Context(), *rule)
	s.audit(r, "rule.apply", fmt.Sprintf("%s/%d", rule.Host, rule.Port), sum.String(), aerr == nil)
	out := map[string]any{"ok": true, "result": sum}
	if aerr != nil {
		out["error"] = aerr.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRuleMapping(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	mappings, err := s.app.Store.ListMappings([]int64{id})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	record, ok := s.app.Mapper.Records()[id]
	writeJSON(w, http.StatusOK, map[string]any{"rule_id": id, "mappings": mappings, "current": record, "has_current": ok})
}

// bulkRulesRequest applies one operation to many rules.
type bulkRulesRequest struct {
	IDs     []int64  `json:"ids"`
	Action  string   `json:"action"`
	Enabled *bool    `json:"enabled"`
	Tags    []string `json:"tags"`
	TagsDel []string `json:"tags_remove"`
	Note    string   `json:"note"`
}

func (s *Server) handleBulkRules(w http.ResponseWriter, r *http.Request) {
	var req bulkRulesRequest
	if err := decodeJSONLoose(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "no rule ids supplied")
		return
	}
	result := map[string]any{}
	switch req.Action {
	case "enable", "disable":
		enabled := req.Action == "enable"
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		n, err := s.app.Store.SetRulesEnabled(req.IDs, enabled)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		result["updated"] = n
		// Provision or tear down each affected rule.
		applied, failed := 0, 0
		for _, id := range req.IDs {
			rule, _ := s.app.Store.GetRule(id)
			if rule == nil {
				continue
			}
			if _, aerr := s.app.Mapper.ApplyRule(r.Context(), *rule); aerr != nil {
				failed++
			} else {
				applied++
			}
			// Bulk enable/disable has to keep the port list honest too.
			if rule.Enabled {
				s.linkPortToRule(rule)
			} else {
				s.unlinkPortsFromRule(rule.ID)
			}
		}
		result["applied"] = applied
		result["failed"] = failed
	case "delete":
		// Release the gateway mappings before deleting the rows: deleting a rule
		// cascades its mappings table entries away, and afterwards nothing records
		// which external port still has to be freed on the router.
		unmapped, unmapFailed := 0, 0
		if s.app.Config.Mapper.CleanupMissing {
			for _, id := range req.IDs {
				rule, gerr := s.app.Store.GetRule(id)
				if gerr != nil || rule == nil {
					continue
				}
				if rerr := s.app.Mapper.RemoveMapping(r.Context(), *rule); rerr != nil {
					logging.Warn("api", "could not remove gateway mapping", "rule", id, "error", rerr.Error())
					unmapFailed++
					continue
				}
				unmapped++
			}
		}
		n, err := s.app.Store.BulkDeleteRules(req.IDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, id := range req.IDs {
			s.unlinkPortsFromRule(id)
			_ = s.app.Store.DeleteMappingsForRule(id)
		}
		result["deleted"] = n
		result["unmapped"] = unmapped
		if unmapFailed > 0 {
			result["unmap_failed"] = unmapFailed
		}
	case "tag":
		n, err := s.app.Store.TagRules(req.IDs, req.Tags, req.TagsDel)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		result["updated"] = n
	case "note":
		updated := 0
		for _, id := range req.IDs {
			rule, err := s.app.Store.GetRule(id)
			if err != nil || rule == nil {
				continue
			}
			rule.Note = req.Note
			if err := s.app.Store.UpdateRule(rule); err == nil {
				updated++
			}
		}
		result["updated"] = updated
	default:
		writeError(w, http.StatusBadRequest, "unknown bulk action (enable|disable|delete|tag|note)")
		return
	}
	s.audit(r, "rule.bulk", req.Action, fmt.Sprintf("%d ids", len(req.IDs)), true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func serviceFor(port int) string {
	if svc := guessService(port); svc != "" {
		return svc
	}
	return ""
}

var _ = time.Now
var _ = strconv.Itoa

// normalizeGatewayPin trims a gateway pin. Accepted forms:
// "kind|address|port" (the key used internally) or a bare kind is rejected, since
// a kind alone cannot identify one gateway when several share it.
func normalizeGatewayPin(v string) string {
	pin := strings.TrimSpace(v)
	if pin == "" {
		return ""
	}
	return pin
}
