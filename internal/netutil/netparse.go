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
		res = append(res, Neighbor{IP: ip, MAC: mac, Iface: iface})
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
		res = append(res, Neighbor{IP: ip.String(), MAC: mac, Iface: iface})
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
