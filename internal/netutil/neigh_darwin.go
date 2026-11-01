//go:build darwin

package netutil

import (
	"context"
	"time"

	"github.com/ekko-studio/natpilot/internal/oscmd"
)

// platformNeighbors reads the IPv4 ARP cache through `arp -an` and the IPv6 NDP
// cache through `ndp -an`. macOS has no /proc/net/arp. The IPv4 rows come first
// and are parsed exactly as before; a missing or failing ndp only drops the IPv6
// rows.
func platformNeighbors(ctx context.Context) ([]Neighbor, error) {
	out, err := oscmd.Run(ctx, 4*time.Second,
		[]string{"/usr/sbin/arp", "/sbin/arp", "arp"}, "-an")
	if err != nil {
		return nil, err
	}
	ns := parseARPOutputDarwin(out)
	if v6, err6 := oscmd.Run(ctx, 4*time.Second,
		[]string{"/usr/sbin/ndp", "/sbin/ndp", "ndp"}, "-an"); err6 == nil {
		ns = append(ns, parseNdpAn(v6)...)
	}
	return ns, nil
}
