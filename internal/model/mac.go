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

// macHex keeps only the hexadecimal digits of an address/pattern.
func macHex(s string) string {
	raw := strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range raw {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// MACMatches reports whether mac falls inside a pattern.
//
// A pattern is either a full address ("aa:bb:cc:dd:ee:ff", any separator) or a
// prefix ("aa:bb:cc" / "aabbcc"), the latter covering a whole vendor OUI. This
// is what lets a scan range or a policy match a device whose IP keeps moving.
func MACMatches(pattern, mac string) bool {
	p, m := macHex(pattern), macHex(mac)
	if p == "" || len(m) != 12 || len(p) > 12 {
		return false
	}
	return strings.HasPrefix(m, p)
}

// ParseMACPatterns splits a user supplied list ("aa:bb:cc:dd:ee:ff, 11:22:33")
// into usable patterns.
func ParseMACPatterns(spec string) []string {
	fields := strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '|'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if v := strings.TrimSpace(f); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// AnyMACMatch reports whether mac matches at least one of the patterns.
func AnyMACMatch(patterns []string, mac string) bool {
	for _, p := range patterns {
		if MACMatches(p, mac) {
			return true
		}
	}
	return false
}

// MACPatternValid reports whether a pattern is usable: at least three bytes of
// hexadecimal digits (an OUI prefix) and at most a full address.
func MACPatternValid(pattern string) bool {
	n := len(macHex(pattern))
	return n >= 6 && n <= 12
}
