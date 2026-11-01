//go:build linux

package netutil

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ekko-studio/natpilot/internal/oscmd"
)

// platformNeighbors reads the kernel neighbour tables: the IPv4 ARP cache
// (preferring /proc/net/arp, falling back to `ip neigh`) followed by the IPv6
// NDP cache (`ip -6 neigh`). IPv4 rows keep the historical order and shape.
func platformNeighbors(ctx context.Context) ([]Neighbor, error) {
	ns := []Neighbor{}
	if v4 := parseProcARP(); len(v4) > 0 {
		ns = append(ns, v4...)
	} else if v4, err := parseIPNeigh(ctx); err == nil && len(v4) > 0 {
		ns = append(ns, v4...)
	}
	ns = append(ns, readIPNeigh6(ctx)...)
	return ns, nil
}

// parseProcARP reads /proc/net/arp.
func parseProcARP() []Neighbor {
	b, err := os.ReadFile("/proc/net/arp")
	if err != nil {
		return nil
	}
	out := []Neighbor{}
	for i, line := range strings.Split(string(b), "\n") {
		if i == 0 {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		mac := strings.ToLower(f[3])
		if mac == "00:00:00:00:00:00" || mac == "" {
			continue
		}
		out = append(out, Neighbor{IP: f[0], MAC: mac, Iface: f[5], Family: FamilyIPv4})
	}
	return out
}

// parseIPNeigh runs `ip neigh show`.
func parseIPNeigh(ctx context.Context) ([]Neighbor, error) {
	path, err := exec.LookPath("ip")
	if err != nil {
		return nil, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, path, "neigh", "show")
	b, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	out := []Neighbor{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		n := Neighbor{IP: f[0], Iface: f[2], Family: FamilyIPv4}
		for i := 3; i < len(f)-1; i++ {
			if f[i] == "lladdr" {
				n.MAC = strings.ToLower(f[i+1])
			}
		}
		if n.MAC == "" {
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

// readIPNeigh6 runs `ip -6 neigh show`, the IPv6 counterpart of `ip neigh`.
//
// /proc/net/ndisc is not a standard interface (the kernel keeps the NDP cache
// out of procfs), so the netlink tool is the only source. A missing `ip` binary
// simply yields no IPv6 neighbours.
func readIPNeigh6(ctx context.Context) []Neighbor {
	out, err := oscmd.Run(ctx, 3*time.Second,
		[]string{"ip", "/sbin/ip", "/usr/sbin/ip", "/bin/ip"}, "-6", "neigh", "show")
	if err != nil {
		return nil
	}
	return parseIPNeigh6(out)
}
