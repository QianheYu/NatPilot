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
	// routePinhole is an IGDv2 WANIPv6FirewallControl pinhole (IPv6, no NAT66).
	routePinhole
	// routePCPv6 is a PCP MAP for an IPv6 internal address. It is the only
	// mechanism that translates a port on a NAT66 network, where a pinhole on
	// the internal address can never be reached.
	routePCPv6
	// routeUnsupportedIPv6 is an IPv6 target with no usable gateway mechanism.
	routeUnsupportedIPv6
)

func (r ruleRoute) String() string {
	switch r {
	case routePinhole:
		return "ipv6-pinhole"
	case routePCPv6:
		return "ipv6-pcp"
	case routeUnsupportedIPv6:
		return "ipv6-unsupported"
	default:
		return "ipv4-mapping"
	}
}

// routeInput is the pure input of the routing decision: the rule target plus
// the capabilities of the discovered gateways and the NAT66 verdict.
type routeInput struct {
	Host string
	// PinholeCapable: a gateway advertises WANIPv6FirewallControl:1 and its
	// provider implements AddPinhole.
	PinholeCapable bool
	// PCPCapable: a gateway speaks PCP (and therefore can carry a MAP).
	PCPCapable bool
	// PCPIPv6: a PCP gateway is reachable over IPv6 / advertises CapPCPIPv6,
	// which makes it usable for IPv6 even when NAT66 was not confirmed.
	PCPIPv6 bool
	// NAT66: the probe detected IPv6 address translation.
	NAT66 bool
}

// decideRoute is the pure routing decision for one rule (no network, no config).
//
// A rule whose host is an IPv6 literal cannot be expressed as an IPv4 NAT
// mapping. The order is:
//
//  1. a PCP MAP when PCP is available and the network NATs IPv6 (or the gateway
//     advertises a v6-capable PCP): this is a real port mapping, the only thing
//     that works behind NAT66;
//  2. otherwise an IGDv2 firewall pinhole (no port translation, native IPv6);
//  3. otherwise routeUnsupportedIPv6, which the mapper turns into an actionable
//     error naming the missing capability.
//
// Loopback/wildcard/"this host" specs are the exception: they mean "this
// machine", which effectiveHost resolves to this machine's LAN IPv4 address, so
// the historical mapping behaviour is kept (no router can forward a pinhole to
// ::1).
func decideRoute(in routeInput) ruleRoute {
	if isLocalHostSpec(in.Host) || !isIPv6Literal(in.Host) {
		return routeMapping
	}
	if in.PCPCapable && (in.NAT66 || in.PCPIPv6) {
		return routePCPv6
	}
	if in.PinholeCapable {
		return routePinhole
	}
	return routeUnsupportedIPv6
}

// isIPv6RuleTarget reports whether a rule targets a concrete IPv6 address (not
// "this host"), i.e. whether it needs an IPv6-specific mechanism.
func isIPv6RuleTarget(host string) bool {
	return !isLocalHostSpec(host) && isIPv6Literal(host)
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

// routeFor resolves the mechanism for one rule and, when it can change the
// decision, the (cached) NAT66 verdict.
//
// The NAT66 probe is skipped for pinhole-only networks: the route is a pinhole
// either way, so there is no reason to contact the echo services. It is also
// skipped for IPv4 rules, which keeps a v4-only deployment's behaviour and
// traffic exactly as before.
func (m *Mapper) routeFor(ctx context.Context, r *model.Rule) (ruleRoute, proto.NAT66State) {
	if !isIPv6RuleTarget(r.Host) {
		return routeMapping, proto.NAT66State{}
	}
	pcp := m.hasPCPProvider()
	pinhole := m.hasPinholeProvider()
	in := routeInput{
		Host:           r.Host,
		PinholeCapable: pinhole,
		PCPCapable:     pcp,
		PCPIPv6:        m.hasPCPv6Provider(),
	}
	var nat proto.NAT66State
	if pcp || !pinhole {
		nat = m.nat66State(ctx)
		in.NAT66 = nat.Available && nat.Nat66
	}
	return decideRoute(in), nat
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
// the operator can do instead. It distinguishes the two failures an IPv6 rule
// can hit, because the remedy is different:
//
//   - the network NATs IPv6 (NAT66) but the gateway has no PCP IPv6 mapping:
//     only PCP can create a usable port mapping; a pinhole cannot cross the
//     translation.
//   - the network does not NAT IPv6 but the gateway has no
//     WANIPv6FirewallControl service: a pinhole is what is needed.
//
// The detected external/local addresses are appended when the probe produced
// them, so the message is actionable without opening the logs.
func (m *Mapper) ipv6UnsupportedError(r *model.Rule, target string, nat proto.NAT66State) error {
	var msg string
	if nat.Available && nat.Nat66 {
		msg = fmt.Sprintf("IPv6 规则 #%d（%s/%d）所在网络使用 NAT66，需要网关支持 PCP IPv6 映射%s；"+
			"当前网关 %s 未提供可用的 PCP 服务。NAT66 会改写 IPv6 源地址，IPv6FirewallControl pinhole 只能放行内网地址、无法被外网访问。"+
			"请换用支持 PCP 的网关，或在路由器管理界面手动添加 IPv6 端口映射（外部端口 → %s:%d）",
			r.ID, target, r.Port, nat66Detail(nat), m.gatewayLabel(), target, r.Port)
	} else {
		msg = fmt.Sprintf("IPv6 规则 #%d（%s/%d）需要网关支持 IPv6FirewallControl（pinhole）才能开启入站 pinhole；"+
			"当前网关 %s 未提供该服务。请换用支持 IPv6FirewallControl 的网关，"+
			"或在路由器防火墙中手动添加 IPv6 入站 pinhole（若路由器不支持 pinhole，只能在主机防火墙上放行）",
			r.ID, target, r.Port, m.gatewayLabel())
		if !nat.Available && nat.Err != "" {
			msg += fmt.Sprintf("；NAT66 探测未得出结论：%s", nat.Err)
		}
	}
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
	// LeaseTime is a uint32 on the wire. Do the capping in int64: 1<<31 does not
	// fit an `int` on 32-bit platforms (linux/arm, 386), where the untyped
	// constant would overflow at compile time — exactly what broke the
	// multi-arch image build.
	const maxPinholeLease = int64(1) << 31
	lease := int64(m.effectiveLease(r))
	if lease <= 0 {
		lease = 86400
	}
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
		return m.failPinholeRule(r, target, m.ipv6UnsupportedError(r, target, m.nat66State(ctx)))
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
	return m.failIPv6Rule(r, target, err)
}

// failIPv6Rule records a failed IPv6 provisioning attempt.
//
// It keeps the mechanism marker of a previously created mapping: deleting the
// record on a failed renewal would otherwise orphan the live pinhole/PCP
// mapping until its lease runs out, because nothing would remember how to tear
// it down.
func (m *Mapper) failIPv6Rule(r *model.Rule, target string, err error) ruleResult {
	desc := buildDescription(r)
	if prev, ok := m.record(r.ID); ok {
		if id, has := pinholeIDFromDescription(prev.Description); has && id > 0 {
			desc = pinholeDescription(desc, id)
		}
		if ext, has := pcpExtPortFromDescription(prev.Description); has && ext > 0 {
			desc = pcpDescription(desc, ext)
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
		m.ev.Publish("mapping", fmt.Sprintf("IPv6 mapping failed for rule #%d (%s/%d): %s",
			r.ID, m.ruleProtocol(r), r.Port, err.Error()),
			model.JSONMap{"rule_id": r.ID, "port": r.Port, "error": err.Error(), "ipv6": true})
	}
	return ruleResult{err: err, changed: true}
}
