package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

func blockInput(ctx context.Context, product, key, target string, day time.Time) store.BlockingInput {
	return store.BlockingInput{IdempotencyKey: key, LegalEntityID: legalEntity(ctx), ProductID: product, Dims: dims("batch", "B1"),
		Qty: dec("4"), Unit: "EA", Target: target, Reason: "QUALITY", PhysicalDate: day}
}

// FR-16.8: scrapping a blocking writes the stock off through a posted ADJUSTMENT
// journal: physical stock and value drop by the blocking's value, the GL entry
// debits the adjustment account and credits inventory, and a second scrap of the
// same blocking posts nothing.
func TestScrapBlockingWritesTheStockOff(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	day := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "2.5", dims("batch", "B1")))
	in := blockInput(ctx, product, "scrap-1", domain.StockQuarantine, day)
	in.ExpectedReleaseDate = "2026-09-09"
	b, _, err := s.CreateBlocking(ctx, in)
	if err != nil || b.ExpectedReleaseDate != "2026-09-09" {
		t.Fatalf("block: %v %+v", err, b)
	}
	in.ExpectedReleaseDate = "2026-09-01"
	in.IdempotencyKey = "scrap-early"
	if _, _, err := s.CreateBlocking(ctx, in); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("expected release before the blocking date: %v", err)
	}
	in = blockInput(ctx, product, "scrap-1", domain.StockQuarantine, day)
	if _, _, err := s.CreateBlocking(ctx, in); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key, no expected date: %v", err)
	}
	if _, err := s.ReleaseBlocking(ctx, b.ID, b.Version, domain.DispositionScrap, day, store.PostParams{InventoryAccount: inventoryAccount}); problemCode(err) != store.CodeAccountMissing {
		t.Fatalf("scrap without an adjustment account: %v", err)
	}
	scrapped, err := s.ReleaseBlocking(ctx, b.ID, b.Version, domain.DispositionScrap, day, postParams())
	if err != nil || scrapped.State != domain.BlockingReleased || scrapped.Disposition != domain.DispositionScrap || scrapped.ScrapJournalID == "" || scrapped.Version != 2 {
		t.Fatalf("scrap: %v %+v", err, scrapped)
	}
	phys, value := onHandFor(t, s, ctx, product)
	avail, blocked := availFor(t, s, ctx, product)
	if !phys.Equal(dec("6")) || !value.Equal(dec("15")) || !avail.Equal(dec("6")) || !blocked.IsZero() {
		t.Fatalf("after scrap: physical %s value %s avail %s blocked %s; want 6, 15, 6, 0", phys, value, avail, blocked)
	}
	j, err := s.GetJournal(ctx, scrapped.ScrapJournalID)
	if err != nil || j.Status != domain.JournalPosted || j.Type != domain.JournalAdjustment || j.GLEntry == nil || len(j.GLEntry.Lines) != 2 {
		t.Fatalf("scrap journal: %v %+v", err, j)
	}
	for _, l := range j.GLEntry.Lines {
		switch l.Account {
		case adjustmentAccount:
			if !l.Debit.Equal(dec("10")) || !l.Credit.IsZero() {
				t.Fatalf("adjustment account line %+v, want debit 10", l)
			}
		case inventoryAccount:
			if !l.Credit.Equal(dec("10")) || !l.Debit.IsZero() {
				t.Fatalf("inventory account line %+v, want credit 10", l)
			}
		default:
			t.Fatalf("unexpected account %s", l.Account)
		}
	}
	if again, err := s.ReleaseBlocking(ctx, b.ID, 1, domain.DispositionScrap, day, postParams()); err != nil || again.Version != 2 || again.ScrapJournalID != scrapped.ScrapJournalID {
		t.Fatalf("second scrap returns the blocking unchanged: %v %+v", err, again)
	}
	var journals int
	if err := admin.QueryRow(context.Background(), `select count(*) from inventory_journal where idempotency_key = $1`, "blocking-scrap:"+b.ID).Scan(&journals); err != nil || journals != 1 {
		t.Fatalf("scrap journals %d (%v), want 1", journals, err)
	}
	var payload []byte
	if err := admin.QueryRow(context.Background(), `select payload from outbox where type = 'dev.passage.inventory.blocking.released.v1'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var ev map[string]any
	_ = json.Unmarshal(payload, &ev)
	data, _ := ev["data"].(map[string]any)
	if data == nil {
		data = ev
	}
	if data["disposition"] != "SCRAP" || data["scrapJournalId"] != scrapped.ScrapJournalID || data["expectedReleaseDate"] != "2026-09-09" {
		t.Fatalf("released event %s", payload)
	}
}

// Scrapping needs the stock: a blocking whose blocked stock was consumed elsewhere cannot be scrapped,
// even where the service allows negative stock, and the failed attempt leaves the blocking active,
// the stock as it was and no journal behind. Another legal entity of the tenant does not see the
// blocking at all.
func TestScrapBlockingLeavesNothingBehindWhenItFails(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	day := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "2.5", dims("batch", "B1")))
	b, _, err := s.CreateBlocking(ctx, blockInput(ctx, product, "scrap-2", domain.StockBlocked, day))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseBlocking(entityCtx(1, ids.New()), b.ID, b.Version, domain.DispositionScrap, day, postParams()); problemCode(err) != domain.CodeBlockingNotFound {
		t.Fatalf("scrap from another legal entity: %v", err)
	}
	// An adjustment takes 3 of the 4 blocked units straight from the blocked row.
	post(t, ctx, s, domain.JournalAdjustment, line(product, "-3", "0", dims("batch", "B1", domain.DimStatus, domain.StockBlocked)))
	lenient := postParams()
	lenient.Negative = domain.NegativePolicy{PhysicalAllowed: true, FinancialAllowed: true}
	if _, err := s.ReleaseBlocking(ctx, b.ID, b.Version, domain.DispositionScrap, day, lenient); problemCode(err) != domain.CodeNegativePhysical {
		t.Fatalf("scrap of more than the blocked row holds: %v", err)
	}
	if phys, value := onHandFor(t, s, ctx, product); !phys.Equal(dec("7")) || !value.Equal(dec("17.5")) {
		t.Fatalf("after the refused scrap: physical %s value %s; want 7, 17.5", phys, value)
	}
	if _, err := s.ReleaseBlocking(ctx, b.ID, b.Version+1, domain.DispositionScrap, day, postParams()); problemCode(err) != store.CodeConflictVersion {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := s.ReleaseBlocking(ctx, b.ID, b.Version, domain.DispositionReturn, day, postParams()); problemCode(err) != domain.CodeDispositionLater {
		t.Fatalf("return: %v", err)
	}
	var journals int
	if err := admin.QueryRow(context.Background(), `select count(*) from inventory_journal where idempotency_key like 'blocking-scrap:%'`).Scan(&journals); err != nil || journals != 0 {
		t.Fatalf("journals after refused scraps: %d (%v)", journals, err)
	}
	if list, err := s.ListBlockings(ctx, store.BlockingFilter{State: domain.BlockingActive}); err != nil || len(list) != 1 {
		t.Fatalf("blocking must stay active: %v %+v", err, list)
	}
}

// FR-16.8: QUARANTINE to BLOCKED moves the stock between restricted statuses
// without changing physical stock or value, then scrap and release work on the
// blocked dimensions.
func TestEscalateQuarantineToBlocked(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	day := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "2.5", dims("batch", "B1")))
	b, _, err := s.CreateBlocking(ctx, blockInput(ctx, product, "esc-1", domain.StockQuarantine, day))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EscalateBlocking(ctx, b.ID, b.Version+1, day); problemCode(err) != store.CodeConflictVersion {
		t.Fatalf("stale version: %v", err)
	}
	e, err := s.EscalateBlocking(ctx, b.ID, b.Version, day)
	if err != nil || e.Target != domain.StockBlocked || e.State != domain.BlockingActive || e.Version != 2 {
		t.Fatalf("escalate: %v %+v", err, e)
	}
	if _, err := s.EscalateBlocking(ctx, b.ID, e.Version, day); problemCode(err) != domain.CodeBlockingTransition {
		t.Fatalf("a blocked blocking does not escalate again: %v", err)
	}
	res, err := s.ListOnHand(ctx, store.OnHandFilter{ProductID: product, GroupBy: []string{"inventory_status"}})
	if err != nil || len(res.Rows) != 3 {
		t.Fatalf("by status: %v %+v", err, res.Rows)
	}
	for _, r := range res.Rows {
		st := r.Group["inventory_status"]
		if (st == domain.StockQuarantine && (!r.PhysicalInvent.IsZero() || !r.PostedValue.IsZero())) || (st == "" && !r.PhysicalInvent.Equal(dec("6"))) {
			t.Fatalf("row %+v: the quarantine row must be empty", r)
		}
		if st == domain.StockBlocked && (!r.PhysicalInvent.Equal(dec("4")) || !r.PostedValue.Equal(dec("10"))) {
			t.Fatalf("blocked row %+v, want 4 / 10", r)
		}
	}
	if c := outboxCount(t, admin, "dev.passage.inventory.blocking.escalated.v1"); c != 1 {
		t.Fatalf("escalated events %d", c)
	}
	released, err := s.ReleaseBlocking(ctx, b.ID, e.Version, "", day, postParams())
	if err != nil || released.Disposition != domain.DispositionRelease {
		t.Fatalf("release of the blocked stock: %v %+v", err, released)
	}
	avail, blocked := availFor(t, s, ctx, product)
	if !avail.Equal(dec("10")) || !blocked.IsZero() {
		t.Fatalf("after release avail %s blocked %s", avail, blocked)
	}
	if _, err := s.EscalateBlocking(ctx, b.ID, released.Version, day); problemCode(err) != domain.CodeBlockingState {
		t.Fatalf("a released blocking does not escalate: %v", err)
	}
}
