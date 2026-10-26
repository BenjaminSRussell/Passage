package domain_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
)

func TestTeamInForce(t *testing.T) {
	day := func(s string) time.Time {
		v, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	end := day("2026-12-31")
	tests := []struct {
		name string
		to   *time.Time
		asOf time.Time
		want bool
	}{
		{"open-ended", nil, day("2030-01-01"), true},
		{"before start", &end, day("2025-12-31"), false},
		{"on start", &end, day("2026-01-01"), true},
		{"mid range", &end, day("2026-06-01"), true},
		{"on end", &end, day("2026-12-31"), true},
		{"after end", &end, day("2027-01-01"), false},
		{"time of day on end", &end, day("2026-12-31").Add(23*time.Hour + 59*time.Minute), true},
		{"time of day before start", &end, day("2026-01-01").Add(-time.Second), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.TeamInForce(day("2026-01-01"), tc.to, tc.asOf); got != tc.want {
				t.Fatalf("TeamInForce = %v, want %v", got, tc.want)
			}
		})
	}
}
