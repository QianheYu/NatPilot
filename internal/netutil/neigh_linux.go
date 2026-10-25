//go:build linux

package netutil

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// platformNeighbors reads the kernel neighbour table, preferring /proc/net/arp
// and falling back to the `ip neigh` command.
func platformNeighbors(ctx context.Context) ([]Neighbor, error) {
	if ns := parseProcARP(); len(ns) > 0 {
		return ns, nil
	}
	return parseIPNeigh(ctx)
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
		out = append(out, Neighbor{IP: f[0], MAC: mac, Iface: f[5]})
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
		n := Neighbor{IP: f[0], Iface: f[2]}
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
