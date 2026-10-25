package proto

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ekko-studio/natpilot/internal/logging"
	"github.com/ekko-studio/natpilot/internal/netutil"
)

// DiscoverOptions tunes gateway discovery and probing.
type DiscoverOptions struct {
	Protocols []string      // preference order, newest first
	Timeout   time.Duration // SSDP window (default 4s)
	// CandidateTimeout bounds a single PCP/NAT-PMP probe (default 900ms).
	// Probes run in parallel, so this also bounds the whole UDP probe phase.
	CandidateTimeout time.Duration
	UDPSources       []string // candidate PCP/NAT-PMP servers (defaults to CandidateServers)
	SSDPTargets      []string
	LocalIP          string   // override for the source address of UDP probes
	SSDPSources      []net.IP // interfaces to send M-SEARCH from (default: all LAN IPv4)
}

// DiscoveredGateway is a gateway plus a live provider handle.
type DiscoveredGateway struct {
	Info     GatewayInfo
	Provider Provider
}

// maxCandidates bounds the number of PCP/NAT-PMP probes. Each probe is cheap
// (a single UDP round trip) but they must not multiply on a machine with many
// virtual adapters.
const maxCandidates = 8

// defaultProbeTimeout is the per-candidate budget for a discovery probe.
const defaultProbeTimeout = 900 * time.Millisecond

// Discover finds every usable NAT gateway/control point on the network and
// returns them ordered by protocol preference (IGD v2, IGD v1, PCP, NAT-PMP).
//
// The SSDP search and every UDP probe run concurrently. Probing candidates
// sequentially used to make discovery take one full timeout per dead address,
// which on a desktop with VPN/VMware/ZeroTier adapters meant minutes before the
// first reconcile pass could start.
func Discover(ctx context.Context, opts DiscoverOptions) ([]DiscoveredGateway, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 4 * time.Second
	}
	if opts.CandidateTimeout <= 0 {
		opts.CandidateTimeout = defaultProbeTimeout
	}
	if len(opts.Protocols) == 0 {
		opts.Protocols = []string{KindIGD2, KindIGD1, KindPCP, KindNATPMP}
	}
	want := map[string]bool{}
	for _, p := range opts.Protocols {
		want[strings.ToLower(p)] = true
	}

	var (
		mu  sync.Mutex
		out []DiscoveredGateway
		wg  sync.WaitGroup
	)
	add := func(g DiscoveredGateway) {
		mu.Lock()
		out = append(out, g)
		mu.Unlock()
	}

	// 1. UPnP / IGD discovery through SSDP (one M-SEARCH per interface).
	if want[KindIGD2] || want[KindIGD1] {
		wg.Add(1)
		go func() {
			defer wg.Done()
			devices, err := SSDPDiscover(ctx, opts.SSDPTargets, opts.SSDPSources, opts.Timeout)
			if err != nil && err != context.Canceled {
				logging.Debug("gateway", "ssdp discovery failed", "error", err.Error())
			}
			seenHost := map[string]bool{}
			for _, d := range devices {
				if ctx.Err() != nil {
					return
				}
				if d.Location == "" || seenHost[d.Address] {
					continue
				}
				cfg, desc, derr := DescribeGateway(ctx, d.Location, opts.Timeout)
				if derr != nil {
					logging.Debug("gateway", "description failed", "location", d.Location, "error", derr.Error())
					continue
				}
				if cfg == nil {
					continue
				}
				if cfg.Name == "" && desc != nil {
					cfg.Name = desc.Device.FriendlyName
				}
				if !want[cfg.Kind] {
					// Still report the gateway, but only if no stricter preference.
					if cfg.Kind == KindIGD2 && !want[KindIGD2] {
						continue
					}
					if cfg.Kind == KindIGD1 && !want[KindIGD1] {
						continue
					}
				}
				seenHost[d.Address] = true
				client := NewIGDClient(*cfg)
				add(DiscoveredGateway{Info: client.Info(), Provider: client})
			}
		}()
	}

	// 2. PCP / NAT-PMP discovery by direct probe. All candidates are probed in
	// parallel, and each probe is sourced from the address that actually routes
	// to that candidate (not from a VPN address).
	if want[KindPCP] || want[KindNATPMP] {
		candidates := opts.UDPSources
		if len(candidates) == 0 {
			candidates = CandidateServers()
		}
		if len(candidates) > maxCandidates {
			candidates = candidates[:maxCandidates]
		}
		sem := make(chan struct{}, 8)
		for _, candidate := range candidates {
			cand := candidate
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if ctx.Err() != nil {
					return
				}
				local := netutil.LocalIPv4For(cand)
				if local == "" {
					local = opts.LocalIP
				}
				probeCtx, cancel := context.WithTimeout(ctx, opts.CandidateTimeout)
				defer cancel()

				if want[KindPCP] {
					pc, err := NewPCPClient(cand, local, opts.CandidateTimeout)
					if err == nil {
						if aerr := pc.Announce(probeCtx); aerr == nil {
							info := pc.Info()
							if local != "" {
								info.Interfaces = []string{local}
							}
							add(DiscoveredGateway{Info: info, Provider: pc})
						} else {
							logging.Debug("gateway", "pcp announce failed", "server", cand, "error", aerr.Error())
							_ = pc.Close()
						}
					}
				}
				if want[KindNATPMP] && probeCtx.Err() == nil {
					nc := NewNATPMPClient(cand, local, opts.CandidateTimeout)
					if _, err := nc.ExternalIP(probeCtx); err == nil {
						add(DiscoveredGateway{Info: nc.Info(), Provider: nc})
					}
				}
			}()
		}
	}

	wg.Wait()

	// Order: protocol preference, then address.
	rank := map[string]int{}
	for i, p := range opts.Protocols {
		rank[strings.ToLower(p)] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, oki := rank[strings.ToLower(out[i].Info.Kind)]
		rj, okj := rank[strings.ToLower(out[j].Info.Kind)]
		if !oki {
			ri = 100
		}
		if !okj {
			rj = 100
		}
		if ri != rj {
			return ri < rj
		}
		return out[i].Info.Address < out[j].Info.Address
	})
	for i := range out {
		out[i].Info.DiscoveredAt = time.Now()
	}
	if ctx.Err() != nil && len(out) == 0 {
		return out, ctx.Err()
	}
	return out, nil
}

// ProbeNATPMPServers scans candidate addresses for NAT-PMP responders.
func ProbeNATPMPServers(ctx context.Context, candidates []string, timeout time.Duration) []string {
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	found := []string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, c := range candidates {
		cand := c
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			client := NewNATPMPClient(cand, netutil.LocalIPv4For(cand), timeout)
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			if _, err := client.ExternalIP(cctx); err == nil {
				mu.Lock()
				found = append(found, cand)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	sort.Strings(found)
	return found
}

// CandidateServers returns plausible PCP/NAT-PMP server addresses, best first:
//
//  1. the default-route next hop
//  2. the gateway of every interface that has one
//  3. the .1/.254 addresses of each private LAN subnet
//
// Link-local (169.254/16) adapters and the RFC2544 benchmark range used by TUN
// clients (198.18.0.0/15, which is what Clash/mihomo and friends allocate) are
// skipped: no NAT device ever lives there, and every extra candidate used to
// cost a full timeout.
func CandidateServers() []string {
	out := []string{}
	if gw := netutil.RouteGateway(); gw != "" {
		out = append(out, gw)
	}
	ifaces, err := netutil.Interfaces()
	if err != nil {
		return dedupe(out)
	}
	for _, i := range ifaces {
		if i.Loopback || !i.Up || i.Gateway == "" {
			continue
		}
		if usableCandidate(i.Gateway) {
			out = append(out, i.Gateway)
		}
	}
	for _, i := range ifaces {
		if i.Loopback || !i.Up || i.Network == "" {
			continue
		}
		ip, ipnet, cerr := net.ParseCIDR(i.Network)
		if cerr != nil {
			continue
		}
		ones, _ := ipnet.Mask.Size()
		if ones < 22 {
			// A /8 or /16 subnet gives no useful hint about where the router is.
			continue
		}
		base := ip.Mask(ipnet.Mask).To4()
		if base == nil {
			continue
		}
		for _, last := range []byte{1, 254} {
			s := net.IPv4(base[0], base[1], base[2], last).String()
			if s == i.IP || !usableCandidate(s) {
				continue
			}
			out = append(out, s)
		}
	}
	return dedupe(out)
}

// usableCandidate filters out addresses no NAT device can occupy.
func usableCandidate(s string) bool {
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil || ip.To4() == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
		return false
	}
	// 198.18.0.0/15: RFC2544 benchmarking range, used as the TUN address pool by
	// Clash/mihomo, Surge and similar proxies. It is a client tunnel, not a LAN.
	if ip.To4()[0] == 198 && (ip.To4()[1] == 18 || ip.To4()[1] == 19) {
		return false
	}
	return true
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// String renders a discovered gateway.
func (g DiscoveredGateway) String() string {
	return fmt.Sprintf("%s[%s]@%s", g.Info.Kind, g.Info.Name, g.Info.Address)
}
