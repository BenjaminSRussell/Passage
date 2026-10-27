package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/money"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func date(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// bundle is the BR-29.1 example: hardware, installation, 12-month support.
func bundle() []Obligation {
	return []Obligation{
		{ID: "hardware", SSP: d("8000"), SSPSource: "EXPLICIT", Method: "POINT_IN_TIME"},
		{ID: "installation", SSP: d("1500"), SSPSource: "EXPLICIT", Method: "MILESTONE"},
		{ID: "support", SSP: d("2500"), SSPSource: "EXPLICIT", Method: "RATABLE"},
	}
}

func TestAllocate_BR29_1Examples(t *testing.T) {
	tests := []struct {
		name  string
		price string
		want  []string
	}{
		{"exact 10800", "10800.00", []string{"7200.00", "1350.00", "2250.00"}},
		{"residual to largest 10799.99", "10799.99", []string{"7200.01", "1349.99", "2249.99"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Allocate(money.Must(tt.price, "USD"), bundle(), ResidualLargest)
			if err != nil {
				t.Fatal(err)
			}
			for i, w := range tt.want {
				if !got[i].Allocated.Equal(d(w)) {
					t.Errorf("%s allocated %s, want %s", got[i].ID, got[i].Allocated, w)
				}
			}
		})
	}
}

func TestAllocate_ResidualRules(t *testing.T) {
	obs := []Obligation{
		{ID: "a", SSP: d("100")},
		{ID: "b", SSP: d("300")},
		{ID: "c", SSP: d("100")},
	}
	price := money.Must("0.01", "USD")
	tests := []struct {
		rule string
		idx  int
	}{{ResidualFirst, 0}, {ResidualLast, 2}, {ResidualLargest, 1}}
	for _, tt := range tests {
		got, err := Allocate(price, obs, tt.rule)
		if err != nil {
			t.Fatal(err)
		}
		for i := range got {
			if (i == tt.idx) != (got[i].Allocated.Sign() > 0) {
				t.Errorf("rule %s: obligation %d allocated %s", tt.rule, i, got[i].Allocated)
			}
		}
	}
}

func TestAllocate_Errors(t *testing.T) {
	tests := []struct {
		name string
		obs  []Obligation
		rule string
		code string
	}{
		{"no obligations", nil, ResidualLargest, "REV-0001"},
		{"zero ssp", []Obligation{{ID: "a", SSP: d("0")}}, ResidualLargest, "REV-0001"},
		{"bad rule", bundle(), "RANDOM", "COM-0001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Allocate(money.Must("10", "USD"), tt.obs, tt.rule)
			var de *Error
			if !errors.As(err, &de) || de.Code != tt.code {
				t.Fatalf("got %v, want code %s", err, tt.code)
			}
		})
	}
}

func TestAllocate_SumsToPriceAcrossCurrencies(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cur := rapid.SampledFrom([]string{"USD", "EUR", "JPY"}).Draw(t, "currency")
		n := rapid.IntRange(1, 6).Draw(t, "obligations")
		exp := money.MinorUnits(cur)
		price := money.Must(decimal.New(rapid.Int64Range(0, 10_000_000_000).Draw(t, "minor"), -exp).String(), cur)
		obs := make([]Obligation, n)
		for i := range obs {
			obs[i] = Obligation{ID: string(rune('a' + i)), SSP: decimal.New(rapid.Int64Range(1, 1_000_000).Draw(t, "ssp"), 0)}
		}
		got, err := Allocate(price, obs, rapid.SampledFrom([]string{ResidualFirst, ResidualLast, ResidualLargest}).Draw(t, "rule"))
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, o := range got {
			if o.Allocated.IsNegative() {
				t.Fatalf("negative allocation %s", o.Allocated)
			}
			sum = sum.Add(o.Allocated)
		}
		if !sum.Equal(price.Value) {
			t.Fatalf("allocations sum to %s, price %s", sum, price.Value)
		}
	})
}

func TestRatableSchedule_SupportObligation(t *testing.T) {
	// 2250.00 over 2027-03-10 .. 2028-03-09: partial March, eleven full months, partial March 2028.
	periods, err := RatableSchedule(money.Must("2250", "USD"), date("2027-03-10"), date("2028-03-09"))
	if err != nil {
		t.Fatal(err)
	}
	if len(periods) != 13 {
		t.Fatalf("got %d periods, want 13", len(periods))
	}
	if !periods[0].Amount.Equal(d("133.06")) {
		t.Errorf("first period %s, want 133.06", periods[0].Amount)
	}
	for _, p := range periods[1:12] {
		if !p.Amount.Equal(d("187.50")) {
			t.Errorf("full month %s, want 187.50", p.Amount)
		}
	}
	if !periods[12].Amount.Equal(d("54.44")) {
		t.Errorf("last period %s, want 54.44", periods[12].Amount)
	}
	sum := decimal.Zero
	for _, p := range periods {
		sum = sum.Add(p.Amount)
	}
	if !sum.Equal(d("2250")) {
		t.Errorf("schedule sums to %s, want 2250.00", sum)
	}
}

func TestRatableSchedule_LeapFebruaryAndInvalidTerm(t *testing.T) {
	periods, err := RatableSchedule(money.Must("29", "USD"), date("2028-02-29"), date("2028-02-29"))
	if err != nil || len(periods) != 1 || !periods[0].Amount.Equal(d("29")) {
		t.Fatalf("single leap day: %v %v", periods, err)
	}
	if _, err := RatableSchedule(money.Must("1", "USD"), date("2027-03-02"), date("2027-03-01")); err == nil {
		t.Fatal("end before start must fail")
	}
}

func TestRatableSchedule_SumsToAmount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, rapid.IntRange(0, 900).Draw(t, "offset"))
		end := start.AddDate(0, 0, rapid.IntRange(0, 800).Draw(t, "length"))
		amount := money.Must(decimal.New(rapid.Int64Range(1, 999_999_999).Draw(t, "minor"), -2).String(), "EUR")
		periods, err := RatableSchedule(amount, start, end)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, p := range periods {
			sum = sum.Add(p.Amount)
		}
		if !sum.Equal(amount.Value) {
			t.Fatalf("schedule sums to %s, want %s", sum, amount.Value)
		}
	})
}
