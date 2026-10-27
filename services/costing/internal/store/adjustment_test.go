package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
)

// fr201Stock records the FR-20.1 example under WEIGHTED_AVG_PERIODIC: receipts
// 100 @ 10.00 and 100 @ 12.00, an issue of 150 at 1,650.00, leaving 50 units
// worth 550.00. It also records a PPV row, which never counts.
func fr201Stock(t *testing.T, s *Postgres, ctx context.Context, le, product, site string) (receipt1 string) {
	t.Helper()
	receipt1 = ids.New()
	rows := []CostTransaction{
		costRow("r1", le, product, site, "FINANCIAL", "100", "1000.00", "2026-01-05"),
		costRow("r2", le, product, site, "FINANCIAL", "100", "1200.00", "2026-01-20"),
		costRow("i1", le, product, site, "FINANCIAL", "-150", "-1650.00", "2026-01-25"),
		costRow("ppv", le, product, site, "PPV", "0", "30.00", "2026-01-20"),
	}
	rows[0].InventoryTransactionID = receipt1
	rows[2].InventoryTransactionID = ids.New()
	if _, err := s.RecordCostTransactions(ctx, rows); err != nil {
		t.Fatal(err)
	}
	return receipt1
}

func onHand(le, product, site, mode, value string) AdjustmentRequest {
	in := AdjustmentRequest{LegalEntityID: le, Kind: domain.AdjustOnHand, Mode: mode, ProductID: product, SiteID: site,
		FinancialDate: "2026-01-31", Reason: "NRV"}
	if mode == domain.ModeUnitCost {
		in.UnitCost = value
	} else {
		in.Amount = value
	}
	return in
}

func TestPostgres_OnHandAdjustmentRevaluesAndEmitsCostChanged(t *testing.T) {
	s, app := setup(t)
	ctx := scopedCtx(testkit.TenantID(21))
	le, product, site := ids.New(), ids.New(), ids.New()
	fr201Stock(t, s, ctx, le, product, site)

	in := onHand(le, product, site, domain.ModeUnitCost, "10")
	preview, err := s.PreviewAdjustment(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Qty != "50" || preview.OldValue != "550.00" || preview.NewValue != "500.00" || preview.Delta != "-50.00" ||
		preview.OldUnitCost != "11" || preview.NewUnitCost != "10" || preview.Currency != "EUR" || preview.ID != "" {
		t.Fatalf("preview = %+v", preview)
	}
	if got := outboxRows(t, ctx, app); len(got) != 0 {
		t.Fatalf("preview wrote %d outbox rows", len(got))
	}

	in.ExpectedValue = "550.00"
	adj, created, err := s.CreateAdjustment(ctx, in, "adj-1", "hash-a"+pad)
	if err != nil || !created || adj.ID == "" || adj.CostTransactionID == "" || adj.Delta != "-50.00" || adj.CreatedAt == nil {
		t.Fatalf("create = %+v, created %v, err %v", adj, created, err)
	}
	bal, err := s.ValueBalances(ctx, BalanceQuery{LegalEntityID: le, AsOf: "2026-01-31"})
	if err != nil || len(bal) != 1 || bal[0].FinancialValue.String() != "500" || bal[0].Qty.String() != "50" {
		t.Fatalf("value after the adjustment = %+v, %v; want 50 units at 500", bal, err)
	}
	page, err := s.ListCostTransactions(ctx, CostTxnFilter{LegalEntityID: le, Kind: domain.KindManualAdj}, "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != adj.CostTransactionID ||
		page.Items[0].SourceKey != adjustmentSourcePrefix+adj.ID || page.Items[0].Amount != "-50.0000" || page.Items[0].Qty != "0.000000" {
		t.Fatalf("MANUAL_ADJ rows = %+v, %v", page.Items, err)
	}
	events := outboxRows(t, ctx, app)
	if len(events) != 1 || events[0].Type != CostChangedEventType {
		t.Fatalf("outbox = %+v, want one cost.changed", events)
	}
	if p := events[0].Payload; p["adjustmentId"] != adj.ID || p["oldCost"] != "11" || p["newCost"] != "10" ||
		p["revaluationAmount"] != "-50.00" || p["onHandQty"] != "50" || p["versionId"] != nil {
		t.Errorf("cost.changed payload = %v", p)
	}

	// Replay: the same key and request return the stored adjustment and write nothing.
	again, created, err := s.CreateAdjustment(ctx, in, "adj-1", "hash-a"+pad)
	if err != nil || created || again.ID != adj.ID || again.Delta != adj.Delta || again.OldValue != "550.00" {
		t.Fatalf("replay = %+v, created %v, err %v", again, created, err)
	}
	if _, _, err := s.CreateAdjustment(ctx, in, "adj-1", "hash-b"+pad); problemCode(err) != CodeIdempotencyReuse {
		t.Errorf("reused key: %v, want %s", err, CodeIdempotencyReuse)
	}
	// The preview said 550.00, but the value is 500.00 now: refused, nothing written.
	if _, _, err := s.CreateAdjustment(ctx, in, "adj-2", "hash-c"+pad); problemCode(err) != CodeAdjustmentStale {
		t.Errorf("stale preview: %v, want %s", err, CodeAdjustmentStale)
	}
	// Setting the cost it already has changes nothing.
	same := onHand(le, product, site, domain.ModeUnitCost, "10")
	if _, _, err := s.CreateAdjustment(ctx, same, "adj-3", "hash-d"+pad); domainCode(err) != domain.CodeAdjustNoChange {
		t.Errorf("no change: %v, want %s", err, domain.CodeAdjustNoChange)
	}
	if got, err := s.GetAdjustment(ctx, adj.ID); err != nil || got.NewValue != "500.00" || got.UnitCost != "10" || got.Reason != "NRV" {
		t.Errorf("get = %+v, %v", got, err)
	}
	if n := len(outboxRows(t, ctx, app)); n != 1 {
		t.Errorf("outbox holds %d rows after the refusals, want 1", n)
	}
	err = dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `update cost_adjustment set reason = 'x'`)
		return err
	})
	if err == nil {
		t.Fatal("an update of a cost adjustment succeeded; the table must be append-only")
	}
}

// pad makes a test hash the 64 characters the table requires.
const pad = "0123456789012345678901234567890123456789012345678901234567"

func TestPostgres_ReceiptAdjustmentAndRefusals(t *testing.T) {
	s, app := setup(t)
	ctx := scopedCtx(testkit.TenantID(22))
	le, product, site := ids.New(), ids.New(), ids.New()
	receipt := fr201Stock(t, s, ctx, le, product, site)

	in := AdjustmentRequest{LegalEntityID: le, Kind: domain.AdjustReceipt, Mode: domain.ModeAmount, ReceiptTransactionID: receipt,
		FinancialDate: "2026-02-10", Amount: "12.5", Reason: "freight invoice"}
	adj, created, err := s.CreateAdjustment(ctx, in, "rcpt-1", "hash-r"+pad)
	if err != nil || !created || adj.ProductID != product || adj.SiteID != site || adj.Qty != "100" ||
		adj.OldValue != "1000.00" || adj.NewValue != "1012.50" || adj.Delta != "12.50" {
		t.Fatalf("receipt adjustment = %+v, %v", adj, err)
	}
	page, err := s.ListCostTransactions(ctx, CostTxnFilter{LegalEntityID: le, Kind: domain.KindManualAdj}, "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].InventoryTransactionID != receipt || page.Items[0].FinancialDate != "2026-02-10" {
		t.Fatalf("MANUAL_ADJ rows = %+v, %v; want one on the receipt", page.Items, err)
	}
	// A second adjustment of the receipt starts from the adjusted value.
	in.Amount = "-2.5"
	if adj2, _, err := s.CreateAdjustment(ctx, in, "rcpt-2", "hash-s"+pad); err != nil || adj2.OldValue != "1012.50" || adj2.NewValue != "1010.00" {
		t.Fatalf("second receipt adjustment = %+v, %v", adj2, err)
	}
	if got := outboxRows(t, ctx, app); len(got) != 0 {
		t.Errorf("a receipt adjustment wrote %d outbox rows; it changes no on-hand unit cost", len(got))
	}

	refusals := []struct {
		name string
		in   AdjustmentRequest
		want string
	}{
		{"an issue is not a receipt", AdjustmentRequest{LegalEntityID: le, Kind: domain.AdjustReceipt, Mode: domain.ModeAmount,
			ReceiptTransactionID: ids.New(), FinancialDate: "2026-02-10", Amount: "1", Reason: "x"}, domain.CodeAdjustNothing},
		{"a receipt before its financial date", func() AdjustmentRequest { r := in; r.FinancialDate = "2026-01-01"; return r }(), domain.CodeAdjustNothing},
		{"no stock of another product", onHand(le, ids.New(), site, domain.ModeAmount, "1"), domain.CodeAdjustNothing},
		{"another warehouse is another balance", func() AdjustmentRequest {
			r := onHand(le, product, site, domain.ModeAmount, "1")
			r.WarehouseID = ids.New()
			return r
		}(), domain.CodeAdjustNothing},
		{"write-down below zero", onHand(le, product, site, domain.ModeAmount, "-560.01"), domain.CodeAdjustNegative},
	}
	for _, tc := range refusals {
		if _, err := s.PreviewAdjustment(ctx, tc.in); domainCode(err) != tc.want {
			t.Errorf("%s: %v, want %s", tc.name, err, tc.want)
		}
	}
	// Row-level security: another tenant and another legal entity see nothing.
	if _, err := s.GetAdjustment(scopedCtx(testkit.TenantID(23)), adj.ID); problemCode(err) != CodeAdjustmentNotFound {
		t.Errorf("other tenant get: %v, want %s", err, CodeAdjustmentNotFound)
	}
	if got, err := s.ListAdjustments(narrowedCtx(testkit.TenantID(22), ids.New()), AdjustmentFilter{}, "", 10); err != nil || len(got.Items) != 0 {
		t.Errorf("other legal entity sees %d adjustments, err %v", len(got.Items), err)
	}
	// Paging: newest financial date first, two per page.
	if _, _, err := s.CreateAdjustment(ctx, onHand(le, product, site, domain.ModeAmount, "-1"), "oh-1", "hash-t"+pad); err != nil {
		t.Fatal(err)
	}
	var seen []string
	after := ""
	for pages := 0; pages < 3; pages++ {
		page, err := s.ListAdjustments(ctx, AdjustmentFilter{LegalEntityID: le}, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range page.Items {
			seen = append(seen, a.FinancialDate+" "+a.Kind)
		}
		if after = page.Next; after == "" {
			break
		}
	}
	if len(seen) != 3 || seen[2] != "2026-01-31 ON_HAND" {
		t.Errorf("paged adjustments = %v, want the two receipt adjustments, then the on-hand one", seen)
	}
	if got, _ := s.ListAdjustments(ctx, AdjustmentFilter{LegalEntityID: le, Kind: domain.AdjustOnHand}, "", 10); len(got.Items) != 1 {
		t.Errorf("ON_HAND filter returned %d, want 1", len(got.Items))
	}
}

func TestPostgres_GroupLedgerAdjustmentStaysInItsLedger(t *testing.T) {
	s, app := setup(t)
	ctx := scopedCtx(testkit.TenantID(24))
	le, product, site := ids.New(), ids.New(), ids.New()
	gaap, err := s.CreateValuationLedger(ctx, ValuationLedger{LegalEntityID: le, LedgerGroupID: ids.New(), Code: "USGAAP",
		Method: "FIFO", Currency: "USD", RateType: "SPOT"})
	if err != nil {
		t.Fatal(err)
	}
	fr201Stock(t, s, ctx, le, product, site)
	row := costRow("g1", le, product, site, "FINANCIAL", "50", "660.00", "2026-01-25")
	row.ValuationLedgerID, row.Currency = gaap.ID, "USD"
	if _, err := s.RecordCostTransactions(ctx, []CostTransaction{row}); err != nil {
		t.Fatal(err)
	}
	in := onHand(le, product, site, domain.ModeUnitCost, "13")
	in.ValuationLedgerID = gaap.ID
	adj, _, err := s.CreateAdjustment(ctx, in, "g-adj", "hash-g"+pad)
	if err != nil || adj.Currency != "USD" || adj.NewValue != "650.00" || adj.ValuationLedgerID != gaap.ID {
		t.Fatalf("group ledger adjustment = %+v, %v", adj, err)
	}
	primary, _ := s.ValueBalances(ctx, BalanceQuery{LegalEntityID: le, AsOf: "2026-01-31"})
	group, _ := s.ValueBalances(ctx, BalanceQuery{LegalEntityID: le, ValuationLedgerID: gaap.ID, AsOf: "2026-01-31"})
	if len(primary) != 1 || primary[0].FinancialValue.String() != "550" || len(group) != 1 || group[0].FinancialValue.String() != "650" {
		t.Errorf("primary %+v group %+v; want 550 EUR untouched and 650 USD", primary, group)
	}
	if got := outboxRows(t, ctx, app); len(got) != 0 {
		t.Errorf("a group ledger adjustment wrote %d cost.changed events; only the primary valuation drives the cost", len(got))
	}
}

func TestPostgres_ReconciliationRunEmitsOneDiffPerDifferingGroup(t *testing.T) {
	s, app := setup(t)
	ctx := scopedCtx(testkit.TenantID(25))
	le, s1, s2 := ids.New(), ids.New(), ids.New()
	run := ReconciliationRun{LegalEntityID: le, AsOf: "2026-03-31", Currency: "EUR", GroupBy: []string{"site"}, Differences: 1,
		TotalDiff: "5.00", Rows: []ReconciliationRow{
			{Key: map[string]string{"site": s1}, Inventory: "1700.00", Ledger: "1700.00", Diff: "0.00"},
			{Key: map[string]string{"site": s2}, Inventory: "50.00", Ledger: "45.00", Diff: "5.00"},
		}}
	stored, created, err := s.CreateReconciliationRun(ctx, run, "rec-1", "hash-1"+pad)
	if err != nil || !created || stored.ID == "" || stored.RowCount != 2 || len(stored.Rows) != 2 || stored.CreatedAt.IsZero() {
		t.Fatalf("create = %+v, created %v, err %v", stored, created, err)
	}
	events := outboxRows(t, ctx, app)
	if len(events) != 1 || events[0].Type != ReconciliationDiffEventType || events[0].AggID != le {
		t.Fatalf("outbox = %+v, want one reconciliation.diff keyed by the legal entity", events)
	}
	if p := events[0].Payload; p["runId"] != stored.ID || p["diff"] != "5.00" || p["ledgerBalance"] != "45.00" ||
		p["dimensions"].(map[string]any)["site"] != s2 || p["currency"] != "EUR" {
		t.Errorf("payload = %v", p)
	}
	// A replay returns the stored run and emits nothing; another body under the key is refused.
	again, created, err := s.CreateReconciliationRun(ctx, run, "rec-1", "hash-1"+pad)
	if err != nil || created || again.ID != stored.ID {
		t.Fatalf("replay = %+v, created %v, err %v", again, created, err)
	}
	if _, _, err := s.CreateReconciliationRun(ctx, run, "rec-1", "hash-2"+pad); problemCode(err) != CodeIdempotencyReuse {
		t.Errorf("reused key: %v, want %s", err, CodeIdempotencyReuse)
	}
	if n := len(outboxRows(t, ctx, app)); n != 1 {
		t.Errorf("outbox holds %d rows after the replay, want 1", n)
	}
	got, err := s.GetReconciliationRun(ctx, stored.ID)
	if err != nil || len(got.Rows) != 2 || got.Rows[1].Diff != "5.00" || got.Ties {
		t.Fatalf("get = %+v, %v", got, err)
	}
	tied := ReconciliationRun{LegalEntityID: le, AsOf: "2026-04-30", Currency: "EUR", GroupBy: []string{}, Ties: true, TotalDiff: "0.00",
		Rows: []ReconciliationRow{{Key: map[string]string{}, Inventory: "0.00", Ledger: "0.00", Diff: "0.00"}}}
	if _, _, err := s.CreateReconciliationRun(ctx, tied, "rec-2", "hash-3"+pad); err != nil {
		t.Fatal(err)
	}
	if n := len(outboxRows(t, ctx, app)); n != 1 {
		t.Errorf("a run that ties emitted events: outbox holds %d rows, want 1", n)
	}
	page, err := s.ListReconciliationRuns(ctx, le, "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].AsOf != "2026-04-30" || page.Items[0].Rows != nil || page.Next == "" {
		t.Fatalf("first page = %+v, %v; want the newest run without rows", page, err)
	}
	if next, err := s.ListReconciliationRuns(ctx, le, page.Next, 1); err != nil || len(next.Items) != 1 || next.Items[0].ID != stored.ID || next.Next != "" {
		t.Fatalf("second page = %+v, %v", next, err)
	}
	if _, err := s.GetReconciliationRun(scopedCtx(testkit.TenantID(26)), stored.ID); problemCode(err) != CodeReconciliationNotFound {
		t.Errorf("other tenant get: %v, want %s", err, CodeReconciliationNotFound)
	}
	if got, _ := s.ListReconciliationRuns(narrowedCtx(testkit.TenantID(25), ids.New()), "", "", 10); len(got.Items) != 0 {
		t.Errorf("other legal entity sees %d runs", len(got.Items))
	}
}

// Unit-cost adjustments of the same stock posted at once are serialised: each
// reads the value the previous one left, so the deltas add up and the value
// ends at 50 x the unit cost of whichever posted last. Without the lock several
// would read 550.00 and the value would drift off every posted unit cost.
func TestPostgres_ConcurrentAdjustmentsAreSerialised(t *testing.T) {
	s, _ := setup(t)
	ctx := scopedCtx(testkit.TenantID(27))
	le, product, site := ids.New(), ids.New(), ids.New()
	fr201Stock(t, s, ctx, le, product, site)
	costs := []string{"2", "3", "4", "5", "6", "7", "8", "9"}
	errs := make(chan error, len(costs))
	start := make(chan struct{})
	for i, cost := range costs {
		go func() {
			<-start
			_, _, err := s.CreateAdjustment(ctx, onHand(le, product, site, domain.ModeUnitCost, cost), "c-"+cost, fmt.Sprintf("hash-%d", i)+pad)
			errs <- err
		}()
	}
	close(start)
	for range costs {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	bal, err := s.ValueBalances(ctx, BalanceQuery{LegalEntityID: le, AsOf: "2026-01-31"})
	if err != nil || len(bal) != 1 {
		t.Fatalf("balances = %+v, %v", bal, err)
	}
	unit := bal[0].FinancialValue.Div(bal[0].Qty)
	if !unit.IsInteger() || unit.LessThan(d("2")) || unit.GreaterThan(d("9")) {
		t.Errorf("value after concurrent revaluations = %s for 50 units, not 50 x any posted unit cost", bal[0].FinancialValue)
	}
}
