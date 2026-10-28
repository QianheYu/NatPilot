// Package mapper owns the NAT gateway lifecycle: discovery, gateway selection,
// reconciliation of the managed rule list into real port mappings, lease
// renewal, conflict detection and conflict-free teardown.
package mapper

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
	"github.com/ekko-studio/natpilot/internal/proto"
	"github.com/ekko-studio/natpilot/internal/store"
)

// Events is the callback surface the mapper uses to inform the rest of the
// system about state changes.
type Events interface {
	Publish(category, message string, meta model.JSONMap)
	Changed()
}

// Mapper reconciles the rule table against the NAT gateways.
type Mapper struct {
	store *store.Store
	cfg   *config.Config
	ev    Events

	mu        sync.RWMutex
	providers []proto.DiscoveredGateway
	preferred string // provider key
	records   map[int64]model.MappingRecord
	conflicts map[int64]int64 // ruleID -> conflicting ruleID
	// extOverride carries the external port chosen by the "rename" conflict policy.
	extOverride map[int64]int

	lastDiscover time.Time
	lastError    string
	discovering  bool

	stop chan struct{}
	wg   sync.WaitGroup
}

// New creates a mapper.
func New(st *store.Store, cfg *config.Config, ev Events) *Mapper {
	return &Mapper{
		store:       st,
		cfg:         cfg,
		ev:          ev,
		records:     map[int64]model.MappingRecord{},
		conflicts:   map[int64]int64{},
		extOverride: map[int64]int{},
		stop:        make(chan struct{}),
	}
}

// LoadRecords restores the in-memory mapping state from the database.
//
// Only records of currently disabled rules are restored: those are the live gateway
// mappings that still have to be torn down, and their rules never go through the
// reconcile pass. Enabled rules are deliberately left out so that every start
// re-provisions them from scratch, which also recovers from a router reboot that
// silently dropped the mappings.
//
// Without this the cleanup paths (reconcile, /api/mapper/cleanup, the scheduled
// job) start from an empty map after a restart and can never find the mappings
// they are supposed to remove.
func (m *Mapper) LoadRecords() {
	byRule, err := m.store.MappingsByRule()
	if err != nil {
		logging.Warn("mapper", "could not restore mapping state", "error", err.Error())
		return
	}
	if len(byRule) == 0 {
		return
	}
	rules, err := m.store.AllRules()
	if err != nil {
		logging.Warn("mapper", "could not restore mapping state", "error", err.Error())
		return
	}
	enabled := make(map[int64]bool, len(rules))
	for _, r := range rules {
		if r.Enabled {
			enabled[r.ID] = true
		}
	}
	restored := 0
	for ruleID, rec := range byRule {
		if rec.Status != model.MapStatusActive || enabled[ruleID] || rec.ProviderName == "dry-run" {
			continue
		}
		m.setRecord(ruleID, rec)
		restored++
	}
	if restored > 0 {
		logging.Info("mapper", "restored mapping state from database", "records", restored)
	}
}

func providerKey(info proto.GatewayInfo) string {
	return fmt.Sprintf("%s|%s|%d", info.Kind, info.Address, info.Port)
}

// Discover refreshes the gateway list from the network and persists it.
func (m *Mapper) Discover(ctx context.Context) ([]model.Gateway, error) {
	m.mu.Lock()
	if m.discovering {
		m.mu.Unlock()
		return m.GatewayList(), nil
	}
	m.discovering = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.discovering = false
		m.mu.Unlock()
	}()

	opts := proto.DiscoverOptions{
		Protocols:  m.cfg.Mapper.Protocols,
		Timeout:    time.Duration(m.discoveryTimeoutSec()) * time.Second,
		UDPSources: proto.CandidateServers(),
	}
	if ip, err := netutil.PrimaryIPv4(); err == nil {
		opts.LocalIP = ip
	}

	discovered, err := proto.Discover(ctx, opts)
	if err != nil {
		m.setLastError(err.Error())
		return nil, err
	}
	logging.Info("mapper", "gateway discovery complete", "count", len(discovered))

	persisted := []model.Gateway{}
	for _, d := range discovered {
		info := d.Info
		gw := &model.Gateway{
			Name:         info.Name,
			Kind:         info.Kind,
			Address:      info.Address,
			Port:         info.Port,
			Location:     info.Location,
			ControlURL:   info.ControlURL,
			ServiceType:  info.ServiceType,
			ExternalIP:   info.ExternalIP,
			ModelName:    info.ModelName,
			Manufacturer: info.Manufacturer,
			Enabled:      true,
			Capabilities: info.Capabilities,
		}
		if err := m.store.UpsertGateway(gw); err != nil {
			logging.Warn("mapper", "persist gateway failed", "error", err.Error())
			continue
		}
		persisted = append(persisted, *gw)
	}

	m.mu.Lock()
	m.providers = discovered
	m.lastDiscover = time.Now()
	m.lastError = ""
	if m.preferred == "" && len(discovered) > 0 {
		m.preferred = providerKey(discovered[0].Info)
	}
	// Drop a stale preference.
	if m.preferred != "" {
		found := false
		for _, d := range discovered {
			if providerKey(d.Info) == m.preferred {
				found = true
				break
			}
		}
		if !found {
			m.preferred = ""
			if len(discovered) > 0 {
				m.preferred = providerKey(discovered[0].Info)
			}
		}
	}
	pref := m.preferred
	m.mu.Unlock()

	if pref != "" {
		if gw, err := m.store.PreferredGateway(); err == nil && gw != nil {
			key := fmt.Sprintf("%s|%s|%d", gw.Kind, gw.Address, gw.Port)
			m.SetPreferred(key)
		}
	}

	if err := m.store.ReconcilePortStatuses(); err != nil {
		logging.Debug("mapper", "port status reconcile failed", "error", err.Error())
	}

	m.ensureProviders()
	if m.ev != nil {
		m.ev.Changed()
	}
	return persisted, nil
}

func (m *Mapper) discoveryTimeoutSec() int {
	t := m.cfg.Mapper.DiscoverInterval
	if t <= 0 {
		t = 300
	}
	return 5
}

func (m *Mapper) setLastError(msg string) {
	m.mu.Lock()
	m.lastError = msg
	m.mu.Unlock()
	if m.ev != nil && msg != "" {
		m.ev.Publish("gateway", "gateway discovery failed", model.JSONMap{"error": msg})
	}
}

// ensureProviders keeps at least one live provider; if discovery has not run it
// probes the default gateway directly.
func (m *Mapper) ensureProviders() {
	m.mu.RLock()
	n := len(m.providers)
	m.mu.RUnlock()
	if n > 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	opts := proto.DiscoverOptions{
		Protocols:  m.cfg.Mapper.Protocols,
		Timeout:    3 * time.Second,
		UDPSources: proto.CandidateServers(),
	}
	if ip, err := netutil.PrimaryIPv4(); err == nil {
		opts.LocalIP = ip
	}
	found, err := proto.Discover(ctx, opts)
	if err != nil || len(found) == 0 {
		return
	}
	m.mu.Lock()
	m.providers = found
	m.mu.Unlock()
}

// GatewayList returns the persisted gateways (with live capability info).
func (m *Mapper) GatewayList() []model.Gateway {
	list, err := m.store.ListGateways()
	if err != nil {
		return nil
	}
	m.mu.RLock()
	pref := m.preferred
	live := map[string]proto.GatewayInfo{}
	for _, d := range m.providers {
		live[providerKey(d.Info)] = d.Info
	}
	m.mu.RUnlock()
	out := make([]model.Gateway, 0, len(list))
	for _, g := range list {
		key := fmt.Sprintf("%s|%s|%d", g.Kind, g.Address, g.Port)
		g.Preferred = key == pref
		if info, ok := live[key]; ok {
			if g.ExternalIP == "" {
				g.ExternalIP = info.ExternalIP
			}
		}
		out = append(out, g)
	}
	return out
}

// Providers returns the live discovered providers.
func (m *Mapper) Providers() []proto.DiscoveredGateway {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]proto.DiscoveredGateway{}, m.providers...)
}

// SetPreferred marks a gateway as preferred by key.
func (m *Mapper) SetPreferred(key string) {
	m.mu.Lock()
	m.preferred = key
	m.mu.Unlock()
	if gw, err := m.findPersisted(key); err == nil && gw != nil {
		_ = m.store.SetGatewayPreferred(gw.ID)
	}
}

// PreferredKey returns the preferred provider key.
func (m *Mapper) PreferredKey() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.preferred
}

func (m *Mapper) findPersisted(key string) (*model.Gateway, error) {
	parts := strings.Split(key, "|")
	if len(parts) != 3 {
		return nil, errors.New("invalid gateway key")
	}
	list, err := m.store.ListGateways()
	if err != nil {
		return nil, err
	}
	for i := range list {
		k := fmt.Sprintf("%s|%s|%d", list[i].Kind, list[i].Address, list[i].Port)
		if k == key {
			return &list[i], nil
		}
	}
	return nil, nil
}

// LastStat returns discovery metadata.
func (m *Mapper) LastDiscover() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastDiscover
}

// LastError returns the most recent discovery/mapping error.
func (m *Mapper) LastError() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastError
}

// candidates returns the providers that can carry the requested protocol, with
// the preferred gateway first.
func (m *Mapper) candidates(protoName string) []proto.DiscoveredGateway {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []proto.DiscoveredGateway{}
	var pref *proto.DiscoveredGateway
	for i := range m.providers {
		if providerKey(m.providers[i].Info) == m.preferred {
			pref = &m.providers[i]
		}
	}
	// Only lead with the preferred gateway when it can actually carry the protocol;
	// otherwise it would be tried first and fail before a capable one is reached.
	if pref != nil && supports(*pref, protoName) {
		out = append(out, *pref)
	}
	for _, d := range m.providers {
		if pref != nil && providerKey(d.Info) == m.preferred {
			continue
		}
		if !supports(d, protoName) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// supports reports whether a gateway can carry the requested protocol.
//
// Gateways advertise their abilities as "map-tcp"/"map-udp", so for those two the
// capability list is authoritative. For any other protocol name there is no
// capability vocabulary to match, and the gateway is given the chance to reject
// the mapping itself. Returning true from both branches of the old switch made the
// filter a no-op and always routed rules to the preferred gateway.
func supports(d proto.DiscoveredGateway, protoName string) bool {
	p := strings.ToLower(strings.TrimSpace(protoName))
	if p == "" {
		return true
	}
	known := p == model.ProtoTCP || p == model.ProtoUDP
	if !known {
		return true
	}
	if len(d.Info.Capabilities) > 0 {
		want := "map-" + p
		for _, c := range d.Info.Capabilities {
			if strings.EqualFold(c, want) {
				return true
			}
		}
		return false
	}
	// A gateway that describes no capabilities at all is assumed to speak the
	// protocols of its transport.
	switch d.Info.Kind {
	case proto.KindIGD2, proto.KindIGD1, proto.KindPCP, proto.KindNATPMP:
		return true
	}
	return false
}

// Reconcile brings the gateway state in line with the enabled rule list.
func (m *Mapper) Reconcile(ctx context.Context) (Summary, error) {
	sum := Summary{}
	rules, err := m.store.EnabledRules()
	if err != nil {
		return sum, err
	}
	allRules, err := m.store.AllRules()
	if err != nil {
		return sum, err
	}

	m.mu.RLock()
	hasProvider := len(m.providers) > 0
	m.mu.RUnlock()
	if !hasProvider {
		m.ensureProviders()
	}

	// Detect external port conflicts among enabled rules.
	m.detectConflicts(rules)

	for i := range rules {
		r := rules[i]
		if ctx.Err() != nil {
			return sum, ctx.Err()
		}
		res := m.ensureRule(ctx, &r)
		switch {
		case res.err != nil:
			sum.Failed++
		case res.created:
			sum.Created++
		case res.renewed:
			sum.Renewed++
		default:
			sum.OK++
		}
		if res.changed {
			sum.Changed++
		}
	}

	// Remove mappings for rules that no longer exist or were disabled.
	if m.cfg.Mapper.CleanupMissing {
		live := map[int64]bool{}
		for _, r := range rules {
			live[r.ID] = true
		}
		for _, r := range allRules {
			if live[r.ID] {
				continue
			}
			if rec, ok := m.record(r.ID); ok && rec.Status == model.MapStatusActive {
				m.removeRule(ctx, r)
				sum.Removed++
			}
		}
	}

	if err := m.store.ReconcilePortStatuses(); err != nil {
		logging.Debug("mapper", "port status reconcile failed", "error", err.Error())
	}
	if m.ev != nil {
		m.ev.Changed()
	}
	return sum, nil
}

// Summary aggregates a reconcile pass.
type Summary struct {
	Created int `json:"created"`
	Renewed int `json:"renewed"`
	OK      int `json:"ok"`
	Failed  int `json:"failed"`
	Removed int `json:"removed"`
	Changed int `json:"changed"`
}

func (s Summary) String() string {
	return fmt.Sprintf("created=%d renewed=%d ok=%d failed=%d removed=%d", s.Created, s.Renewed, s.OK, s.Failed, s.Removed)
}

func (m *Mapper) record(ruleID int64) (model.MappingRecord, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.records[ruleID]
	return r, ok
}

func (m *Mapper) setRecord(ruleID int64, rec model.MappingRecord) {
	m.mu.Lock()
	m.records[ruleID] = rec
	m.mu.Unlock()
}

func (m *Mapper) delRecord(ruleID int64) {
	m.mu.Lock()
	delete(m.records, ruleID)
	m.mu.Unlock()
}

// Records returns a copy of the current mapping records.
func (m *Mapper) Records() map[int64]model.MappingRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[int64]model.MappingRecord{}
	for k, v := range m.records {
		out[k] = v
	}
	return out
}

// Conflicts returns the ruleID -> conflicting ruleID map.
func (m *Mapper) Conflicts() map[int64]int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[int64]int64{}
	for k, v := range m.conflicts {
		out[k] = v
	}
	return out
}

// conflictPolicy returns the configured duplicate-port policy, defaulting to skip.
func (m *Mapper) conflictPolicy() string {
	switch p := strings.ToLower(strings.TrimSpace(m.cfg.Mapper.ConflictPolicy)); p {
	case "override", "rename", "error":
		return p
	default:
		return "skip"
	}
}

// detectConflicts finds enabled rules that request the same external port and
// applies mapper.conflict_policy:
//
//	skip (default) keep the lowest id, later duplicates are marked conflicting
//	error          same as skip, but the duplicate is also reported as an error
//	override       the highest id wins and the earlier rules step aside
//	rename         every duplicate gets a free external port above the requested one
func (m *Mapper) detectConflicts(rules []model.Rule) {
	policy := m.conflictPolicy()
	conflicts := map[int64]int64{}
	overrides := map[int64]int{}

	ordered := append([]model.Rule{}, rules...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if policy == "override" {
			// The newest rule wins, so walk from the highest id downwards.
			return ordered[i].ID > ordered[j].ID
		}
		return ordered[i].ID < ordered[j].ID
	})

	used := map[string]int64{}
	for _, r := range ordered {
		ext := r.ExtPort
		if ext == 0 {
			// No explicit external port: the gateway assigns one, so there is
			// nothing to conflict over.
			continue
		}
		key := fmt.Sprintf("%s:%d", strings.ToLower(r.Protocol), ext)
		own, taken := used[key]
		if !taken {
			used[key] = r.ID
			continue
		}
		if policy == "rename" {
			if port, ok := freeExtPort(used, r.Protocol, ext); ok {
				overrides[r.ID] = port
				used[fmt.Sprintf("%s:%d", strings.ToLower(r.Protocol), port)] = r.ID
				continue
			}
		}
		conflicts[r.ID] = own
	}
	m.mu.Lock()
	m.conflicts = conflicts
	m.extOverride = overrides
	m.mu.Unlock()
}

// freeExtPort finds an unused external port for the protocol, starting above the
// requested one. The search is bounded so a saturated range cannot spin.
func freeExtPort(used map[string]int64, protocol string, start int) (int, bool) {
	p := strings.ToLower(strings.TrimSpace(protocol))
	for port := start + 1; port <= 65535 && port <= start+2000; port++ {
		if _, taken := used[fmt.Sprintf("%s:%d", p, port)]; !taken {
			return port, true
		}
	}
	return 0, false
}

type ruleResult struct {
	created bool
	renewed bool
	changed bool
	err     error
}

// ensureRule makes sure a single rule has an active mapping.
func (m *Mapper) ensureRule(ctx context.Context, r *model.Rule) ruleResult {
	res := ruleResult{}
	if m.cfg.Mapper.DryRun {
		// The preview must show the address that would really be pushed to the
		// gateway, not the raw stored value, otherwise a rule defaulting to
		// 127.0.0.1 looks broken in the UI while the real mapping would be fine.
		m.persistRecord(r, model.MappingRecord{
			RuleID: r.ID, ProviderName: "dry-run", Protocol: m.ruleProtocol(r), Status: model.MapStatusActive,
			InternalHost: effectiveHost(m, r), InternalPort: r.Port, ExternalPort: m.effectiveExtPort(r),
			LeaseSeconds: m.effectiveLease(r),
			Description:  r.Name, ExternalIP: "dry-run",
		})
		return res
	}

	// Conflicting rules are skipped rather than clobbering each other.
	m.mu.RLock()
	if other, bad := m.conflicts[r.ID]; bad {
		m.mu.RUnlock()
		m.persistRecord(r, model.MappingRecord{
			RuleID: r.ID, ProviderName: "", Protocol: r.Protocol, Status: model.MapStatusConflict,
			InternalHost: r.Host, InternalPort: r.Port,
			Error: fmt.Sprintf("external port conflict with rule #%d", other),
		})
		return ruleResult{err: fmt.Errorf("conflict with rule %d", other), changed: true}
	}
	m.mu.RUnlock()

	prev, hadPrev := m.record(r.ID)
	if hadPrev && prev.Status == model.MapStatusActive && prev.ExternalPort > 0 {
		// Renew when the lease is close to expiry (or always for long leases).
		ttl := m.cfg.Mapper.RenewBefore
		if ttl <= 0 {
			ttl = 120
		}
		exp := time.Time{}
		if prev.ExpiresAt.Valid {
			exp = prev.ExpiresAt.Time.Time
		}
		if exp.IsZero() || time.Until(exp) > time.Duration(ttl)*time.Second {
			// Still valid and the rule has not changed its target.
			if prev.InternalPort == r.Port && prev.InternalHost == effectiveHost(m, r) {
				return ruleResult{}
			}
		}
		res.renewed = true
	}

	mapping := proto.Mapping{
		Protocol:     m.ruleProtocol(r),
		InternalHost: effectiveHost(m, r),
		InternalPort: r.Port,
		ExternalPort: m.effectiveExtPort(r),
		LeaseSeconds: m.effectiveLease(r),
		Description:  buildDescription(r),
		Enabled:      true,
	}

	var lastErr error
	for _, d := range m.candidates(mapping.Protocol) {
		cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		result, err := d.Provider.AddMapping(cctx, mapping)
		cancel()
		if err != nil {
			lastErr = err
			logging.Debug("mapper", "mapping attempt failed",
				"gateway", d.Info.Kind, "address", d.Info.Address, "error", err.Error())
			continue
		}
		expires := model.NullTime{}
		if result.LeaseSeconds > 0 {
			expires = model.Ptr(result.ExpiresAt())
		}
		rec := model.MappingRecord{
			RuleID:       r.ID,
			ProviderName: providerKey(d.Info),
			Protocol:     result.Protocol,
			Status:       model.MapStatusActive,
			ExternalIP:   result.ExternalIP,
			ExternalPort: result.ExternalPort,
			InternalHost: result.InternalHost,
			InternalPort: result.InternalPort,
			LeaseSeconds: result.LeaseSeconds,
			Description:  mapping.Description,
			ExpiresAt:    expires,
		}
		m.persistRecord(r, rec)
		res.created = !hadPrev
		res.changed = true
		return res
	}

	if lastErr == nil {
		lastErr = proto.ErrNoGateway
	}
	m.persistRecord(r, model.MappingRecord{
		RuleID: r.ID, Protocol: r.Protocol, Status: model.MapStatusFailed,
		InternalHost: mapping.InternalHost, InternalPort: r.Port, ExternalPort: mapping.ExternalPort,
		Description: mapping.Description, Error: lastErr.Error(),
	})
	res.err = lastErr
	res.changed = true
	if m.ev != nil {
		m.ev.Publish("mapping", fmt.Sprintf("mapping failed for rule #%d (%s/%d): %s", r.ID, r.Protocol, r.Port, lastErr.Error()),
			model.JSONMap{"rule_id": r.ID, "port": r.Port, "error": lastErr.Error()})
	}
	return res
}

// effectiveHost resolves a rule's internal host, defaulting to this machine.
//
// A loopback or wildcard address is treated exactly like an empty host: no router
// can forward traffic to 127.0.0.1, so a rule stored without an explicit host (the
// store defaults it to 127.0.0.1) still has to target this machine's LAN address.
func effectiveHost(m *Mapper, r *model.Rule) string {
	if isLocalHostSpec(r.Host) {
		if ip, err := netutil.PrimaryIPv4(); err == nil {
			return ip
		}
		return "127.0.0.1"
	}
	return strings.TrimSpace(r.Host)
}

// isLocalHostSpec reports whether the host means "this machine" rather than a
// concrete address on the LAN.
func isLocalHostSpec(h string) bool {
	switch strings.ToLower(strings.TrimSpace(h)) {
	case "", "this", "self", "localhost", "0.0.0.0", "::", "[::]":
		return true
	}
	if ip := net.ParseIP(strings.Trim(strings.TrimSpace(h), "[]")); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return false
}

// effectiveExtPort returns the external port to request (0 = let the gateway pick).
// A port chosen by the "rename" conflict policy wins over the requested one.
func (m *Mapper) effectiveExtPort(r *model.Rule) int {
	m.mu.RLock()
	override, hasOverride := m.extOverride[r.ID]
	m.mu.RUnlock()
	if hasOverride && override > 0 {
		return override
	}
	if r.ExtPort > 0 && r.ExtPort != r.Port {
		return r.ExtPort
	}
	if !m.cfg.Mapper.AllowExtRange {
		return r.Port
	}
	return r.ExtPort
}

// ruleProtocol resolves the protocol a rule is mapped with. An empty protocol falls
// back to mapper.default_protocol (and finally to tcp), so the configuration knob
// actually decides the default instead of being ignored.
func (m *Mapper) ruleProtocol(r *model.Rule) string {
	p := strings.TrimSpace(r.Protocol)
	if p == "" {
		p = strings.TrimSpace(m.cfg.Mapper.DefaultProtocol)
	}
	return proto.NormalizeProto(p)
}

// effectiveLease returns the lease to request, clamped by mapper.max_lease_seconds
// so neither a rule nor the default can exceed the configured cap.
func (m *Mapper) effectiveLease(r *model.Rule) int {
	lease := m.cfg.Mapper.DefaultLease
	if r != nil && r.LeaseSeconds > 0 {
		lease = r.LeaseSeconds
	}
	if lease <= 0 {
		lease = 86400
	}
	if max := m.cfg.Mapper.MaxLease; max > 0 && lease > max {
		lease = max
	}
	return lease
}

func buildDescription(r *model.Rule) string {
	base := strings.TrimSpace(r.Name)
	if base == "" {
		base = "natpilot"
	}
	if len(r.Tags) > 0 {
		base += " [" + strings.Join(r.Tags, ",") + "]"
	}
	if len(base) > 60 {
		base = base[:60]
	}
	return base
}

func (m *Mapper) persistRecord(r *model.Rule, rec model.MappingRecord) {
	rec.RuleID = r.ID
	if rec.Protocol == "" {
		rec.Protocol = r.Protocol
	}
	if err := m.store.UpsertMapping(&rec); err != nil {
		logging.Warn("mapper", "persist mapping failed", "error", err.Error())
	}
	m.setRecord(r.ID, rec)
}

// removeRule deletes a mapping from the gateway and clears local state.
func (m *Mapper) removeRule(ctx context.Context, r model.Rule) error {
	rec, ok := m.record(r.ID)
	if !ok {
		rec = model.MappingRecord{RuleID: r.ID, Protocol: r.Protocol, InternalPort: r.Port}
	}
	extPort := rec.ExternalPort
	if extPort == 0 {
		extPort = r.Port
	}
	mapping := proto.Mapping{
		Protocol:     proto.NormalizeProto(r.Protocol),
		InternalHost: rec.InternalHost,
		InternalPort: r.Port,
		ExternalPort: extPort,
	}
	var lastErr error
	for _, d := range m.candidates(mapping.Protocol) {
		if !hasCapability(d, "delete") {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := d.Provider.DeleteMapping(cctx, mapping)
		cancel()
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = err
	}
	m.delRecord(r.ID)
	if err := m.store.DeleteMappingsForRule(r.ID); err != nil {
		logging.Debug("mapper", "delete mapping rows failed", "error", err.Error())
	}
	if lastErr != nil {
		logging.Warn("mapper", "delete mapping failed", "rule", r.ID, "error", lastErr.Error())
	}
	if m.ev != nil {
		m.ev.Publish("mapping", fmt.Sprintf("removed mapping for rule #%d (%s/%d)", r.ID, r.Protocol, r.Port),
			model.JSONMap{"rule_id": r.ID, "port": r.Port})
	}
	return lastErr
}

func hasCapability(d proto.DiscoveredGateway, cap string) bool {
	for _, c := range d.Info.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// RemoveMapping tears down the mapping of a single rule.
func (m *Mapper) RemoveMapping(ctx context.Context, rule model.Rule) error {
	return m.removeRule(ctx, rule)
}

// ApplyRule provisions (or removes) the mapping of a single rule immediately.
func (m *Mapper) ApplyRule(ctx context.Context, rule model.Rule) (Summary, error) {
	if !rule.Enabled {
		err := m.removeRule(ctx, rule)
		return Summary{Removed: 1}, err
	}
	m.mu.RLock()
	hasProvider := len(m.providers) > 0
	m.mu.RUnlock()
	if !hasProvider {
		m.ensureProviders()
	}
	res := m.ensureRule(ctx, &rule)
	sum := Summary{Changed: 1}
	switch {
	case res.err != nil:
		sum.Failed = 1
	case res.created:
		sum.Created = 1
	case res.renewed:
		sum.Renewed = 1
	default:
		sum.OK = 1
	}
	if err := m.store.ReconcilePortStatuses(); err != nil {
		logging.Debug("mapper", "port status reconcile failed", "error", err.Error())
	}
	if m.ev != nil {
		m.ev.Changed()
	}
	return sum, res.err
}

// ApplyRules applies a batch of rules sharing one gateway discovery attempt.
//
// ApplyRule re-runs discovery whenever no provider is cached, which is the common
// case on a network without a usable IGD. Adding N ports then cost N discovery
// timeouts (one every ~6s), so the request that created them looked like it hung.
// Batching the discovery keeps the call bounded no matter how many ports the user
// selected; the per-rule work itself is unchanged.
func (m *Mapper) ApplyRules(ctx context.Context, rules []model.Rule) (Summary, error) {
	total := Summary{}
	if len(rules) == 0 {
		return total, nil
	}
	m.mu.RLock()
	hasProvider := len(m.providers) > 0
	m.mu.RUnlock()
	if !hasProvider {
		m.ensureProviders()
	}
	var firstErr error
	for i := range rules {
		r := rules[i]
		if !r.Enabled {
			if err := m.removeRule(ctx, r); err != nil && firstErr == nil {
				firstErr = err
			}
			total.Removed++
			total.Changed++
			continue
		}
		res := m.ensureRule(ctx, &r)
		switch {
		case res.err != nil:
			total.Failed++
			if firstErr == nil {
				firstErr = res.err
			}
		case res.created:
			total.Created++
		case res.renewed:
			total.Renewed++
		default:
			total.OK++
		}
		total.Changed++
	}
	if err := m.store.ReconcilePortStatuses(); err != nil {
		logging.Debug("mapper", "port status reconcile failed", "error", err.Error())
	}
	if m.ev != nil {
		m.ev.Changed()
	}
	return total, firstErr
}

// ExternalIP asks the preferred gateway (or the first that answers) for the WAN
// address.
func (m *Mapper) ExternalIP(ctx context.Context) (string, error) {
	var lastErr error
	for _, d := range m.candidates("") {
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		ip, err := d.Provider.ExternalIP(cctx)
		cancel()
		if err == nil && ip != "" {
			return ip, nil
		}
		if err == nil {
			err = errors.New("gateway reported an empty external address")
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = proto.ErrNoGateway
	}
	return "", lastErr
}

// ListGatewayMappings enumerates mappings known to the gateways (IGD only).
func (m *Mapper) ListGatewayMappings(ctx context.Context) ([]proto.Mapping, error) {
	out := []proto.Mapping{}
	var lastErr error
	for _, d := range m.candidates("") {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		list, err := d.Provider.ListMappings(cctx)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		out = append(out, list...)
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

// Run starts the background discovery/reconcile loop until ctx is canceled.
func (m *Mapper) Run(ctx context.Context) {
	if m.cfg.Mapper.DryRun {
		logging.Info("mapper", "mapper is in dry-run mode; no gateway changes will be made")
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		discoverEvery := time.Duration(m.cfg.Mapper.DiscoverInterval) * time.Second
		if discoverEvery < 30*time.Second {
			discoverEvery = 30 * time.Second
		}
		reconcileEvery := time.Duration(m.cfg.Mapper.ReconcileEvery) * time.Second
		if reconcileEvery < 15*time.Second {
			reconcileEvery = 15 * time.Second
		}

		firstRun := true
		dctx, dcancel := context.WithTimeout(ctx, 12*time.Second)
		if _, err := m.Discover(dctx); err != nil {
			logging.Warn("mapper", "initial gateway discovery failed", "error", err.Error())
		}
		dcancel()
		if _, err := m.Reconcile(ctx); err != nil {
			logging.Warn("mapper", "initial reconcile failed", "error", err.Error())
		}
		firstRun = false
		_ = firstRun

		reconcileT := time.NewTicker(reconcileEvery)
		discoverT := time.NewTicker(discoverEvery)
		defer reconcileT.Stop()
		defer discoverT.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stop:
				return
			case <-discoverT.C:
				dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				if _, err := m.Discover(dctx); err != nil {
					logging.Debug("mapper", "periodic discovery failed", "error", err.Error())
				}
				cancel()
			case <-reconcileT.C:
				if !m.cfg.Mapper.Enabled {
					continue
				}
				sum, err := m.Reconcile(ctx)
				if err != nil && !errors.Is(err, context.Canceled) {
					logging.Warn("mapper", "reconcile failed", "error", err.Error())
				} else if sum.Changed > 0 {
					logging.Info("mapper", "reconcile pass", "summary", sum.String())
				}
			}
		}
	}()
}

// Stop halts the background loop.
func (m *Mapper) Stop() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
}

// Shutdown releases providers.
func (m *Mapper) Shutdown() {
	m.Stop()
	m.wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.providers {
		_ = d.Provider.Close()
	}
	m.providers = nil
}
