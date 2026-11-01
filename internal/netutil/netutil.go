// Package netutil contains OS level helpers: interface enumeration, CIDR math,
// port specification parsing, neighbour discovery and small lookups.
package netutil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Address families reported by Iface.Family and Neighbor.Family.
const (
	FamilyIPv4 = "ipv4"
	FamilyIPv6 = "ipv6"
)

// Iface describes a local network interface address.
type Iface struct {
	Name      string `json:"name"`
	Index     int    `json:"index"`
	IP        string `json:"ip"`
	IPNet     string `json:"ip_net"`  // CIDR of this address
	Network   string `json:"network"` // network address of the subnet
	Mask      string `json:"mask"`
	MAC       string `json:"mac,omitempty"`
	MTU       int    `json:"mtu"`
	Up        bool   `json:"up"`
	Loopback  bool   `json:"loopback"`
	Broadcast string `json:"broadcast,omitempty"`
	Gateway   string `json:"gateway,omitempty"`
	Family    string `json:"family"` // FamilyIPv4 or FamilyIPv6
	// Zone is the interface name to use as the IPv6 scope zone. It is set for
	// link-local addresses only (see Scoped) and empty otherwise. Addresses
	// themselves never carry a "%zone" suffix; the zone lives here.
	Zone string `json:"zone,omitempty"`
	// Scoped is true for fe80::/10 link-local IPv6 addresses, which are only
	// reachable through the interface named by Zone.
	Scoped bool `json:"scoped,omitempty"`
}

// IsV6 reports whether the entry describes an IPv6 address. Entries built by
// hand without Family set are classified by parsing IP.
func (i Iface) IsV6() bool {
	if i.Family != "" {
		return i.Family == FamilyIPv6
	}
	ip := net.ParseIP(strings.TrimSpace(i.IP))
	return ip != nil && ip.To4() == nil && ip.To16() != nil
}

// Interfaces enumerates local interface addresses for both address families,
// one entry per (interface, address). Loopback addresses are included but
// flagged.
//
// Ordering: the IPv4 entries keep exactly the order this function has always
// produced (non-loopback before loopback, then by interface name); the IPv6
// entries follow, with the same rule among themselves. That keeps
// display-order-dependent callers (PickInterface, reachable URLs) seeing the
// IPv4 address of an interface first.
//
// IPv6 specifics: Network is the network CIDR and there is no Broadcast. Zone
// holds the interface name for link-local addresses (Scoped). Gateway is filled
// only when the OS exposes an IPv6 default route that can be attributed to the
// interface; link-local gateways are stored without a "%zone" suffix because
// Zone already names the scope.
func Interfaces() ([]Iface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	gw := RouteGateway()
	out := []Iface{}
	hasV6 := false
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil {
				ip6 := ipnet.IP.To16()
				if ip6 == nil {
					continue
				}
				out = append(out, ifaceV6(ifc, ipnet, ip6))
				hasV6 = true
				continue
			}
			ones, bits := ipnet.Mask.Size()
			e := Iface{
				Name:     ifc.Name,
				Index:    ifc.Index,
				IP:       ip4.String(),
				IPNet:    fmt.Sprintf("%s/%d", ip4.String(), ones),
				Mask:     ipnet.Mask.String(),
				MAC:      ifc.HardwareAddr.String(),
				MTU:      ifc.MTU,
				Up:       ifc.Flags&net.FlagUp != 0,
				Loopback: ifc.Flags&net.FlagLoopback != 0,
				Family:   FamilyIPv4,
			}
			if bits == 32 && ones < 32 {
				netAddr := ip4.Mask(ipnet.Mask)
				e.Network = fmt.Sprintf("%s/%d", netAddr.String(), ones)
				bc := make(net.IP, 4)
				for i := 0; i < 4; i++ {
					bc[i] = netAddr[i] | ^ipnet.Mask[i]
				}
				e.Broadcast = bc.String()
			} else {
				e.Network = e.IPNet
			}
			if gw != "" && e.Network != "" && !e.Loopback && SameSubnet(gw, e.Network) {
				e.Gateway = gw
			}
			out = append(out, e)
		}
	}
	// The IPv6 default route needs an extra OS lookup on windows and darwin, so
	// it is only consulted when the host actually has an IPv6 address.
	if hasV6 {
		fillV6Gateways(out, defaultRoute6())
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Family != out[j].Family {
			return out[i].Family == FamilyIPv4
		}
		if out[i].Loopback != out[j].Loopback {
			return !out[i].Loopback
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ifaceV6 builds the Iface entry for one IPv6 address.
func ifaceV6(ifc net.Interface, ipnet *net.IPNet, ip6 net.IP) Iface {
	ones, _ := ipnet.Mask.Size()
	e := Iface{
		Name:     ifc.Name,
		Index:    ifc.Index,
		IP:       ip6.String(),
		IPNet:    fmt.Sprintf("%s/%d", ip6.String(), ones),
		Mask:     ipnet.Mask.String(),
		MAC:      ifc.HardwareAddr.String(),
		MTU:      ifc.MTU,
		Up:       ifc.Flags&net.FlagUp != 0,
		Loopback: ifc.Flags&net.FlagLoopback != 0,
		Family:   FamilyIPv6,
		Scoped:   ip6.IsLinkLocalUnicast(),
	}
	if e.Scoped {
		e.Zone = ifc.Name
	}
	if ones == 128 {
		e.Network = e.IPNet
	} else if netAddr := ip6.Mask(ipnet.Mask); netAddr != nil {
		e.Network = fmt.Sprintf("%s/%d", netAddr.String(), ones)
	}
	return e
}

// fillV6Gateways copies the IPv6 default next hop onto the interfaces it routes
// through. Entries it cannot attribute are left with an empty Gateway: a wrong
// gateway is worse than a missing one (the mapper would otherwise treat a
// neighbour as the NAT device).
func fillV6Gateways(out []Iface, gw route6Info) {
	if gw.Gateway == "" && gw.Iface == "" && gw.IfIndex == 0 {
		return
	}
	gwIP, gwZone := splitIPv6Zone(gw.Gateway)
	gwAddr := net.ParseIP(gwIP)
	for i := range out {
		e := &out[i]
		if !e.IsV6() || e.Loopback || e.Gateway != "" {
			continue
		}
		if !route6AppliesTo(gw, gwZone, gwAddr, *e) {
			continue
		}
		e.Gateway = gwIP
	}
}

// route6AppliesTo reports whether the default IPv6 route can be attributed to e.
func route6AppliesTo(gw route6Info, gwZone string, gwAddr net.IP, e Iface) bool {
	switch {
	case gwZone != "":
		return strings.EqualFold(gwZone, e.Zone) || strings.EqualFold(gwZone, e.Name)
	case gw.Iface != "":
		return strings.EqualFold(gw.Iface, e.Name)
	case gw.IfIndex != 0:
		return gw.IfIndex == e.Index
	}
	// The OS only gave us the next hop: a link-local one is ambiguous without a
	// zone, a global one is attributable when it shares the link's subnet.
	if gwAddr == nil || gwAddr.IsLinkLocalUnicast() {
		return false
	}
	return !e.Scoped && e.Network != "" && SameSubnet(gwAddr.String(), e.Network)
}

// PrimaryIPv4 returns the IPv4 address that represents this host on the LAN.
//
// Taking "the first non-loopback address" is not good enough on a real desktop:
// VPN/TUN adapters (Tailscale, WireGuard, Clash/mihomo, OpenVPN), VM host-only
// adapters (VMware/Hyper-V/VirtualBox), ZeroTier and Bluetooth PAN interfaces all
// sit in the same list, often sorted before the real LAN interface. Picking one
// of those makes mappings point at an address the router cannot reach and sends
// PCP/NAT-PMP probes out of the tunnel.
//
// Scores are therefore based on routing relevance, not on enumerations order:
//
//	owns the default route (its subnet contains the gateway)   +150
//	private (RFC1918 / CGNAT) address                          +40
//	link-local (169.254/16) address                             +0
//	interface is up                                            +20
//	loopback                                                    1 (last resort)
//
// The result is the highest scoring address, so a LAN interface that carries
// the default route always wins over a TUN adapter that does not.
func PrimaryIPv4() (string, error) {
	list, err := Interfaces()
	if err != nil {
		return "", err
	}
	if ip := PickPrimaryIPv4(list); ip != "" {
		return ip, nil
	}
	return "", errors.New("no local IPv4 address")
}

// PickPrimaryIPv4 implements the selection policy behind PrimaryIPv4. It is pure
// so the policy can be unit tested on every platform, including with interface
// sets that only exist on Windows or macOS.
func PickPrimaryIPv4(list []Iface) string {
	bestIP := ""
	bestScore := -1
	for _, i := range list {
		ip := net.ParseIP(i.IP)
		if ip == nil || ip.To4() == nil {
			continue
		}
		score := 0
		switch {
		case i.Loopback:
			score = 1
		default:
			score = 10
			if i.Up {
				score += 20
			}
			switch {
			case isPrivateIPv4(ip):
				score += 40
			case ip.IsLinkLocalUnicast():
				// link-local addresses are only a last resort
			default:
				score += 10
			}
			if i.Gateway != "" && SameSubnet(i.Gateway, i.Network) {
				score += 150
			}
		}
		if score > bestScore {
			bestIP, bestScore = i.IP, score
		}
	}
	return bestIP
}

// isPrivateIPv4 reports whether ip is in RFC1918 or RFC6598 (CGNAT) space.
func isPrivateIPv4(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	switch {
	case v4[0] == 10:
		return true
	case v4[0] == 172 && v4[1]&0xf0 == 16:
		return true
	case v4[0] == 192 && v4[1] == 168:
		return true
	case v4[0] == 100 && v4[1]&0xc0 == 64: // 100.64.0.0/10 carrier grade NAT
		return true
	}
	return false
}

// PrimaryIPv6 returns the IPv6 address that represents this host on the LAN.
//
// The policy mirrors PrimaryIPv4: loopback addresses are never considered,
// link-local (fe80::/10) addresses are only a last resort, and an address whose
// interface carries the IPv6 default route wins outright. Unique local addresses
// (fc00::/7) rank slightly above global unicast, because in practice the ULA is
// the LAN and the global prefix is often a privacy/temporary address.
//
// The result never carries a "%zone" suffix, exactly like Iface.IP; when the
// winner is link-local, use the matching Iface.Zone (or JoinHostPort) to dial it.
func PrimaryIPv6() (string, error) {
	list, err := Interfaces()
	if err != nil {
		return "", err
	}
	if ip := PickPrimaryIPv6(list); ip != "" {
		return ip, nil
	}
	return "", errors.New("no local IPv6 address")
}

// PickPrimaryIPv6 implements the selection policy behind PrimaryIPv6. It is
// pure so the policy can be unit tested on every platform.
//
// Scores:
//
//	owns the IPv6 default route (Gateway attributed to this interface)  +150
//	unique local (fc00::/7) address                                    +40
//	link-local (fe80::/10) address                                      +0
//	interface is up                                                    +20
//	other global unicast address                                       +10
//
// Loopback, multicast and unspecified addresses are skipped, never scored.
func PickPrimaryIPv6(list []Iface) string {
	bestIP := ""
	bestScore := -1
	for _, i := range list {
		if i.Loopback || !i.IsV6() {
			continue
		}
		ip, _ := splitIPv6Zone(i.IP)
		if ip == "" {
			continue
		}
		parsed := net.ParseIP(ip)
		if parsed == nil || parsed.To4() != nil || parsed.IsMulticast() || parsed.IsUnspecified() {
			continue
		}
		score := 10
		if i.Up {
			score += 20
		}
		switch {
		case isULAIPv6(parsed):
			score += 40
		case parsed.IsLinkLocalUnicast():
			// link-local addresses are only a last resort
		default:
			score += 10
		}
		if gatewayOnIfaceV6(i) {
			score += 150
		}
		if score > bestScore {
			bestIP, bestScore = ip, score
		}
	}
	return bestIP
}

// gatewayOnIfaceV6 reports whether i.Gateway is a plausible IPv6 next hop for i.
func gatewayOnIfaceV6(i Iface) bool {
	gwIP, gwZone := splitIPv6Zone(i.Gateway)
	if gwIP == "" {
		return false
	}
	gw := net.ParseIP(gwIP)
	if gw == nil {
		return false
	}
	if gw.IsLinkLocalUnicast() {
		if i.Scoped {
			// A link-local next hop only means something on the interface whose
			// zone it was seen on.
			return i.Zone != "" && (gwZone == "" || strings.EqualFold(gwZone, i.Zone))
		}
		// A global address with a link-local gateway got that gateway because
		// the route was attributed to its interface (see fillV6Gateways).
		return true
	}
	if i.Network == "" {
		return true
	}
	return SameSubnet(gwIP, i.Network)
}

// isULAIPv6 reports whether ip is in the fc00::/7 unique local range.
func isULAIPv6(ip net.IP) bool {
	v6 := ip.To16()
	return v6 != nil && ip.To4() == nil && v6[0]&0xfe == 0xfc
}

// JoinHostPort joins a host and port, bracketing IPv6 literals:
//
//	JoinHostPort("192.168.1.1", 80)         -> "192.168.1.1:80"
//	JoinHostPort("::1", 80)                 -> "[::1]:80"
//	JoinHostPort("fe80::1%eth0", 80)        -> "[fe80::1%eth0]:80"
//	JoinHostPort("[2001:db8::1]", 80)       -> "[2001:db8::1]:80"
//
// Unlike net.JoinHostPort it does not attempt to parse the host, so an
// already-zoned link-local literal keeps its "%zone" suffix (which net's
// SplitHostPort expects inside the brackets).
func JoinHostPort(host string, port int) string {
	h := strings.TrimSpace(host)
	if len(h) >= 2 && h[0] == '[' && h[len(h)-1] == ']' {
		h = h[1 : len(h)-1]
	}
	if strings.Contains(h, ":") {
		return "[" + h + "]:" + strconv.Itoa(port)
	}
	return h + ":" + strconv.Itoa(port)
}

// LocalIPv4For returns the local address the kernel would use to reach target.
//
// It is the standard "connect a UDP socket without sending anything" trick, and
// it is the only portable way to answer "which of my addresses is on the same
// network as this gateway?". Without it, a probe aimed at the LAN router can be
// sourced from a VPN address, so the router either drops the packet or replies
// to an address that never receives it.
func LocalIPv4For(target string) string {
	ip := net.ParseIP(strings.TrimSpace(strings.Trim(target, "[]")))
	if ip == nil || ip.To4() == nil {
		return ""
	}
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: ip, Port: 9})
	if err != nil {
		return ""
	}
	defer conn.Close()
	if la, ok := conn.LocalAddr().(*net.UDPAddr); ok && la.IP != nil && la.IP.To4() != nil {
		return la.IP.String()
	}
	return ""
}

// PrimaryInterface returns the local interface used to reach the default route.
//
// It may now return an IPv6 entry, but only when that entry is the first one
// carrying a Gateway: Interfaces sorts IPv4 first, so a dual-stack host with an
// IPv4 default route keeps returning its IPv4 interface.
func PrimaryInterface() (*Iface, error) {
	list, err := Interfaces()
	if err != nil {
		return nil, err
	}
	for i := range list {
		if !list[i].Loopback && list[i].Up && list[i].Gateway != "" {
			return &list[i], nil
		}
	}
	for i := range list {
		if !list[i].Loopback && list[i].Up {
			return &list[i], nil
		}
	}
	return nil, errors.New("no usable interface")
}

// DefaultGateway returns the default IPv4 gateway address.
func DefaultGateway() (string, error) {
	if gw := RouteGateway(); gw != "" {
		return gw, nil
	}
	return "", errors.New("no default gateway found")
}

// RouteGateway returns the default IPv4 next hop.
//
// The lookup is platform specific: linux reads /proc/net/route, darwin asks
// the routing socket (route -n get default), windows parses the IPv4 routing
// table (route print -4). See route_<os>.go.
//
// The result is memoised for a few seconds because on darwin and windows every
// lookup spawns a system utility, and the value is consulted for every rule
// during a reconcile pass (the default host of a mapping is derived from it via
// Interfaces/ PrimaryIPv4).
func RouteGateway() string {
	routeCache.mu.Lock()
	defer routeCache.mu.Unlock()
	if !routeCache.at.IsZero() && time.Since(routeCache.at) < routeCacheTTL {
		return routeCache.gw
	}
	gw := platformRouteGateway()
	routeCache.at, routeCache.gw = time.Now(), gw
	return gw
}

// routeCacheTTL bounds how stale a cached default route may be.
const routeCacheTTL = 5 * time.Second

var routeCache struct {
	mu sync.Mutex
	at time.Time
	gw string
}

// route6Info describes the IPv6 default route. Exactly one of the fields is
// guaranteed to be non-zero when a default route exists.
type route6Info struct {
	Gateway string // next hop, without a "%zone" suffix (may be empty for an on-link default)
	Iface   string // outgoing interface name, when the OS reveals it
	IfIndex int    // outgoing interface index, when only that is known
}

var route6Cache struct {
	mu sync.Mutex
	at time.Time
	r  route6Info
}

// defaultRoute6 returns the IPv6 default next hop, memoised for the same short
// window as RouteGateway: on windows and darwin the lookup spawns a utility and
// Interfaces is consulted for every reconcile pass.
//
// The value is only consulted when the host has an IPv6 address, so an
// IPv4-only machine pays nothing for it.
func defaultRoute6() route6Info {
	route6Cache.mu.Lock()
	defer route6Cache.mu.Unlock()
	if !route6Cache.at.IsZero() && time.Since(route6Cache.at) < routeCacheTTL {
		return route6Cache.r
	}
	r := platformDefaultRoute6()
	route6Cache.at, route6Cache.r = time.Now(), r
	return r
}

// ParsePortSpec expands a port specification such as "80,443,8000-8100" into a
// sorted, de-duplicated port list. An empty spec yields defaults.
func ParsePortSpec(spec string, max int) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, errors.New("empty port specification")
	}
	if max <= 0 {
		max = 65535
	}
	set := map[int]struct{}{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			ab := strings.SplitN(part, "-", 2)
			lo, err := strconv.Atoi(strings.TrimSpace(ab[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid port range %q", part)
			}
			hi, err := strconv.Atoi(strings.TrimSpace(ab[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid port range %q", part)
			}
			if lo > hi {
				lo, hi = hi, lo
			}
			if lo < 1 {
				lo = 1
			}
			if hi > 65535 {
				hi = 65535
			}
			if hi-lo+1 > max {
				return nil, fmt.Errorf("port range %q exceeds limit of %d ports", part, max)
			}
			for p := lo; p <= hi; p++ {
				set[p] = struct{}{}
				if len(set) > max {
					return nil, fmt.Errorf("port specification exceeds limit of %d ports", max)
				}
			}
			continue
		}
		p, err := strconv.Atoi(part)
		if err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("invalid port %q", part)
		}
		set[p] = struct{}{}
	}
	out := make([]int, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Ints(out)
	return out, nil
}

// ParsePortsInRange returns ports inside [minPort,maxPort] that the spec allows.
func ParsePortsInRange(spec string, minPort, maxPort int) ([]int, error) {
	all, err := ParsePortSpec(spec, 65535)
	if err != nil {
		return nil, err
	}
	out := []int{}
	for _, p := range all {
		if p >= minPort && p <= maxPort {
			out = append(out, p)
		}
	}
	return out, nil
}

// ExpandCIDR lists host addresses for a CIDR block, bounded by maxHosts.
//
// IPv4 is unchanged: a /32 (or a bare IPv4 literal) is a single host, prefixes
// of /30 or larger skip the network and broadcast addresses, and a block whose
// host count exceeds maxHosts is an error rather than a partial scan.
//
// IPv6 has no broadcast address and the all-zero host part is the
// subnet-router anycast address, so that one address is skipped for /126 and
// wider prefixes; /127 and /128 have no separate network address and return
// every address they contain (the IPv6 analogue of IPv4 /31 and /32). Because a
// /64 holds 2^64 addresses, IPv6 never errors on a range that is too large: it
// returns the first maxHosts addresses and stops, so the result is never larger
// than maxHosts and the address space is never wrapped. A "%zone" suffix on the
// input ("fe80::1%eth0/64", "fe80::1%eth0") is accepted and dropped; the
// returned literals carry no zone, because the zone is a routing scope rather
// than part of the address (see Iface.Zone).
func ExpandCIDR(cidr string, maxHosts int) ([]string, error) {
	cidr = strings.TrimSpace(cidr)
	if cidr == "" {
		return nil, errors.New("empty CIDR")
	}
	if normalized, zone := stripZoneSuffix(cidr); zone != "" {
		cidr = normalized
	}
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		// Allow a bare IP meaning /32 (or a single IPv6 host).
		if single := net.ParseIP(cidr); single != nil {
			return []string{single.String()}, nil
		}
		return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}
	if maxHosts <= 0 {
		maxHosts = 4096
	}
	ones, bits := ipnet.Mask.Size()
	if bits == 128 {
		return expandIPv6(ipnet, ones, maxHosts)
	}
	if bits == 32 && ones == 32 {
		return []string{ip.String()}, nil
	}
	out := []string{}
	n := ip.Mask(ipnet.Mask)
	for ipnet.Contains(n) {
		host := n.String()
		// Skip network/broadcast for /30 and larger blocks only.
		if ones <= 30 {
			if host == ipnet.IP.String() {
				if len(out) == 0 {
					n = nextIP(n)
					continue
				}
			}
			bc := lastIP(ipnet)
			if host == bc {
				break
			}
		}
		out = append(out, host)
		if len(out) > maxHosts {
			return nil, fmt.Errorf("CIDR %q exceeds host limit of %d", cidr, maxHosts)
		}
		n = nextIP(n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("CIDR %q yields no host addresses", cidr)
	}
	return out, nil
}

// expandIPv6 lists the host addresses of an IPv6 prefix in ascending order. See
// ExpandCIDR for the network-address skip rule and the truncation contract.
func expandIPv6(ipnet *net.IPNet, ones, maxHosts int) ([]string, error) {
	netAddr := ipnet.IP.Mask(ipnet.Mask)
	if netAddr == nil {
		return nil, errors.New("invalid IPv6 network")
	}
	if ones == 128 {
		return []string{netAddr.String()}, nil
	}
	// With two or more host bits there is a distinct all-zero host part; that is
	// the anycast address for the subnet router, so it is not scanned.
	skipNetwork := 128-ones >= 2
	capHint := maxHosts
	if capHint > 4096 {
		capHint = 4096
	}
	out := make([]string, 0, capHint)
	cur := make(net.IP, len(netAddr))
	copy(cur, netAddr)
	if skipNetwork {
		cur = nextIP(cur)
	}
	for len(out) < maxHosts && ipnet.Contains(cur) {
		out = append(out, cur.String())
		next := nextIP(cur)
		if !ipGreater(next, cur) {
			// The increment wrapped past ffff...ff; never re-enter the block.
			break
		}
		cur = next
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("CIDR %q yields no host addresses", ipnet.String())
	}
	return out, nil
}

// ipGreater reports whether a sorts after b (same length IPs only).
func ipGreater(a, b net.IP) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func nextIP(ip net.IP) net.IP {
	out := make(net.IP, len(ip))
	copy(out, ip)
	for i := len(out) - 1; i >= 0; i-- {
		out[i]++
		if out[i] != 0 {
			break
		}
	}
	return out
}

func lastIP(n *net.IPNet) string {
	ip := make(net.IP, len(n.IP))
	copy(ip, n.IP)
	for i := range ip {
		ip[i] |= ^n.Mask[i]
	}
	return ip.String()
}

// SameSubnet reports whether ip is inside the CIDR block.
func SameSubnet(ip, cidr string) bool {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	p := net.ParseIP(ip)
	if p == nil {
		return false
	}
	return ipnet.Contains(p)
}

// LocalAddresses returns every local IPv4 address as a set.
//
// IPv6 entries from Interfaces are deliberately excluded: callers key this set
// on IPv4 literals returned by ExpandCIDR and the ARP parsers, and adding v6
// keys would silently change "is this host me" answers. Use Interfaces or
// PrimaryIPv6 for IPv6.
func LocalAddresses() (map[string]bool, error) {
	list, err := Interfaces()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, i := range list {
		if i.IsV6() {
			continue
		}
		out[i.IP] = true
	}
	out["127.0.0.1"] = true
	return out, nil
}

// Neighbor is a discovered LAN peer.
type Neighbor struct {
	IP       string
	MAC      string
	Iface    string
	Hostname string
	Vendor   string
	// Family is FamilyIPv4 or FamilyIPv6. IP never carries a "%zone" suffix for
	// IPv6; the zone is the interface name in Iface.
	Family string
}

// DiscoverNeighbors reads the OS neighbour tables: the IPv4 ARP cache and the
// IPv6 neighbour discovery (NDP) cache, IPv4 rows first.
//
// The lookup is platform specific: linux reads /proc/net/arp (falling back to
// `ip neigh`) and `ip -6 neigh`, darwin parses `arp -an` and `ndp -an`, windows
// parses `arp -a` and `netsh interface ipv6 show neighbors`. See neigh_<os>.go.
// Entries without a usable unicast MAC (incomplete/FAILED probes, multicast
// rows) are dropped for both families.
func DiscoverNeighbors(ctx context.Context) ([]Neighbor, error) {
	return platformNeighbors(ctx)
}

// ReverseDNS resolves a PTR record with a bounded timeout.
func ReverseDNS(ctx context.Context, ip string) string {
	cctx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(cctx, ip)
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[0], ".")
}

// ExternalIPHTTP queries public IP echo services. Disabled unless explicitly
// enabled in configuration because it leaks the WAN address to third parties.
func ExternalIPHTTP(ctx context.Context, endpoints []string) (string, error) {
	if len(endpoints) == 0 {
		endpoints = []string{
			"https://api.ipify.org",
			"https://ifconfig.me/ip",
			"https://icanhazip.com",
		}
	}
	var lastErr error
	for _, ep := range endpoints {
		cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		body, err := httpGet(cctx, ep)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		s := strings.TrimSpace(string(body))
		if net.ParseIP(s) != nil {
			return s, nil
		}
		lastErr = fmt.Errorf("%s returned unusable body", ep)
	}
	if lastErr == nil {
		lastErr = errors.New("no external IP endpoint configured")
	}
	return "", lastErr
}
