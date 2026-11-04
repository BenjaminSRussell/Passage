package domain_test

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

func TestSpreadAccrual_EqualTwelveOccurrences(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got, err := domain.SpreadAccrual(decimal.RequireFromString("1200.00"), start, domain.AccrualScheme{
		Occurrences: 12, Spread: domain.SpreadEqual, AutoReverse: true,
	}, 2)
	if err != nil || len(got) != 12 {
		t.Fatalf("got %d occurrences, %v", len(got), err)
	}
	sum := decimal.Zero
	for i, o := range got {
		if !o.Amount.Equal(decimal.RequireFromString("100.00")) {
			t.Errorf("occurrence %d amount %s, want 100.00", i, o.Amount)
		}
		wantDate := time.Date(2026, time.Month(i+1), 1, 0, 0, 0, 0, time.UTC)
		if !o.Date.Equal(wantDate) {
			t.Errorf("occurrence %d date %s, want %s", i, o.Date.Format("2006-01-02"), wantDate.Format("2006-01-02"))
		}
		wantReverse := time.Date(2026, time.Month(i+2), 1, 0, 0, 0, 0, time.UTC)
		if i == 11 {
			wantReverse = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		if !o.ReverseOn.Equal(wantReverse) {
			t.Errorf("occurrence %d reverse %s, want %s", i, o.ReverseOn.Format("2006-01-02"), wantReverse.Format("2006-01-02"))
		}
		sum = sum.Add(o.Amount)
	}
	if !sum.Equal(decimal.RequireFromString("1200.00")) {
		t.Fatalf("sum %s, want 1200.00", sum)
	}
}

func TestSpreadAccrual_ByDaysResidualOnLast(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got, err := domain.SpreadAccrual(decimal.RequireFromString("100.00"), start, domain.AccrualScheme{
		Occurrences: 2, Spread: domain.SpreadByDays,
	}, 2)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v %v", got, err)
	}
	// Jan 31 days, Feb 28 days, total 59. 100 * 31/59 = 52.54, residual 47.46 on last.
	if !got[0].Amount.Equal(decimal.RequireFromString("52.54")) || !got[1].Amount.Equal(decimal.RequireFromString("47.46")) {
		t.Fatalf("got %s + %s", got[0].Amount, got[1].Amount)
	}
	if !got[0].Amount.Add(got[1].Amount).Equal(decimal.RequireFromString("100.00")) {
		t.Fatal("by_days residual must keep the total")
	}
}

func TestSpreadAccrual_RejectsBadInput(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	one := decimal.NewFromInt(1)
	tests := []struct {
		name   string
		total  decimal.Decimal
		scheme domain.AccrualScheme
	}{
		{"zero occurrences", one, domain.AccrualScheme{Occurrences: 0, Spread: domain.SpreadEqual}},
		{"unknown spread", one, domain.AccrualScheme{Occurrences: 1, Spread: "weekly"}},
		{"custom length mismatch", one, domain.AccrualScheme{Occurrences: 2, Spread: domain.SpreadCustom, Custom: []decimal.Decimal{one}}},
		{"custom does not sum", one, domain.AccrualScheme{Occurrences: 2, Spread: domain.SpreadCustom, Custom: []decimal.Decimal{decimal.RequireFromString("0.40"), decimal.RequireFromString("0.40")}}},
	}
	for _, tc := range tests {
		if _, err := domain.SpreadAccrual(tc.total, start, tc.scheme, 2); code(err) != domain.CodeValidation {
			t.Errorf("%s: got %v, want %s", tc.name, err, domain.CodeValidation)
		}
	}
}

func TestSpreadAccrual_MonthEndStartStaysOnMonthEnds(t *testing.T) {
	start := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	got, err := domain.SpreadAccrual(decimal.RequireFromString("300.00"), start, domain.AccrualScheme{Occurrences: 4, Spread: domain.SpreadEqual, AutoReverse: true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	wantDates := []string{"2026-01-31", "2026-02-28", "2026-03-31", "2026-04-30"}
	wantReverse := []string{"2026-02-28", "2026-03-31", "2026-04-30", "2026-05-31"}
	for i, o := range got {
		if o.Date.Format("2006-01-02") != wantDates[i] || o.ReverseOn.Format("2006-01-02") != wantReverse[i] {
			t.Errorf("occurrence %d on %s reversing %s, want %s and %s", i, o.Date.Format("2006-01-02"), o.ReverseOn.Format("2006-01-02"), wantDates[i], wantReverse[i])
		}
	}
}

func TestSpreadAccrual_MidMonthStartClampsToShortMonths(t *testing.T) {
	start := time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC)
	got, err := domain.SpreadAccrual(decimal.RequireFromString("90.00"), start, domain.AccrualScheme{Occurrences: 3, Spread: domain.SpreadEqual}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got[1].Date.Format("2006-01-02") != "2026-02-28" || got[2].Date.Format("2006-01-02") != "2026-03-30" {
		t.Fatalf("got %s and %s", got[1].Date.Format("2006-01-02"), got[2].Date.Format("2006-01-02"))
	}
}

func TestSpreadAccrual_RejectsExtraDecimalsAndNegativeResidual(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		total string
		n     int
	}{
		{"more decimals than the currency", "100.005", 2},
		{"residual would be negative", "0.10", 12},
		{"zero total", "0", 2},
	}
	for _, tc := range tests {
		_, err := domain.SpreadAccrual(decimal.RequireFromString(tc.total), start, domain.AccrualScheme{Occurrences: tc.n, Spread: domain.SpreadEqual}, 2)
		if code(err) != domain.CodeValidation {
			t.Errorf("%s: got %v, want %s", tc.name, err, domain.CodeValidation)
		}
	}
}
