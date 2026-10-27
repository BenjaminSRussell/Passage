package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestComputeLease_WorkedExampleArrears(t *testing.T) {
	// FR-37.10 / AC-37.6: annual USD 10,000 in arrears for 5 years at 5%.
	s, err := ComputeLease(LeaseTerms{Payment: d("10000"), PaymentsPerYear: 1, TermMonths: 60, AnnualRate: d("0.05"), Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Liability.StringFixed(2) != "43294.77" {
		t.Fatalf("liability = %s, want 43294.77", s.Liability)
	}
	interest := []string{"2164.74", "1772.98", "1361.62", "929.71", "476.18"}
	closing := []string{"35459.51", "27232.49", "18594.11", "9523.82", "0.00"}
	for i, l := range s.Lines {
		if l.Interest.StringFixed(2) != interest[i] || l.Closing.StringFixed(2) != closing[i] {
			t.Errorf("Y%d interest %s closing %s, want %s / %s", i+1, l.Interest, l.Closing, interest[i], closing[i])
		}
		if i > 0 && !l.Opening.Equal(s.Lines[i-1].Closing) {
			t.Errorf("Y%d opening %s != prior closing", i+1, l.Opening)
		}
	}
	rou := []string{"8658.95", "8658.95", "8658.95", "8658.95", "8658.97"}
	for i, l := range s.Lines {
		if l.ROUDepreciation.StringFixed(2) != rou[i] {
			t.Errorf("Y%d ROU %s, want %s", i+1, l.ROUDepreciation, rou[i])
		}
	}
}

func TestComputeLease_AdvanceClosesToZero(t *testing.T) {
	s, err := ComputeLease(LeaseTerms{Payment: d("1000"), PaymentsPerYear: 12, TermMonths: 36, Advance: true, AnnualRate: d("0.06"), Currency: "EUR"})
	if err != nil {
		t.Fatal(err)
	}
	if last := s.Lines[len(s.Lines)-1]; !last.Closing.IsZero() {
		t.Fatalf("final closing = %s, want 0", last.Closing)
	}
	if s.Lines[0].Payment.StringFixed(2) != "1000.00" {
		t.Fatalf("first payment = %s", s.Lines[0].Payment)
	}
}

func TestComputeLease_Rejections(t *testing.T) {
	base := LeaseTerms{Payment: d("100"), PaymentsPerYear: 12, TermMonths: 12, AnnualRate: d("0.05"), Currency: "USD"}
	cases := []struct {
		name   string
		mutate func(*LeaseTerms)
		code   string
	}{
		{"zero rate", func(l *LeaseTerms) { l.AnnualRate = d("0") }, CodeIBRRange},
		{"rate above 50%", func(l *LeaseTerms) { l.AnnualRate = d("0.51") }, CodeIBRRange},
		{"term not whole", func(l *LeaseTerms) { l.PaymentsPerYear = 4; l.TermMonths = 14 }, CodeInvalidLease},
		{"bad frequency", func(l *LeaseTerms) { l.PaymentsPerYear = 3 }, CodeInvalidLease},
		{"term above 50 years", func(l *LeaseTerms) { l.TermMonths = 612 }, CodeInvalidLease},
		{"zero payment", func(l *LeaseTerms) { l.Payment = d("0") }, CodeInvalidLease},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			terms := base
			c.mutate(&terms)
			_, err := ComputeLease(terms)
			de, ok := err.(*Error)
			if !ok || de.Code != c.code {
				t.Fatalf("err = %v, want %s", err, c.code)
			}
		})
	}
}

func TestComputeLease_Properties(t *testing.T) {
	// Property: last closing is zero, periods chain, Σ interest = Σ payments − liability,
	// Σ ROU depreciation = ROU asset, for random terms.
	rapid.Check(t, func(t *rapid.T) {
		ppy := rapid.SampledFrom([]int{1, 2, 4, 12}).Draw(t, "ppy")
		years := rapid.IntRange(1, 10).Draw(t, "years")
		cents := rapid.Int64Range(100, 10_000_000).Draw(t, "cents")
		bps := rapid.Int64Range(1, 5000).Draw(t, "bps")
		terms := LeaseTerms{Payment: decimal.New(cents, -2), PaymentsPerYear: ppy, TermMonths: years * 12,
			Advance: rapid.Bool().Draw(t, "advance"), AnnualRate: decimal.New(bps, -4), Currency: "USD"}
		s, err := ComputeLease(terms)
		if err != nil {
			t.Fatal(err)
		}
		n := years * ppy
		if len(s.Lines) != n {
			t.Fatalf("lines = %d, want %d", len(s.Lines), n)
		}
		if last := s.Lines[n-1]; !last.Closing.IsZero() {
			t.Fatalf("final closing %s", last.Closing)
		}
		interest, payments, rou := decimal.Zero, decimal.Zero, decimal.Zero
		for i, l := range s.Lines {
			if i > 0 && !l.Opening.Equal(s.Lines[i-1].Closing) {
				t.Fatalf("period %d opening != prior closing", i+1)
			}
			interest = interest.Add(l.Interest)
			payments = payments.Add(l.Payment)
			rou = rou.Add(l.ROUDepreciation)
		}
		if !interest.Equal(payments.Sub(s.Liability)) {
			t.Fatalf("Σ interest %s != Σ payments − liability %s", interest, payments.Sub(s.Liability))
		}
		if !rou.Equal(s.ROUAsset) {
			t.Fatalf("Σ ROU %s != ROU %s", rou, s.ROUAsset)
		}
	})
}
