package netutil

import (
	"net"
	"strconv"
	"strings"
)

// This file holds the pure parsing helpers used by the per-platform route and
// neighbour implementations. They are deliberately free of build tags so they
// can be unit tested on any host — that is how the darwin and windows code paths
// are verified without access to those operating systems.

// ---- default route ----

// parseRouteGetDarwin pulls the gateway out of `route -n get default`:
//
//	   route to: default
//	destination: default
//	       mask: default
//	    gateway: 192.168.1.1
//	  interface: en0
func parseRouteGetDarwin(out string) string {
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(key), "gateway") {
			continue
		}
		if gw := strings.TrimSpace(val); net.ParseIP(gw) != nil {
			return gw
		}
	}
	return ""
}

// parseNetstatDarwin pulls the default gateway out of BSD `netstat -rn`:
//
//	Destination        Gateway            Flags        Netif Expire
//	default            192.168.1.1        UGScg          en0
func parseNetstatDarwin(out string) string {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "default" {
			continue
		}
		if gw := f[1]; net.ParseIP(gw) != nil {
			return gw
		}
	}
	return ""
}

// parseRoutePrintWindows pulls the default gateway out of Windows
// `route print -4` (Windows `netstat -rn` prints the same table shape):
//
//	Network Destination        Netmask          Gateway       Interface  Metric
//	          0.0.0.0          0.0.0.0      192.168.1.1     192.168.1.100     25
//
// Only the numeric columns are inspected, so the result does not depend on the
// system language.
//
// A multi-homed machine (VPN, Hyper-V, VMware, ZeroTier, a TUN adapter) has
// several 0.0.0.0 rows and the kernel uses the one with the lowest metric. The
// first row in the table is NOT that row, so the metric column decides.
func parseRoutePrintWindows(out string) string {
	best := ""
	bestMetric := -1
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		// Active routes have five columns (destination, netmask, gateway,
		// interface, metric); the "persistent routes" table at the bottom of the
		// same output has four and must be ignored, whatever the section header
		// says in the current language.
		if len(f) < 5 {
			continue
		}
		if f[0] != "0.0.0.0" || f[1] != "0.0.0.0" {
			continue
		}
		ip := net.ParseIP(f[2])
		if ip == nil || ip.To4() == nil || ip.IsUnspecified() {
			continue
		}
		iface := net.ParseIP(f[3])
		if iface == nil || iface.To4() == nil {
			continue
		}
		metric := 0
		if m, err := strconv.Atoi(f[len(f)-1]); err == nil {
			metric = m
		}
		if bestMetric < 0 || metric < bestMetric {
			best, bestMetric = ip.String(), metric
		}
	}
	return best
}

// ---- MAC helpers ----

// normalizeMAC lowercases a MAC address and zero pads single digit octets,
// because macOS prints them unpadded ("0:11:22:33:44:55").
func normalizeMAC(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", ":")
	s = strings.ReplaceAll(s, ".", ":") // Cisco style aabb.ccdd.eeff
	parts := strings.Split(s, ":")
	if len(parts) != 6 {
		return ""
	}
	for i, p := range parts {
		switch len(p) {
		case 1:
			parts[i] = "0" + p
		case 2:
			// already padded
		default:
			return ""
		}
		if _, err := strconv.ParseUint(parts[i], 16, 8); err != nil {
			return ""
		}
	}
	return strings.Join(parts, ":")
}

// isUsableMAC reports whether a normalised MAC is a real unicast unicast host
// address. Multicast/broadcast frames and the all-zero placeholder are rejected.
func isUsableMAC(mac string) bool {
	parts := strings.Split(mac, ":")
	if len(parts) != 6 {
		return false
	}
	first, err := strconv.ParseUint(parts[0], 16, 8)
	if err != nil {
		return false
	}
	if first&0x01 != 0 { // group bit: multicast or broadcast
		return false
	}
	allZero := true
	for _, p := range parts {
		if p != "00" {
			allZero = false
			break
		}
	}
	return !allZero
}

// ---- neighbour tables ----

// parseARPOutputDarwin parses BSD `arp -an`:
//
//	? (192.168.1.1) at 0:11:22:33:44:55 on en0 ifscope [ethernet]
//	? (192.168.1.20) at b8:27:eb:11:22:33 on en0 ifscope [ethernet]
//	? (192.168.1.99) at (incomplete) on en0 ifscope [ethernet]
func parseARPOutputDarwin(out string) []Neighbor {
	res := []Neighbor{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		open := strings.Index(line, "(")
		closing := strings.Index(line, ")")
		if open < 0 || closing < open {
			continue
		}
		ip := strings.TrimSpace(line[open+1 : closing])
		if net.ParseIP(ip) == nil {
			continue
		}
		at := strings.Index(line, " at ")
		if at < 0 {
			continue
		}
		rest := line[at+4:]
		macRaw := rest
		iface := ""
		if on := strings.Index(rest, " on "); on >= 0 {
			macRaw = rest[:on]
			if f := strings.Fields(rest[on+4:]); len(f) > 0 {
				iface = f[0]
			}
		}
		mac := normalizeMAC(macRaw)
		if !isUsableMAC(mac) {
			continue
		}
		res = append(res, Neighbor{IP: ip, MAC: mac, Iface: iface, Family: FamilyIPv4})
	}
	return res
}

// parseARPOutputWindows parses Windows `arp -a`:
//
//	Interface: 192.168.1.100 --- 0xb
//	  Internet Address      Physical Address      Type
//	  192.168.1.1           00-11-22-33-44-55     dynamic
//	  192.168.1.255         ff-ff-ff-ff-ff-ff     static
//	  224.0.0.22            01-00-5e-00-00-16     static
//
// The column headers are localised, so rows are recognised by shape (an IPv4
// literal followed by a MAC literal) rather than by header text. The interface
// header is localised too ("接口: ... --- 0xb" on Chinese Windows, "Schnittstelle:"
// on German), so it is detected by the "--- 0x<index>" suffix that every locale
// prints, not by the leading label.
func parseARPOutputWindows(out string) []Neighbor {
	res := []Neighbor{}
	iface := ""
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if ip, ok := parseWinARPInterfaceHeader(trimmed); ok {
			iface = ip
			continue
		}
		f := strings.Fields(trimmed)
		if len(f) < 2 {
			continue
		}
		ip := net.ParseIP(f[0])
		if ip == nil || ip.To4() == nil {
			continue
		}
		if ip.IsMulticast() || ip.IsUnspecified() {
			continue
		}
		mac := normalizeMAC(f[1])
		if !isUsableMAC(mac) {
			continue
		}
		res = append(res, Neighbor{IP: ip.String(), MAC: mac, Iface: iface, Family: FamilyIPv4})
	}
	return res
}

// parseWinARPInterfaceHeader extracts the interface address from an `arp -a`
// section header, whatever the system language calls it:
//
//	Interface: 192.168.1.100 --- 0xb     (English)
//	接口: 192.168.1.100 --- 0xb           (Chinese)
//	Schnittstelle: 192.168.1.100 --- 0xb (German)
//
// The MAC column never contains three consecutive dashes, so "---" is an
// unambiguous marker for a header line.
func parseWinARPInterfaceHeader(line string) (string, bool) {
	idx := strings.Index(line, "---")
	if idx <= 0 {
		return "", false
	}
	for _, tok := range strings.Fields(line[:idx]) {
		tok = strings.Trim(tok, "[]()")
		if ip := net.ParseIP(tok); ip != nil && ip.To4() != nil && !ip.IsUnspecified() {
			return ip.String(), true
		}
	}
	return "", false
}

// ---- IPv6 helpers ----

// splitIPv6Zone separates a scope zone from an IPv6 literal:
//
//	"fe80::1%eth0" -> ("fe80::1", "eth0")
//	"2001:db8::1"  -> ("2001:db8::1", "")
//	"10.0.0.1"     -> ("", "")
//
// The returned address is canonical and never carries the zone: the zone is a
// routing scope, not part of the address, so callers keep it separately.
func splitIPv6Zone(addr string) (ip, zone string) {
	addr = strings.TrimSpace(strings.Trim(addr, "[]"))
	if addr == "" {
		return "", ""
	}
	if i := strings.LastIndexByte(addr, '%'); i >= 0 {
		zone = addr[i+1:]
		addr = addr[:i]
	}
	p := net.ParseIP(addr)
	if p == nil || p.To4() != nil || p.To16() == nil {
		return "", ""
	}
	return p.String(), zone
}

// stripZoneSuffix removes a "%zone" suffix from a bare address or a CIDR
// literal, keeping any "/prefix" part:
//
//	"fe80::1%eth0/64" -> ("fe80::1/64", "eth0")
//	"2001:db8::/32"   -> ("2001:db8::/32", "")
//
// Only IPv6 literals can carry a zone; anything else is returned unchanged.
func stripZoneSuffix(s string) (string, string) {
	if !strings.Contains(s, "%") {
		return s, ""
	}
	addr, suffix := s, ""
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		addr, suffix = s[:i], s[i:]
	}
	j := strings.LastIndexByte(addr, '%')
	if j < 0 {
		return s, ""
	}
	return addr[:j] + suffix, addr[j+1:]
}

// decodeIPv6Hex decodes the 32 hex digit big-endian form the Linux kernel uses
// for IPv6 addresses in /proc.
func decodeIPv6Hex(s string) net.IP {
	if len(s) != 32 {
		return nil
	}
	ip := make(net.IP, 16)
	for i := 0; i < 16; i++ {
		v, err := strconv.ParseUint(s[i*2:i*2+2], 16, 8)
		if err != nil {
			return nil
		}
		ip[i] = byte(v)
	}
	return ip
}

// ---- IPv6 default route ----

// parseProcIPv6Route finds the lowest-metric default route in
// /proc/net/ipv6_route (Linux):
//
//	00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000f00 00000000 00000000     eth0
//
// Columns: destination, destination prefix length, source, source prefix
// length, next hop, metric, refcount, use, flags, device. A next hop of all
// zeros means the default is on-link, so there is no gateway address (the
// outgoing interface is still reported).
func parseProcIPv6Route(out string) route6Info {
	best := route6Info{}
	bestMetric := -1
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		if f[0] != strings.Repeat("0", 32) || f[1] != "00" {
			continue
		}
		metric, err := strconv.ParseUint(f[5], 16, 32)
		if err != nil {
			continue
		}
		if bestMetric >= 0 && int(metric) >= bestMetric {
			continue
		}
		info := route6Info{Iface: f[9]}
		if gw := decodeIPv6Hex(f[4]); gw != nil && !gw.IsUnspecified() {
			info.Gateway = gw.String()
		}
		best, bestMetric = info, int(metric)
	}
	return best
}

// parseRoutePrintWindows6 pulls the IPv6 default route out of `route print -6`:
//
//	If Metric Network Destination      Gateway
//	 1    281 ::/0                     fe80::1
//	12     41 ::/0                     On-link
//
// The headers are localised, so rows are recognised by shape: a numeric
// interface index, a numeric metric, the literal "::/0" and a next hop that
// parses as an IPv6 address. "On-link" (in any language) never parses as an
// address and is dropped. The persistent routes table prints "none" where the
// index goes, so it is skipped by the numeric-index rule.
func parseRoutePrintWindows6(out string) route6Info {
	best := route6Info{}
	bestMetric := -1
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		ifIndex, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		metric, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		if f[2] != "::/0" {
			continue
		}
		gw, zone := splitIPv6Zone(f[3])
		if gw == "" {
			continue
		}
		if bestMetric >= 0 && metric >= bestMetric {
			continue
		}
		best, bestMetric = route6Info{Gateway: gw, IfIndex: ifIndex, Iface: zone}, metric
	}
	return best
}

// parseRouteGetDarwin6 pulls the IPv6 default route out of
// `route -n get -inet6 default`:
//
//	   route to: ::
//	destination: ::
//	       mask: default
//	    gateway: fe80::1%en0
//	  interface: en0
//
// The gateway is returned without its zone; the outgoing interface is returned
// separately, because a link-local next hop is meaningless without it.
func parseRouteGetDarwin6(out string) route6Info {
	info := route6Info{}
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "gateway":
			gw, zone := splitIPv6Zone(strings.TrimSpace(val))
			if gw == "" {
				continue
			}
			info.Gateway = gw
			if info.Iface == "" {
				info.Iface = zone
			}
		case "interface":
			if ifc := strings.TrimSpace(val); ifc != "" {
				info.Iface = ifc
			}
		}
	}
	return info
}

// parseNetstatDarwin6 parses the default row of `netstat -rn -f inet6`:
//
//	Destination        Gateway            Flags        Netif Expire
//	default            fe80::1%en0        UGScg          en0
func parseNetstatDarwin6(out string) route6Info {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "default" {
			continue
		}
		gw, zone := splitIPv6Zone(f[1])
		if gw == "" {
			continue
		}
		return route6Info{Gateway: gw, Iface: zone}
	}
	return route6Info{}
}

// ---- IPv6 neighbour tables (NDP) ----

// parseWinNetshNeighbors parses `netsh interface ipv6 show neighbors`:
//
//	Interface 12: WLAN
//
//	Internet Address                              Physical Address   Type
//	--------------------------------------------  -----------------  -----------
//	fe80::1                                       00-11-22-33-44-55  Reachable
//	2001:db8::20                                  b8-27-eb-11-22-33  Stale
//	fe80::9999                                    --                 Unreachable
//
// The table is fully localised ("接口 12: WLAN", "Internet 地址", "可访问"/"陈旧"),
// and the state column ("Reachable", "Stale", "router", ...) is never inspected,
// so the current language cannot hide or invent a neighbour. A section header is
// the only line whose second field is a bare "<index>:" token; addresses and MAC
// columns never look like that. Rows whose MAC is missing or is a placeholder
// ("--", "(incomplete)", all zero) are dropped: a failed NDP probe says nothing
// about which host is at that address, and inventing one would poison host-bound
// policies.
func parseWinNetshNeighbors(out string) []Neighbor {
	res := []Neighbor{}
	iface := ""
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		f := strings.Fields(trimmed)
		if isWinNetshIfaceHeader(f) {
			iface = strings.Join(f[2:], " ")
			continue
		}
		if len(f) < 2 {
			continue
		}
		addr, zone := splitIPv6Zone(f[0])
		if addr == "" {
			continue
		}
		ip := net.ParseIP(addr)
		if ip == nil || ip.IsMulticast() || ip.IsUnspecified() {
			continue
		}
		mac := normalizeMAC(f[1])
		if !isUsableMAC(mac) {
			continue
		}
		seenOn := zone
		if seenOn == "" {
			seenOn = iface
		}
		res = append(res, Neighbor{IP: addr, MAC: mac, Iface: seenOn, Family: FamilyIPv6})
	}
	return res
}

// isWinNetshIfaceHeader reports whether the fields look like the
// "Interface 12: <name>" / "接口 12: <name>" section header of
// `netsh interface ipv6 show neighbors`.
func isWinNetshIfaceHeader(f []string) bool {
	if len(f) < 2 || !strings.HasSuffix(f[1], ":") || len(f[1]) < 2 {
		return false
	}
	for _, r := range f[1][:len(f[1])-1] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseIPNeigh6 parses `ip -6 neigh show`:
//
//	2001:db8::1 dev eth0 lladdr 00:11:22:33:44:55 REACHABLE
//	fe80::1 dev eth0 lladdr 00:11:22:33:44:55 router STALE
//	fe80::2 dev eth0  INCOMPLETE
//	ff02::1 dev eth0  NOARP
//
// Only rows that carry a usable "lladdr" are reported, which is exactly the
// behaviour of the IPv4 parser: INCOMPLETE and FAILED rows have no MAC and must
// not become hosts. The state keyword is not inspected beyond that.
func parseIPNeigh6(out string) []Neighbor {
	res := []Neighbor{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		addr, zone := splitIPv6Zone(f[0])
		if addr == "" {
			continue
		}
		ip := net.ParseIP(addr)
		if ip == nil || ip.IsMulticast() || ip.IsUnspecified() {
			continue
		}
		iface, mac := "", ""
		for i := 1; i < len(f); i++ {
			switch f[i] {
			case "dev":
				if i+1 < len(f) {
					iface = f[i+1]
					i++
				}
			case "lladdr":
				if i+1 < len(f) {
					mac = normalizeMAC(f[i+1])
				}
			}
		}
		if !isUsableMAC(mac) {
			continue
		}
		if iface == "" {
			iface = zone
		}
		res = append(res, Neighbor{IP: addr, MAC: mac, Iface: iface, Family: FamilyIPv6})
	}
	return res
}

// parseNdpAn parses `ndp -an`:
//
//	Neighbor                             Linklayer Address  Netif Expire    S Flags
//	fe80::1%en0                          0:11:22:33:44:55   en0   23h59m59s S R
//	2001:db8::20                         b8:27:eb:11:22:33  en0   23h58m11s   R
//	2001:db8::99                         (incomplete)       en0   expired   I
//
// macOS prints unpadded MAC octets ("0:11:...") which normalizeMAC pads. The
// "%zone" suffix of a link-local neighbour is split off the address and becomes
// the Iface value; the address stays bare. Entries without a real MAC are
// dropped, so an incomplete entry never turns into a host.
func parseNdpAn(out string) []Neighbor {
	res := []Neighbor{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		addr, zone := splitIPv6Zone(f[0])
		if addr == "" {
			continue
		}
		ip := net.ParseIP(addr)
		if ip == nil || ip.IsMulticast() || ip.IsUnspecified() {
			continue
		}
		mac := normalizeMAC(f[1])
		if !isUsableMAC(mac) {
			continue
		}
		iface := f[2]
		if iface == "---" {
			iface = ""
		}
		if zone != "" {
			iface = zone
		}
		res = append(res, Neighbor{IP: addr, MAC: mac, Iface: iface, Family: FamilyIPv6})
	}
	return res
}
