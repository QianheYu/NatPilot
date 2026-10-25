// Package logging provides leveled, categorized logging with pluggable hooks,
// so that every record can additionally be persisted and streamed as a system
// event without threading a logger through every call site.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// Record is a structured log record handed to hooks.
type Record struct {
	Time     time.Time      `json:"time"`
	Level    string         `json:"level"`
	Category string         `json:"category"`
	Message  string         `json:"message"`
	Meta     map[string]any `json:"meta,omitempty"`
}

// Hook receives every emitted record.
type Hook func(Record)

var (
	mu     sync.RWMutex
	logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	hooks  []Hook
	minLvl = slog.LevelInfo
)

// Init configures the process wide logger. format is one of text|json.
func Init(level, format string) {
	lvl := ParseLevel(level)
	out := io.Writer(os.Stderr)
	var h slog.Handler
	if strings.EqualFold(format, "json") {
		h = slog.NewJSONHandler(out, &slog.HandlerOptions{Level: lvl})
	} else {
		h = slog.NewTextHandler(out, &slog.HandlerOptions{Level: lvl})
	}
	mu.Lock()
	logger = slog.New(h)
	minLvl = lvl
	mu.Unlock()
}

// ParseLevel maps a textual level to slog.Level.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug", "trace", "":
		if s == "" {
			return slog.LevelInfo
		}
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "err":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// LevelOf reports the configured minimum level.
func LevelOf() string {
	mu.RLock()
	defer mu.RUnlock()
	return strings.ToLower(minLvl.String())
}

// AddHook registers a hook receiving all future records.
func AddHook(h Hook) {
	if h == nil {
		return
	}
	mu.Lock()
	hooks = append(hooks, h)
	mu.Unlock()
}

func toMeta(kv []any) map[string]any {
	if len(kv) == 0 {
		return nil
	}
	m := make(map[string]any, len(kv)/2+1)
	for i := 0; i+1 < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			k = fmt.Sprint(kv[i])
		}
		m[k] = kv[i+1]
	}
	return m
}

func emit(lvl slog.Level, cat, msg string, kv ...any) {
	mu.RLock()
	lg := logger
	floor := minLvl
	hs := make([]Hook, len(hooks))
	copy(hs, hooks)
	mu.RUnlock()

	if lvl < floor {
		return
	}
	meta := toMeta(kv)
	args := make([]any, 0, len(kv)+2)
	args = append(args, "cat", cat)
	args = append(args, kv...)
	lg.Log(nil, lvl, msg, args...)

	if len(hs) == 0 {
		return
	}
	rec := Record{Time: time.Now(), Level: strings.ToLower(lvl.String()), Category: cat, Message: msg, Meta: meta}
	for _, h := range hs {
		func() {
			defer func() { _ = recover() }() // a broken hook must never break logging
			h(rec)
		}()
	}
}

// Debug logs at debug level.
func Debug(cat, msg string, kv ...any) { emit(slog.LevelDebug, cat, msg, kv...) }

// Info logs at info level.
func Info(cat, msg string, kv ...any) { emit(slog.LevelInfo, cat, msg, kv...) }

// Warn logs at warn level.
func Warn(cat, msg string, kv ...any) { emit(slog.LevelWarn, cat, msg, kv...) }

// Error logs at error level.
func Error(cat, msg string, kv ...any) { emit(slog.LevelError, cat, msg, kv...) }
