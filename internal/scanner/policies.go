package scanner

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ekko-studio/natpilot/internal/logging"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/netutil"
)

// ApplyPolicies evaluates the enabled policies against freshly discovered ports
// and applies their automatic decision (add / watch / ignore). It returns the
// number of decisions applied.
func (s *Scanner) ApplyPolicies(ctx context.Context, run *model.ScanRun, ports []model.DiscoveredPort) (int, error) {
	policies, err := s.store.EnabledPolicies()
	if err != nil {
		return 0, err
	}
	if len(policies) == 0 {
		return 0, nil
	}
	applied := 0
	for i := range ports {
		p := &ports[i]
		if p.Ignored {
			continue
		}
		pol := matchPolicy(policies, p, s.hostMAC(p.Host))
		if pol == nil {
			continue
		}
		action := pol.AutoAction
		if action == "" {
			action = model.ActionWatch
		}
		if pol.AutoAdd {
			action = model.ActionAdd
		}
		switch action {
		case model.ActionAdd:
			if p.Added {
				continue
			}
			ruleID, rerr := s.addRuleFromPort(p, pol)
			if rerr != nil {
				logging.Warn("scanner", "auto add failed", "host", p.Host, "port", p.Port, "error", rerr.Error())
				continue
			}
			p.RuleID = ruleID
			p.Added = true
			p.Status = model.StatusAdded
			_ = s.store.MarkPortAdded(p.ID, ruleID)
			applied++
			if s.ev != nil {
				s.ev.Publish("policy", fmt.Sprintf("policy %q added rule #%d for %s:%d", pol.Name, ruleID, p.Host, p.Port),
					model.JSONMap{"policy_id": pol.ID, "rule_id": ruleID, "host": p.Host, "port": p.Port})
			}
		case model.ActionIgnore:
			p.Ignored = true
			p.Status = model.StatusClosed
			_ = s.store.MarkPortIgnored(p.ID)
			applied++
		case model.ActionDelete:
			_ = s.store.DeletePort(p.ID)
			applied++
		default: // watch
			if p.Status == "" {
				p.Status = model.StatusNotAdded
			}
			_ = s.store.SetPortStatus(p.ID, p.Status, model.ActionWatch)
			applied++
		}
	}
	if applied > 0 && s.ev != nil {
		s.ev.Changed()
	}
	return applied, nil
}

// hostPolicyTargets returns the current addresses of every host that has its own
// policy. Resolving the MAC here (rather than storing the IP on the policy) is
// what makes host monitoring survive DHCP.
func (s *Scanner) hostPolicyTargets() []string {
	pols, err := s.store.EnabledPolicies()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, p := range pols {
		if p.MAC == "" {
			continue
		}
		h, herr := s.store.GetHostByMAC(p.MAC)
		if herr != nil || h == nil || strings.TrimSpace(h.IP) == "" {
			continue
		}
		if seen[h.IP] {
			continue
		}
		seen[h.IP] = true
		out = append(out, h.IP)
	}
	return out
}

// hostMAC resolves the hardware address of a discovered host. Host-bound
// policies are matched on this instead of the IP, so a DHCP lease change does
// not silently stop a policy from applying.
func (s *Scanner) hostMAC(ip string) string {
	if strings.TrimSpace(ip) == "" {
		return ""
	}
	h, err := s.store.GetHostByIP(ip)
	if err != nil || h == nil {
		return ""
	}
	return h.MAC
}

// matchPolicy returns the highest priority policy matching a port.
func matchPolicy(policies []model.Policy, p *model.DiscoveredPort, hostMAC string) *model.Policy {
	for i := range policies {
		pol := &policies[i]
		if !policyTargetMatches(pol, p, hostMAC) {
			continue
		}
		if pol.Protocol != "" && pol.Protocol != "any" && !strings.EqualFold(pol.Protocol, p.Protocol) {
			continue
		}
		if pol.MinPort > 0 && p.Port < pol.MinPort {
			continue
		}
		if pol.MaxPort > 0 && p.Port > pol.MaxPort {
			continue
		}
		if strings.TrimSpace(pol.PortRanges) != "" && !portInRanges(pol.PortRanges, p.Port) {
			continue
		}
		if strings.TrimSpace(pol.ExcludeRanges) != "" && portInRanges(pol.ExcludeRanges, p.Port) {
			continue
		}
		return pol
	}
	return nil
}

func policyTargetMatches(pol *model.Policy, p *model.DiscoveredPort, hostMAC string) bool {
	// A host-bound policy only ever applies to that one machine: matching by MAC
	// keeps it pointed at the device even after DHCP hands out a new address.
	if pol.MAC != "" {
		return model.SameMAC(pol.MAC, hostMAC)
	}
	// A MAC 范围 matches a group of devices by hardware address (full address or
	// OUI prefix), the DHCP-safe alternative to matching by subnet.
	if patterns := model.ParseMACPatterns(pol.MACPatterns); len(patterns) > 0 {
		return model.AnyMACMatch(patterns, hostMAC)
	}
	kind := pol.MatchTarget
	if kind == "" || kind == "any" {
		return true
	}
	// "mac" as a match kind without patterns cannot match anything: silently
	// falling through to "match everything" would map ports on every host.
	if kind == model.ScanTargetMAC {
		return false
	}
	if kind == model.ScanTargetLocal {
		return p.TargetKind == model.ScanTargetLocal
	}
	if kind == model.ScanTargetCIDR || kind == model.ScanTargetRemote {
		return p.TargetKind == model.ScanTargetCIDR || p.TargetKind == model.ScanTargetRemote
	}
	return true
}

func portInRanges(spec string, port int) bool {
	ports, err := netutil.ParsePortSpec(spec, 65535)
	if err != nil {
		return false
	}
	for _, p := range ports {
		if p == port {
			return true
		}
	}
	return false
}

// addRuleFromPort creates a managed rule for a discovered port.
func (s *Scanner) addRuleFromPort(p *model.DiscoveredPort, pol *model.Policy) (int64, error) {
	existing, err := s.store.GetRuleByKey(p.Host, p.Port, p.Protocol)
	if err != nil {
		return 0, err
	}
	if existing != nil {
		return existing.ID, nil
	}
	name := fmt.Sprintf("%s:%d", p.Host, p.Port)
	if p.Service != "" {
		name = fmt.Sprintf("%s %s:%d", strings.ToUpper(p.Protocol), p.Host, p.Port)
	}
	host := p.Host
	if p.TargetKind == model.ScanTargetLocal {
		host = "127.0.0.1"
	}
	rule := &model.Rule{
		Name:        name,
		Host:        host,
		Port:        p.Port,
		Protocol:    p.Protocol,
		Tags:        pol.Tags,
		Note:        fmt.Sprintf("auto-added by policy %q", pol.Name),
		Enabled:     true,
		AutoCreated: true,
		Source:      "scan",
	}
	return s.store.CreateRule(rule)
}

// ---- auto monitor ----

// MonitorStatus reports the auto-monitor state.
type MonitorStatus struct {
	Active       bool      `json:"active"`
	LastRun      time.Time `json:"last_run,omitempty"`
	IntervalSec  int       `json:"interval_seconds"`
	PolicyCount  int       `json:"policy_count"`
	Targets      []string  `json:"targets"`
	LastError    string    `json:"last_error,omitempty"`
	ScansStarted int       `json:"scans_started"`
}

// MonitorStatus returns the current auto-monitor state.
func (s *Scanner) MonitorStatus() MonitorStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	pols, _ := s.store.EnabledPolicies()
	targets := []string{"local"}
	targets = append(targets, s.cfg.Scanner.AutoScanCIDR...)
	return MonitorStatus{
		Active:      s.monitor.active,
		LastRun:     s.monitor.last,
		IntervalSec: s.cfg.Scanner.AutoIntervalSec,
		PolicyCount: len(pols),
		Targets:     targets,
	}
}

// StartMonitor starts the automatic monitoring loop. It refuses to start unless
// at least one default policy is configured, which is the documented condition
// for automatic monitoring.
func (s *Scanner) StartMonitor(ctx context.Context) error {
	s.mu.Lock()
	if s.monitor.active {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	pols, err := s.store.EnabledPolicies()
	if err != nil {
		return err
	}
	if len(pols) == 0 {
		return errors.New("auto monitor requires at least one enabled default policy")
	}
	s.cfg.Scanner.AutoMonitorLocal = true

	mctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.monitor.cancel = cancel
	s.monitor.active = true
	s.mu.Unlock()

	go func() {
		interval := time.Duration(s.cfg.Scanner.AutoIntervalSec) * time.Second
		if interval < 30*time.Second {
			interval = 30 * time.Second
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		s.runMonitorPass(mctx)
		for {
			select {
			case <-mctx.Done():
				s.mu.Lock()
				s.monitor.active = false
				s.mu.Unlock()
				return
			case <-t.C:
				s.runMonitorPass(mctx)
			}
		}
	}()
	if s.ev != nil {
		s.ev.Publish("scanner", "auto monitor started", model.JSONMap{"interval_seconds": s.cfg.Scanner.AutoIntervalSec})
	}
	return nil
}

// StopMonitor stops the automatic monitoring loop. It is idempotent: calling it
// when no monitor is running does not emit a misleading event.
func (s *Scanner) StopMonitor() {
	s.mu.Lock()
	cancel := s.monitor.cancel
	wasActive := s.monitor.active
	s.monitor.active = false
	s.monitor.cancel = nil
	s.mu.Unlock()
	s.cfg.Scanner.AutoMonitorLocal = false
	if !wasActive {
		return
	}
	if cancel != nil {
		cancel()
	}
	if s.ev != nil {
		s.ev.Publish("scanner", "auto monitor stopped", nil)
	}
}

func (s *Scanner) runMonitorPass(ctx context.Context) {
	pols, err := s.store.EnabledPolicies()
	if err != nil || len(pols) == 0 {
		return
	}
	if s.Running() > 0 {
		return // never stack scans
	}
	req := Request{
		Name:        "auto monitor",
		TargetKind:  model.ScanTargetLocal,
		PortsSpec:   s.cfg.Scanner.DefaultPorts,
		Trigger:     "auto",
		ApplyPolicy: true,
		FilterPorts: false,
		PolicyID:    s.autoPolicyID(),
	}
	if _, err := s.Run(ctx, req); err != nil {
		logging.Debug("scanner", "auto monitor local scan failed", "error", err.Error())
	}
	for _, cidr := range s.cfg.Scanner.AutoScanCIDR {
		if strings.TrimSpace(cidr) == "" {
			continue
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		req := Request{
			Name:        "auto monitor " + cidr,
			TargetKind:  model.ScanTargetCIDR,
			TargetRef:   cidr,
			PortsSpec:   s.cfg.Scanner.DefaultPorts,
			Trigger:     "auto",
			ApplyPolicy: true,
		}
		if _, err := s.Run(ctx, req); err != nil {
			logging.Debug("scanner", "auto monitor cidr scan failed", "cidr", cidr, "error", err.Error())
		}
	}
	// Hosts that were given their own policies are monitored by their current
	// address. The address is looked up through the MAC on every round, so a
	// host that picked up a new DHCP lease is scanned at its new location
	// without the user touching the policy.
	for _, ip := range s.hostPolicyTargets() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		req := Request{
			Name:        "auto monitor host " + ip,
			TargetKind:  model.ScanTargetCIDR,
			TargetRef:   ip + "/32",
			PortsSpec:   s.cfg.Scanner.DefaultPorts,
			Trigger:     "auto",
			ApplyPolicy: true,
		}
		if _, err := s.Run(ctx, req); err != nil {
			logging.Debug("scanner", "auto monitor host scan failed", "host", ip, "error", err.Error())
		}
	}
	s.mu.Lock()
	s.monitor.last = time.Now()
	s.mu.Unlock()
}

// autoAddOpenLimit bounds how many rules one scan may create, so enabling
// auto_add_open on a wide CIDR scan cannot flood the rule list.
const autoAddOpenLimit = 200

// autoAddOpen creates rules for discovered open ports when
// scanner.auto_add_open is enabled; scanner.auto_add_ports optionally narrows
// which ports qualify. Ports that already have a rule are left alone.
func (s *Scanner) autoAddOpen(ctx context.Context, ports []model.DiscoveredPort, run *model.ScanRun) int {
	if !s.cfg.Scanner.AutoAddOpen {
		return 0
	}
	var allowed map[int]bool
	if spec := strings.TrimSpace(s.cfg.Scanner.AutoAddPorts); spec != "" {
		list, err := netutil.ParsePortSpec(spec, s.cfg.Scanner.MaxPortsPerScan)
		if err != nil {
			logging.Warn("scanner", "invalid auto_add_ports spec", "error", err.Error())
			return 0
		}
		allowed = map[int]bool{}
		for _, p := range list {
			allowed[p] = true
		}
	}
	added := 0
	for i := range ports {
		p := ports[i]
		if p.Added || p.RuleID > 0 {
			continue
		}
		if p.Status != model.StatusOpen && p.Status != model.StatusNotAdded {
			continue
		}
		if len(allowed) > 0 && !allowed[p.Port] {
			continue
		}
		if added >= autoAddOpenLimit {
			logging.Warn("scanner", "auto add open hit the per-scan limit", "limit", autoAddOpenLimit)
			break
		}
		select {
		case <-ctx.Done():
			return added
		default:
		}
		rule := &model.Rule{
			Name:        fmt.Sprintf("auto %s:%d", p.Host, p.Port),
			Host:        p.Host,
			Port:        p.Port,
			Protocol:    model.ProtoTCP,
			Enabled:     true,
			AutoCreated: true,
			Source:      "scan",
			Note:        "auto-added by scanner.auto_add_open",
		}
		id, err := s.store.CreateRule(rule)
		if err != nil {
			logging.Debug("scanner", "auto add rule failed", "host", p.Host, "port", p.Port, "error", err.Error())
			continue
		}
		added++
		if s.ev != nil {
			s.ev.Publish("scanner", fmt.Sprintf("auto added rule #%d for %s:%d", id, p.Host, p.Port),
				model.JSONMap{"rule_id": id, "host": p.Host, "port": p.Port})
		}
	}
	if added > 0 && s.ev != nil {
		s.ev.Changed()
	}
	return added
}

// autoPolicyID resolves scanner.auto_local_policy (policy name or numeric id) so the
// monitored local scan actually runs the configured default policy.
func (s *Scanner) autoPolicyID() int64 {
	name := strings.TrimSpace(s.cfg.Scanner.AutoLocalPolicy)
	if name == "" {
		return 0
	}
	pols, err := s.store.EnabledPolicies()
	if err != nil {
		return 0
	}
	for _, p := range pols {
		if strings.EqualFold(strings.TrimSpace(p.Name), name) || strconv.FormatInt(p.ID, 10) == name {
			return p.ID
		}
	}
	logging.Debug("scanner", "auto_local_policy not found", "policy", name)
	return 0
}
