// Package scanner implements port discovery: local listener enumeration,
// loopback verification and CIDR/LAN TCP scans, plus the policy driven
// auto-monitor loop.
package scanner

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ekko-studio/natpilot/internal/config"
	"github.com/ekko-studio/natpilot/internal/logging"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/netutil"
	"github.com/ekko-studio/natpilot/internal/store"
)

// Events is the callback surface used to report progress.
type Events interface {
	Publish(category, message string, meta model.JSONMap)
	Changed()
	Progress(scanID int64, pct int, message string)
}

// Request describes a scan to execute.
type Request struct {
	Name string
	// TargetKind is local|cidr|remote|mac.
	//   local  this machine's listeners
	//   cidr   a network block
	//   remote a network block (treated like cidr)
	//   mac    hosts owning the given hardware addresses, resolved to their
	//          current IPs so DHCP reassignments do not break the scan
	TargetKind string
	// TargetRef is the CIDR for cidr/remote and a comma separated MAC list (full
	// address or OUI prefix) for mac.
	TargetRef   string
	PortsSpec   string
	FilterPorts bool // when true, PortsSpec acts as a filter (local) or target list
	Protocol    string
	PolicyID    int64
	Trigger     string // manual|schedule|api|auto
	ApplyPolicy bool
	MaxHosts    int
	// SkipLANCheck lets an explicit host-targeted scan (mac) run even when
	// broad CIDR scanning is disabled: the addresses come from the host list,
	// not from sweeping a subnet.
	SkipLANCheck bool
}

// Scanner runs port scans.
type Scanner struct {
	store *store.Store
	cfg   *config.Config
	ev    Events

	mu      sync.Mutex
	running map[int64]context.CancelFunc
	monitor struct {
		cancel context.CancelFunc
		active bool
		last   time.Time
	}
	// limiter paces probe starts for scanner.rate_limit_pps (0 = unlimited).
	limiter struct {
		mu   sync.Mutex
		next time.Time
	}
}

// New builds a scanner.
func New(st *store.Store, cfg *config.Config, ev Events) *Scanner {
	return &Scanner{store: st, cfg: cfg, ev: ev, running: map[int64]context.CancelFunc{}}
}

// Start launches a scan in the background and returns the created record.
func (s *Scanner) Start(ctx context.Context, req Request) (*model.ScanRun, error) {
	run, err := s.prepare(req)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.running[run.ID] = cancel
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.running, run.ID)
			s.mu.Unlock()
			cancel()
		}()
		if err := s.execute(cctx, run, req); err != nil {
			logging.Warn("scanner", "scan failed", "id", run.ID, "error", err.Error())
		}
	}()
	return run, nil
}

// Run executes a scan synchronously.
func (s *Scanner) Run(ctx context.Context, req Request) (*model.ScanRun, error) {
	run, err := s.prepare(req)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	s.running[run.ID] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, run.ID)
		s.mu.Unlock()
	}()
	if err := s.execute(cctx, run, req); err != nil {
		return run, err
	}
	return run, nil
}

// Cancel stops a running scan.
func (s *Scanner) Cancel(scanID int64) bool {
	s.mu.Lock()
	cancel, ok := s.running[scanID]
	s.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// Running reports how many scans are in flight.
func (s *Scanner) Running() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running)
}

// Shutdown cancels every in-flight scan and stops the monitor. The app calls it on
// the way down so a scan cannot outlive the process that owns it.
func (s *Scanner) Shutdown() {
	s.StopMonitor()
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.running))
	for _, cancel := range s.running {
		cancels = append(cancels, cancel)
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// pace blocks until the next probe is allowed by scanner.rate_limit_pps. A value of
// zero (the default) means no pacing. The deadline is shared by all workers so the
// configured rate applies to the scan as a whole, not to each goroutine.
func (s *Scanner) pace(ctx context.Context) {
	pps := s.cfg.Scanner.RateLimitPPS
	if pps <= 0 {
		return
	}
	if pps > 100000 {
		pps = 100000
	}
	interval := time.Second / time.Duration(pps)
	s.limiter.mu.Lock()
	now := time.Now()
	if s.limiter.next.Before(now) {
		s.limiter.next = now
	}
	wait := s.limiter.next.Sub(now)
	s.limiter.next = s.limiter.next.Add(interval)
	s.limiter.mu.Unlock()
	if wait <= 0 {
		return
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (s *Scanner) prepare(req Request) (*model.ScanRun, error) {
	if req.TargetKind == "" {
		req.TargetKind = model.ScanTargetLocal
	}
	switch req.TargetKind {
	case model.ScanTargetLocal, model.ScanTargetCIDR, model.ScanTargetRemote, model.ScanTargetMAC, model.ScanTargetHosts:
	default:
		return nil, fmt.Errorf("invalid target kind %q", req.TargetKind)
	}
	if req.TargetKind != model.ScanTargetLocal && strings.TrimSpace(req.TargetRef) == "" {
		if req.TargetKind == model.ScanTargetMAC {
			return nil, errors.New("至少需要一个 MAC 地址（可用 aa:bb:cc:dd:ee:ff 或前缀 aa:bb:cc）")
		}
		if req.TargetKind == model.ScanTargetHosts {
			return nil, errors.New("至少需要一个主机地址")
		}
		return nil, errors.New("target CIDR is required for network scans")
	}
	if req.PortsSpec == "" {
		req.PortsSpec = s.cfg.Scanner.DefaultPorts
	}
	if req.Trigger == "" {
		req.Trigger = "manual"
	}
	run := &model.ScanRun{
		Name:       req.Name,
		TargetKind: req.TargetKind,
		TargetRef:  req.TargetRef,
		PortsSpec:  req.PortsSpec,
		Trigger:    req.Trigger,
		Status:     model.ScanStatusRunning,
		PolicyID:   req.PolicyID,
		Engine:     engineName(),
	}
	id, err := s.store.CreateScan(run)
	if err != nil {
		return nil, err
	}
	run.ID = id
	if s.ev != nil {
		// A local scan has no target ref, so "%s %s" used to leave a trailing space
		// in the event text.
		target := strings.TrimSpace(req.TargetKind)
		if ref := strings.TrimSpace(req.TargetRef); ref != "" {
			target += " " + ref
		}
		s.ev.Publish("scan", fmt.Sprintf("scan #%d started (%s)", id, target),
			model.JSONMap{"scan_id": id, "kind": req.TargetKind, "ref": req.TargetRef})
		s.ev.Changed()
	}
	return run, nil
}

func engineName() string {
	return "natpilot-scanner"
}

func (s *Scanner) execute(ctx context.Context, run *model.ScanRun, req Request) error {
	start := time.Now()
	var (
		ports []model.DiscoveredPort
		err   error
	)
	switch req.TargetKind {
	case model.ScanTargetLocal:
		ports, err = s.scanLocal(ctx, run, req)
	case model.ScanTargetCIDR, model.ScanTargetRemote:
		ports, err = s.scanNetwork(ctx, run, req)
	case model.ScanTargetMAC:
		ports, err = s.scanByMAC(ctx, run, req)
	case model.ScanTargetHosts:
		ports, err = s.scanHostList(ctx, run, req)
	}
	duration := time.Since(start)

	if err != nil && len(ports) == 0 {
		_ = s.store.FinishScan(run.ID, model.ScanStatusFailed, duration.Milliseconds(), 0, 0, 1, err.Error())
		if s.ev != nil {
			s.ev.Publish("scan", fmt.Sprintf("scan #%d failed: %s", run.ID, err.Error()),
				model.JSONMap{"scan_id": run.ID, "error": err.Error()})
			s.ev.Changed()
		}
		return err
	}

	newCount := 0
	openCount := 0
	for i := range ports {
		p := &ports[i]
		_, created, uerr := s.store.UpsertPort(p)
		if uerr != nil {
			logging.Debug("scanner", "persist port failed", "error", uerr.Error())
			continue
		}
		if created {
			newCount++
		}
		if p.Status == model.StatusOpen || p.Status == model.StatusNotAdded || p.Status == model.StatusAdded {
			openCount++
		}
	}

	// Apply automatic decisions (policies) unless the caller opted out.
	applied := 0
	if req.ApplyPolicy && s.cfg.Scanner.AutoApplyPolicy {
		n, aerr := s.ApplyPolicies(ctx, run, ports)
		if aerr != nil {
			logging.Warn("scanner", "policy application failed", "error", aerr.Error())
		}
		applied = n
	}

	// scanner.auto_add_open turns discovered open ports into rules without a policy.
	autoAdded := s.autoAddOpen(ctx, ports, run)

	status := model.ScanStatusDone
	msg := fmt.Sprintf("%d open port(s), %d new, %d policy decision(s)", openCount, newCount, applied)
	if autoAdded > 0 {
		msg += fmt.Sprintf(", %d auto added", autoAdded)
	}
	if ctx.Err() != nil {
		status = model.ScanStatusCancel
		msg = "scan canceled"
	}
	_ = s.store.FinishScan(run.ID, status, duration.Milliseconds(), openCount, newCount, 0, msg)
	// Refresh the in-memory record so synchronous callers see final counters.
	if fresh, err := s.store.GetScan(run.ID); err == nil && fresh != nil {
		*run = *fresh
	}
	if s.ev != nil {
		s.ev.Publish("scan", fmt.Sprintf("scan #%d finished: %s", run.ID, msg),
			model.JSONMap{"scan_id": run.ID, "open": openCount, "new": newCount, "duration_ms": duration.Milliseconds()})
		s.ev.Changed()
	}
	return nil
}

// scanLocal enumerates this machine's listening sockets.
func (s *Scanner) scanLocal(ctx context.Context, run *model.ScanRun, req Request) ([]model.DiscoveredPort, error) {
	listeners, err := listLocalListeners()
	if err != nil {
		// Fall back to probing loopback with the requested port list.
		return s.probeHost(ctx, "127.0.0.1", req, run)
	}
	spec, specErr := netutil.ParsePortSpec(req.PortsSpec, s.cfg.Scanner.MaxPortsPerScan)
	allowed := map[int]bool{}
	if specErr == nil {
		// An explicit port list acts as a filter; without one the scan reports
		// every socket the machine is listening on, including ports outside the
		// configured default set.
		for _, p := range spec {
			allowed[p] = true
		}
	}
	filterGiven := req.FilterPorts && specErr == nil && len(allowed) > 0

	out := []model.DiscoveredPort{}
	now := time.Now()
	hostFallback := listenerHostFallback()
	type bannerJob struct {
		index int
		host  string
		port  int
	}
	jobs := []bannerJob{}
	for i, l := range listeners {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if filterGiven && !allowed[l.Port] {
			continue
		}
		if req.Protocol != "" && req.Protocol != "any" && strings.ToLower(l.Proto) != strings.ToLower(req.Protocol) {
			continue
		}
		// The address a socket is bound to is not necessarily a usable mapping
		// target: a wildcard bind (0.0.0.0 / ::) and, on windows/darwin, every
		// enumerated listener used to be reported as 127.0.0.1, which produced
		// rules that pointed the router at loopback. Report the LAN address
		// instead and keep the real bind address in the banner.
		p := model.DiscoveredPort{
			ScanID:     run.ID,
			Host:       normalizeListenerHost(l.Host, hostFallback),
			Port:       l.Port,
			Protocol:   l.Proto,
			Process:    l.Process,
			PID:        l.PID,
			Service:    netutil.GuessService(l.Port),
			Status:     model.StatusOpen,
			TargetKind: model.ScanTargetLocal,
			TargetRef:  "local",
			FirstSeen:  model.Time{Time: now},
			LastSeen:   model.Time{Time: now},
		}
		if l.Host != "0.0.0.0" && l.Host != "::" && l.Host != "" {
			p.Banner = "bound to " + l.Host
		}
		s.decorate(&p)
		// Banner grabbing is a full connect+read per socket, so it is done after
		// this loop, in parallel. Sequential grabbing made a local scan of a busy
		// windows machine take almost a minute (one timeout per silent service).
		if s.cfg.Scanner.GrabBanners && l.Proto == "tcp" {
			jobs = append(jobs, bannerJob{index: len(out), host: bannerTarget(l.Host), port: l.Port})
		}
		out = append(out, p)
		if s.ev != nil && i%10 == 0 {
			s.ev.Progress(run.ID, min(99, i+1), fmt.Sprintf("%d/%d listeners", i+1, len(listeners)))
		}
	}
	if len(jobs) > 0 {
		bannerTimeout := time.Duration(s.cfg.Scanner.TimeoutMS) * time.Millisecond
		if bannerTimeout <= 0 {
			bannerTimeout = 800 * time.Millisecond
		}
		sem := make(chan struct{}, 16)
		var bwg sync.WaitGroup
		for _, job := range jobs {
			j := job
			bwg.Add(1)
			sem <- struct{}{}
			go func() {
				defer bwg.Done()
				defer func() { <-sem }()
				bctx, cancel := context.WithTimeout(ctx, bannerTimeout)
				banner := netutil.GrabBanner(bctx, j.host, j.port, bannerTimeout)
				cancel()
				if banner != "" {
					out[j.index].Banner = banner
				}
			}()
		}
		bwg.Wait()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	if len(out) == 0 {
		if filterGiven {
			return out, fmt.Errorf("no listening socket matched the port filter %q (%d socket(s) are listening)", req.PortsSpec, len(listeners))
		}
		return out, errors.New("no listening sockets found (is /proc readable?)")
	}
	return out, nil
}

// scanByMAC scans the hosts that currently own the given hardware addresses.
//
// A MAC target is resolved against the hosts table at scan time, which is the
// whole point: the device may have been given a different IP by DHCP since the
// scan was configured, and it is still found.
func (s *Scanner) scanByMAC(ctx context.Context, run *model.ScanRun, req Request) ([]model.DiscoveredPort, error) {
	patterns := model.ParseMACPatterns(req.TargetRef)
	if len(patterns) == 0 {
		return nil, errors.New("至少需要一个 MAC 地址（可用 aa:bb:cc:dd:ee:ff 或前缀 aa:bb:cc）")
	}

	known, err := s.store.ListHosts(2000)
	if err != nil {
		return nil, err
	}
	matched := make([]model.Host, 0, len(patterns))
	seenIP := map[string]bool{}
	for _, h := range known {
		if strings.TrimSpace(h.IP) == "" || seenIP[h.IP] {
			continue
		}
		if !model.AnyMACMatch(patterns, h.MAC) {
			continue
		}
		seenIP[h.IP] = true
		matched = append(matched, h)
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf("没有主机匹配 MAC 范围 %q，请先执行一次“发现局域网主机”以刷新 MAC/IP 对应关系", req.TargetRef)
	}

	logging.Info("scanner", "resolved MAC target", "patterns", req.TargetRef, "hosts", len(matched))

	ips := make([]string, 0, len(matched))
	for _, h := range matched {
		ips = append(ips, h.IP)
	}
	return s.scanIPList(ctx, run, req, ips)
}

// ParseHostList splits the comma/space separated address list used by a
// "hosts" scan, keeping only valid IPv4/IPv6 literals.
func ParseHostList(spec string, max int) ([]string, error) {
	fields := strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '|'
	})
	out := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, f := range fields {
		ip := strings.TrimSpace(f)
		if ip == "" {
			continue
		}
		// 允许 "[fd00::1]" 与链路本地 "fe80::1%以太网"：前者是 URL 里常见的写法，
		// 后者是 IPv6 link-local 探测必需的 scope（保留 %zone 供 socket 使用）。
		ip = strings.TrimPrefix(strings.TrimSuffix(ip, "]"), "[")
		bare := ip
		zone := ""
		if i := strings.IndexByte(ip, '%'); i > 0 {
			bare, zone = ip[:i], ip[i+1:]
		}
		if net.ParseIP(bare) == nil {
			return nil, fmt.Errorf("不是有效的 IP 地址: %q", ip)
		}
		if zone != "" {
			ip = bare + "%" + zone
		} else {
			ip = bare
		}
		if seen[ip] {
			continue
		}
		seen[ip] = true
		out = append(out, ip)
	}
	if len(out) == 0 {
		return nil, errors.New("至少需要一个主机地址")
	}
	if max > 0 && len(out) > max {
		return nil, fmt.Errorf("最多一次扫描 %d 台主机，当前 %d 台", max, len(out))
	}
	return out, nil
}

// scanHostList scans exactly the addresses the user picked in the host list.
func (s *Scanner) scanHostList(ctx context.Context, run *model.ScanRun, req Request) ([]model.DiscoveredPort, error) {
	ips, err := ParseHostList(req.TargetRef, 512)
	if err != nil {
		return nil, err
	}
	logging.Info("scanner", "scanning selected hosts", "count", len(ips))
	return s.scanIPList(ctx, run, req, ips)
}

// scanIPList runs a /32 scan per address and merges the results.
func (s *Scanner) scanIPList(ctx context.Context, run *model.ScanRun, req Request, ips []string) ([]model.DiscoveredPort, error) {
	out := []model.DiscoveredPort{}
	var lastErr error
	for _, ip := range ips {
		if ctx.Err() != nil {
			break
		}
		sub := req
		sub.TargetKind = model.ScanTargetCIDR
		sub.TargetRef = ip + "/32"
		sub.SkipLANCheck = true
		ports, serr := s.scanNetwork(ctx, run, sub)
		if serr != nil {
			lastErr = serr
			logging.Warn("scanner", "host scan failed", "host", ip, "error", serr.Error())
			continue
		}
		out = append(out, ports...)
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

// scanNetwork performs a TCP connect scan against a CIDR block.
func (s *Scanner) scanNetwork(ctx context.Context, run *model.ScanRun, req Request) ([]model.DiscoveredPort, error) {
	if !s.cfg.Scanner.EnableLANScan && req.TargetKind != model.ScanTargetLocal && !req.SkipLANCheck {
		return nil, errors.New("LAN/CIDR scanning is disabled in configuration")
	}
	maxHosts := req.MaxHosts
	if maxHosts <= 0 {
		maxHosts = s.cfg.Scanner.MaxHostsPerScan
	}
	hosts, err := netutil.ExpandCIDR(req.TargetRef, maxHosts)
	if err != nil {
		return nil, err
	}
	ports, err := netutil.ParsePortSpec(req.PortsSpec, s.cfg.Scanner.MaxPortsPerScan)
	if err != nil {
		return nil, err
	}
	_ = s.store.UpdateScanProgress(run.ID, 0, len(hosts)*len(ports), 0, 0, 0, "probing hosts")

	local, _ := netutil.LocalAddresses()
	out := []model.DiscoveredPort{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	conc := s.cfg.Scanner.MaxConcurrency
	if conc <= 0 {
		conc = 256
	}
	sem := make(chan struct{}, conc)
	timeout := time.Duration(s.cfg.Scanner.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 800 * time.Millisecond
	}

	total := len(hosts) * len(ports)
	done := 0
	var progressMu sync.Mutex

	for _, host := range hosts {
		if ctx.Err() != nil {
			break
		}
		h := host
		for _, port := range ports {
			if ctx.Err() != nil {
				break
			}
			p := port
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				cctx, cancel := context.WithTimeout(ctx, timeout)
				open, lat, err := netutil.TCPCheck(cctx, h, p, timeout)
				cancel()
				progressMu.Lock()
				done++
				if s.ev != nil && done%50 == 0 {
					s.ev.Progress(run.ID, done*100/maxInt(1, total), fmt.Sprintf("%d/%d probes", done, total))
				}
				progressMu.Unlock()
				if err != nil || !open {
					return
				}
				dp := model.DiscoveredPort{
					ScanID:     run.ID,
					Host:       h,
					Port:       p,
					Protocol:   model.ProtoTCP,
					Service:    netutil.GuessService(p),
					Status:     model.StatusOpen,
					LatencyMS:  int(lat.Milliseconds()),
					TargetKind: req.TargetKind,
					TargetRef:  req.TargetRef,
					FirstSeen:  model.Time{Time: time.Now()},
					LastSeen:   model.Time{Time: time.Now()},
				}
				if local[h] {
					dp.Process = "local"
				}
				if s.cfg.Scanner.GrabBanners {
					bctx, bcancel := context.WithTimeout(ctx, timeout)
					dp.Banner = netutil.GrabBanner(bctx, h, p, timeout)
					bcancel()
				}
				s.decorate(&dp)
				mu.Lock()
				out = append(out, dp)
				mu.Unlock()
			}()
		}
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].Port < out[j].Port
	})
	return out, nil
}

// probeHost TCP-connects every port in the spec against a single host.
func (s *Scanner) probeHost(ctx context.Context, host string, req Request, run *model.ScanRun) ([]model.DiscoveredPort, error) {
	ports, err := netutil.ParsePortSpec(req.PortsSpec, s.cfg.Scanner.MaxPortsPerScan)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(s.cfg.Scanner.TimeoutMS) * time.Millisecond
	out := []model.DiscoveredPort{}
	for _, p := range ports {
		if ctx.Err() != nil {
			break
		}
		s.pace(ctx)
		cctx, cancel := context.WithTimeout(ctx, timeout)
		open, lat, _ := netutil.TCPCheck(cctx, host, p, timeout)
		cancel()
		if !open {
			continue
		}
		dp := model.DiscoveredPort{
			ScanID: run.ID, Host: listenerHostFallback(), Port: p, Protocol: model.ProtoTCP,
			Service: netutil.GuessService(p), Status: model.StatusOpen,
			LatencyMS: int(lat.Milliseconds()), TargetKind: model.ScanTargetLocal, TargetRef: "local",
			FirstSeen: model.Time{Time: time.Now()}, LastSeen: model.Time{Time: time.Now()},
		}
		s.decorate(&dp)
		out = append(out, dp)
	}
	return out, nil
}

// decorate fills the rule-linked lifecycle status of a discovered port.
func (s *Scanner) decorate(p *model.DiscoveredPort) {
	if p.Protocol == "" {
		p.Protocol = model.ProtoTCP
	}
	rule, err := s.store.GetRuleByKey(p.Host, p.Port, p.Protocol)
	if err != nil {
		logging.Debug("scanner", "rule lookup failed", "error", err.Error())
	}
	if rule != nil {
		p.RuleID = rule.ID
		p.Added = true
		p.Status = model.StatusAdded
		return
	}
	// A rule that manages this port from a different host still means the user
	// already tracks it.
	rules, _ := s.store.AllRules()
	for _, r := range rules {
		if r.Port == p.Port && strings.EqualFold(r.Protocol, p.Protocol) {
			p.RuleID = r.ID
			p.Added = true
			p.Status = model.StatusAdded
			return
		}
	}
	if p.ID > 0 {
		if prev, err := s.store.GetPort(p.ID); err == nil && prev != nil && prev.Ignored {
			p.Ignored = true
			p.Status = model.StatusClosed
			return
		}
	}
	// Open but not managed yet: awaiting a user decision.
	p.Status = model.StatusNotAdded
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// bannerTarget returns the loopback-safe address to connect to for a banner
// probe: a concrete bind address is used as-is, a wildcard bind means loopback.
func bannerTarget(bind string) string {
	h := strings.Trim(strings.TrimSpace(bind), "[]")
	if i := strings.LastIndex(h, "%"); i > 0 {
		h = h[:i]
	}
	switch strings.ToLower(h) {
	case "", "*", "0.0.0.0", "::":
		return "127.0.0.1"
	}
	return h
}

// listenerHostFallback is the address reported for ports whose socket is bound
// to a wildcard address. Using the LAN address (instead of 127.0.0.1) keeps the
// rules created from a scan usable: the router must forward to a routable host.
func listenerHostFallback() string {
	if ip, err := netutil.PrimaryIPv4(); err == nil && ip != "" {
		return ip
	}
	return "127.0.0.1"
}

// normalizeListenerHost maps the bind address of a listening socket to the
// address that should appear in the managed rule list.
//
// The port list answers "which service could I expose on the LAN?", so:
//
//   - a wildcard bind (0.0.0.0 / :: / *) becomes the LAN address;
//   - a concrete IPv4 bind is kept (that is the address the service listens on);
//   - an IPv6 bind is folded onto IPv4, because IGD/PCP internal clients are
//     IPv4 addresses: ::1 becomes 127.0.0.1, anything else becomes the LAN
//     address. Folding also collapses the dual-stack duplicates that linux
//     reports twice (0.0.0.0:80 and [::]:80).
//
// Pure so the mapping can be unit tested on every platform.
func normalizeListenerHost(bind, fallback string) string {
	lan := fallback
	if lan == "" {
		lan = "127.0.0.1"
	}
	h := strings.Trim(strings.TrimSpace(bind), "[]")
	if i := strings.LastIndex(h, "%"); i > 0 {
		h = h[:i] // drop the IPv6 zone id ("fe80::1%7")
	}
	switch strings.ToLower(h) {
	case "", "*", "0.0.0.0", "::":
		return lan
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return h
	}
	if ip.To4() != nil {
		return ip.String()
	}
	if ip.IsUnspecified() {
		return lan
	}
	if ip.IsLoopback() {
		return "127.0.0.1"
	}
	// 具体的 IPv6 监听地址（全局 / ULA）保留下来，和 IPv4 行为对齐：
	// 端口列表要如实反映"这个服务在哪个地址上可达"。link-local 没有 zone 无法
	// 复现，仍折叠到 LAN 地址以免生成不可用的规则。
	if ip.IsLinkLocalUnicast() {
		return lan
	}
	return ip.String()
}
