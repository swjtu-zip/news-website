package main

import (
	"testing"
	"time"
)

func TestNextDailyRun(t *testing.T) {
	loc := time.FixedZone("CST", 8*60*60)
	now := time.Date(2026, 9, 1, 13, 30, 0, 0, loc)
	next := nextDailyRun(now, []string{"12:00", "18:00"}, loc)
	if next.Hour() != 18 || next.Day() != 1 {
		t.Fatalf("next = %v, want 18:00 same day", next)
	}
	now = time.Date(2026, 9, 1, 19, 0, 0, 0, loc)
	next = nextDailyRun(now, []string{"12:00", "18:00"}, loc)
	if next.Hour() != 12 || next.Day() != 2 {
		t.Fatalf("next = %v, want 12:00 next day", next)
	}
	now = time.Date(2026, 9, 1, 12, 0, 0, 0, loc)
	next = nextDailyRun(now, []string{"12:00"}, loc)
	if next.Day() != 2 {
		t.Fatalf("exact-now should roll to next day, got %v", next)
	}
}
