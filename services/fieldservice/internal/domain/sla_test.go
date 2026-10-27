package domain

import (
	"testing"
	"time"
)

func TestClock_PauseMovesDue(t *testing.T) {
	loc := chicago(t)
	cal := MondayToFriday(loc, 8*60, 18*60)
	// BR-45.2 example: priority 2, 480 business-minute resolution target,
	// opened Fri 2026-10-23 16:00 America/Chicago.
	opened := time.Date(2026, 10, 23, 16, 0, 0, 0, loc)

	unpaused := Clock{Opened: opened, TargetMinutes: 480, Cal: cal}
	if got, want := unpaused.Due(), time.Date(2026, 10, 26, 14, 0, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("unpaused due = %s, want %s", got, want)
	}
	// The pause is 120 business minutes, so due = Add(opened, 480+120) and the
	// due instant moves 120 business minutes later (Mon 14:00 -> Mon 16:00).
	paused := Clock{Opened: opened, TargetMinutes: 480, PausedMinutes: 120, Cal: cal}
	if got, want := paused.Due(), time.Date(2026, 10, 26, 16, 0, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("paused due = %s, want %s", got, want)
	}
}

func TestClock_WarningAndBreach(t *testing.T) {
	loc := chicago(t)
	cal := MondayToFriday(loc, 8*60, 18*60)
	c := Clock{Opened: time.Date(2026, 10, 23, 16, 0, 0, 0, loc), TargetMinutes: 480, Cal: cal}
	tests := []struct {
		name     string
		now      time.Time
		warn, br bool
	}{
		{"25 percent elapsed", time.Date(2026, 10, 26, 8, 0, 0, 0, loc).Add(0), false, false},
		{"75 percent elapsed warns", time.Date(2026, 10, 26, 12, 0, 0, 0, loc), true, false},
		{"exactly due is not yet breached", time.Date(2026, 10, 26, 14, 0, 0, 0, loc), true, false},
		{"after due is breached", time.Date(2026, 10, 26, 14, 1, 0, 0, loc), true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.Warning(tc.now, 75); got != tc.warn {
				t.Errorf("Warning = %v, want %v (elapsed %d)", got, tc.warn, c.Elapsed(tc.now))
			}
			if got := c.Breached(tc.now); got != tc.br {
				t.Errorf("Breached = %v, want %v", got, tc.br)
			}
		})
	}
}

func TestClock_ElapsedExcludesPause(t *testing.T) {
	loc := chicago(t)
	cal := MondayToFriday(loc, 8*60, 18*60)
	c := Clock{Opened: time.Date(2026, 10, 23, 16, 0, 0, 0, loc), TargetMinutes: 480, PausedMinutes: 120, Cal: cal}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, loc) // 360 business minutes after open
	if got := c.Elapsed(now); got != 240 {
		t.Fatalf("Elapsed = %d, want 240 (360 minus 120 paused)", got)
	}
}
