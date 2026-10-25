package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateCellKeepsRunes guards the CLI table formatting: cutting bytes in
// the middle of a multi-byte character produced mojibake for CJK process names
// (very common on Chinese Windows) and misaligned the tabwriter columns.
func TestTruncateCellKeepsRunes(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"chrome.exe", 20, "chrome.exe"},
		{"chrome.exe", 9, "chrome.e…"},
		{"微信", 10, "微信"},
		{"企业微信.exe", 4, "企业微…"},
		{"a", 1, "a"},
		{"ab", 1, "…"},
		{"", 5, ""},
		{"anything", 0, ""},
	}
	for _, c := range cases {
		got := truncateCell(c.in, c.n)
		if got != c.want {
			t.Errorf("truncateCell(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("truncateCell(%q, %d) produced invalid UTF-8: %q", c.in, c.n, got)
		}
		if n := utf8.RuneCountInString(got); c.n > 0 && n > c.n {
			t.Errorf("truncateCell(%q, %d) returned %d runes", c.in, c.n, n)
		}
	}
}

// TestTruncateCellDoesNotSplitUTF8 exhaustively slices a CJK string at every
// width and requires valid UTF-8 each time.
func TestTruncateCellDoesNotSplitUTF8(t *testing.T) {
	s := "蓝牙网络连接.exe"
	for n := 1; n <= len([]rune(s))+2; n++ {
		got := truncateCell(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("width %d produced invalid UTF-8: %q", n, got)
		}
		if strings.HasPrefix(got, "\uFFFD") {
			t.Fatalf("width %d produced a replacement character: %q", n, got)
		}
	}
}
