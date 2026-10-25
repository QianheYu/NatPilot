package model

import "strings"

// NormalizeMAC canonicalises a hardware address so the many spellings that
// appear in the wild compare equal:
//
//	"AA-BB-CC-DD-EE-FF", "aabb.ccdd.eeff" and "aa:bb:cc:dd:ee:ff"
//
// This matters because a host's IP changes with its DHCP lease, which makes the
// MAC the only stable identity for host-bound policies — and the comparison must
// not depend on which tool reported the address.
func NormalizeMAC(mac string) string {
	raw := strings.ToLower(strings.TrimSpace(mac))
	if raw == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range raw {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			b.WriteRune(r)
		}
	}
	hex := b.String()
	if len(hex) != 12 {
		return raw // not a 48-bit address: keep it verbatim
	}
	parts := make([]string, 0, 6)
	for i := 0; i < 12; i += 2 {
		parts = append(parts, hex[i:i+2])
	}
	return strings.Join(parts, ":")
}

// SameMAC reports whether two MAC spellings identify the same device.
func SameMAC(a, b string) bool {
	na, nb := NormalizeMAC(a), NormalizeMAC(b)
	return na != "" && na == nb
}
