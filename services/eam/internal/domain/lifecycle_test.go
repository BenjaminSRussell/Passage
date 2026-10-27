package domain

import (
	"testing"
	"time"
)

func TestDowntimeMinutes(t *testing.T) {
	base := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		to   time.Time
		want int
		ok   bool
	}{
		{"four hours", base.Add(4 * time.Hour), 240, true},
		{"truncates partial minute", base.Add(90 * time.Second), 1, true},
		{"sub-minute rejected", base.Add(30 * time.Second), 0, false},
		{"empty rejected", base, 0, false},
		{"reversed rejected", base.Add(-time.Minute), 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DowntimeMinutes(base, tc.to)
			if tc.ok != (err == nil) {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if got != tc.want {
				t.Fatalf("minutes = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestLifecycleAllowed(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{"ACTIVE", "INACTIVE", true},
		{"INACTIVE", "DISPOSED", true},
		{"CREATED", "DISPOSED", true},
		{"ACTIVE", "DISPOSED", false},
		{"DISPOSED", "ACTIVE", false},
		{"DISPOSED", "INACTIVE", false},
		{"INACTIVE", "ACTIVE", false}, // activation is its own action
		{"CREATED", "INACTIVE", false},
	}
	for _, tc := range cases {
		if got := LifecycleAllowed(tc.from, tc.to); got != tc.want {
			t.Errorf("LifecycleAllowed(%s, %s) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}
