//go:build darwin

package netutil

import (
	"context"
	"time"

	"github.com/ekko-studio/natpilot/internal/oscmd"
)

// platformNeighbors reads the ARP cache through `arp -an`. macOS has no
// /proc/net/arp.
func platformNeighbors(ctx context.Context) ([]Neighbor, error) {
	out, err := oscmd.Run(ctx, 4*time.Second,
		[]string{"/usr/sbin/arp", "/sbin/arp", "arp"}, "-an")
	if err != nil {
		return nil, err
	}
	return parseARPOutputDarwin(out), nil
}
