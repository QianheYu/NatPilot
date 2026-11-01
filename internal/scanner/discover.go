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
	// default route happens to be. Empty means "let the OS choose".
	SourceIP string
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
	if p.SourceIP != "" {
		if ip := net.ParseIP(p.SourceIP); ip == nil || ip.To4() == nil {
			return fmt.Errorf("invalid source ip %q", p.SourceIP)
		}
	}
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

// ProbeCIDR sweeps the subnet and returns the addresses that answered.
func ProbeCIDR(ctx context.Context, p HostProbe) ([]string, error) {
	if err := p.Normalize(); err != nil {
		return nil, err
	}
	addrs, err := netutil.ExpandCIDR(p.CIDR, p.MaxHosts)
	if err != nil {
		return nil, err
	}

	var local net.IP
	if p.SourceIP != "" {
		local = net.ParseIP(p.SourceIP).To4()
	}

	alive := make([]string, 0, 8)
	var mu sync.Mutex
	sem := make(chan struct{}, p.Concurrency)
	var wg sync.WaitGroup

	for _, addr := range addrs {
		select {
		case <-ctx.Done():
			wg.Wait()
			return alive, ctx.Err()
		default:
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(host string) {
			defer wg.Done()
			defer func() { <-sem }()
			if hostAlive(ctx, host, p.Ports, p.Timeout, local) {
				mu.Lock()
				alive = append(alive, host)
				mu.Unlock()
			}
		}(addr)
	}
	wg.Wait()
	return alive, nil
}

// hostAlive reports whether any of the ports completes a TCP handshake.
func hostAlive(ctx context.Context, host string, ports []int, timeout time.Duration, local net.IP) bool {
	for _, port := range ports {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		d := net.Dialer{Timeout: timeout}
		if local != nil {
			d.LocalAddr = &net.TCPAddr{IP: local}
		}
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
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

// PickInterface resolves an interface by name ("以太网", "eth0") or by its IPv4
// address, so the UI can send either. It returns the interface and, when a CIDR
// was not given, the interface's own subnet as the default target.
func PickInterface(ifaces []netutil.Iface, ref string) (netutil.Iface, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return netutil.Iface{}, false
	}
	for _, i := range ifaces {
		if strings.EqualFold(i.Name, ref) || i.IP == ref {
			return i, true
		}
	}
	return netutil.Iface{}, false
}
