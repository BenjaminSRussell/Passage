package domain_test

import (
	"errors"
	"math/rand"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

func line(t testing.TB, qty, price, unit, pct, tax string) domain.LineInput {
	return domain.LineInput{Qty: dec(t, qty), UnitPrice: dec(t, price), PriceUnit: dec(t, unit), DiscountPct: dec(t, pct), Tax: dec(t, tax)}
}

func TestComputeLine_RoundsOnceHalfEven(t *testing.T) {
	tests := []struct {
		name                           string
		in                             domain.LineInput
		minor                          int32
		gross, discount, amount, taxed string
	}{
		{"half-even ties go to the even cent", line(t, "1", "0.125", "1", "0", "0"), 2, "0.12", "0", "0.12", "0"},
		{"discount is gross minus net", line(t, "10", "1.00", "1", "10", "0"), 2, "10", "1", "9", "0"},
		{"price per 100 units", line(t, "1000", "12.3456", "100", "0", "0"), 2, "123.46", "0", "123.46", "0"},
		{"tax is rounded to the minor unit", line(t, "1", "1", "1", "0", "0.505"), 2, "1", "0", "1", "0.5"},
		{"zero minor units (JPY)", line(t, "3", "100.5", "1", "0", "0"), 0, "302", "0", "302", "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domain.ComputeLine(tc.in, tc.minor)
			if err != nil {
				t.Fatal(err)
			}
			for field, pair := range map[string][2]decimal.Decimal{
				"gross":    {got.Gross, dec(t, tc.gross)},
				"discount": {got.Discount, dec(t, tc.discount)},
				"amount":   {got.Amount, dec(t, tc.amount)},
				"tax":      {got.Tax, dec(t, tc.taxed)},
			} {
				if !pair[0].Equal(pair[1]) {
					t.Errorf("%s = %s, want %s", field, pair[0], pair[1])
				}
			}
		})
	}
}

func TestComputeLine_RejectsInvalidInput(t *testing.T) {
	for name, in := range map[string]domain.LineInput{
		"zero qty":      line(t, "0", "1", "1", "0", "0"),
		"negative cost": line(t, "1", "-1", "1", "0", "0"),
		"zero unit":     line(t, "1", "1", "0", "0", "0"),
		"discount 101":  line(t, "1", "1", "1", "101", "0"),
	} {
		_, err := domain.ComputeLine(in, 2)
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeValidation {
			t.Errorf("%s: got %v, want %s", name, err, domain.CodeValidation)
		}
	}
}

func TestChargeAmount(t *testing.T) {
	base, qty := dec(t, "200.00"), dec(t, "10")
	tests := []struct {
		calc, value, want string
	}{
		{domain.CalcFixed, "5", "5"},
		{domain.CalcPerUnit, "0.10", "1"},
		{domain.CalcPercent, "2.5", "5"},
	}
	for _, tc := range tests {
		got, err := domain.ChargeAmount(domain.ChargeInput{Calc: tc.calc, Value: dec(t, tc.value)}, qty, base, 2)
		if err != nil || !got.Equal(dec(t, tc.want)) {
			t.Errorf("%s %s: got %s, %v; want %s", tc.calc, tc.value, got, err, tc.want)
		}
	}
	if _, err := domain.ChargeAmount(domain.ChargeInput{Calc: "BOGUS"}, qty, base, 2); err == nil {
		t.Error("unknown calculation accepted")
	}
}

func TestPrice_HeaderAndLinesAgree(t *testing.T) {
	doc, err := domain.Price(
		[]domain.LineInput{line(t, "10", "1.00", "1", "10", "0.90"), line(t, "2", "50", "1", "0", "0")},
		[]domain.ChargeInput{{Calc: domain.CalcFixed, Value: dec(t, "5")}},
		2)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Subtotal": "110", "DiscountTotal": "1", "ChargesTotal": "5", "TaxTotal": "0.90", "Total": "114.90"}
	got := map[string]decimal.Decimal{"Subtotal": doc.Totals.Subtotal, "DiscountTotal": doc.Totals.DiscountTotal,
		"ChargesTotal": doc.Totals.ChargesTotal, "TaxTotal": doc.Totals.TaxTotal, "Total": doc.Totals.Total}
	for k, v := range want {
		if !got[k].Equal(dec(t, v)) {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
}

// TestPrice_LineCharges prices charges on one line (FR-17.9): PER_UNIT takes that line's quantity and
// PERCENT its amount after discount, and they count in the charges total.
func TestPrice_LineCharges(t *testing.T) {
	lines := []domain.LineInput{line(t, "10", "1.00", "1", "10", "0"), line(t, "2", "50", "1", "0", "0")}
	doc, err := domain.Price(lines, []domain.ChargeInput{
		{Calc: domain.CalcPercent, Value: dec(t, "10"), Line: 1},   // 10% of 9.00
		{Calc: domain.CalcPerUnit, Value: dec(t, "1.50"), Line: 2}, // 2 x 1.50
		{Calc: domain.CalcPercent, Value: dec(t, "1")},             // 1% of 109.00 (header)
		{Calc: domain.CalcFixed, Value: dec(t, "4"), Line: 2},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"0.90", "3.00", "1.09", "4.00"} {
		if !doc.Charges[i].Equal(dec(t, want)) {
			t.Errorf("charge %d = %s, want %s", i, doc.Charges[i], want)
		}
	}
	if !doc.Totals.ChargesTotal.Equal(dec(t, "8.99")) || !doc.Totals.Total.Equal(dec(t, "117.99")) {
		t.Errorf("charges %s total %s, want 8.99 and 117.99", doc.Totals.ChargesTotal, doc.Totals.Total)
	}
	for _, bad := range []int{-1, 3} {
		if _, err := domain.Price(lines, []domain.ChargeInput{{Calc: domain.CalcFixed, Value: dec(t, "1"), Line: bad}}, 2); err == nil {
			t.Errorf("a charge on line %d of 2 should be refused", bad)
		}
	}
}

// TestPrice_TotalsIdentityOver1000RandomPOs is AC4 and AC5: for 1,000 seeded
// random POs with 1 to 50 lines, charges and tax, the total equals
// subtotal − discount + charges + tax to the cent and equals the sum of parts.
func TestPrice_TotalsIdentityOver1000RandomPOs(t *testing.T) {
	r := rand.New(rand.NewSource(17))
	calcs := []string{domain.CalcFixed, domain.CalcPerUnit, domain.CalcPercent}
	units := []int64{1, 100, 1000}
	for po := 0; po < 1000; po++ {
		lines := make([]domain.LineInput, 1+r.Intn(50))
		for i := range lines {
			lines[i] = domain.LineInput{
				Qty:         decimal.New(int64(1+r.Intn(100_000)), -3),
				UnitPrice:   decimal.New(int64(r.Intn(10_000_000)), -4),
				PriceUnit:   decimal.NewFromInt(units[r.Intn(len(units))]),
				DiscountPct: decimal.New(int64(r.Intn(10_001)), -2),
				Tax:         decimal.New(int64(r.Intn(1_000_000)), -2),
			}
		}
		charges := make([]domain.ChargeInput, r.Intn(4))
		for i := range charges {
			charges[i] = domain.ChargeInput{Calc: calcs[r.Intn(len(calcs))], Value: decimal.New(int64(r.Intn(100_000)), -2),
				Line: r.Intn(len(lines) + 1)}
		}
		doc, err := domain.Price(lines, charges, 2)
		if err != nil {
			t.Fatalf("po %d: %v", po, err)
		}
		tot := doc.Totals
		identity := tot.Subtotal.Sub(tot.DiscountTotal).Add(tot.ChargesTotal).Add(tot.TaxTotal)
		if !identity.Equal(tot.Total) {
			t.Fatalf("po %d: total %s, identity %s", po, tot.Total, identity)
		}
		sum := decimal.Zero
		for _, la := range doc.Lines {
			if !la.Gross.Sub(la.Discount).Equal(la.Amount) {
				t.Fatalf("po %d: gross %s - discount %s != amount %s", po, la.Gross, la.Discount, la.Amount)
			}
			sum = sum.Add(la.Amount).Add(la.Tax)
		}
		for _, c := range doc.Charges {
			sum = sum.Add(c)
		}
		if !sum.Equal(tot.Total) {
			t.Fatalf("po %d: sum of parts %s != total %s", po, sum, tot.Total)
		}
		for _, v := range []decimal.Decimal{tot.Subtotal, tot.DiscountTotal, tot.ChargesTotal, tot.TaxTotal, tot.Total} {
			if !v.Equal(v.Round(2)) {
				t.Fatalf("po %d: %s is not to the cent", po, v)
			}
		}
	}
}
