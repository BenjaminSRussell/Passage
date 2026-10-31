package store_test

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

// TestStore_PartialDisposalsPostGainLossOnEveryBook is the Wave 37 proof
// "Disposal gain/loss posting correct across partial disposals" on a real
// database: two books of one asset, an IFRS straight-line book carrying
// depreciation and an impairment, and a TAX MACRS book depreciated by a posted
// 2026 run (IRS Table A-1: 20% of 10,000.00), are sold 25%, scrapped 33.33%
// and sold out. Every posted transaction (cost, accumulated, impairment
// share, proceeds, gain or loss) equals the amounts worked by hand below, and
// Σ transactions per book reconcile to the book after each step.
//
//	IFRS 10,000.00 / acc 4,000.00 (impairment 1,000.00)   TAX 10,000.00 / acc 2,000.00
//	1. sale 25% for 2,000.00
//	   IFRS: -2,500.00 / -1,000.00 / -250.00, NBV 1,500.00, gain 500.00
//	   TAX:  -2,500.00 /   -500.00 /    0.00, NBV 2,000.00, gain 0.00
//	2. scrap 33.33% (of 7,500.00 / 3,000.00 / 750.00 and 7,500.00 / 1,500.00)
//	   IFRS: -2,499.75 / -999.90 / -249.98 (249.975 half-up), NBV 1,499.85, loss -1,499.85
//	   TAX:  -2,499.75 / -499.95 /   0.00, NBV 1,999.80, loss -1,999.80
//	3. sale 100% for 3,500.00 (of 5,000.25 / 2,000.10 / 500.02 and 5,000.25 / 1,000.05)
//	   IFRS: -5,000.25 / -2,000.10 / -500.02, NBV 3,000.15, gain 499.85
//	   TAX:  -5,000.25 / -1,000.05 /    0.00, NBV 4,000.20, loss -500.20
//	Σ gain/loss: IFRS -500.00 = 5,500.00 proceeds - 6,000.00 NBV;
//	             TAX -2,500.00 = 5,500.00 - 8,000.00.
func TestStore_PartialDisposalsPostGainLossOnEveryBook(t *testing.T) {
	s, _, ctx := setup(t)
	le := ids.New()
	asset := newAsset(t, s, ctx, le, "FA-DISP-30")
	ifrs, err := s.AddBook(ctx, asset.ID, store.Book{BookCode: "IFRS", Currency: "USD", Method: "StraightLine", ServiceLifePeriods: 60, Convention: "None",
		InServiceDate: day("2026-01-01"), Cost: d("10000"), SalvageValue: d("0")}, "b-ifrs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddBook(ctx, asset.ID, store.Book{BookCode: "TAX", Currency: "USD", Method: string(domain.MethodMACRS), ServiceLifePeriods: 5,
		Convention: "HalfYear", InServiceDate: day("2026-01-01"), Cost: d("10000"), SalvageValue: d("0")}, "b-tax"); err != nil {
		t.Fatalf("MACRS book: %v", err)
	}
	for i, adj := range []store.AdjustmentInput{
		{BookID: ifrs.ID, Type: domain.AdjustmentDepreciation, Date: day("2026-12-31"), Amount: d("3000")},
		{BookID: ifrs.ID, Type: domain.AdjustmentImpairment, Date: day("2026-12-31"), Amount: d("1000")},
	} {
		if _, err := s.Adjust(ctx, asset.ID, adj, "adj-"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	run, err := s.ProposeRun(ctx, store.RunInput{LegalEntityID: le, BookCode: "TAX", Period: "2026"}, "run-tax-2026")
	if err != nil {
		t.Fatal(err)
	}
	if run.TotalAmount.StringFixed(2) != "2000.00" || run.ExceptionCount != 0 {
		t.Fatalf("MACRS 2026 run = %+v, want 2,000.00 (20%% of 10,000.00)", run)
	}
	if _, err := s.PostRun(ctx, run.ID, run.Version); err != nil {
		t.Fatal(err)
	}

	type posting struct{ cost, acc, imp, proceeds, gain string }
	steps := []struct {
		kind     domain.DisposalKind
		pct      string
		proceeds string
		date     string
		status   string
		want     map[string]posting
	}{
		{domain.DisposalSale, "25", "2000", "2027-02-01", "Open", map[string]posting{
			"IFRS": {"-2500.00", "-1000.00", "-250.00", "2000.00", "500.00"},
			"TAX":  {"-2500.00", "-500.00", "0.00", "2000.00", "0.00"}}},
		{domain.DisposalScrap, "33.33", "0", "2027-03-01", "Open", map[string]posting{
			"IFRS": {"-2499.75", "-999.90", "-249.98", "0.00", "-1499.85"},
			"TAX":  {"-2499.75", "-499.95", "0.00", "0.00", "-1999.80"}}},
		{domain.DisposalSale, "100", "3500", "2027-04-01", "Sold", map[string]posting{
			"IFRS": {"-5000.25", "-2000.10", "-500.02", "3500.00", "499.85"},
			"TAX":  {"-5000.25", "-1000.05", "0.00", "3500.00", "-500.20"}}},
	}
	gains := map[string]decimal.Decimal{}
	for n, st := range steps {
		detail, err := s.GetAsset(ctx, asset.ID)
		if err != nil {
			t.Fatal(err)
		}
		disp, err := s.Dispose(ctx, asset.ID, store.DisposalInput{Version: detail.Version, Kind: st.kind, Percentage: d(st.pct), Proceeds: d(st.proceeds),
			Date: day(st.date)}, "disp-"+st.date)
		if err != nil {
			t.Fatalf("step %d: %v", n+1, err)
		}
		if disp.Asset.Status != st.status || len(disp.Transactions) != 2 {
			t.Fatalf("step %d: asset %s with %d transactions", n+1, disp.Asset.Status, len(disp.Transactions))
		}
		for i, b := range disp.Books {
			tx, w := disp.Transactions[i], st.want[b.BookCode]
			got := posting{tx.CostAmount.StringFixed(2), tx.AccumulatedAmount.StringFixed(2), tx.ImpairmentAmount.StringFixed(2), "", ""}
			if tx.ProceedsAmount != nil {
				got.proceeds = tx.ProceedsAmount.StringFixed(2)
			}
			if tx.GainLoss != nil {
				got.gain = tx.GainLoss.StringFixed(2)
				gains[b.BookCode] = gains[b.BookCode].Add(*tx.GainLoss)
			}
			if got != w || b.Status != st.status {
				t.Errorf("step %d book %s: posted %+v (%s), want %+v (%s)", n+1, b.BookCode, got, b.Status, w, st.status)
			}
		}
		reconcileWithImpairment(t, s, ctx, asset.ID, n+1)
	}
	if gains["IFRS"].StringFixed(2) != "-500.00" || gains["TAX"].StringFixed(2) != "-2500.00" {
		t.Fatalf("Σ gain/loss = %v, want IFRS -500.00 and TAX -2,500.00", gains)
	}
}

// reconcile checks the subledger invariant on every book: Σ transaction cost,
// accumulated and impairment amounts equal the book's projection, and the
// impairment still recorded never exceeds its accumulated depreciation.
func reconcileWithImpairment(t *testing.T, s *store.Postgres, ctx context.Context, assetID string, step int) {
	t.Helper()
	txs, err := s.ListTransactions(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.GetAsset(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range detail.Books {
		cost, acc, imp := decimal.Zero, decimal.Zero, decimal.Zero
		for _, tx := range txs {
			if tx.AssetBookID == b.ID {
				cost, acc, imp = cost.Add(tx.CostAmount), acc.Add(tx.AccumulatedAmount), imp.Add(tx.ImpairmentAmount)
			}
		}
		if !cost.Equal(b.Cost) || !acc.Equal(b.AccumulatedDepreciation) || imp.IsNegative() || imp.GreaterThan(acc) {
			t.Errorf("step %d book %s: Σ cost %s / acc %s / impairment %s vs projection %s / %s", step, b.BookCode, cost, acc, imp, b.Cost, b.AccumulatedDepreciation)
		}
	}
}
