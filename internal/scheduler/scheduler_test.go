package scheduler

import (
	"testing"
	"time"
)

func TestParseCronValid(t *testing.T) {
	cases := map[string]bool{
		"*/5 * * * *":     true,
		"0 3 * * *":       true,
		"30 4 * * 1":      true,
		"*/30 * * * *":    true,
		"0 0 1 1 *":       true,
		"1,2,3 * * * *":   true,
		"0-30/10 * * * *": true,
		"bad":             false,
		"* * * *":         false,
		"60 * * * *":      false,
		"* 25 * * *":      false,
		"abc * * * *":     false,
	}
	for expr, wantOK := range cases {
		_, err := ParseCron(expr)
		if wantOK && err != nil {
			t.Errorf("ParseCron(%q) unexpected error: %v", expr, err)
		}
		if !wantOK && err == nil {
			t.Errorf("ParseCron(%q) expected error, got nil", expr)
		}
	}
}

func TestNextAfter(t *testing.T) {
	base := time.Date(2026, 3, 10, 10, 7, 30, 0, time.UTC)

	// Every 30 minutes: the next slot after 10:07 is 10:30.
	next, err := NextAfter("*/30 * * * *", base)
	if err != nil {
		t.Fatalf("NextAfter: %v", err)
	}
	want := time.Date(2026, 3, 10, 10, 30, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("NextAfter(*/30) = %v, want %v", next, want)
	}

	// Daily at 03:00 rolls into the next day when the time has passed.
	next, err = NextAfter("0 3 * * *", base)
	if err != nil {
		t.Fatalf("NextAfter: %v", err)
	}
	want = time.Date(2026, 3, 11, 3, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("NextAfter(0 3 * * *) = %v, want %v", next, want)
	}

	// The result must always be strictly after the input.
	next, err = NextAfter("*/5 * * * *", base)
	if err != nil {
		t.Fatalf("NextAfter: %v", err)
	}
	if !next.After(base) {
		t.Errorf("NextAfter returned %v which is not after %v", next, base)
	}
}

func TestParseCronFields(t *testing.T) {
	sc, err := ParseCron("15 2,14 * * 1-5")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	if len(sc.Minute) != 1 || sc.Minute[0] != 15 {
		t.Errorf("minutes = %v, want [15]", sc.Minute)
	}
	if len(sc.Hour) != 2 || sc.Hour[0] != 2 || sc.Hour[1] != 14 {
		t.Errorf("hours = %v, want [2 14]", sc.Hour)
	}
	if len(sc.DOW) != 5 {
		t.Errorf("day-of-week = %v, want 5 weekdays", sc.DOW)
	}
}
