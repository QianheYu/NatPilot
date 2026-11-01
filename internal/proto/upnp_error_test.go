package proto

import (
	"strings"
	"testing"
)

func TestUPnPErrorHints(t *testing.T) {
	cases := []struct{ code, want string }{
		{"606", "secure mode"},
		{"718", "已被其它映射占用"},
		{"724", "内外网端口一致"},
		{"725", "永久租约"},
		{"501", "网关执行失败"},
	}
	for _, c := range cases {
		err := upnpError("AddPortMapping", c.code, "some description")
		if err == nil {
			t.Fatalf("code %s: nil error", c.code)
		}
		msg := err.Error()
		if !strings.Contains(msg, "upnp error "+c.code) {
			t.Fatalf("code %s: raw code missing from %q", c.code, msg)
		}
		if !strings.Contains(msg, c.want) {
			t.Fatalf("code %s: hint %q missing from %q", c.code, c.want, msg)
		}
	}
	// 未知错误码不应被加提示，但必须保留原始信息。
	msg := upnpError("AddPortMapping", "999", "custom").Error()
	if msg != "upnp error 999: custom" {
		t.Fatalf("unknown code message = %q", msg)
	}
}
