package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func period(id, start, end string, qty, used int64) QuotaPeriod {
	return QuotaPeriod{ID: id, Start: day(start), End: day(end), Quantity: decimal.NewFromInt(qty), Consumed: decimal.NewFromInt(used)}
}

func TestPlanAllocation(t *testing.T) {
	march := period("mar", "2027-03-01", "2027-03-31", 100, 0)
	april := period("apr", "2027-04-01", "2027-04-30", 100, 0)
	may := period("may", "2027-05-01", "2027-05-31", 100, 100)
	june := period("jun", "2027-06-01", "2027-06-30", 100, 40)
	cases := []struct {
		name    string
		periods []QuotaPeriod
		req     string
		qty     int64
		outcome string
		quota   string
		date    string
	}{
		{"fits the requested period", []QuotaPeriod{march, april}, "2027-03-15", 100, AllocationRequested, "mar", "2027-03-15"},
		{"exhausted period spills to the next", []QuotaPeriod{period("mar", "2027-03-01", "2027-03-31", 100, 90), april}, "2027-03-15", 20, AllocationNextPeriod, "apr", "2027-04-01"},
		{"skips a full period", []QuotaPeriod{period("apr", "2027-04-01", "2027-04-30", 100, 100), may, june}, "2027-04-10", 50, AllocationNextPeriod, "jun", "2027-06-01"},
		{"nothing left anywhere", []QuotaPeriod{may, june}, "2027-05-05", 80, AllocationExhausted, "", ""},
		{"no quota covers the date", []QuotaPeriod{march, april}, "2027-02-10", 5, AllocationUnconstrained, "", ""},
		{"gap between periods is unconstrained", []QuotaPeriod{march, june}, "2027-04-10", 500, AllocationUnconstrained, "", ""},
		{"last day of period counts", []QuotaPeriod{march}, "2027-03-31", 100, AllocationRequested, "mar", "2027-03-31"},
		{"one more than remaining spills", []QuotaPeriod{june, period("jul", "2027-07-01", "2027-07-31", 10, 0)}, "2027-06-30", 61, AllocationExhausted, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanAllocation(tc.periods, day(tc.req), decimal.NewFromInt(tc.qty))
			if got.Outcome != tc.outcome || got.QuotaID != tc.quota {
				t.Fatalf("plan = %+v, want %s %q", got, tc.outcome, tc.quota)
			}
			if tc.date != "" && got.ConfirmDate.Format("2006-01-02") != tc.date {
				t.Fatalf("confirm date %s, want %s", got.ConfirmDate.Format("2006-01-02"), tc.date)
			}
		})
	}
}

func TestValidateQuotaChange(t *testing.T) {
	if err := ValidateQuotaChange(decimal.NewFromInt(60), decimal.NewFromInt(60)); err != nil {
		t.Fatalf("equal to consumed is allowed: %v", err)
	}
	if err := ValidateQuotaChange(decimal.NewFromInt(59), decimal.NewFromInt(60)); !errors.Is(err, ErrQuotaBelowConsumed) {
		t.Fatalf("below consumed: %v", err)
	}
	if err := ValidateQuotaChange(decimal.Zero, decimal.Zero); err == nil {
		t.Fatal("zero quota must be refused")
	}
}

func TestValidateQuotaPeriod(t *testing.T) {
	if f := ValidateQuotaPeriod(day("2027-03-01"), day("2027-03-31"), decimal.NewFromInt(5)); len(f) != 0 {
		t.Fatalf("valid: %v", f)
	}
	if f := ValidateQuotaPeriod(day("2027-03-31"), day("2027-03-01"), decimal.NewFromInt(-1)); len(f) != 2 {
		t.Fatalf("invalid: %v", f)
	}
}

func TestQuotaQuantityUpperBound(t *testing.T) {
	big := decimal.New(1, 14)
	if f := ValidateQuotaPeriod(day("2027-03-01"), day("2027-03-31"), big); f["quotaQty"] == "" {
		t.Fatalf("10^14 does not fit numeric(20,6) and must be a field error: %v", f)
	}
	if err := ValidateQuotaChange(big, decimal.Zero); err == nil || errors.Is(err, ErrQuotaBelowConsumed) {
		t.Fatalf("10^14 change must be refused as invalid, not below consumed: %v", err)
	}
	if f := ValidateQuotaPeriod(day("2027-03-01"), day("2027-03-31"), big.Sub(decimal.New(1, -6))); len(f) != 0 {
		t.Fatalf("the largest storable quantity is valid: %v", f)
	}
}
