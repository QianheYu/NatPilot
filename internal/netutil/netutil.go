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
}

// Interfaces enumerates usable local IPv4 addresses (loopback included but flagged).
func Interfaces() ([]Iface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	gw := RouteGateway()
	out := []Iface{}
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
	sort.Slice(out, func(i, j int) bool {
		if out[i].Loopback != out[j].Loopback {
			return !out[i].Loopback
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
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
func ExpandCIDR(cidr string, maxHosts int) ([]string, error) {
	cidr = strings.TrimSpace(cidr)
	if cidr == "" {
		return nil, errors.New("empty CIDR")
	}
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		// Allow a bare IP meaning /32.
		if single := net.ParseIP(cidr); single != nil {
			return []string{single.String()}, nil
		}
		return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}
	if maxHosts <= 0 {
		maxHosts = 4096
	}
	ones, bits := ipnet.Mask.Size()
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
func LocalAddresses() (map[string]bool, error) {
	list, err := Interfaces()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, i := range list {
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
}

// DiscoverNeighbors reads the OS neighbour table (ARP cache).
//
// The lookup is platform specific: linux reads /proc/net/arp (falling back to
// `ip neigh`), darwin parses `arp -an`, windows parses `arp -a`. See
// neigh_<os>.go.
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
