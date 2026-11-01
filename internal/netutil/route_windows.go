//go:build windows

package netutil

import (
	"context"
	"time"

	"github.com/ekko-studio/natpilot/internal/oscmd"
)

// platformRouteGateway reads the Windows IPv4 routing table. `route print -4`
// and `netstat -rn` emit the same five-column table, so one parser serves both.
func platformRouteGateway() string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	if out, err := oscmd.Run(ctx, 4*time.Second,
		[]string{"route.exe", "route", `C:\Windows\System32\route.exe`}, "print", "-4"); err == nil {
		if gw := parseRoutePrintWindows(out); gw != "" {
			return gw
		}
	}
	if out, err := oscmd.Run(ctx, 4*time.Second,
		[]string{"netstat.exe", "netstat", `C:\Windows\System32\netstat.exe`}, "-rn"); err == nil {
		if gw := parseRoutePrintWindows(out); gw != "" {
			return gw
		}
	}
	return ""
}

// platformDefaultRoute6 reads the Windows IPv6 routing table. `route print -6`
// carries the interface index in its first column, which is enough to attribute
// a link-local next hop to the interface that owns it.
func platformDefaultRoute6() route6Info {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	out, err := oscmd.Run(ctx, 4*time.Second,
		[]string{"route.exe", "route", `C:\Windows\System32\route.exe`}, "print", "-6")
	if err != nil {
		return route6Info{}
	}
	return parseRoutePrintWindows6(out)
}
