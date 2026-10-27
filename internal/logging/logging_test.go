package logging

import (
	"log/slog"
	"sync/atomic"
	"testing"
)

// TestParseLevelOff covers the "不记录" level: off (and its aliases) must map to
// LevelOff so that the emit() gate drops every record before it reaches the
// writer and the hooks.
func TestParseLevelOff(t *testing.T) {
	for _, raw := range []string{"off", "none", "silent", "disabled", "OFF", "  off  "} {
		if got := ParseLevel(raw); got != LevelOff {
			t.Fatalf("ParseLevel(%q) = %v, want LevelOff(%v)", raw, got, LevelOff)
		}
	}
	if LevelOff <= slog.LevelError {
		t.Fatal("LevelOff must be above error, otherwise error records would still pass")
	}
}

// TestOffIsTheDefaultConfigValue is asserted in the config package; here we only
// check that an empty level still means info (explicit "" is not "off").
func TestParseLevelEmptyIsInfo(t *testing.T) {
	if got := ParseLevel(""); got != slog.LevelInfo {
		t.Fatalf(`ParseLevel("") = %v, want info`, got)
	}
}

func TestLevelOfReportsOff(t *testing.T) {
	Init("off", "text")
	if got := LevelOf(); got != "off" {
		t.Fatalf("LevelOf() = %q, want off", got)
	}
	Init("warn", "text")
	if got := LevelOf(); got != "warn" {
		t.Fatalf("LevelOf() = %q, want warn", got)
	}
	Init("debug", "text")
	if got := LevelOf(); got != "debug" {
		t.Fatalf("LevelOf() = %q, want debug", got)
	}
	Init("info", "text")
}

// TestOffSuppressesHooks proves that "off" also stops the persisted event stream
// (the web console's 日志与审计 page), not just stderr output: hooks are gated by
// the same level check in emit().
func TestOffSuppressesHooks(t *testing.T) {
	var seen int32
	AddHook(func(Record) { atomic.AddInt32(&seen, 1) })

	Init("off", "text")
	Info("test", "dropped")
	Warn("test", "dropped")
	Error("test", "dropped")
	if n := atomic.LoadInt32(&seen); n != 0 {
		t.Fatalf("hooks received %d records while logging is off", n)
	}

	Init("info", "text")
	Warn("test", "passes")
	if n := atomic.LoadInt32(&seen); n == 0 {
		t.Fatal("hooks received nothing after enabling logging again")
	}
	Init("info", "text")
}
