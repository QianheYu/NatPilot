package store

import "time"

// small helpers used only by tests.

func timeNow() time.Time { return time.Now().UTC() }

func timeHour() time.Duration { return time.Hour }
