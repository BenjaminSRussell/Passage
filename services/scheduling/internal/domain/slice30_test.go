package domain

import (
	"testing"
	"time"
)

func TestFlaggedFromIgnoresPastAndUnflaggedWeeks(t *testing.T) {
	w0 := time.Date(2027, 1, 4, 0, 0, 0, 0, time.UTC)
	loads := []WeekLoad{
		{WeekStart: w0, LoadPct: 120, Bottleneck: true},
		{WeekStart: w0.AddDate(0, 0, 7), LoadPct: 95, Bottleneck: true},
		{WeekStart: w0.AddDate(0, 0, 14), LoadPct: 99, Bottleneck: true},
		{WeekStart: w0.AddDate(0, 0, 21), LoadPct: 150, Bottleneck: false},
	}
	f, ok := FlaggedFrom(loads, w0.AddDate(0, 0, 7))
	if !ok || f.Weeks != 2 || !f.First.Equal(w0.AddDate(0, 0, 7)) || !f.Last.Equal(w0.AddDate(0, 0, 14)) || f.PeakPct != 99 || len(f.Periods) != 2 {
		t.Fatalf("got %+v ok=%v", f, ok)
	}
	if _, ok := FlaggedFrom(loads, w0.AddDate(0, 0, 28)); ok {
		t.Fatal("no flagged week is left after the last one")
	}
}
