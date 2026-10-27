package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/events"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

var reconDay = time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)

func reconcileIn(ctx context.Context, currency string) store.ReconcileInput {
	return store.ReconcileInput{LegalEntityID: legalEntity(ctx), Currency: currency, Account: inventoryAccount, RunDate: reconDay}
}

// FR-16.12: after receipts, an issue at the running average and a blocking (a
// valued pair with no GL effect), the inventory value of the legal entity
// equals the net of the inventory account over its posted journals, so the
// GL-derived run matches and emits nothing. Stock valued in another currency
// stays out of the USD run and matches in its own.
func TestReconcileMatchesThePostedJournals(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product, euroProduct := ids.New(), ids.New()
	gl := decimal.Zero
	for _, j := range []domain.Journal{
		post(t, ctx, s, domain.JournalOpeningBalance, line(product, "100", "2", dims()), line(product, "50", "3.333333", dims("batch", "B1"))),
		post(t, ctx, s, domain.JournalMovement, line(product, "-7", "0", dims())),
		post(t, ctx, s, domain.JournalAdjustment, line(product, "-1.5", "0", dims("batch", "B1"))),
	} {
		gl = gl.Add(j.GLEntry.Net(inventoryAccount))
	}
	if _, _, err := s.CreateBlocking(ctx, store.BlockingInput{IdempotencyKey: "recon-blk", LegalEntityID: legalEntity(ctx), ProductID: product,
		Dims: dims(), Qty: dec("10"), Unit: "EA", Target: domain.StockBlocked, Reason: "QUALITY", PhysicalDate: reconDay}); err != nil {
		t.Fatal(err)
	}
	euro, err := newJournalIn(ctx, s, domain.JournalOpeningBalance, "EUR", line(euroProduct, "4", "2.5", dims()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, euro.ID, euro.Version, postParams()); err != nil {
		t.Fatal(err)
	}

	run, err := s.Reconcile(ctx, reconcileIn(ctx, "USD"))
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.ReconciliationMatched || !run.Diff.IsZero() || run.LedgerSource != domain.LedgerSourceJournals {
		t.Fatalf("GL-derived run: status %s diff %s (inventory %s, ledger %s)", run.Status, run.Diff, run.InventoryValue, run.LedgerBalance)
	}
	if gl.IsZero() || !run.InventoryValue.Equal(gl) || !run.LedgerBalance.Equal(gl) {
		t.Fatalf("inventory %s ledger %s, want both the GL inventory net %s", run.InventoryValue, run.LedgerBalance, gl)
	}
	if run.Account != inventoryAccount || !run.RunDate.Equal(reconDay) || run.NegativeRows != 0 || run.ID == "" {
		t.Fatalf("run %+v", run)
	}
	eur, err := s.Reconcile(ctx, reconcileIn(ctx, "EUR"))
	if err != nil {
		t.Fatal(err)
	}
	if eur.Status != domain.ReconciliationMatched || !eur.InventoryValue.Equal(dec("10")) || eur.ID == run.ID {
		t.Fatalf("EUR run %+v, want its own matched row of 10", eur)
	}
	if n := outboxCount(t, admin, events.ReconciliationDiff); n != 0 {
		t.Fatalf("%d diff events for matched runs, want 0", n)
	}
}

// AC-16.9: a manual ledger journal of 75.50 posted to the inventory account
// without an inventory transaction (supplied as the ledger balance) gives a
// diff event with the exact amount. The same-day re-run overwrites the row.
func TestReconcileDifferenceEmitsTheDiffEvent(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "100", "2", dims()), line(product, "50", "3", dims()))

	first, err := s.Reconcile(ctx, reconcileIn(ctx, "USD"))
	if err != nil || first.Status != domain.ReconciliationMatched {
		t.Fatalf("first run %+v: %v", first, err)
	}
	in := reconcileIn(ctx, "USD")
	ledger := dec("425.50")
	in.LedgerBalance = &ledger
	run, err := s.Reconcile(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.ReconciliationDifference || !run.Diff.Equal(dec("-75.50")) || run.LedgerSource != domain.LedgerSourceOverride {
		t.Fatalf("override run: status %s diff %s source %s", run.Status, run.Diff, run.LedgerSource)
	}
	if run.ID != first.ID {
		t.Fatalf("same-day run created a second row: %s vs %s", run.ID, first.ID)
	}
	var data events.ReconciliationDiffData
	eventData(t, admin, events.ReconciliationDiff, "'runId'", run.ID, &data)
	if data.Diff != "-75.5" || data.InventoryValue != "350" || data.LedgerBalance != "425.5" || data.Account != inventoryAccount ||
		data.LegalEntityID != legalEntity(ctx) || data.RunDate != "2026-10-27" || data.LedgerSource != domain.LedgerSourceOverride {
		t.Fatalf("diff event %+v", data)
	}
	if n := outboxCount(t, admin, events.ReconciliationDiff); n != 1 {
		t.Fatalf("%d diff events, want 1", n)
	}

	got, err := s.GetReconciliation(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ReconciliationDifference || !got.Diff.Equal(dec("-75.50")) || !got.RunDate.Equal(reconDay) || got.CreatedAt.IsZero() {
		t.Fatalf("stored run %+v", got)
	}
	all, err := s.ListReconciliations(ctx, store.ReconciliationFilter{})
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %d runs, %v; want the one overwritten row", len(all), err)
	}
	matched, err := s.ListReconciliations(ctx, store.ReconciliationFilter{Status: domain.ReconciliationMatched})
	if err != nil || len(matched) != 0 {
		t.Fatalf("MATCHED filter: %d runs, %v", len(matched), err)
	}
}

// FR-16.4: where the policy allows negative stock, negative on-hand rows are
// flagged by the daily run.
func TestReconcileCountsNegativeRows(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "5", "2", dims()), line(product, "5", "2", dims("batch", "B1")))
	j, err := newJournal(ctx, s, domain.JournalAdjustment, line(product, "-8", "0", dims()))
	if err != nil {
		t.Fatal(err)
	}
	params := postParams()
	params.Negative = domain.NegativePolicy{PhysicalAllowed: true, FinancialAllowed: true}
	if _, err := s.PostJournal(ctx, j.ID, j.Version, params); err != nil {
		t.Fatal(err)
	}
	run, err := s.Reconcile(ctx, reconcileIn(ctx, "USD"))
	if err != nil {
		t.Fatal(err)
	}
	if run.NegativeRows != 1 || run.Status != domain.ReconciliationMatched {
		t.Fatalf("negative rows %d status %s diff %s, want 1 and MATCHED", run.NegativeRows, run.Status, run.Diff)
	}
}

// Runs are tenant- and legal-entity-scoped: another entity's principal neither
// sees the run nor can run the check for that entity.
func TestReconciliationIsScoped(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	post(t, ctx, s, domain.JournalOpeningBalance, line(ids.New(), "3", "4", dims()))
	run, err := s.Reconcile(ctx, reconcileIn(ctx, "USD"))
	if err != nil {
		t.Fatal(err)
	}
	for name, other := range map[string]context.Context{"other entity": entityCtx(1, ids.New()), "other tenant": tenantWideCtx(2)} {
		if _, err := s.GetReconciliation(other, run.ID); problemCode(err) != domain.CodeReconciliationNotFound {
			t.Fatalf("%s read the run: %v", name, err)
		}
		if list, err := s.ListReconciliations(other, store.ReconciliationFilter{}); err != nil || len(list) != 0 {
			t.Fatalf("%s listed %d runs: %v", name, len(list), err)
		}
	}
	foreign := reconcileIn(ctx, "USD")
	if _, err := s.Reconcile(entityCtx(1, ids.New()), foreign); problemCode(err) != store.CodeOutOfScope {
		t.Fatalf("run for a legal entity outside the scope: %v, want %s", err, store.CodeOutOfScope)
	}
	if _, err := s.Reconcile(ctx, store.ReconcileInput{LegalEntityID: legalEntity(ctx), Currency: "US", Account: inventoryAccount, RunDate: reconDay}); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("currency US: %v, want %s", err, domain.CodeInvalid)
	}
}
