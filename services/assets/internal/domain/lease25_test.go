package domain

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

type lease25Line struct {
	Period          int    `json:"period"`
	Opening         string `json:"opening"`
	Interest        string `json:"interest"`
	Payment         string `json:"payment"`
	Closing         string `json:"closing"`
	ROUDepreciation string `json:"rouDepreciation"`
}

type lease25 struct {
	Name            string        `json:"name"`
	Currency        string        `json:"currency"`
	Payment         string        `json:"payment"`
	PaymentsPerYear int           `json:"paymentsPerYear"`
	TermMonths      int           `json:"termMonths"`
	Timing          string        `json:"timing"`
	IBRRate         string        `json:"ibrRate"`
	RateMethod      string        `json:"rateMethod"`
	Liability       string        `json:"liability"`
	ROUAsset        string        `json:"rouAsset"`
	TotalInterest   string        `json:"totalInterest"`
	Lines           []lease25Line `json:"lines"`
}

// TestLease25Fixtures_MatchTheIndependentWorkbook checks 25 IFRS 16 schedules
// (arrears and advance; 1, 2, 4 and 12 payments a year; Nominal and Effective
// rates; USD, EUR, GBP, JPY, KWD; 1 to 600 payments) line by line against
// testdata/lease-25.json, computed by testdata/gen_lease25.py with exact
// rationals and no code shared with the engine. It is the workbook candidate
// for the Wave 37 proof "schedules match accountant-reviewed workbook under
// IFRS 16"; the accountant's review of that workbook is a human step this
// test does not claim.
func TestLease25Fixtures_MatchTheIndependentWorkbook(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/lease-25.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Leases []lease25 `json:"leases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Leases) != 25 {
		t.Fatalf("fixture has %d leases, want 25", len(fx.Leases))
	}
	for _, l := range fx.Leases {
		t.Run(l.Name, func(t *testing.T) {
			s, err := ComputeLease(LeaseTerms{Payment: d(l.Payment), PaymentsPerYear: l.PaymentsPerYear, TermMonths: l.TermMonths,
				Advance: l.Timing == "advance", AnnualRate: d(l.IBRRate), Currency: l.Currency, RateMethod: l.RateMethod})
			if err != nil {
				t.Fatal(err)
			}
			minor := money.MinorUnits(l.Currency)
			if s.Liability.StringFixed(minor) != l.Liability || s.ROUAsset.StringFixed(minor) != l.ROUAsset {
				t.Fatalf("liability %s / ROU %s, want %s / %s", s.Liability.StringFixed(minor), s.ROUAsset.StringFixed(minor), l.Liability, l.ROUAsset)
			}
			if len(s.Lines) != len(l.Lines) {
				t.Fatalf("%d periods, want %d", len(s.Lines), len(l.Lines))
			}
			interest := decimal.Zero
			for i, g := range s.Lines {
				w := l.Lines[i]
				got := lease25Line{Period: g.PeriodNo, Opening: g.Opening.StringFixed(minor), Interest: g.Interest.StringFixed(minor),
					Payment: g.Payment.StringFixed(minor), Closing: g.Closing.StringFixed(minor), ROUDepreciation: g.ROUDepreciation.StringFixed(minor)}
				if got != w {
					t.Errorf("period %d: got %+v, want %+v", w.Period, got, w)
				}
				interest = interest.Add(g.Interest)
			}
			if interest.StringFixed(minor) != l.TotalInterest {
				t.Errorf("Σ interest %s, want %s", interest.StringFixed(minor), l.TotalInterest)
			}
		})
	}
}
