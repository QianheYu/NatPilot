package model

import (
	"net"
	"strings"
)

// Address family values used by HostAddress.Family.
const (
	FamilyIPv4 = "ipv4"
	FamilyIPv6 = "ipv6"
)

// HostAddress is one network address observed for a host.
//
// A dual-stack device is reachable over several addresses at the same time: an
// IPv4 DHCP lease, a global/ULA IPv6 address from SLAAC or DHCPv6, and often a
// link-local IPv6 address as well. Keeping them in a single string field made
// the last observation win, so a device discovered over ARP lost its IPv6
// address and vice versa. Host.Addresses keeps every address; Host.IP mirrors
// the primary one for older clients.
type HostAddress struct {
	IP       string `json:"ip"`
	Family   string `json:"family"`           // "ipv4" | "ipv6"
	Zone     string `json:"zone,omitempty"`   // link-local IPv6 scope (interface name)
	Primary  bool   `json:"primary"`          // mirrors Host.IP (set when read from the store)
	Source   string `json:"source,omitempty"` // arp | ndp | interface | scan | manual
	LastSeen Time   `json:"last_seen"`
}

// IPFamily reports the address family of ip as "ipv4" or "ipv6", and "" when ip
// is not a parseable IP address. An optional IPv6 zone suffix ("%eth0") is
// ignored, because the zone is carried separately in HostAddress.Zone.
func IPFamily(ip string) string {
	parsed := parseHostIP(ip)
	if parsed == nil {
		return ""
	}
	if parsed.To4() != nil {
		return FamilyIPv4
	}
	return FamilyIPv6
}

// AddressOf returns the host's address of the requested family ("ipv4" or
// "ipv6"). The primary address is preferred; if no address is flagged primary
// the first match is returned. Host.IP is used as a fallback when Addresses has
// not been populated (for example a host built by an older caller).
func (h *Host) AddressOf(family string) (HostAddress, bool) {
	if h == nil {
		return HostAddress{}, false
	}
	family = strings.ToLower(strings.TrimSpace(family))
	if family != FamilyIPv4 && family != FamilyIPv6 {
		return HostAddress{}, false
	}
	var fallback *HostAddress
	for i := range h.Addresses {
		a := h.Addresses[i]
		if !addressIsFamily(a, family) {
			continue
		}
		if a.Primary {
			return a, true
		}
		if fallback == nil {
			fallback = &h.Addresses[i]
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	if h.IP != "" && IPFamily(h.IP) == family {
		return HostAddress{IP: h.IP, Family: family, Primary: true, Source: h.Source, LastSeen: h.LastSeen}, true
	}
	return HostAddress{}, false
}

// addressIsFamily matches an address whose Family field is empty but whose IP
// still identifies the family, so callers are not required to fill it in.
func addressIsFamily(a HostAddress, family string) bool {
	if f := strings.ToLower(strings.TrimSpace(a.Family)); f != "" {
		return f == family
	}
	return IPFamily(a.IP) == family
}

// parseHostIP parses an address, accepting the bracketed form used in URLs and
// ignoring a "%zone" suffix.
func parseHostIP(raw string) net.IP {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		raw = raw[1 : len(raw)-1]
	}
	if i := strings.IndexByte(raw, '%'); i >= 0 {
		raw = raw[:i]
	}
	return net.ParseIP(raw)
}
