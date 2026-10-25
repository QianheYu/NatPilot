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
