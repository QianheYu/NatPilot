package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/netutil"
	"github.com/ekko-studio/natpilot/internal/scanner"
)

// parsePorts expands a port spec.
func (s *Server) parsePorts(spec string) ([]int, error) {
	return netutil.ParsePortSpec(spec, s.app.Config.Scanner.MaxPortsPerScan)
}

type scanRequest struct {
	Name        string `json:"name"`
	TargetKind  string `json:"target_kind"`
	TargetRef   string `json:"target_ref"`
	PortsSpec   string `json:"ports_spec"`
	Protocol    string `json:"protocol"`
	PolicyID    int64  `json:"policy_id"`
	ApplyPolicy *bool  `json:"apply_policy"`
	MaxHosts    int    `json:"max_hosts"`
	Wait        bool   `json:"wait"`
}

func (s *Server) handleStartScan(w http.ResponseWriter, r *http.Request) {
	var req scanRequest
	if err := decodeJSONLoose(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	sc, err := s.buildScanRequest(req, "manual")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "scan.start", fmt.Sprintf("%s %s", sc.TargetKind, sc.TargetRef), sc.PortsSpec, true)
	if req.Wait {
		run, rerr := s.app.Scanner.Run(r.Context(), sc)
		out := map[string]any{"ok": true, "scan": run}
		if rerr != nil {
			out["error"] = rerr.Error()
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	// The scan outlives this HTTP request: it keeps running in the background
	// until it finishes or is cancelled through /api/scans/{id}/cancel. Passing
	// r.Context() here would abort every asynchronous scan the moment the handler
	// returns, because net/http cancels the request context with ServeHTTP.
	run, err := s.app.Scanner.Start(context.WithoutCancel(r.Context()), sc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "scan": run})
}

func (s *Server) buildScanRequest(req scanRequest, trigger string) (scanner.Request, error) {
	kind := strings.ToLower(strings.TrimSpace(req.TargetKind))
	if kind == "" {
		kind = model.ScanTargetLocal
	}
	switch kind {
	case model.ScanTargetLocal, model.ScanTargetCIDR, model.ScanTargetRemote, model.ScanTargetMAC, model.ScanTargetHosts:
	default:
		return scanner.Request{}, fmt.Errorf("target_kind must be local, cidr, remote, mac or hosts")
	}
	if kind == model.ScanTargetMAC {
		patterns := model.ParseMACPatterns(req.TargetRef)
		if len(patterns) == 0 {
			return scanner.Request{}, fmt.Errorf("target_ref (MAC 地址或前缀) is required for MAC scans")
		}
		if len(strings.TrimSpace(req.TargetRef)) > 512 {
			return scanner.Request{}, fmt.Errorf("target_ref is too long")
		}
		// Resolve now so the user gets an immediate answer instead of a scan that
		// starts and instantly fails because no host owns that address.
		known, herr := s.app.Store.ListHosts(2000)
		if herr != nil {
			return scanner.Request{}, herr
		}
		matched := 0
		for _, h := range known {
			if model.AnyMACMatch(patterns, h.MAC) {
				matched++
			}
		}
		if matched == 0 {
			return scanner.Request{}, fmt.Errorf(
				"没有主机匹配 MAC 范围 %q，请先执行一次“发现局域网主机”以刷新 MAC/IP 对应关系", req.TargetRef)
		}
	} else if kind == model.ScanTargetHosts {
		if _, herr := scanner.ParseHostList(req.TargetRef, 512); herr != nil {
			return scanner.Request{}, herr
		}
	} else if kind != model.ScanTargetLocal && strings.TrimSpace(req.TargetRef) == "" {
		return scanner.Request{}, fmt.Errorf("target_ref (CIDR) is required for network scans")
	}
	portsSpec := strings.TrimSpace(req.PortsSpec)
	userSpec := portsSpec != ""
	if portsSpec == "" {
		portsSpec = s.app.Config.Scanner.DefaultPorts
	}
	if _, err := squeryPorts(portsSpec, s.app.Config.Scanner.MaxPortsPerScan); err != nil {
		return scanner.Request{}, err
	}
	apply := true
	if req.ApplyPolicy != nil {
		apply = *req.ApplyPolicy
	}
	return scanner.Request{
		Name:        req.Name,
		TargetKind:  kind,
		TargetRef:   strings.TrimSpace(req.TargetRef),
		PortsSpec:   portsSpec,
		FilterPorts: userSpec,
		Protocol:    req.Protocol,
		PolicyID:    req.PolicyID,
		Trigger:     trigger,
		ApplyPolicy: apply,
		MaxHosts:    req.MaxHosts,
	}, nil
}

func squeryPorts(spec string, max int) ([]int, error) {
	return netutil.ParsePortSpec(spec, max)
}

// handleScanPreview validates and summarizes a scan without running it.
func (s *Server) handleScanPreview(w http.ResponseWriter, r *http.Request) {
	var req scanRequest
	if err := decodeJSONLoose(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	sc, err := s.buildScanRequest(req, "preview")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ports, perr := squeryPorts(sc.PortsSpec, s.app.Config.Scanner.MaxPortsPerScan)
	if perr != nil {
		writeError(w, http.StatusBadRequest, perr.Error())
		return
	}
	out := map[string]any{
		"ok":                true,
		"ports":             len(ports),
		"port_list":         ports,
		"target":            sc.TargetKind,
		"warnings":          []string{},
		"policies":          []model.Policy{},
		"estimated_seconds": 0,
	}
	warnings := []string{}
	hosts := 1
	if sc.TargetKind == model.ScanTargetHosts {
		ips, herr := scanner.ParseHostList(sc.TargetRef, 512)
		if herr != nil {
			writeError(w, http.StatusBadRequest, herr.Error())
			return
		}
		hosts = len(ips)
		out["hosts"] = hosts
		out["host_list"] = ips
		out["target_preview"] = ips[:min(len(ips), 32)]
	} else if sc.TargetKind == model.ScanTargetMAC {
		// Preview a MAC scan by resolving the patterns to the addresses those
		// devices hold right now — exactly what the scan itself will do.
		patterns := model.ParseMACPatterns(sc.TargetRef)
		known, herr := s.app.Store.ListHosts(2000)
		if herr != nil {
			writeError(w, http.StatusInternalServerError, herr.Error())
			return
		}
		ips := []string{}
		for _, h := range known {
			if model.AnyMACMatch(patterns, h.MAC) && strings.TrimSpace(h.IP) != "" {
				ips = append(ips, h.IP)
			}
		}
		if len(ips) == 0 {
			warnings = append(warnings, "没有主机匹配该 MAC 范围，请先执行“发现局域网主机”")
		}
		hosts = max(len(ips), 1)
		out["hosts"] = len(ips)
		out["host_list"] = ips
		out["target_preview"] = ips[:min(len(ips), 32)]
	} else if sc.TargetKind != model.ScanTargetLocal {
		expanded, eerr := netutil.ExpandCIDR(sc.TargetRef, s.app.Config.Scanner.MaxHostsPerScan)
		if eerr != nil {
			writeError(w, http.StatusBadRequest, eerr.Error())
			return
		}
		hosts = len(expanded)
		out["hosts"] = hosts
		out["host_list"] = expanded
		out["target_preview"] = expanded[:min(len(expanded), 32)]
	} else {
		out["hosts"] = 1
	}
	if hosts*len(ports) > 200000 {
		warnings = append(warnings, fmt.Sprintf("%d probes is a lot; consider narrowing the port list", hosts*len(ports)))
	}
	conc := s.app.Config.Scanner.MaxConcurrency
	if conc <= 0 {
		conc = 256
	}
	timeout := time.Duration(s.app.Config.Scanner.TimeoutMS) * time.Millisecond
	perProbe := (timeout * time.Duration(hosts*len(ports)) / time.Duration(conc))
	out["estimated_seconds"] = int(perProbe.Seconds()) + 1
	pols, _ := s.app.Store.EnabledPolicies()
	out["policies"] = pols
	out["warnings"] = warnings
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListScans(w http.ResponseWriter, r *http.Request) {
	limit := atoiDefault(r.URL.Query().Get("limit"), 50)
	offset := atoiDefault(r.URL.Query().Get("offset"), 0)
	scans, total, err := s.app.Store.ListScans(limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scans": scans, "total": total, "running": s.app.Scanner.Running()})
}

func (s *Server) handleGetScan(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid scan id")
		return
	}
	run, err := s.app.Store.GetScan(id)
	if err != nil || run == nil {
		writeError(w, http.StatusNotFound, "scan not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scan": run})
}

func (s *Server) handleDeleteScan(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid scan id")
		return
	}
	run, _ := s.app.Store.GetScan(id)
	if run != nil && run.Status == model.ScanStatusRunning {
		s.app.Scanner.Cancel(id)
	}
	if err := s.app.Store.DeleteScan(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, map[string]any{"deleted": id})
}

func (s *Server) handleCancelScan(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid scan id")
		return
	}
	ok := s.app.Scanner.Cancel(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "canceled": ok})
}

func (s *Server) handleScanResults(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid scan id")
		return
	}
	f := storePortFilter(r)
	f.ScanID = id
	ports, total, err := s.app.Store.ListPorts(f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ports": ports, "total": total, "scan_id": id})
}

// ---- policies ----

func (s *Server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := pageArgs(q)
	pols, total, err := s.app.Store.ListPoliciesPage(strings.TrimSpace(q.Get("q")), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": pols, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) handleCreatePolicy(w http.ResponseWriter, r *http.Request) {
	var p model.Policy
	if err := decodeJSONLoose(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validatePolicy(&p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.app.Store.CreatePolicy(&p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "policy.create", p.Name, fmt.Sprintf("id=%d", id), true)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "policy": p})
}

func validatePolicy(p *model.Policy) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return fmt.Errorf("policy name is required")
	}
	if strings.TrimSpace(p.PortRanges) != "" {
		if _, err := netutil.ParsePortSpec(p.PortRanges, 65535); err != nil {
			return fmt.Errorf("invalid port_ranges: %w", err)
		}
	}
	if strings.TrimSpace(p.ExcludeRanges) != "" {
		if _, err := netutil.ParsePortSpec(p.ExcludeRanges, 65535); err != nil {
			return fmt.Errorf("invalid exclude_ranges: %w", err)
		}
	}
	switch p.AutoAction {
	case "", model.ActionAdd, model.ActionWatch, model.ActionIgnore, model.ActionDelete:
	default:
		return fmt.Errorf("auto_action must be add, watch, ignore or delete")
	}
	switch p.MatchTarget {
	case "", "any", model.ScanTargetLocal, model.ScanTargetCIDR, model.ScanTargetRemote, model.ScanTargetMAC:
	default:
		return fmt.Errorf("match_target_kind must be any, local, cidr, remote or mac")
	}
	// A MAC 范围 policy is only meaningful if it can match something: require a
	// usable pattern and reject junk early instead of silently matching nothing.
	if patterns := model.ParseMACPatterns(p.MACPatterns); len(patterns) > 0 {
		for _, pat := range patterns {
			if !model.MACPatternValid(pat) {
				return fmt.Errorf("invalid MAC pattern %q: 需要完整地址 (aa:bb:cc:dd:ee:ff) 或至少 3 字节前缀", pat)
			}
		}
	} else if p.MatchTarget == model.ScanTargetMAC && strings.TrimSpace(p.MAC) == "" {
		// A host-bound policy carries the device in MAC and may leave the pattern
		// list empty; only a mac-kind policy with neither is unmatchable.
		return fmt.Errorf("match_target_kind=mac 需要填写 mac_patterns 或绑定主机 MAC")
	}
	return nil
}

func (s *Server) handleUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid policy id")
		return
	}
	existing, err := s.app.Store.GetPolicy(id)
	if err != nil || existing == nil {
		writeError(w, http.StatusNotFound, "policy not found")
		return
	}
	var p model.Policy
	if err := decodeJSONLoose(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	p.ID = id
	if strings.TrimSpace(p.Name) == "" {
		p.Name = existing.Name
	}
	// 策略页的表单没有"绑定主机 MAC"这一项（那是主机入口隐含的），所以编辑
	// 主机绑定的策略时 mac 会被解成空值，等于悄悄解绑。只要这次编辑仍然按
	// MAC 匹配，就沿用原来的绑定；改成其它匹配范围时才真正解绑。
	if strings.TrimSpace(p.MAC) == "" &&
		strings.TrimSpace(existing.MAC) != "" &&
		strings.EqualFold(strings.TrimSpace(p.MatchTarget), model.ScanTargetMAC) {
		p.MAC = existing.MAC
	}
	if err := validatePolicy(&p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.app.Store.UpdatePolicy(&p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "policy.update", p.Name, fmt.Sprintf("id=%d", id), true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "policy": p})
}

func (s *Server) handleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid policy id")
		return
	}
	if err := s.app.Store.DeletePolicy(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "policy.delete", fmt.Sprintf("policy %d", id), "", true)
	writeOK(w, map[string]any{"deleted": id})
}

// ---- auto monitor ----

func (s *Server) handleMonitorStatus(w http.ResponseWriter, r *http.Request) {
	status := s.app.Scanner.MonitorStatus()
	pols, _ := s.app.Store.EnabledPolicies()
	writeJSON(w, http.StatusOK, map[string]any{
		"monitor":       status,
		"policies":      pols,
		"auto_add_open": s.app.Config.Scanner.AutoAddOpen,
		"interval":      s.app.Config.Scanner.AutoIntervalSec,
		"targets":       append([]string{"local"}, s.app.Config.Scanner.AutoScanCIDR...),
	})
}

func (s *Server) handleMonitorStart(w http.ResponseWriter, r *http.Request) {
	// The monitor is a long-lived background loop; it must not be tied to the
	// lifetime of the request that started it. It stops via StopMonitor or when
	// the process exits.
	if err := s.app.Scanner.StartMonitor(context.WithoutCancel(r.Context())); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "monitor.start", "auto monitor", "", true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "monitor": s.app.Scanner.MonitorStatus()})
}

func (s *Server) handleMonitorStop(w http.ResponseWriter, r *http.Request) {
	s.app.Scanner.StopMonitor()
	s.audit(r, "monitor.stop", "auto monitor", "", true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "monitor": s.app.Scanner.MonitorStatus()})
}

// ---- schedules ----

func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := pageArgs(q)
	list, total, err := s.app.Store.ListSchedulesPage(strings.TrimSpace(q.Get("q")), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type row struct {
		model.Schedule
		NextRunAt *time.Time `json:"computed_next_run,omitempty"`
	}
	out := make([]row, 0, len(list))
	for _, sc := range list {
		rw := row{Schedule: sc}
		if n, ok := s.app.Scheduler.NextRun(sc.ID); ok {
			t := n
			rw.NextRunAt = &t
		}
		out = append(out, rw)
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": out, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	var sc model.Schedule
	if err := decodeJSONLoose(r, &sc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.validateSchedule(&sc); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.app.Store.CreateSchedule(&sc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "schedule.create", sc.Name, fmt.Sprintf("id=%d cron=%s", id, sc.Cron), true)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "schedule": sc})
}

func (s *Server) validateSchedule(sc *model.Schedule) error {
	sc.Name = strings.TrimSpace(sc.Name)
	if sc.Name == "" {
		sc.Name = sc.Kind + " job"
	}
	switch sc.Kind {
	case "", "scan", "reconcile", "cleanup", "discover":
	default:
		return fmt.Errorf("kind must be scan, reconcile, cleanup or discover")
	}
	if strings.TrimSpace(sc.Cron) == "" {
		return fmt.Errorf("cron expression is required")
	}
	if err := s.app.Scheduler.ValidateCron(sc.Cron); err != nil {
		return err
	}
	return nil
}

func (s *Server) handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid schedule id")
		return
	}
	existing, err := s.app.Store.GetSchedule(id)
	if err != nil || existing == nil {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	var sc model.Schedule
	if err := decodeJSONLoose(r, &sc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	sc.ID = id
	if strings.TrimSpace(sc.Name) == "" {
		sc.Name = existing.Name
	}
	if sc.Cron == "" {
		sc.Cron = existing.Cron
	}
	if sc.Kind == "" {
		sc.Kind = existing.Kind
	}
	if err := s.validateSchedule(&sc); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.app.Store.UpdateSchedule(&sc); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "schedule.update", sc.Name, fmt.Sprintf("id=%d", id), true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "schedule": sc})
}

func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid schedule id")
		return
	}
	if err := s.app.Store.DeleteSchedule(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, map[string]any{"deleted": id})
}

func (s *Server) handleInterfaces(w http.ResponseWriter, r *http.Request) {
	list, err := netutil.Interfaces()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	gw, _ := netutil.DefaultGateway()
	neighbors, _ := netutil.DiscoverNeighbors(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"interfaces": list,
		"gateway":    gw,
		"neighbors":  neighbors,
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
