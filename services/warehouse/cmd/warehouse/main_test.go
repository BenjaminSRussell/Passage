package main

import (
	"testing"
	"time"
)

// WMS_LOCK_TIMEOUT_MIN is the PRD lock_timeout_min parameter: 5 to 240, default 30.
func TestLockTimeout(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 30 * time.Minute, true},
		{"5", 5 * time.Minute, true},
		{"240", 240 * time.Minute, true},
		{"4", 0, false},
		{"241", 0, false},
		{"thirty", 0, false},
	} {
		got, err := lockTimeout(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("lockTimeout(%q) = %v, %v; want %v ok=%v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}
