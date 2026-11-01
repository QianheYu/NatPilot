//go:build linux

package netutil

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// platformRouteGateway reads the kernel routing table. /proc/net/route lists the
// destination as a hex little-endian IPv4 and the gateway the same way, with the
// default route marked by destination 00000000.
func platformRouteGateway() string {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	for i, line := range strings.Split(string(b), "\n") {
		if i == 0 {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		if f[1] != "00000000" { // destination 0.0.0.0 = default route
			continue
		}
		hex := f[2]
		if len(hex) != 8 {
			continue
		}
		b0, _ := strconv.ParseUint(hex[6:8], 16, 8)
		b1, _ := strconv.ParseUint(hex[4:6], 16, 8)
		b2, _ := strconv.ParseUint(hex[2:4], 16, 8)
		b3, _ := strconv.ParseUint(hex[0:2], 16, 8)
		return fmt.Sprintf("%d.%d.%d.%d", b0, b1, b2, b3)
	}
	return ""
}

// platformDefaultRoute6 reads the kernel IPv6 routing table. /proc/net/ipv6_route
// is a plain file read and names the outgoing device, which a link-local next hop
// needs to be usable; there is no /proc equivalent for the neighbour cache, but
// there is one for routes.
func platformDefaultRoute6() route6Info {
	b, err := os.ReadFile("/proc/net/ipv6_route")
	if err != nil {
		return route6Info{}
	}
	return parseProcIPv6Route(string(b))
}
