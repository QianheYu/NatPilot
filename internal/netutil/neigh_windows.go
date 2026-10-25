//go:build windows

package netutil

import (
	"context"
	"time"

	"github.com/ekko-studio/natpilot/internal/oscmd"
)

// platformNeighbors reads the ARP cache through `arp -a`. Windows has no
// /proc/net/arp.
func platformNeighbors(ctx context.Context) ([]Neighbor, error) {
	out, err := oscmd.Run(ctx, 4*time.Second,
		[]string{"arp.exe", "arp", `C:\Windows\System32\arp.exe`}, "-a")
	if err != nil {
		return nil, err
	}
	return parseARPOutputWindows(out), nil
}
