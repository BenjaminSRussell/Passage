package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// AC-29.10: 3 installments 30/30/40 at +0/+30/+60 days on 1,000.01 USD.
func TestSplitInstallments_AC29_10(t *testing.T) {
	terms := []InstallmentTerm{
		{Percent: d("30"), OffsetDays: 0},
		{Percent: d("30"), OffsetDays: 30},
		{Percent: d("40"), OffsetDays: 60},
	}
	got, err := SplitInstallments("USD", d("1000.01"), day("2027-03-10"), terms)
	if err != nil {
		t.Fatal(err)
	}
	wantAmounts := []string{"300.00", "300.00", "400.01"}
	wantDue := []string{"2027-03-10", "2027-04-09", "2027-05-09"}
	for i, inst := range got {
		if !inst.Amount.Equal(d(wantAmounts[i])) {
			t.Errorf("installment %d amount %s, want %s", inst.No, inst.Amount, wantAmounts[i])
		}
		if inst.DueDate.Format("2006-01-02") != wantDue[i] {
			t.Errorf("installment %d due %s, want %s", inst.No, inst.DueDate.Format("2006-01-02"), wantDue[i])
		}
	}
}

func TestSplitInstallments_PercentageMustTotal100(t *testing.T) {
	_, err := SplitInstallments("USD", d("10"), day("2027-03-10"), []InstallmentTerm{{Percent: d("50")}, {Percent: d("40")}})
	var de *Error
	if !errors.As(err, &de) || de.Code != "BIL-0011" {
		t.Fatalf("got %v, want BIL-0011", err)
	}
}

func TestSplitInstallments_SumsToGross(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cur := rapid.SampledFrom([]string{"USD", "EUR", "JPY"}).Draw(t, "currency")
		gross := money.Must(decimal.New(rapid.Int64Range(1, 10_000_000_000).Draw(t, "minor"), -2).String(), cur).Value
		n := rapid.IntRange(1, 6).Draw(t, "installments")
		terms := make([]InstallmentTerm, n)
		left := 100
		for i := range terms {
			pct := left
			if i < n-1 {
				pct = rapid.IntRange(1, left-(n-1-i)).Draw(t, "pct")
			}
			left -= pct
			terms[i] = InstallmentTerm{Percent: decimal.NewFromInt(int64(pct)), OffsetDays: i * 30}
		}
		got, err := SplitInstallments(cur, gross, day("2027-03-10"), terms)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, inst := range got {
			sum = sum.Add(inst.Amount)
		}
		if !sum.Equal(gross) {
			t.Fatalf("installments sum to %s, gross %s", sum, gross)
		}
	})
}

// AC-29.7: 300.00 EUR monthly from 2028-02-10 (leap year) bills 20 of 29 days.
func TestProrate_Table(t *testing.T) {
	eur300 := money.Must("300", "EUR")
	usd100 := money.Must("100", "USD")
	tests := []struct {
		name   string
		amount money.Amount
		pFrom  string
		pTo    string
		sFrom  string
		sTo    string
		want   string
	}{
		{"AC-29.7 mid-period start, leap February", eur300, "2028-02-01", "2028-02-29", "2028-02-10", "2028-02-29", "206.90"},
		{"leap day only", eur300, "2028-02-01", "2028-02-29", "2028-02-29", "2028-02-29", "10.34"},
		{"non-leap February, start on the 15th", money.Must("300", "EUR"), "2027-02-01", "2027-02-28", "2027-02-15", "2027-02-28", "150.00"},
		{"cancel mid-month", eur300, "2027-02-01", "2027-02-28", "2027-02-01", "2027-02-14", "150.00"},
		{"full period", usd100, "2027-04-01", "2027-04-30", "2027-04-01", "2027-04-30", "100.00"},
		{"no overlap", usd100, "2027-04-01", "2027-04-30", "2027-05-01", "2027-05-31", "0.00"},
		{"cancel effective on the last day of a 31-day month", usd100, "2027-03-01", "2027-03-31", "2027-03-01", "2027-03-31", "100.00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Prorate(tt.amount, day(tt.pFrom), day(tt.pTo), day(tt.sFrom), day(tt.sTo))
			if err != nil {
				t.Fatal(err)
			}
			if got.Value.StringFixed(2) != tt.want {
				t.Errorf("got %s, want %s", got.Value.StringFixed(2), tt.want)
			}
		})
	}
}

func TestProrate_StaysWithinPeriodAmount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		start := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)
		periodEnd := start.AddDate(0, 1, -1)
		from := start.AddDate(0, 0, rapid.IntRange(0, 30).Draw(t, "from"))
		to := from.AddDate(0, 0, rapid.IntRange(0, 30).Draw(t, "span"))
		amount := money.Must("999.99", "EUR")
		got, err := Prorate(amount, start, periodEnd, from, to)
		if err != nil {
			t.Fatal(err)
		}
		if got.Value.IsNegative() || got.Value.GreaterThan(amount.Value) {
			t.Fatalf("prorated %s outside [0, %s]", got.Value, amount.Value)
		}
	})
}
