//go:build darwin

package netutil

import (
	"context"
	"time"

	"github.com/ekko-studio/natpilot/internal/oscmd"
)

// platformRouteGateway asks the routing socket for the default route and falls
// back to parsing the routing table. macOS has no /proc, so both sources are
// system utilities.
func platformRouteGateway() string {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	if out, err := oscmd.Run(ctx, 3*time.Second,
		[]string{"/sbin/route", "/usr/sbin/route", "route"}, "-n", "get", "default"); err == nil {
		if gw := parseRouteGetDarwin(out); gw != "" {
			return gw
		}
	}
	if out, err := oscmd.Run(ctx, 3*time.Second,
		[]string{"/usr/sbin/netstat", "/bin/netstat", "netstat"}, "-rn", "-f", "inet"); err == nil {
		if gw := parseNetstatDarwin(out); gw != "" {
			return gw
		}
	}
	return ""
}

// platformDefaultRoute6 asks the routing socket for the IPv6 default route and
// falls back to the inet6 routing table. Both expose the outgoing interface,
// which is required to scope a link-local next hop.
func platformDefaultRoute6() route6Info {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	if out, err := oscmd.Run(ctx, 3*time.Second,
		[]string{"/sbin/route", "/usr/sbin/route", "route"}, "-n", "get", "-inet6", "default"); err == nil {
		if r := parseRouteGetDarwin6(out); r.Gateway != "" || r.Iface != "" {
			return r
		}
	}
	if out, err := oscmd.Run(ctx, 3*time.Second,
		[]string{"/usr/sbin/netstat", "/bin/netstat", "netstat"}, "-rn", "-f", "inet6"); err == nil {
		return parseNetstatDarwin6(out)
	}
	return route6Info{}
}
