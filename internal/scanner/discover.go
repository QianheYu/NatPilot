package scanner

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ekko-studio/natpilot/internal/netutil"
)

// HostProbe describes an active host discovery sweep over a subnet.
//
// The sweep is deliberately TCP-based: a connect attempt to a few common ports
// needs no elevated privileges (unlike raw ICMP), works on every platform, and
// has the pleasant side effect of populating the ARP/neighbour table for the
// hosts that answer — which is what fills in the MAC address, and the MAC is
// what host-bound monitoring policies key on.
type HostProbe struct {
	// CIDR is the subnet to sweep, e.g. "192.168.1.0/24". Required.
	CIDR string
	// SourceIP binds the outgoing sockets to one interface address, so a
	// multi-NIC machines scans the link the user picked instead of whatever the
	// default route happens to be. Empty means "let the OS choose". IPv4 and
	// IPv6 literals are both accepted; a "%zone" suffix is split into
	// SourceZone.
	SourceIP string
	// SourceZone is the IPv6 scope zone (an interface name) used when SourceIP
	// is a link-local IPv6 address. It is ignored for IPv4 and global IPv6
	// sources. Empty leaves the scope to the OS, which only works for global
	// addresses.
	SourceZone string
	// TargetZone is the IPv6 scope zone applied to link-local IPv6 destinations
	// that do not carry their own "%zone" suffix, e.g. "fe80::1" becomes
	// "fe80::1%<TargetZone>" before the port is joined. It is applied to
	// link-local IPv6 destinations only: IPv4 and global IPv6 targets are
	// dialled exactly as expanded, and a destination that already contains its
	// own "%zone" keeps it. When both are empty, link-local destinations are
	// deliberately skipped and reported in ProbeCIDR's skipped count instead of
	// being dialled (they cannot be reached without a scope).
	TargetZone string
	// Ports are the TCP ports tried per host. A host is considered alive when any
	// one of them completes a handshake.
	Ports []int
	// Timeout applies to each individual connect attempt.
	Timeout time.Duration
	// Concurrency bounds the number of hosts probed at once.
	Concurrency int
	// MaxHosts caps how many addresses are expanded from CIDR.
	MaxHosts int
}

// DefaultProbePorts are the ports used when the caller does not specify any.
// They span the usual suspects on home networks (router UI, NAS, SMB, RDP, ssh).
var DefaultProbePorts = []int{80, 443, 22, 445, 3389, 8080}

// Normalize fills in defaults and validates the probe.
func (p *HostProbe) Normalize() error {
	p.CIDR = strings.TrimSpace(p.CIDR)
	if p.CIDR == "" {
		return fmt.Errorf("cidr is required")
	}
	if _, _, err := net.ParseCIDR(p.CIDR); err != nil {
		return fmt.Errorf("invalid cidr %q: %w", p.CIDR, err)
	}
	p.TargetZone = normalizeZone(p.TargetZone)
	if p.SourceIP != "" {
		ip, zone := splitZonedIP(p.SourceIP)
		if ip == nil {
			return fmt.Errorf("invalid source ip %q", p.SourceIP)
		}
		p.SourceIP = ip.String()
		if zone != "" && p.SourceZone == "" {
			p.SourceZone = zone
		}
	}
	p.SourceZone = normalizeZone(p.SourceZone)
	if len(p.Ports) == 0 {
		p.Ports = append([]int{}, DefaultProbePorts...)
	}
	for _, port := range p.Ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("invalid port %d", port)
		}
	}
	if p.Timeout <= 0 {
		p.Timeout = 400 * time.Millisecond
	}
	if p.Timeout > 5*time.Second {
		p.Timeout = 5 * time.Second
	}
	if p.Concurrency <= 0 {
		p.Concurrency = 128
	}
	if p.Concurrency > 1024 {
		p.Concurrency = 1024
	}
	if p.MaxHosts <= 0 || p.MaxHosts > 4096 {
		p.MaxHosts = 1024
	}
	return nil
}

// ProbeCIDR sweeps the subnet and returns the addresses that answered, plus the
// number of destinations it deliberately skipped.
//
// The only skipped destinations are link-local IPv6 addresses that can never be
// dialled from this probe: they carry no "%zone" of their own and the probe has
// no TargetZone to lend them. Counting them lets the caller explain an empty
// result ("fe80::/64 needs a scope") instead of silently scanning nothing.
func ProbeCIDR(ctx context.Context, p HostProbe) (alive []string, skipped int, err error) {
	if err := p.Normalize(); err != nil {
		return nil, 0, err
	}
	addrs, err := netutil.ExpandCIDR(p.CIDR, p.MaxHosts)
	if err != nil {
		return nil, 0, err
	}

	var local *net.TCPAddr
	if p.SourceIP != "" {
		if ip := net.ParseIP(p.SourceIP); ip != nil {
			local = &net.TCPAddr{IP: ip}
			if ip.To4() == nil {
				local.Zone = p.SourceZone
			}
		}
	}

	alive = make([]string, 0, 8)
	var mu sync.Mutex
	sem := make(chan struct{}, p.Concurrency)
	var wg sync.WaitGroup

	for _, addr := range addrs {
		select {
		case <-ctx.Done():
			wg.Wait()
			return alive, skipped, ctx.Err()
		default:
		}
		if linkLocalNeedsZone(addr, p.TargetZone) {
			skipped++
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(host string) {
			defer wg.Done()
			defer func() { <-sem }()
			if hostAlive(ctx, host, p.Ports, p.Timeout, local, p.TargetZone) {
				mu.Lock()
				alive = append(alive, host)
				mu.Unlock()
			}
		}(addr)
	}
	wg.Wait()
	return alive, skipped, nil
}

// hostAlive reports whether any of the ports completes a TCP handshake.
func hostAlive(ctx context.Context, host string, ports []int, timeout time.Duration, local *net.TCPAddr, targetZone string) bool {
	for _, port := range ports {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		d := net.Dialer{Timeout: timeout}
		if local != nil {
			d.LocalAddr = local
		}
		conn, err := d.DialContext(ctx, "tcp", netutil.JoinHostPort(dialHost(host, targetZone), port))
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}

// dialHost prepares one expanded destination for dialling: a link-local IPv6
// literal that has no "%zone" of its own gets zone appended, while IPv4, global
// IPv6, already-zoned literals and hostnames are returned unchanged. The result
// is still just a host (no port); JoinHostPort adds the brackets and the port.
func dialHost(host, zone string) string {
	h := strings.TrimSpace(host)
	if len(h) >= 2 && h[0] == '[' && h[len(h)-1] == ']' {
		h = h[1 : len(h)-1]
	}
	zone = normalizeZone(zone)
	if zone == "" || strings.Contains(h, "%") {
		return h
	}
	ip := net.ParseIP(h)
	if ip == nil || ip.To4() != nil || !ip.IsLinkLocalUnicast() {
		return h
	}
	return h + "%" + zone
}

// linkLocalNeedsZone reports whether host is a link-local IPv6 literal that can
// be reached neither with a zone of its own nor with the probe's TargetZone.
func linkLocalNeedsZone(host, zone string) bool {
	if normalizeZone(zone) != "" {
		return false
	}
	h := strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.Contains(h, "%") {
		return false
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.To4() == nil && ip.IsLinkLocalUnicast()
}

// normalizeZone trims an IPv6 scope zone and an optional leading "%".
func normalizeZone(zone string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(zone), "%"))
}

// splitZonedIP parses an IPv4/IPv6 literal and separates an optional "%zone"
// suffix. Brackets around IPv6 literals are accepted. It returns a nil IP when
// the address part does not parse.
func splitZonedIP(s string) (net.IP, string) {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '[' && s[len(s)-1] == ']' {
		s = s[1 : len(s)-1]
	}
	if i := strings.Index(s, "%"); i >= 0 {
		return net.ParseIP(s[:i]), normalizeZone(s[i+1:])
	}
	return net.ParseIP(s), ""
}

// ParseProbePorts accepts "80,443,22" or an empty string (defaults).
func ParseProbePorts(spec string) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	out := make([]int, 0, 8)
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("invalid port %q", part)
		}
		if n < 1 || n > 65535 {
			return nil, fmt.Errorf("port out of range: %d", n)
		}
		out = append(out, n)
	}
	return out, nil
}

// PickInterface resolves a UI interface reference to one interface entry.
//
// Accepted forms:
//
//	"WLAN|fe80::1234%12"  composite "<name>|<ip>" sent by the host-scan UI: the
//	                      name tells the IPv4 and IPv6 entries that share it
//	                      apart, and the address picks the exact entry
//	"fe80::1234%12"       a bare IPv4/IPv6 literal, with or without a "%zone"
//	"WLAN"                an interface name; the first entry with that name wins
//	                      (the IPv4 one, because Interfaces orders IPv4 first)
//
// A zone in a bare address is only a preference: it selects among entries that
// share the address (link-local addresses can repeat on several links) by
// Iface.Zone, Iface.Name or Iface.Index. Plain names keep their historical
// first-match meaning so existing callers and tests are unaffected.
func PickInterface(ifaces []netutil.Iface, ref string) (netutil.Iface, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return netutil.Iface{}, false
	}
	if name, addr, ok := strings.Cut(ref, "|"); ok {
		name = strings.TrimSpace(name)
		ip, _ := splitZonedIP(addr)
		if ip == nil {
			return netutil.Iface{}, false
		}
		for _, i := range ifaces {
			if name != "" && !strings.EqualFold(i.Name, name) {
				continue
			}
			if ifaceHasIP(i, ip) {
				return i, true
			}
		}
		return netutil.Iface{}, false
	}
	if ip, zone := splitZonedIP(ref); ip != nil {
		if zone != "" {
			for _, i := range ifaces {
				if ifaceHasIP(i, ip) && ifaceHasZone(i, zone) {
					return i, true
				}
			}
		}
		for _, i := range ifaces {
			if ifaceHasIP(i, ip) {
				return i, true
			}
		}
	}
	for _, i := range ifaces {
		if strings.EqualFold(i.Name, ref) {
			return i, true
		}
	}
	return netutil.Iface{}, false
}

// ifaceHasIP reports whether the entry holds ip, ignoring a zone suffix.
func ifaceHasIP(i netutil.Iface, ip net.IP) bool {
	entry, _ := splitZonedIP(i.IP)
	return entry != nil && entry.Equal(ip)
}

// ifaceHasZone reports whether zone names the entry, either by its interface
// name/zone or by its numeric index (the form Windows uses in "%12").
func ifaceHasZone(i netutil.Iface, zone string) bool {
	zone = normalizeZone(zone)
	return zone != "" &&
		(strings.EqualFold(zone, i.Zone) ||
			strings.EqualFold(zone, i.Name) ||
			zone == strconv.Itoa(i.Index))
}
