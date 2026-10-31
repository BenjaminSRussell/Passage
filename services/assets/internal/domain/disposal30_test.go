package domain

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/money"
)

type disposalStep struct {
	Kind                 DisposalKind `json:"kind"`
	Percentage           string       `json:"percentage"`
	Proceeds             string       `json:"proceeds"`
	DisposedCost         string       `json:"disposedCost"`
	DisposedAccumulated  string       `json:"disposedAccumulated"`
	DisposedImpairment   string       `json:"disposedImpairment"`
	NBVDisposed          string       `json:"nbvDisposed"`
	GainLoss             string       `json:"gainLoss"`
	RemainingCost        string       `json:"remainingCost"`
	RemainingAccumulated string       `json:"remainingAccumulated"`
	RemainingImpairment  string       `json:"remainingImpairment"`
}

type disposalBook struct {
	Name        string         `json:"name"`
	Currency    string         `json:"currency"`
	Cost        string         `json:"cost"`
	Accumulated string         `json:"accumulated"`
	Impairment  string         `json:"impairment"`
	Steps       []disposalStep `json:"steps"`
}

// TestDisposal30Fixtures_GainLossAcrossPartialDisposals is the Wave 37 proof
// "Disposal gain/loss posting correct across partial disposals" on the
// subledger arithmetic: 30 books (USD, EUR, JPY, KWD), 51 partial steps and
// the final disposals, each step's disposed cost, accumulated depreciation,
// impairment share, NBV, gain or loss, remaining balances and resulting status
// equal testdata/disposal-30.json (computed by testdata/gen_disposal30.py,
// which shares no code with this package).
func TestDisposal30Fixtures_GainLossAcrossPartialDisposals(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/disposal-30.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Books []disposalBook `json:"books"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Books) != 30 {
		t.Fatalf("fixture has %d books, want 30", len(fx.Books))
	}
	partial := 0
	for _, b := range fx.Books {
		t.Run(b.Name, func(t *testing.T) {
			minor := money.MinorUnits(b.Currency)
			cost, acc, imp := d(b.Cost), d(b.Accumulated), d(b.Impairment)
			status := StatusOpen
			for i, s := range b.Steps {
				pct := d(s.Percentage)
				res, err := Dispose(cost, acc, d(s.Proceeds), pct, b.Currency)
				if err != nil {
					t.Fatalf("step %d: %v", i+1, err)
				}
				di := DisposedShare(imp, pct, b.Currency)
				got := map[string]string{
					"disposedCost": res.DisposedCost.StringFixed(minor), "disposedAccumulated": res.DisposedAccumulated.StringFixed(minor),
					"disposedImpairment": di.StringFixed(minor), "nbvDisposed": res.NBVDisposed.StringFixed(minor),
					"gainLoss": res.GainLoss.StringFixed(minor), "remainingCost": res.RemainingCost.StringFixed(minor),
					"remainingAccumulated": res.RemainingAccumulated.StringFixed(minor), "remainingImpairment": imp.Sub(di).StringFixed(minor),
				}
				want := map[string]string{
					"disposedCost": s.DisposedCost, "disposedAccumulated": s.DisposedAccumulated, "disposedImpairment": s.DisposedImpairment,
					"nbvDisposed": s.NBVDisposed, "gainLoss": s.GainLoss, "remainingCost": s.RemainingCost,
					"remainingAccumulated": s.RemainingAccumulated, "remainingImpairment": s.RemainingImpairment,
				}
				for k, w := range want {
					if got[k] != w {
						t.Errorf("step %d (%s %s%%): %s = %s, want %s", i+1, s.Kind, s.Percentage, k, got[k], w)
					}
				}
				if status, err = AfterDisposal(status, pct, s.Kind); err != nil {
					t.Fatalf("step %d status: %v", i+1, err)
				}
				wantStatus := StatusOpen
				if s.Percentage == "100" {
					wantStatus = map[DisposalKind]AssetStatus{DisposalSale: StatusSold, DisposalScrap: StatusScrapped}[s.Kind]
				} else {
					partial++
				}
				if status != wantStatus {
					t.Errorf("step %d: status %s, want %s", i+1, status, wantStatus)
				}
				// The remaining book keeps 0 <= impairment <= accumulated <= cost.
				cost, acc, imp = res.RemainingCost, res.RemainingAccumulated, imp.Sub(di)
				if imp.IsNegative() || imp.GreaterThan(acc) || acc.GreaterThan(cost) {
					t.Errorf("step %d leaves cost %s, accumulated %s, impairment %s", i+1, cost, acc, imp)
				}
			}
		})
	}
	if partial < 30 {
		t.Errorf("only %d partial disposal steps exercised", partial)
	}
}

// TestDispose_HandDerivedPartialChain anchors the rule with literals worked by
// hand: cost 10,000.00, accumulated 4,000.00 (1,000.00 of it impairment).
//   - sell 25% for 2,000.00: disposed cost 2,500.00, accumulated 1,000.00,
//     impairment 250.00, NBV 1,500.00, gain 500.00; left 7,500.00 / 3,000.00 / 750.00.
//   - scrap 33.33% of that: cost 2,499.75, accumulated 999.90, impairment
//     249.98 (249.975 half-up), NBV 1,499.85, loss -1,499.85; left 5,000.25 /
//     2,000.10 / 500.02.
//   - sell the rest (100%) for 3,500.00: NBV 3,000.15, gain 499.85.
//
// Σ gain/loss = 500.00 - 1,499.85 + 499.85 = -500.00 = Σ proceeds 5,500.00 -
// NBV at the start 6,000.00.
func TestDispose_HandDerivedPartialChain(t *testing.T) {
	type step struct{ pct, proceeds, dc, da, di, nbv, gl string }
	steps := []step{
		{"25", "2000", "2500.00", "1000.00", "250.00", "1500.00", "500.00"},
		{"33.33", "0", "2499.75", "999.90", "249.98", "1499.85", "-1499.85"},
		{"100", "3500", "5000.25", "2000.10", "500.02", "3000.15", "499.85"},
	}
	cost, acc, imp := d("10000"), d("4000"), d("1000")
	total := d("0")
	for i, s := range steps {
		res, err := Dispose(cost, acc, d(s.proceeds), d(s.pct), "USD")
		if err != nil {
			t.Fatal(err)
		}
		di := DisposedShare(imp, d(s.pct), "USD")
		got := []string{res.DisposedCost.StringFixed(2), res.DisposedAccumulated.StringFixed(2), di.StringFixed(2), res.NBVDisposed.StringFixed(2), res.GainLoss.StringFixed(2)}
		want := []string{s.dc, s.da, s.di, s.nbv, s.gl}
		for k := range want {
			if got[k] != want[k] {
				t.Errorf("step %d field %d: got %s, want %s", i+1, k, got[k], want[k])
			}
		}
		total = total.Add(res.GainLoss)
		cost, acc, imp = res.RemainingCost, res.RemainingAccumulated, imp.Sub(di)
	}
	if !cost.IsZero() || !acc.IsZero() || !imp.IsZero() || total.StringFixed(2) != "-500.00" {
		t.Fatalf("after the chain: cost %s acc %s imp %s, Σ gain/loss %s", cost, acc, imp, total)
	}
}
