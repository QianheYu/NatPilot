package model

import "testing"

func TestMACMatches(t *testing.T) {
	cases := []struct {
		pattern string
		mac     string
		want    bool
	}{
		{"aa:bb:cc:dd:ee:ff", "AA-BB-CC-DD-EE-FF", true},
		{"aa-bb-cc-dd-ee-ff", "aa:bb:cc:dd:ee:ff", true},
		{"aabbccddeeff", "aa:bb:cc:dd:ee:ff", true},
		// OUI prefix covers a whole vendor range, the DHCP-safe scoping form.
		{"aa:bb:cc", "aa:bb:cc:dd:ee:ff", true},
		{"aa:bb:cc:*", "aa:bb:cc:11:22:33", true},
		{"aabbcc", "aa:bb:cc:11:22:33", true},
		{"aa:bb:cd", "aa:bb:cc:dd:ee:ff", false},
		{"aa:bb:cc:dd:ee:ff", "aa:bb:cc:dd:ee:00", false},
		// Garbage never matches, so a typo cannot silently widen a rule.
		{"", "aa:bb:cc:dd:ee:ff", false},
		{"zz:zz", "aa:bb:cc:dd:ee:ff", false},
		{"aa:bb:cc", "", false},
	}
	for _, c := range cases {
		if got := MACMatches(c.pattern, c.mac); got != c.want {
			t.Fatalf("MACMatches(%q, %q) = %v, want %v", c.pattern, c.mac, got, c.want)
		}
	}
}

func TestParseMACPatterns(t *testing.T) {
	got := ParseMACPatterns(" aa:bb:cc:dd:ee:ff, 11:22:33 ;aabbcc  |  ")
	want := []string{"aa:bb:cc:dd:ee:ff", "11:22:33", "aabbcc"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if n := len(ParseMACPatterns("   ")); n != 0 {
		t.Fatalf("blank spec must yield no patterns, got %d", n)
	}
}

func TestAnyMACMatch(t *testing.T) {
	patterns := ParseMACPatterns("aa:bb:cc:dd:ee:01, 11:22:33")
	if !AnyMACMatch(patterns, "11:22:33:44:55:66") {
		t.Fatal("prefix pattern should match")
	}
	if !AnyMACMatch(patterns, "AA-BB-CC-DD-EE-01") {
		t.Fatal("exact pattern should match regardless of spelling")
	}
	if AnyMACMatch(patterns, "99:99:99:99:99:99") {
		t.Fatal("unrelated MAC must not match")
	}
}

func TestMACPatternValid(t *testing.T) {
	for _, ok := range []string{"aa:bb:cc", "aabbcc", "aa:bb:cc:dd:ee:ff", "aabbccddeeff"} {
		if !MACPatternValid(ok) {
			t.Fatalf("%q should be a valid pattern", ok)
		}
	}
	for _, bad := range []string{"", "aa", "aaaa", "zz:zz:zz:zz:zz:zz", "aa:bb:cc:dd:ee:ff:11"} {
		if MACPatternValid(bad) {
			t.Fatalf("%q should be rejected", bad)
		}
	}
}
