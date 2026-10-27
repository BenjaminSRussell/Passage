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

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestComputeTotals_Table(t *testing.T) {
	tests := []struct {
		name     string
		currency string
		lines    []Line
		tax      []string
		charges  string
		gross    string
		net      string
	}{
		{
			name: "dmf-de 100 EUR at 19 percent", currency: "EUR",
			lines: []Line{{No: 1, Qty: d("1"), UnitPrice: d("100"), RevenueAccount: "4900"}},
			tax:   []string{"19.00"}, charges: "0", net: "100.00", gross: "119.00",
		},
		{
			name: "discount rounds once per line", currency: "USD",
			lines: []Line{{No: 1, Qty: d("3"), UnitPrice: d("0.333"), DiscountPct: d("10"), RevenueAccount: "4000"}},
			tax:   []string{"0.09"}, charges: "1.005", net: "0.90", gross: "1.99",
		},
		{
			name: "JPY has no minor unit", currency: "JPY",
			lines: []Line{{No: 1, Qty: d("2"), UnitPrice: d("333.5"), RevenueAccount: "4000"}},
			tax:   []string{"67"}, charges: "0", net: "667", gross: "734",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tax := make([]decimal.Decimal, len(tt.tax))
			for i, s := range tt.tax {
				tax[i] = d(s)
			}
			_, got, err := Compute(tt.currency, tt.lines, tax, d(tt.charges))
			if err != nil {
				t.Fatal(err)
			}
			if !got.Net.Equal(d(tt.net)) || !got.Gross.Equal(d(tt.gross)) {
				t.Errorf("net %s gross %s, want net %s gross %s", got.Net, got.Gross, tt.net, tt.gross)
			}
		})
	}
}

// AC-29.1: gross = Σ line_net + charges + tax with every amount on the minor unit.
func TestComputeTotals_GrossIdentityAcrossCurrencies(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cur := rapid.SampledFrom([]string{"USD", "EUR", "JPY"}).Draw(t, "currency")
		exp := int32(money.MinorUnits(cur))
		n := rapid.IntRange(1, 25).Draw(t, "lines")
		lines := make([]Line, n)
		tax := make([]decimal.Decimal, n)
		for i := range lines {
			qty := decimal.New(rapid.Int64Range(1, 1_000_000).Draw(t, "qty"), -3)
			price := decimal.New(rapid.Int64Range(0, 10_000_000).Draw(t, "price"), -6)
			disc := decimal.New(rapid.Int64Range(0, 100).Draw(t, "disc"), 0)
			lines[i] = Line{No: i + 1, Description: "x", Qty: qty, UnitPrice: price, DiscountPct: disc, RevenueAccount: "4000"}
			tax[i] = decimal.New(rapid.Int64Range(0, 9_999_999).Draw(t, "tax"), -6)
		}
		charges := decimal.New(rapid.Int64Range(0, 100_000).Draw(t, "charges"), -4)
		nets, got, err := Compute(cur, lines, tax, charges)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, net := range nets {
			if !net.Equal(net.RoundBank(exp)) {
				t.Fatalf("line net %s is off the minor unit of %s", net, cur)
			}
			sum = sum.Add(net)
		}
		if !sum.Equal(got.Net) {
			t.Fatalf("net %s != sum of lines %s", got.Net, sum)
		}
		if !got.Gross.Equal(got.Net.Add(got.Charges).Add(got.Tax)) {
			t.Fatalf("gross %s != net+charges+tax", got.Gross)
		}
		if !got.Gross.Equal(got.Gross.RoundBank(exp)) {
			t.Fatalf("gross %s is off the minor unit", got.Gross)
		}
	})
}

func TestComputeTotals_TaxLengthMismatch(t *testing.T) {
	_, _, err := Compute("USD", []Line{{Qty: d("1"), UnitPrice: d("1")}}, nil, d("0"))
	var be *Error
	if !errors.As(err, &be) || be.Code != "BIL-0001" {
		t.Fatalf("got %v, want BIL-0001", err)
	}
}

func TestLineValidate(t *testing.T) {
	ok := Line{Description: "Widget", Qty: d("1"), UnitPrice: d("5"), RevenueAccount: "4000", TaxCode: "DE19"}
	tests := []struct {
		name string
		mut  func(l *Line)
		code string
	}{
		{"valid", func(*Line) {}, ""},
		{"no account", func(l *Line) { l.RevenueAccount = "" }, "BIL-0004"},
		{"zero qty", func(l *Line) { l.Qty = d("0") }, "COM-0001"},
		{"negative price", func(l *Line) { l.UnitPrice = d("-1") }, "COM-0001"},
		{"discount over 100", func(l *Line) { l.DiscountPct = d("101") }, "COM-0001"},
		{"no tax code", func(l *Line) { l.TaxCode = "" }, "COM-0001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := ok
			tt.mut(&l)
			err := l.Validate()
			if tt.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var de *Error
			if !errors.As(err, &de) || de.Code != tt.code {
				t.Fatalf("got %v, want %s", err, tt.code)
			}
		})
	}
}

func TestTaxMoved(t *testing.T) {
	if TaxMoved(d("19.00"), d("19.01")) {
		t.Error("a move of exactly 0.01 is within tolerance")
	}
	if !TaxMoved(d("19.00"), d("19.02")) {
		t.Error("a move above 0.01 must require review")
	}
}

func TestCanReverse_BR29_7(t *testing.T) {
	posted := day("2027-03-10")
	tests := []struct {
		name    string
		status  string
		today   string
		settled int
		allowed bool
	}{
		{"same day, no settlements", StatusPosted, "2027-03-10", 0, true},
		{"next day", StatusPosted, "2027-03-11", 0, false},
		{"same day, settled", StatusPosted, "2027-03-10", 1, false},
		{"already reversed", StatusReversed, "2027-03-10", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CanReverse(tt.status, posted, day(tt.today), tt.settled)
			if (err == nil) != tt.allowed {
				t.Fatalf("allowed=%v, err=%v", tt.allowed, err)
			}
		})
	}
}

func TestLineNet_RoundingIsHalfEven(t *testing.T) {
	// 0.125 USD at quantity 1 rounds half-even to 0.12; 0.135 rounds to 0.14.
	if got := (Line{Qty: d("1"), UnitPrice: d("0.125")}).Net("USD"); !got.Equal(d("0.12")) {
		t.Errorf("0.125 -> %s, want 0.12", got)
	}
	if got := (Line{Qty: d("1"), UnitPrice: d("0.135")}).Net("USD"); !got.Equal(d("0.14")) {
		t.Errorf("0.135 -> %s, want 0.14", got)
	}
}
