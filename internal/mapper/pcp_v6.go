package mapper

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ekko-studio/natpilot/internal/logging"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/proto"
)

// ---- IPv6 PCP MAP ----
//
// On a NAT66 network a WANIPv6FirewallControl pinhole is useless: the gateway
// translates the internal prefix, so no pinhole on the internal address can be
// reached from the WAN. PCP MAP is the one mechanism that still gives a real
// external port mapping, and the mapping record has to remember that this is
// how the rule was provisioned, so renewal and deletion use the same path.
//
// The record has no field for the mechanism (model.MappingRecord is outside
// this change's write scope), so — exactly like the pinhole UniqueID — it rides
// in the persisted Description as a parseable marker.

// pcpMarker starts the description marker that records the external port of an
// IPv6 PCP mapping, e.g. "web [ipv6-pcp:9090]".
const pcpMarker = "[ipv6-pcp:"

// pcpDescription appends the PCP mechanism marker to a human readable
// description, keeping the base text so the mapping stays identifiable on the
// gateway.
func pcpDescription(desc string, extPort int) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		desc = "natpilot"
	}
	return fmt.Sprintf("%s %s%d]", desc, pcpMarker, extPort)
}

// pcpExtPortFromDescription extracts the external port recorded by
// pcpDescription. ok is false when the marker is absent or malformed.
func pcpExtPortFromDescription(desc string) (int, bool) {
	i := strings.LastIndex(desc, pcpMarker)
	if i < 0 {
		return 0, false
	}
	rest := desc[i+len(pcpMarker):]
	j := strings.IndexByte(rest, ']')
	if j < 0 {
		return 0, false
	}
	port, err := strconv.ParseUint(strings.TrimSpace(rest[:j]), 10, 16)
	if err != nil || port == 0 {
		return 0, false
	}
	return int(port), true
}

// pcpExtPortFromRecord reports the external port of a PCP-provisioned mapping.
func pcpExtPortFromRecord(rec model.MappingRecord) (int, bool) {
	return pcpExtPortFromDescription(rec.Description)
}

// ipv6Mechanism is the transport an existing IPv6 record was provisioned with.
type ipv6Mechanism int

const (
	// mechanismPinhole is the historical E.2 IGDv2 firewall pinhole.
	mechanismPinhole ipv6Mechanism = iota
	// mechanismPCP is a PCP IPv6 port mapping (NAT66 networks).
	mechanismPCP
)

// recordMechanism decides how an existing IPv6 mapping must be torn down. The
// description marker wins; for records written before the PCP marker existed,
// the provider kind tells PCP apart from an IGD. A record without any hint is
// treated as a pinhole, which is the only mechanism older releases used.
func recordMechanism(rec model.MappingRecord) ipv6Mechanism {
	if _, ok := pcpExtPortFromRecord(rec); ok {
		return mechanismPCP
	}
	if _, ok := pinholeIDFromDescription(rec.Description); ok {
		return mechanismPinhole
	}
	if strings.HasPrefix(strings.TrimSpace(rec.ProviderName), proto.KindPCP+"|") {
		return mechanismPCP
	}
	return mechanismPinhole
}

// supportsPCP reports whether a discovered gateway is a PCP provider that can
// carry a MAP request.
func supportsPCP(d proto.DiscoveredGateway) bool {
	if !strings.EqualFold(strings.TrimSpace(d.Info.Kind), proto.KindPCP) || d.Provider == nil {
		return false
	}
	_, ok := d.Provider.(proto.Provider)
	return ok
}

// hasPCPProvider reports whether any discovered gateway speaks PCP.
func (m *Mapper) hasPCPProvider() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.providers {
		if supportsPCP(m.providers[i]) {
			return true
		}
	}
	return false
}

// hasPCPv6Provider reports whether any PCP provider advertises an IPv6 MAP
// (PCP reachable over IPv6). Such a gateway can be used for an IPv6 rule even
// when the NAT66 probe is inconclusive.
func (m *Mapper) hasPCPv6Provider() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.providers {
		if supportsPCP(m.providers[i]) && m.providers[i].Info.SupportsPCPIPv6() {
			return true
		}
	}
	return false
}

// pcpCandidates returns the PCP gateways that can carry the protocol, preferred
// first and then by capability rank. A non-empty pinned key restricts the list
// to that gateway, matching the strict pinning of the IPv4 path.
func (m *Mapper) pcpCandidates(pinned, protoName string) []proto.DiscoveredGateway {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []proto.DiscoveredGateway{}
	for _, d := range m.providers {
		if !supportsPCP(d) || !supports(d, protoName) {
			continue
		}
		if pinned != "" && providerKey(d.Info) != pinned {
			continue
		}
		out = append(out, d)
	}
	preferred := m.preferred
	sort.SliceStable(out, func(i, j int) bool {
		pi := providerKey(out[i].Info) == preferred
		pj := providerKey(out[j].Info) == preferred
		if pi != pj {
			return pi
		}
		return protocolRank(out[i].Info.Kind) < protocolRank(out[j].Info.Kind)
	})
	return out
}

// ensurePCPv6 creates (or renews) an IPv6 port mapping through PCP MAP. This is
// the NAT66 path: the external port really is translated, unlike a pinhole.
func (m *Mapper) ensurePCPv6(ctx context.Context, r *model.Rule, nat proto.NAT66State) ruleResult {
	res := ruleResult{}
	target, ok := canonicalIPv6(r.Host)
	if !ok {
		err := fmt.Errorf("IPv6 规则 #%d 的目标 %q 不是合法的 IPv6 地址", r.ID, r.Host)
		return m.failPCPv6Rule(r, target, err)
	}
	if r.Port <= 0 || r.Port > 65535 {
		err := fmt.Errorf("IPv6 规则 #%d 的内网端口 %d 非法（需要 1-65535）", r.ID, r.Port)
		return m.failPCPv6Rule(r, target, err)
	}
	protocol := m.ruleProtocol(r)
	if protocol != proto.TCP && protocol != proto.UDP {
		err := fmt.Errorf("PCP IPv6 映射只支持 TCP/UDP，规则 #%d 的协议 %q 不受支持", r.ID, r.Protocol)
		return m.failPCPv6Rule(r, target, err)
	}
	// See ensurePinhole: cap in int64 so 1<<31 does not overflow a 32-bit int.
	const maxLease = int64(1) << 31
	lease := int64(m.effectiveLease(r))
	if lease <= 0 {
		lease = 86400
	}
	if lease > maxLease {
		lease = maxLease
	}

	prev, hadPrev := m.record(r.ID)
	if hadPrev && prev.Status == model.MapStatusActive {
		if ext, hasPCP := pcpExtPortFromRecord(prev); hasPCP && ext > 0 && pcpRecordStillValid(m, prev, r, target) {
			return res
		}
		if _, hasPCP := pcpExtPortFromRecord(prev); hasPCP {
			res.renewed = true
		}
	}

	cands := m.pcpCandidates(strings.TrimSpace(r.Provider), protocol)
	if len(cands) == 0 {
		// The route only sends a rule here when PCP was available; if discovery
		// changed under us, fall back to a pinhole when one exists.
		if m.hasPinholeProvider() {
			logging.Debug("mapper", "no PCP gateway for IPv6 rule, falling back to pinhole", "rule", r.ID)
			return m.ensurePinhole(ctx, r)
		}
		return m.failPCPv6Rule(r, target, m.ipv6UnsupportedError(r, target, nat))
	}

	mapping := proto.Mapping{
		Protocol:     protocol,
		InternalHost: target,
		InternalPort: r.Port,
		ExternalPort: m.effectiveExtPort(r),
		LeaseSeconds: int(lease),
		Description:  buildDescription(r),
		Enabled:      true,
	}

	var lastErr error
	for _, d := range cands {
		cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		result, err := d.Provider.AddMapping(cctx, mapping)
		cancel()
		if err != nil {
			lastErr = err
			logging.Debug("mapper", "ipv6 pcp mapping attempt failed",
				"gateway", d.Info.Kind, "address", d.Info.Address, "error", err.Error())
			continue
		}
		extPort := result.ExternalPort
		if extPort <= 0 {
			extPort = mapping.ExternalPort
		}
		if extPort <= 0 {
			extPort = r.Port
		}
		expires := model.NullTime{}
		if result.LeaseSeconds > 0 {
			expires = model.Ptr(result.ExpiresAt())
		}
		internalHost := result.InternalHost
		if h, ok := canonicalIPv6(internalHost); ok {
			internalHost = h
		} else {
			internalHost = target
		}
		m.persistRecord(r, model.MappingRecord{
			RuleID:       r.ID,
			ProviderName: providerKey(d.Info),
			Protocol:     result.Protocol,
			Status:       model.MapStatusActive,
			ExternalIP:   result.ExternalIP,
			ExternalPort: extPort,
			InternalHost: internalHost,
			InternalPort: result.InternalPort,
			LeaseSeconds: result.LeaseSeconds,
			Description:  pcpDescription(buildDescription(r), extPort),
			ExpiresAt:    expires,
		})
		res.created = !hadPrev
		res.changed = true
		return res
	}

	if lastErr == nil {
		lastErr = proto.ErrNoGateway
	}
	err := fmt.Errorf("IPv6 规则 #%d（%s/%d）的 PCP IPv6 端口映射失败：%w%s", r.ID, target, r.Port, lastErr, nat66Hint(nat))
	return m.failPCPv6Rule(r, target, err)
}

// nat66Hint explains that a failing PCP mapping is the only usable mechanism on
// a NAT66 network.
func nat66Hint(state proto.NAT66State) string {
	if !state.Available || !state.Nat66 {
		return ""
	}
	return "；该网络使用 NAT66，需要网关支持 PCP IPv6 映射（IPv6FirewallControl pinhole 无法穿透 NAT66）" + nat66Detail(state)
}

// pcpRecordStillValid reports whether an active PCP record can be reused
// without contacting the gateway: same target/port, external port known and the
// lease not close to expiry.
func pcpRecordStillValid(m *Mapper, prev model.MappingRecord, r *model.Rule, target string) bool {
	if prev.ExternalPort <= 0 {
		return false
	}
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

// removePCPv6 deletes the PCP mapping of a rule. Like the pinhole path, a
// gateway that no longer offers PCP must not fail the cleanup: the local record
// is removed and the mapping lease expires on its own.
func (m *Mapper) removePCPv6(ctx context.Context, r model.Rule, rec model.MappingRecord, target string) error {
	extPort, ok := pcpExtPortFromRecord(rec)
	if !ok || extPort <= 0 {
		extPort = rec.ExternalPort
	}
	if extPort <= 0 {
		extPort = m.effectiveExtPort(&r)
	}
	internalPort := rec.InternalPort
	if internalPort <= 0 {
		internalPort = r.Port
	}
	protocol := proto.NormalizeProto(rec.Protocol)
	if protocol != proto.TCP && protocol != proto.UDP {
		protocol = m.ruleProtocol(&r)
	}

	cands := m.pcpCandidates(strings.TrimSpace(r.Provider), protocol)
	if len(cands) == 0 {
		logging.Warn("mapper", "ipv6 pcp removal skipped: no PCP gateway available",
			"rule", r.ID, "target", target, "external_port", extPort)
		return nil
	}
	mapping := proto.Mapping{
		Protocol:     protocol,
		InternalHost: target,
		InternalPort: internalPort,
		ExternalPort: extPort,
	}
	var lastErr error
	for _, d := range cands {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := d.Provider.DeleteMapping(cctx, mapping)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		logging.Debug("mapper", "ipv6 pcp delete failed",
			"gateway", d.Info.Kind, "address", d.Info.Address, "error", err.Error())
	}
	return lastErr
}

// failPCPv6Rule records a failed PCP IPv6 attempt and reports it.
func (m *Mapper) failPCPv6Rule(r *model.Rule, target string, err error) ruleResult {
	return m.failIPv6Rule(r, target, err)
}
