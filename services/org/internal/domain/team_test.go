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

func TestMemberRangeViolation(t *testing.T) {
	day := func(s string) time.Time {
		v, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	ptr := func(s string) *time.Time { v := day(s); return &v }
	from, end := day("2026-01-01"), ptr("2026-12-31")
	tests := []struct {
		name      string
		teamTo    *time.Time
		validFrom time.Time
		validTo   *time.Time
		today     time.Time
		want      string
	}{
		{"inside", end, day("2026-02-01"), ptr("2026-06-30"), day("2026-03-01"), ""},
		{"starts after team end", end, day("2027-01-15"), ptr("2027-02-01"), day("2026-03-01"), "range"},
		{"starts before team", end, day("2025-12-31"), ptr("2026-06-30"), day("2026-03-01"), "range"},
		{"open-ended in bounded team", end, day("2026-02-01"), nil, day("2026-03-01"), "range"},
		{"ends after team", end, day("2026-02-01"), ptr("2027-01-01"), day("2026-03-01"), "range"},
		{"open-ended in open-ended team", nil, day("2026-02-01"), nil, day("2030-01-01"), ""},
		{"expired", end, day("2026-02-01"), ptr("2026-06-30"), day("2027-01-01"), "expired"},
		{"expired wins over range", end, day("2027-01-15"), nil, day("2027-01-01"), "expired"},
		{"last day not expired", end, day("2026-02-01"), ptr("2026-06-30"), day("2026-12-31"), ""},
		{"time of day ignored", end, day("2026-12-31").Add(23 * time.Hour), ptr("2026-12-31"), day("2026-12-31").Add(23*time.Hour + 59*time.Minute), ""},
		{"time of day before team start", end, day("2026-01-01").Add(-time.Second), ptr("2026-06-30"), day("2026-03-01"), "range"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.MemberRangeViolation(from, tc.teamTo, tc.validFrom, tc.validTo, tc.today); got != tc.want {
				t.Fatalf("MemberRangeViolation = %q, want %q", got, tc.want)
			}
		})
	}
}
