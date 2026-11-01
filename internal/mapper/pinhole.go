package mapper

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ekko-studio/natpilot/internal/logging"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/proto"
)

// ruleRoute is the transport a rule needs.
type ruleRoute int

const (
	// routeMapping is a NAT mapping (IPv4). This is the historical behaviour and
	// must stay the default for every rule that is not an IPv6 literal.
	routeMapping ruleRoute = iota
	// routePinhole is an IGDv2 WANIPv6FirewallControl pinhole (IPv6).
	routePinhole
	// routeUnsupportedIPv6 is an IPv6 target with no pinhole-capable gateway.
	routeUnsupportedIPv6
)

func (r ruleRoute) String() string {
	switch r {
	case routePinhole:
		return "ipv6-pinhole"
	case routeUnsupportedIPv6:
		return "ipv6-unsupported"
	default:
		return "ipv4-mapping"
	}
}

// decideRoute is the pure routing decision for one rule (no network, no config).
//
// A rule whose host is an IPv6 literal cannot be expressed as an IPv4 NAT
// mapping, so it needs a firewall pinhole. Loopback/wildcard/"this host" specs
// are the exception: they mean "this machine", which effectiveHost resolves to
// this machine's LAN IPv4 address, so the historical mapping behaviour is kept
// (no router can forward a pinhole to ::1).
func decideRoute(host string, pinholeCapable bool) ruleRoute {
	if isLocalHostSpec(host) {
		return routeMapping
	}
	if !isIPv6Literal(host) {
		return routeMapping
	}
	if pinholeCapable {
		return routePinhole
	}
	return routeUnsupportedIPv6
}

// isIPv6Literal reports whether host is an IPv6 literal. IPv4-mapped addresses
// such as ::ffff:192.168.1.5 count as IPv4 (net.IP.To4 is non-nil).
func isIPv6Literal(host string) bool {
	_, ok := canonicalIPv6(host)
	return ok
}

// canonicalIPv6 normalizes an IPv6 literal: no brackets, no zone id, and the
// canonical string form the gateway expects in InternalClient.
func canonicalIPv6(host string) (string, bool) {
	h := strings.TrimSpace(host)
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	if i := strings.IndexByte(h, '%'); i >= 0 {
		h = h[:i] // fe80::1%eth0: the pinhole argument has no zone slot
	}
	ip := net.ParseIP(h)
	if ip == nil || ip.To4() != nil {
		return "", false
	}
	return ip.String(), true
}

// supportsPinhole reports whether a discovered gateway can open IPv6 pinholes:
// the description must advertise WANIPv6FirewallControl:1 and the provider must
// implement the pinhole API.
func supportsPinhole(d proto.DiscoveredGateway) bool {
	if !d.Info.SupportsIPv6Pinhole() {
		return false
	}
	_, ok := d.Provider.(proto.IPv6FirewallProvider)
	return ok
}

func (m *Mapper) hasPinholeProvider() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.providers {
		if supportsPinhole(m.providers[i]) {
			return true
		}
	}
	return false
}

// pinholeCandidate pairs the gateway description with its pinhole backend.
type pinholeCandidate struct {
	info     proto.GatewayInfo
	provider proto.IPv6FirewallProvider
}

// pinholeCandidates returns the pinhole-capable gateways, preferred first and
// then by capability rank. A non-empty pinned key restricts the list to that
// gateway, matching the strict pinning of the IPv4 path.
func (m *Mapper) pinholeCandidates(pinned string) []pinholeCandidate {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]pinholeCandidate, 0, len(m.providers))
	for _, d := range m.providers {
		if !supportsPinhole(d) {
			continue
		}
		if pinned != "" && providerKey(d.Info) != pinned {
			continue
		}
		p, _ := d.Provider.(proto.IPv6FirewallProvider)
		out = append(out, pinholeCandidate{info: d.Info, provider: p})
	}
	preferred := m.preferred
	sort.SliceStable(out, func(i, j int) bool {
		pi := providerKey(out[i].info) == preferred
		pj := providerKey(out[j].info) == preferred
		if pi != pj {
			return pi
		}
		return protocolRank(out[i].info.Kind) < protocolRank(out[j].info.Kind)
	})
	return out
}

// gatewayLabel names the gateway a rule would have used, for error messages.
func (m *Mapper) gatewayLabel() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.providers) == 0 {
		return "（未发现任何网关）"
	}
	pick := -1
	for i := range m.providers {
		if providerKey(m.providers[i].Info) == m.preferred {
			pick = i
			break
		}
	}
	if pick < 0 {
		pick = 0
	}
	info := m.providers[pick].Info
	name := strings.TrimSpace(info.Name)
	if name == "" {
		name = info.Address
	}
	label := name
	if info.Kind != "" {
		label += " (" + info.Kind + ")"
	}
	if info.Address != "" && info.Address != name {
		label += " " + info.Address
	}
	return label
}

// ipv6UnsupportedError explains why an IPv6 rule cannot be provisioned and what
// the operator can do instead. It always names the gateway that was tried.
func (m *Mapper) ipv6UnsupportedError(r *model.Rule, target string) error {
	msg := fmt.Sprintf("IPv6 规则 #%d（%s/%d）需要网关提供 IGDv2 WANIPv6FirewallControl 服务才能开启入站 pinhole；"+
		"当前网关 %s 未提供该服务。请换用支持 IPv6FirewallControl 的网关，"+
		"或在路由器防火墙中手动添加 IPv6 入站 pinhole（若路由器不支持 pinhole，只能在主机防火墙上放行）",
		r.ID, target, r.Port, m.gatewayLabel())
	if pinned := strings.TrimSpace(r.Provider); pinned != "" {
		msg += fmt.Sprintf("；该规则固定到网关 %q，请改为自动选择或选择支持该服务的网关", pinned)
	}
	return fmt.Errorf("%s", msg)
}

// ---- pinhole unique-id persistence ----
//
// model.MappingRecord has no dedicated field for the gateway-assigned pinhole
// id and the write scope for this change excludes internal/model. The id is
// therefore carried as a marker in the record's Description, which is persisted
// in the mappings table, so a restart can still delete the pinhole.
const pinholeMarker = "[ipv6-pinhole:"

func pinholeDescription(desc string, id uint16) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		desc = "natpilot"
	}
	return fmt.Sprintf("%s %s%d]", desc, pinholeMarker, id)
}

func pinholeIDFromRecord(rec model.MappingRecord) (uint16, bool) {
	return pinholeIDFromDescription(rec.Description)
}

func pinholeIDFromDescription(desc string) (uint16, bool) {
	i := strings.LastIndex(desc, pinholeMarker)
	if i < 0 {
		return 0, false
	}
	rest := desc[i+len(pinholeMarker):]
	j := strings.IndexByte(rest, ']')
	if j < 0 {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(rest[:j]), 10, 16)
	if err != nil {
		return 0, false
	}
	return uint16(n), true
}

// pinholeTarget reports the IPv6 target of a rule that must be torn down via
// DeletePinhole. The persisted record wins so a rule whose host changed (or was
// deleted) still tears down the pinhole it actually created.
func pinholeTarget(r model.Rule, rec model.MappingRecord) (string, bool) {
	if h, ok := canonicalIPv6(rec.InternalHost); ok {
		return h, true
	}
	if isLocalHostSpec(r.Host) {
		return "", false
	}
	return canonicalIPv6(r.Host)
}

// ensurePinhole creates (or renews) an IPv6 firewall pinhole for a rule.
func (m *Mapper) ensurePinhole(ctx context.Context, r *model.Rule) ruleResult {
	res := ruleResult{}
	target, ok := canonicalIPv6(r.Host)
	if !ok {
		err := fmt.Errorf("IPv6 规则 #%d 的目标 %q 不是合法的 IPv6 地址", r.ID, r.Host)
		return m.failPinholeRule(r, target, err)
	}
	if r.Port <= 0 || r.Port > 65535 {
		err := fmt.Errorf("IPv6 规则 #%d 的内网端口 %d 非法（需要 1-65535）", r.ID, r.Port)
		return m.failPinholeRule(r, target, err)
	}
	protocol := m.ruleProtocol(r)
	if protocol != proto.TCP && protocol != proto.UDP {
		err := fmt.Errorf("IPv6 pinhole 只支持 TCP/UDP，规则 #%d 的协议 %q 不受支持", r.ID, r.Protocol)
		return m.failPinholeRule(r, target, err)
	}
	lease := m.effectiveLease(r)
	if lease <= 0 {
		lease = 86400
	}
	// LeaseTime is uint32. Cap at ~68 years, which fits an int on every platform
	// and is indistinguishable from "forever" in practice.
	const maxPinholeLease = 1 << 31
	if lease > maxPinholeLease {
		lease = maxPinholeLease
	}

	prev, hadPrev := m.record(r.ID)
	if hadPrev && prev.Status == model.MapStatusActive {
		if id, hasID := pinholeIDFromRecord(prev); hasID && id > 0 && pinholeStillValid(m, prev, r, target) {
			return res
		}
		if _, hasID := pinholeIDFromRecord(prev); hasID {
			res.renewed = true
		}
	}

	cands := m.pinholeCandidates(strings.TrimSpace(r.Provider))
	if len(cands) == 0 {
		return m.failPinholeRule(r, target, m.ipv6UnsupportedError(r, target))
	}

	req := proto.IPv6Pinhole{
		// Any source: the common case for a published service. Gateways that
		// refuse the wildcard answer 708, which the error hint explains.
		RemoteHost:     "",
		RemotePort:     0,
		InternalClient: target,
		InternalPort:   uint16(r.Port),
		Protocol:       protocol,
		LeaseSeconds:   uint32(lease),
	}

	var lastErr error
	for _, cand := range cands {
		cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		id, granted, err := cand.provider.AddPinhole(cctx, req)
		cancel()
		if err != nil {
			lastErr = err
			logging.Debug("mapper", "ipv6 pinhole attempt failed",
				"gateway", cand.info.Kind, "address", cand.info.Address, "error", err.Error())
			continue
		}
		leaseSec := int(granted)
		if leaseSec < 0 {
			leaseSec = 0
		}
		expires := model.NullTime{}
		if leaseSec > 0 {
			expires = model.Ptr(time.Now().Add(time.Duration(leaseSec) * time.Second))
		}
		m.persistRecord(r, model.MappingRecord{
			RuleID:       r.ID,
			ProviderName: providerKey(cand.info),
			Protocol:     protocol,
			Status:       model.MapStatusActive,
			// A pinhole has no external port: the gateway forwards the same
			// address/port on the WAN side.
			ExternalPort: 0,
			InternalHost: target,
			InternalPort: r.Port,
			LeaseSeconds: leaseSec,
			Description:  pinholeDescription(buildDescription(r), id),
			ExpiresAt:    expires,
		})
		res.created = !hadPrev
		res.changed = true
		return res
	}
	if lastErr == nil {
		lastErr = proto.ErrNoGateway
	}
	return m.failPinholeRule(r, target, lastErr)
}

// pinholeStillValid reports whether an active pinhole record can be reused
// without contacting the gateway: same target, lease not close to expiry.
func pinholeStillValid(m *Mapper, prev model.MappingRecord, r *model.Rule, target string) bool {
	ttl := m.cfg.Mapper.RenewBefore
	if ttl <= 0 {
		ttl = 120
	}
	exp := time.Time{}
	if prev.ExpiresAt.Valid {
		exp = prev.ExpiresAt.Time.Time
	}
	if !exp.IsZero() && time.Until(exp) <= time.Duration(ttl)*time.Second {
		return false
	}
	if prev.InternalPort != r.Port {
		return false
	}
	prevHost, ok := canonicalIPv6(prev.InternalHost)
	return ok && prevHost == target
}

// removePinhole closes the pinhole of a rule. It never fails the cleanup when
// the gateway no longer offers the service: the local record is removed either
// way, and the pinhole lease expires on its own.
func (m *Mapper) removePinhole(ctx context.Context, r model.Rule, rec model.MappingRecord, target string) error {
	id, hasID := pinholeIDFromRecord(rec)
	if !hasID || id == 0 {
		logging.Debug("mapper", "ipv6 pinhole removal skipped: no unique id in mapping record",
			"rule", r.ID, "target", target)
		return nil
	}
	cands := m.pinholeCandidates(strings.TrimSpace(r.Provider))
	if len(cands) == 0 {
		logging.Warn("mapper", "ipv6 pinhole removal skipped: no gateway advertises WANIPv6FirewallControl",
			"rule", r.ID, "target", target, "unique_id", id)
		return nil
	}
	var lastErr error
	for _, cand := range cands {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := cand.provider.DeletePinhole(cctx, id)
		cancel()
		if err == nil {
			return nil
		}
		if code, ok := proto.UPnPErrorCode(err); ok {
			switch code {
			case "704":
				// NoSuchEntry: already gone (expired or cleared by the gateway).
				return nil
			case "702", "703":
				// Firewall/pinhole permission changed under us: nothing left to do.
				logging.Debug("mapper", "ipv6 pinhole removal treated as no-op",
					"rule", r.ID, "code", code)
				return nil
			}
		}
		lastErr = err
		logging.Debug("mapper", "ipv6 pinhole delete failed",
			"gateway", cand.info.Kind, "address", cand.info.Address, "error", err.Error())
	}
	return lastErr
}

// failPinholeRule records an IPv6 pinhole failure and reports it.
func (m *Mapper) failPinholeRule(r *model.Rule, target string, err error) ruleResult {
	desc := buildDescription(r)
	// Keep the id of a previously created pinhole: deleting the record on a
	// failed renewal would otherwise orphan the live pinhole until its lease runs
	// out, because nothing would remember which id to delete.
	if prev, ok := m.record(r.ID); ok {
		if id, has := pinholeIDFromRecord(prev); has && id > 0 {
			desc = pinholeDescription(desc, id)
		}
	}
	m.persistRecord(r, model.MappingRecord{
		RuleID:       r.ID,
		Protocol:     m.ruleProtocol(r),
		Status:       model.MapStatusFailed,
		InternalHost: target,
		InternalPort: r.Port,
		Description:  desc,
		Error:        err.Error(),
	})
	if m.ev != nil {
		m.ev.Publish("mapping", fmt.Sprintf("IPv6 pinhole failed for rule #%d (%s/%d): %s",
			r.ID, m.ruleProtocol(r), r.Port, err.Error()),
			model.JSONMap{"rule_id": r.ID, "port": r.Port, "error": err.Error(), "ipv6": true})
	}
	return ruleResult{err: err, changed: true}
}
