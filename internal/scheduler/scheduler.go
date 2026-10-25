// Package scheduler runs recurring jobs described by a small cron subset:
// "*/n", "a-b", "a,b,c" and "*" over five fields (minute hour dom month dow).
package scheduler

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ekko-studio/natpilot/internal/logging"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/store"
)

// JobFunc executes a scheduled job and returns a status string.
type JobFunc func(ctx context.Context, sc model.Schedule) (string, error)

// Scheduler owns the schedule loop.
type Scheduler struct {
	store *store.Store
	jobs  map[string]JobFunc

	mu      sync.Mutex
	nextRun map[int64]time.Time
	stop    chan struct{}
	wg      sync.WaitGroup
}

// New builds a scheduler.
func New(st *store.Store) *Scheduler {
	return &Scheduler{
		store:   st,
		jobs:    map[string]JobFunc{},
		nextRun: map[int64]time.Time{},
		stop:    make(chan struct{}),
	}
}

// Register binds a job kind to its implementation.
func (s *Scheduler) Register(kind string, fn JobFunc) { s.jobs[kind] = fn }

// Start begins the schedule loop.
func (s *Scheduler) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		s.tick(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stop:
				return
			case <-t.C:
				s.tick(ctx)
			}
		}
	}()
}

// Stop halts the scheduler.
func (s *Scheduler) Stop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	s.wg.Wait()
}

func (s *Scheduler) tick(ctx context.Context) {
	list, err := s.store.EnabledSchedules()
	if err != nil {
		return
	}
	now := time.Now()
	for _, sc := range list {
		s.mu.Lock()
		next, ok := s.nextRun[sc.ID]
		s.mu.Unlock()
		if !ok {
			n, err := NextAfter(sc.Cron, now)
			if err != nil {
				continue
			}
			s.mu.Lock()
			s.nextRun[sc.ID] = n
			s.mu.Unlock()
			// Only the computed time is persisted here; counting this as a run made
			// a schedule that had not executed yet show run_count 1 and a last_run_at.
			_ = s.store.SetScheduleNextRun(sc.ID, model.Ptr(n))
			continue
		}
		if now.Before(next) {
			continue
		}
		fn, ok := s.jobs[sc.Kind]
		if !ok {
			logging.Warn("scheduler", "no handler registered for job kind", "kind", sc.Kind)
			continue
		}
		status, jerr := func() (string, error) {
			jctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
			return fn(jctx, sc)
		}()
		if jerr != nil {
			status = "error: " + jerr.Error()
			logging.Warn("scheduler", "scheduled job failed", "id", sc.ID, "kind", sc.Kind, "error", jerr.Error())
		} else {
			logging.Info("scheduler", "scheduled job ran", "id", sc.ID, "kind", sc.Kind, "status", status)
		}
		n, nerr := NextAfter(sc.Cron, time.Now())
		if nerr != nil {
			n = time.Now().Add(time.Hour)
		}
		s.mu.Lock()
		s.nextRun[sc.ID] = n
		s.mu.Unlock()
		_ = s.store.MarkScheduleRun(sc.ID, status, model.Ptr(n))
	}
}

// NextRun returns the computed next fire time.
func (s *Scheduler) NextRun(id int64) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.nextRun[id]
	return t, ok
}

// Schedule parses a cron expression into its five fields.
type Schedule struct {
	Minute, Hour, DOM, Month, DOW []int
}

// ParseCron parses a five field cron expression.
func ParseCron(expr string) (*Schedule, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron expression must have 5 fields, got %d", len(fields))
	}
	specs := []struct {
		min, max int
	}{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
	out := &Schedule{}
	parsed := make([][]int, 5)
	for i, f := range fields {
		vals, err := parseField(f, specs[i].min, specs[i].max)
		if err != nil {
			return nil, fmt.Errorf("invalid cron field %d (%q): %w", i+1, f, err)
		}
		parsed[i] = vals
	}
	out.Minute, out.Hour, out.DOM, out.Month, out.DOW = parsed[0], parsed[1], parsed[2], parsed[3], parsed[4]
	return out, nil
}

func parseField(field string, min, max int) ([]int, error) {
	set := map[int]bool{}
	field = strings.TrimSpace(field)
	if field == "" {
		return nil, fmt.Errorf("empty field")
	}
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		step := 1
		if idx := strings.Index(part, "/"); idx >= 0 {
			s, err := strconv.Atoi(part[idx+1:])
			if err != nil || s <= 0 {
				return nil, fmt.Errorf("invalid step in %q", part)
			}
			step = s
			part = part[:idx]
		}
		lo, hi := min, max
		switch {
		case part == "*" || part == "":
			// keep lo/hi
		case strings.Contains(part, "-"):
			ab := strings.SplitN(part, "-", 2)
			a, err1 := strconv.Atoi(strings.TrimSpace(ab[0]))
			b, err2 := strconv.Atoi(strings.TrimSpace(ab[1]))
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("invalid range %q", part)
			}
			lo, hi = a, b
		default:
			v, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid value %q", part)
			}
			lo, hi = v, v
		}
		if lo < min {
			lo = min
		}
		if hi > max {
			hi = max
		}
		if lo > hi {
			return nil, fmt.Errorf("inverted range %q", part)
		}
		for v := lo; v <= hi; v += step {
			set[v] = true
		}
	}
	out := []int{}
	for v := min; v <= max; v++ {
		if set[v] {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("field matches nothing")
	}
	return out, nil
}

// NextAfter returns the first fire time strictly after t (minute resolution).
func NextAfter(expr string, t time.Time) (time.Time, error) {
	sc, err := ParseCron(expr)
	if err != nil {
		return time.Time{}, err
	}
	candidate := t.Truncate(time.Minute).Add(time.Minute)
	// Search up to 366 days ahead.
	limit := candidate.AddDate(1, 0, 0)
	for candidate.Before(limit) {
		if contains(sc.Month, int(candidate.Month())) &&
			contains(sc.DOM, candidate.Day()) &&
			contains(sc.DOW, int(candidate.Weekday())) &&
			contains(sc.Hour, candidate.Hour()) &&
			contains(sc.Minute, candidate.Minute()) {
			return candidate, nil
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("no next run within a year for %q", expr)
}

func contains(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Validate checks an expression.
func Validate(expr string) error {
	_, err := ParseCron(expr)
	return err
}

// ValidateCron validates a cron expression (method form for API callers).
func (s *Scheduler) ValidateCron(expr string) error {
	_, err := ParseCron(expr)
	return err
}

// DescribeHuman renders a short human summary of common patterns.
func DescribeHuman(expr string) string {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return expr
	}
	if fields[0] == "*" && fields[1] == "*" {
		return "every minute"
	}
	if strings.HasPrefix(fields[0], "*/") && fields[1] == "*" {
		return "every " + strings.TrimPrefix(fields[0], "*/") + " minutes"
	}
	return expr
}
