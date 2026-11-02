package mapper

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/ekko-studio/natpilot/internal/config"
	"github.com/ekko-studio/natpilot/internal/logging"
	"github.com/ekko-studio/natpilot/internal/proto"
)

// nat66CacheTTL bounds how often the NAT66 probe is repeated. The probe costs
// several HTTP round trips, so reconcile must not pay it for every IPv6 rule.
// A definitive answer (NAT66 or not) is cached for the full TTL; a failed probe
// is retried sooner, because a transient network error must not hide a NAT66
// network for five minutes.
const (
	nat66CacheTTL     = 5 * time.Minute
	nat66ErrorCacheTTL = time.Minute
)

// nat66Settings mirrors the two documented mapper configuration keys:
//
//	mapper.external_ipv6_check      bool     (default true)
//	mapper.external_ipv6_endpoints  []string (default proto.DefaultExternalIPv6Endpoints)
//
// internal/config owns the Mapper section and is outside this change's write
// scope, so the settings are resolved through the section's JSON form: as soon
// as internal/config declares the two fields (with those json tags) they take
// effect without touching this code, and until then the documented defaults
// apply. Mapper.SetNAT66Probe / the defaults keep the probe fully injectable.
type nat66Settings struct {
	Check     *bool    `json:"external_ipv6_check"`
	Endpoints []string `json:"external_ipv6_endpoints"`
}

// nat66SettingsFrom resolves the effective settings for a Mapper config section.
func nat66SettingsFrom(cfg config.Mapper) nat66Settings {
	s := nat66Settings{}
	if b, err := json.Marshal(cfg); err == nil {
		// A decode error just means the section has none of the keys; the
		// defaults below stay in place.
		_ = json.Unmarshal(b, &s)
	}
	if s.Check == nil {
		enabled := true
		s.Check = &enabled
	}
	if len(s.Endpoints) == 0 {
		s.Endpoints = proto.DefaultExternalIPv6Endpoints()
	}
	return s
}

func (s nat66Settings) enabled() bool { return s.Check == nil || *s.Check }

// SetNAT66Probe overrides the NAT66 prober (nil restores the built-in HTTP
// probe). It exists so tests and embedders can decide NAT66 from their own
// source without network access, and so the result is injectable per deploy.
func (m *Mapper) SetNAT66Probe(fn func(context.Context) proto.NAT66State) {
	m.mu.Lock()
	m.nat66Probe = fn
	// Drop the cache: a new prober must not inherit the previous verdict.
	m.nat66At = time.Time{}
	m.nat66Cached = proto.NAT66State{}
	m.mu.Unlock()
}

// NAT66 returns the (cached) NAT66 verdict for the current network. Callers
// such as the API or UI can show it without triggering a per-rule probe.
func (m *Mapper) NAT66(ctx context.Context) proto.NAT66State {
	return m.nat66State(ctx)
}

// nat66State returns the cached verdict, probing when the cache is cold.
//
// Only IPv6 rules call this (see Mapper.routeFor), so a v4-only network keeps
// the historical behaviour and never contacts the IPv6 echo services.
func (m *Mapper) nat66State(ctx context.Context) proto.NAT66State {
	m.mu.RLock()
	cached := m.nat66Cached
	at := m.nat66At
	probe := m.nat66Probe
	m.mu.RUnlock()

	if !at.IsZero() {
		ttl := nat66CacheTTL
		if !cached.Available {
			ttl = nat66ErrorCacheTTL
		}
		if time.Since(at) < ttl {
			return cached
		}
	}

	if probe == nil {
		probe = m.probeNAT66
	}
	state := probe(ctx)
	state.Gateway = strings.TrimSpace(state.Gateway)
	if state.Gateway == "" {
		state.Gateway = m.gatewayLabel()
	}

	m.mu.Lock()
	m.nat66Cached = state
	m.nat66At = time.Now()
	m.mu.Unlock()
	if state.Available {
		logging.Debug("mapper", "nat66 probe",
			"nat66", state.Nat66, "external", state.ExternalIPv6, "local", state.LocalIPv6, "gateway", state.Gateway)
	} else {
		logging.Debug("mapper", "nat66 probe inconclusive", "error", state.Err)
	}
	return state
}

// probeNAT66 is the built-in prober: it prefers an external IPv6 that a gateway
// itself reports (IGDv2 has no such action, so this is opportunistic) and falls
// back to the HTTP echo probe configured for the mapper.
func (m *Mapper) probeNAT66(ctx context.Context) proto.NAT66State {
	settings := nat66SettingsFrom(m.cfg.Mapper)
	if !settings.enabled() {
		return proto.NAT66State{
			Gateway: m.gatewayLabel(),
			Err:     "mapper.external_ipv6_check 已关闭，跳过 IPv6 外部地址探测，无法判断是否使用 NAT66",
		}
	}
	opts := proto.NAT66Options{
		Endpoints: settings.Endpoints,
		Gateway:   m.gatewayLabel(),
	}
	if ext := m.gatewayExternalIPv6(); ext != "" {
		opts.ExternalIPv6 = ext
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return proto.DetectNAT66(cctx, opts)
}

// gatewayExternalIPv6 returns an external IPv6 address reported by a gateway,
// preferring the preferred gateway. Empty when no gateway reports one, which is
// the normal case: IGDv2 has no "get external IPv6" action.
func (m *Mapper) gatewayExternalIPv6() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, d := range m.providers {
		if providerKey(d.Info) == m.preferred && proto.IsNAT66Address(d.Info.ExternalIP) {
			return d.Info.ExternalIP
		}
	}
	for _, d := range m.providers {
		if proto.IsNAT66Address(d.Info.ExternalIP) {
			return d.Info.ExternalIP
		}
	}
	return ""
}

// nat66Detail renders the detected addresses for an operator-facing message.
func nat66Detail(state proto.NAT66State) string {
	if !state.Available || !state.Nat66 {
		return ""
	}
	parts := []string{}
	if state.ExternalIPv6 != "" {
		parts = append(parts, "外部 IPv6 "+state.ExternalIPv6)
	}
	if state.LocalIPv6 != "" {
		parts = append(parts, "本机 IPv6 "+state.LocalIPv6)
	}
	if len(parts) == 0 {
		return ""
	}
	return "（检测到" + strings.Join(parts, "，") + "）"
}
