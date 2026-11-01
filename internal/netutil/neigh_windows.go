//go:build windows

package netutil

import (
	"context"
	"time"

	"github.com/ekko-studio/natpilot/internal/oscmd"
)

// platformNeighbors reads the IPv4 ARP cache through `arp -a` and the IPv6 NDP
// cache through `netsh interface ipv6 show neighbors`. Windows has neither
// /proc/net/arp nor a netlink socket. The IPv4 rows come first and are parsed
// exactly as before; a missing or failing netsh only drops the IPv6 rows.
func platformNeighbors(ctx context.Context) ([]Neighbor, error) {
	out, err := oscmd.Run(ctx, 4*time.Second,
		[]string{"arp.exe", "arp", `C:\Windows\System32\arp.exe`}, "-a")
	if err != nil {
		return nil, err
	}
	ns := parseARPOutputWindows(out)
	if v6, err6 := oscmd.Run(ctx, 4*time.Second,
		[]string{"netsh.exe", "netsh", `C:\Windows\System32\netsh.exe`},
		"interface", "ipv6", "show", "neighbors"); err6 == nil {
		ns = append(ns, parseWinNetshNeighbors(v6)...)
	}
	return ns, nil
}
