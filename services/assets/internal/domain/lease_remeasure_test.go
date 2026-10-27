package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// workedLease is the PRD FR-37.10 lease: USD 10,000 a year in arrears for 5
// years at 5%.
func workedLease(t *testing.T) (LeaseTerms, LeaseSchedule) {
	t.Helper()
	terms := LeaseTerms{Payment: d("10000"), PaymentsPerYear: 1, TermMonths: 60, AnnualRate: d("0.05"), Currency: "USD"}
	s, err := ComputeLease(terms)
	if err != nil {
		t.Fatal(err)
	}
	return terms, s
}

// carryingAfter is the ROU carrying amount after the first paid periods.
func carryingAfter(rou decimal.Decimal, lines []LeaseLine, paid int) decimal.Decimal {
	for _, l := range lines[:paid] {
		rou = rou.Sub(l.ROUDepreciation)
	}
	return rou
}

func TestRemeasure_PaymentIncreaseAfterYearTwo(t *testing.T) {
	// BR-37.8: a payment change only keeps the original rate. After Y2 the
	// liability is 27,232.49 and the ROU carrying amount 43,294.77 - 2 x 8,658.95.
	terms, s := workedLease(t)
	terms.Payment = d("12000")
	carrying := carryingAfter(s.ROUAsset, s.Lines, 2)
	if carrying.StringFixed(2) != "25976.87" {
		t.Fatalf("carrying ROU = %s", carrying)
	}
	rm, err := Remeasure(terms, 2, s.Lines[2].Opening, s.Lines[2].Opening, carrying)
	if err != nil {
		t.Fatal(err)
	}
	// 12,000 x (1 - 1.05^-3) / 0.05 = 32,678.98
	if rm.NewLiability.StringFixed(2) != "32678.98" || rm.Adjustment.StringFixed(2) != "5446.49" {
		t.Fatalf("new liability %s adjustment %s", rm.NewLiability, rm.Adjustment)
	}
	if rm.ROUAsset.StringFixed(2) != "31423.36" || !rm.ROUAdjustment.Equal(rm.Adjustment) || !rm.ProfitLossExcess.IsZero() {
		t.Fatalf("ROU %s ROU adjustment %s excess %s", rm.ROUAsset, rm.ROUAdjustment, rm.ProfitLossExcess)
	}
	first := rm.Lines[0]
	if first.PeriodNo != 3 || first.Opening.StringFixed(2) != "27232.49" || first.Remeasurement.StringFixed(2) != "5446.49" {
		t.Fatalf("first line = %+v", first)
	}
	// Y3 interest is on the remeasured balance: 32,678.98 x 5% = 1,633.95.
	if first.Interest.StringFixed(2) != "1633.95" || first.Payment.StringFixed(2) != "12000.00" {
		t.Fatalf("Y3 interest %s payment %s", first.Interest, first.Payment)
	}
	assertChainClears(t, rm.Lines, rm.ROUAsset)
	for _, l := range rm.Lines[1:] {
		if !l.Remeasurement.IsZero() {
			t.Fatalf("period %d carries remeasurement %s", l.PeriodNo, l.Remeasurement)
		}
	}
}

func TestRemeasure_ScopeChangeExtendsTermAtRevisedRate(t *testing.T) {
	// Extending to 7 years at a revised 6%: 5 payments remain after Y2.
	terms, s := workedLease(t)
	terms.TermMonths, terms.AnnualRate = 84, d("0.06")
	carrying := carryingAfter(s.ROUAsset, s.Lines, 2)
	rm, err := Remeasure(terms, 2, s.Lines[2].Opening, s.Lines[2].Opening, carrying)
	if err != nil {
		t.Fatal(err)
	}
	// 10,000 x (1 - 1.06^-5) / 0.06 = 42,123.64
	if len(rm.Lines) != 5 || rm.Lines[4].PeriodNo != 7 || rm.NewLiability.StringFixed(2) != "42123.64" {
		t.Fatalf("%d lines, last period %d, new liability %s", len(rm.Lines), rm.Lines[len(rm.Lines)-1].PeriodNo, rm.NewLiability)
	}
	if !rm.ROUAsset.Equal(carrying.Add(rm.Adjustment)) {
		t.Fatalf("ROU %s != carrying %s + adjustment %s", rm.ROUAsset, carrying, rm.Adjustment)
	}
	assertChainClears(t, rm.Lines, rm.ROUAsset)
}

func TestRemeasure_ROUShortfallHitsProfitLoss(t *testing.T) {
	// BR-37.8: if the ROU would go below zero the excess hits P&L.
	terms, s := workedLease(t)
	terms.Payment = d("1000")
	rm, err := Remeasure(terms, 2, s.Lines[2].Opening, s.Lines[2].Opening, d("1000"))
	if err != nil {
		t.Fatal(err)
	}
	// New liability 1,000 x 2.723248 = 2,723.25; adjustment -24,509.24.
	if rm.NewLiability.StringFixed(2) != "2723.25" || rm.Adjustment.StringFixed(2) != "-24509.24" {
		t.Fatalf("new liability %s adjustment %s", rm.NewLiability, rm.Adjustment)
	}
	if !rm.ROUAsset.IsZero() || rm.ROUAdjustment.StringFixed(2) != "-1000.00" || rm.ProfitLossExcess.StringFixed(2) != "23509.24" {
		t.Fatalf("ROU %s ROU adjustment %s excess %s", rm.ROUAsset, rm.ROUAdjustment, rm.ProfitLossExcess)
	}
	for _, l := range rm.Lines {
		if !l.ROUDepreciation.IsZero() {
			t.Fatalf("period %d depreciates %s of a zero ROU", l.PeriodNo, l.ROUDepreciation)
		}
	}
	assertChainClears(t, rm.Lines, rm.ROUAsset)
}

func TestRemeasure_SecondModificationOfSamePeriodIsIncremental(t *testing.T) {
	// A second modification at the same effective period adjusts from the
	// carrying liability the first one left, while the line's remeasurement is
	// the total change from the unmodified opening.
	terms, s := workedLease(t)
	opening := s.Lines[2].Opening
	carrying := carryingAfter(s.ROUAsset, s.Lines, 2)
	terms.Payment = d("12000")
	first, err := Remeasure(terms, 2, opening, opening, carrying)
	if err != nil {
		t.Fatal(err)
	}
	terms.Payment = d("11000")
	second, err := Remeasure(terms, 2, opening, first.NewLiability, first.ROUAsset)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Adjustment.Equal(second.NewLiability.Sub(first.NewLiability)) || !second.Lines[0].Remeasurement.Equal(second.NewLiability.Sub(opening)) {
		t.Fatalf("adjustment %s, line remeasurement %s", second.Adjustment, second.Lines[0].Remeasurement)
	}
	if !second.ROUAsset.Equal(carrying.Add(second.NewLiability).Sub(opening)) {
		t.Fatalf("ROU %s after two modifications", second.ROUAsset)
	}
}

func TestRemeasure_Rejections(t *testing.T) {
	terms, s := workedLease(t)
	opening := s.Lines[2].Opening
	cases := []struct {
		name   string
		mutate func(*LeaseTerms)
		paid   int
		rou    string
		code   string
	}{
		{"no payment left", func(*LeaseTerms) {}, 5, "100", CodeInvalidLease},
		{"term shortened below payments made", func(l *LeaseTerms) { l.TermMonths = 24 }, 2, "100", CodeInvalidLease},
		{"negative paid", func(*LeaseTerms) {}, -1, "100", CodeInvalidLease},
		{"revised rate above 50%", func(l *LeaseTerms) { l.AnnualRate = d("0.55") }, 2, "100", CodeIBRRange},
		{"zero payment", func(l *LeaseTerms) { l.Payment = d("0") }, 2, "100", CodeInvalidLease},
		{"negative carrying ROU", func(*LeaseTerms) {}, 2, "-1", CodeInvalidLease},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tt := terms
			tc.mutate(&tt)
			_, err := Remeasure(tt, tc.paid, opening, opening, d(tc.rou))
			if ErrorCode(err) != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
		})
	}
}

func TestCanModify(t *testing.T) {
	for status, ok := range map[string]bool{"Active": true, "Modified": true, "Terminated": false, "Draft": false} {
		if err := CanModify(status); (err == nil) != ok {
			t.Errorf("%s: err = %v", status, err)
		}
	}
}

func TestComputeLease_EffectiveRateCompoundsToAnnual(t *testing.T) {
	// lease.periodicRateMethod = Effective: twelve monthly periods compound
	// back to the annual IBR, and the liability is higher than at nominal/12.
	terms := LeaseTerms{Payment: d("1000"), PaymentsPerYear: 12, TermMonths: 12, AnnualRate: d("0.05"), Currency: "USD", RateMethod: RateEffective}
	r := periodicRate(terms)
	if r.StringFixed(10) != "0.0040741238" {
		t.Fatalf("effective monthly rate = %s", r)
	}
	pow := decimal.NewFromInt(1)
	for i := 0; i < 12; i++ {
		pow = pow.Mul(decimal.NewFromInt(1).Add(r))
	}
	if diff := pow.Sub(d("1.05")).Abs(); diff.GreaterThan(d("0.000000000001")) {
		t.Fatalf("(1 + r)^12 = %s, want 1.05", pow)
	}
	eff, err := ComputeLease(terms)
	if err != nil {
		t.Fatal(err)
	}
	terms.RateMethod = ""
	nom, err := ComputeLease(terms)
	if err != nil {
		t.Fatal(err)
	}
	if !eff.Liability.GreaterThan(nom.Liability) {
		t.Fatalf("effective liability %s should exceed nominal %s (a lower monthly rate)", eff.Liability, nom.Liability)
	}
	assertChainClears(t, eff.Lines, eff.ROUAsset)
	annual := LeaseTerms{Payment: d("10000"), PaymentsPerYear: 1, TermMonths: 60, AnnualRate: d("0.05"), Currency: "USD", RateMethod: RateEffective}
	if s, err := ComputeLease(annual); err != nil || s.Liability.StringFixed(2) != "43294.77" {
		t.Fatalf("annual effective = nominal: %v %v", s.Liability, err)
	}
}

func TestComputeLease_UnknownRateMethod(t *testing.T) {
	_, err := ComputeLease(LeaseTerms{Payment: d("1"), PaymentsPerYear: 1, TermMonths: 12, AnnualRate: d("0.05"), Currency: "USD", RateMethod: "Bogus"})
	if ErrorCode(err) != CodeInvalidLease {
		t.Fatalf("err = %v", err)
	}
}

// assertChainClears checks closing(n) = opening(n+1), a zero final closing,
// that the ROU depreciation sums to rou, and that only the first line carries
// a remeasurement.
func assertChainClears(t interface {
	Helper()
	Fatalf(string, ...any)
}, lines []LeaseLine, rou decimal.Decimal) {
	t.Helper()
	dep := decimal.Zero
	for i, l := range lines {
		dep = dep.Add(l.ROUDepreciation)
		if i+1 < len(lines) && !l.Closing.Equal(lines[i+1].Opening) {
			t.Fatalf("period %d closing %s != next opening %s", l.PeriodNo, l.Closing, lines[i+1].Opening)
		}
		if i > 0 && !l.Remeasurement.IsZero() {
			t.Fatalf("period %d carries a remeasurement", l.PeriodNo)
		}
	}
	if last := lines[len(lines)-1]; !last.Closing.IsZero() {
		t.Fatalf("final closing = %s, want 0", last.Closing)
	}
	if !dep.Equal(rou) {
		t.Fatalf("ROU depreciation %s != ROU %s", dep, rou)
	}
}

func TestRemeasure_PropertyScheduleClearsAndInterestBalances(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ppy := []int{1, 2, 4, 12}[rapid.IntRange(0, 3).Draw(t, "ppy")]
		periods := rapid.IntRange(2, 60).Draw(t, "periods")
		terms := LeaseTerms{
			Payment:         decimal.NewFromInt(int64(rapid.IntRange(100, 50000).Draw(t, "payment"))),
			PaymentsPerYear: ppy,
			TermMonths:      periods * 12 / ppy,
			Advance:         rapid.Bool().Draw(t, "advance"),
			AnnualRate:      decimal.NewFromInt(int64(rapid.IntRange(1, 2500).Draw(t, "bps"))).Div(decimal.NewFromInt(10000)),
			Currency:        "USD",
			RateMethod:      []string{RateNominal, RateEffective}[rapid.IntRange(0, 1).Draw(t, "method")],
		}
		if terms.TermMonths > MaxLeaseTermMonths {
			t.Skip("term beyond the limit")
		}
		s, err := ComputeLease(terms)
		if err != nil {
			t.Fatal(err)
		}
		paid := rapid.IntRange(0, periods-1).Draw(t, "paid")
		terms.Payment = decimal.NewFromInt(int64(rapid.IntRange(1, 60000).Draw(t, "newPayment")))
		carrying := carryingAfter(s.ROUAsset, s.Lines, paid)
		opening := s.Lines[paid].Opening
		rm, err := Remeasure(terms, paid, opening, opening, carrying)
		if err != nil {
			t.Fatal(err)
		}
		if len(rm.Lines) != periods-paid || rm.Lines[0].PeriodNo != paid+1 {
			t.Fatalf("%d lines from period %d", len(rm.Lines), rm.Lines[0].PeriodNo)
		}
		if rm.ROUAsset.IsNegative() || rm.ProfitLossExcess.IsNegative() || (!rm.ROUAsset.IsZero() && !rm.ProfitLossExcess.IsZero()) {
			t.Fatalf("ROU %s excess %s", rm.ROUAsset, rm.ProfitLossExcess)
		}
		// The ROU absorbs the adjustment except what P&L takes.
		if !rm.ROUAdjustment.Sub(rm.ProfitLossExcess).Equal(rm.Adjustment) && !rm.ROUAdjustment.Equal(rm.Adjustment) {
			t.Fatalf("ROU adjustment %s, excess %s, adjustment %s", rm.ROUAdjustment, rm.ProfitLossExcess, rm.Adjustment)
		}
		// Σ interest = Σ payments - remeasured liability.
		interest, payments := decimal.Zero, decimal.Zero
		for _, l := range rm.Lines {
			interest, payments = interest.Add(l.Interest), payments.Add(l.Payment)
		}
		if !interest.Equal(payments.Sub(rm.NewLiability)) {
			t.Fatalf("interest %s != payments %s - liability %s", interest, payments, rm.NewLiability)
		}
		assertChainClears(t, rm.Lines, rm.ROUAsset)
	})
}
