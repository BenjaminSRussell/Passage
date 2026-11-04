package store_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

func legalEntity(ctx context.Context) string {
	p, _ := tenant.FromContext(ctx)
	return p.LegalEntityID
}

func approverCtx(ctx context.Context) context.Context {
	p, _ := tenant.FromContext(ctx)
	p.UserID = ids.New()
	return tenant.With(context.Background(), p)
}

func reserve(ctx context.Context, s *store.Postgres, product, qty string, policy domain.ReservationPolicy, d domain.Dimensions, key string) (store.ReserveResult, error) {
	return s.Reserve(ctx, store.ReserveInput{LegalEntityID: legalEntity(ctx), ProductID: product, Qty: dec(qty), DemandType: domain.DemandSalesOrder,
		DemandID: "SO-1", Policy: policy, Dims: d, RequiredDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), IdempotencyKey: key})
}

// availFor returns the product's available physical and blocked quantity.
func availFor(t *testing.T, s *store.Postgres, ctx context.Context, product string) (decimal.Decimal, decimal.Decimal) {
	t.Helper()
	res, err := s.ListOnHand(ctx, store.OnHandFilter{ProductID: product})
	if err != nil || len(res.Rows) != 1 {
		t.Fatalf("on-hand: %v %+v", err, res.Rows)
	}
	return res.Rows[0].AvailPhysical, res.Rows[0].Blocked
}

func outboxCount(t *testing.T, admin *pgxpool.Pool, typ string) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = $1`, typ).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// FR-16.7: a manual reservation takes availability under the row lock, so an
// issue cannot consume reserved stock; release gives it back once.
func TestManualReservationHoldsStockUntilReleased(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "2", dims()))

	res, err := reserve(ctx, s, product, "6", domain.PolicyManual, dims(), "r-1")
	if err != nil || len(res.Reservations) != 1 || !res.Reservations[0].Qty.Equal(dec("6")) || !res.Shortfall.IsZero() {
		t.Fatalf("reserve: %v %+v", err, res)
	}
	if avail, _ := availFor(t, s, ctx, product); !avail.Equal(dec("4")) {
		t.Fatalf("avail %s after reserving 6 of 10, want 4", avail)
	}
	issue, err := newJournal(ctx, s, domain.JournalAdjustment, line(product, "-5", "0", dims()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, issue.ID, issue.Version, postParams()); problemCode(err) != domain.CodeNegativePhysical {
		t.Fatalf("issue into reserved stock: %v, want %s", err, domain.CodeNegativePhysical)
	}
	if _, err := reserve(ctx, s, product, "5", domain.PolicyManual, dims(), "r-2"); problemCode(err) != domain.CodeReserveShortfall {
		t.Fatalf("over-reservation: %v, want %s", err, domain.CodeReserveShortfall)
	}
	id := res.Reservations[0].ID
	for i := 0; i < 2; i++ {
		released, err := s.ReleaseReservation(ctx, id)
		if err != nil || released.Status != domain.ReservationReleased || released.ReleasedAt == nil {
			t.Fatalf("release %d: %v %+v", i, err, released)
		}
	}
	if avail, _ := availFor(t, s, ctx, product); !avail.Equal(dec("10")) {
		t.Fatalf("avail %s after release (twice), want 10", avail)
	}
	if _, err := s.PostJournal(ctx, issue.ID, issue.Version, postParams()); err != nil {
		t.Fatalf("issue after release: %v", err)
	}
	if c, r := outboxCount(t, admin, "dev.passage.inventory.reservation.created.v1"), outboxCount(t, admin, "dev.passage.inventory.reservation.released.v1"); c != 1 || r != 1 {
		t.Fatalf("reservation events created=%d released=%d, want 1 and 1", c, r)
	}
	if _, err := s.ReleaseReservation(ctx, ids.New()); problemCode(err) != domain.CodeReservationNotFound {
		t.Fatalf("unknown reservation: %v", err)
	}
}

func TestReservationIdempotencyAndValidation(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims()))
	first, err := reserve(ctx, s, product, "3", domain.PolicyFIFO, domain.Dimensions{"site": "S1"}, "k")
	if err != nil {
		t.Fatal(err)
	}
	again, err := reserve(ctx, s, product, "3", domain.PolicyFIFO, domain.Dimensions{"site": "S1"}, "k")
	if err != nil || !again.Replayed || len(again.Reservations) != 1 || again.Reservations[0].ID != first.Reservations[0].ID {
		t.Fatalf("replay: %v %+v", err, again)
	}
	if avail, _ := availFor(t, s, ctx, product); !avail.Equal(dec("7")) {
		t.Fatalf("replay reserved twice: avail %s, want 7", avail)
	}
	if _, err := reserve(ctx, s, product, "4", domain.PolicyFIFO, domain.Dimensions{"site": "S1"}, "k"); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key, other body: %v", err)
	}
	if _, err := reserve(ctx, s, product, "1", domain.PolicyManual, dims("inventory_status", "BLOCKED"), "b"); problemCode(err) != domain.CodeBlockedReserve {
		t.Fatalf("reserving blocked stock: %v", err)
	}
	if _, err := reserve(ctx, s, product, "1", domain.PolicyManual, domain.Dimensions{"warehouse": "W1"}, "m"); problemCode(err) != domain.CodeDimensionMandatory {
		t.Fatalf("manual reservation without a site: %v", err)
	}
	other := entityCtx(1, ids.New())
	if _, err := reserve(other, s, product, "1", domain.PolicyFIFO, nil, "o"); problemCode(err) != domain.CodeReserveShortfall {
		t.Fatalf("another legal entity reserved this one's stock: %v", err)
	}
	if page, err := s.ListReservations(tenantWideCtx(2), store.ReservationFilter{}); err != nil || len(page) != 0 {
		t.Fatalf("tenant 2 sees reservations: %v %d", err, len(page))
	}
	page, err := s.ListReservations(ctx, store.ReservationFilter{ProductID: product, Status: domain.ReservationActive})
	if err != nil || len(page) != 1 || page[0].DemandID != "SO-1" || page[0].Dims["site"] != "S1" {
		t.Fatalf("list: %v %+v", err, page)
	}
}

// AC-16.4 against the database: FEFO spans batches in expiry order, skips
// blocked stock and batches expiring inside the margin, and returns the
// shortfall when partial reservation is allowed.
func TestFEFOReservationSpansBatchesInExpiryOrder(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	expiries := map[string]int{"B1": 40, "B2": 10, "B3": 20, "B4": 3, "B5": 30}
	var lines []domain.JournalLine
	for batch, days := range expiries {
		at := day.AddDate(0, 0, days)
		if err := s.UpsertBatchExpiry(ctx, product, batch, &at); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line(product, "5", "1", dims("batch", batch)))
	}
	lines = append(lines, line(product, "5", "1", dims("batch", "B6"))) // no expiry: last
	post(t, ctx, s, domain.JournalOpeningBalance, lines...)
	if _, _, err := s.CreateBlocking(ctx, store.BlockingInput{IdempotencyKey: "blk", LegalEntityID: legalEntity(ctx), ProductID: product,
		Dims: dims("batch", "B2"), Qty: dec("5"), Unit: "EA", Target: domain.StockBlocked, Reason: "QUALITY", PhysicalDate: day}); err != nil {
		t.Fatal(err)
	}
	in := store.ReserveInput{LegalEntityID: legalEntity(ctx), ProductID: product, Qty: dec("14"), DemandType: domain.DemandSalesOrder, DemandID: "SO-9",
		Policy: domain.PolicyFEFO, Dims: domain.Dimensions{"site": "S1"}, RequiredDate: day, ShelfLifeMarginDays: 5, IdempotencyKey: "fefo"}
	res, err := s.Reserve(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range res.Reservations {
		got = append(got, fmt.Sprintf("%s:%s", r.Dims["batch"], r.Qty))
	}
	// B4 expires inside the 5-day margin and B2 is blocked: B3, B5, then 4 of B1.
	if want := "[B3:5 B5:5 B1:4]"; fmt.Sprint(got) != want {
		t.Fatalf("FEFO allocations %v, want %s", got, want)
	}
	in.Qty, in.IdempotencyKey, in.AllowPartial = dec("10"), "fefo-2", true
	res, err = s.Reserve(ctx, in)
	if err != nil || len(res.Reservations) != 2 || !res.Shortfall.Equal(dec("4")) {
		t.Fatalf("partial: %v allocations %d shortfall %s; want B1:1, B6:5 and shortfall 4", err, len(res.Reservations), res.Shortfall)
	}
	in.Qty, in.IdempotencyKey, in.AllowPartial = dec("1"), "fefo-3", false
	if _, err := s.Reserve(ctx, in); problemCode(err) != domain.CodeReserveNoEligible {
		t.Fatalf("nothing eligible left: %v, want %s", err, domain.CodeReserveNoEligible)
	}
}

// Concurrent reservations serialize on the row locks and never over-reserve.
func TestConcurrentReservationsNeverOverReserve(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "20", "1", dims()))
	var ok, short, other atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := reserve(ctx, s, product, "1", domain.PolicyFIFO, nil, fmt.Sprintf("c-%d", i))
			switch {
			case err == nil:
				ok.Add(1)
			case problemCode(err) == domain.CodeReserveShortfall:
				short.Add(1)
			default:
				other.Add(1)
				t.Log(err)
			}
		}(i)
	}
	wg.Wait()
	if ok.Load() != 20 || short.Load() != 40 || other.Load() != 0 {
		t.Fatalf("ok=%d short=%d other=%d, want 20/40/0", ok.Load(), short.Load(), other.Load())
	}
	if avail, _ := availFor(t, s, ctx, product); !avail.IsZero() {
		t.Fatalf("avail %s, want 0", avail)
	}
}

// FR-16.8 / BR-16.10: blocking keeps physical stock and value, lowers
// availability, keeps restricted stock out of reservation, and a quarantine
// needs a disposition to exit.
func TestBlockingMovesStockOutOfAvailability(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	day := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "2.5", dims("batch", "B1")))
	in := store.BlockingInput{IdempotencyKey: "q-1", LegalEntityID: legalEntity(ctx), ProductID: product, Dims: dims("batch", "B1"),
		Qty: dec("4"), Unit: "EA", Target: domain.StockQuarantine, Reason: "RECALL", Note: "lot recall", PhysicalDate: day}
	b, replayed, err := s.CreateBlocking(ctx, in)
	if err != nil || replayed || b.State != domain.BlockingActive || b.Version != 1 || b.Note != "lot recall" {
		t.Fatalf("block: %v replayed=%v %+v", err, replayed, b)
	}
	phys, value := onHandFor(t, s, ctx, product)
	avail, blocked := availFor(t, s, ctx, product)
	if !phys.Equal(dec("10")) || !value.Equal(dec("25")) || !avail.Equal(dec("6")) || !blocked.Equal(dec("4")) {
		t.Fatalf("after block: physical %s value %s avail %s blocked %s; want 10, 25, 6, 4", phys, value, avail, blocked)
	}
	res, err := s.ListOnHand(ctx, store.OnHandFilter{ProductID: product, GroupBy: []string{"inventory_status"}})
	if err != nil || len(res.Rows) != 2 {
		t.Fatalf("by status: %v %+v", err, res.Rows)
	}
	for _, r := range res.Rows {
		if r.Group["inventory_status"] == domain.StockQuarantine && (!r.PostedValue.Equal(dec("10")) || !r.AvailPhysical.IsZero()) {
			t.Fatalf("quarantine row %+v: value 10 moves with the stock and none of it is available", r)
		}
	}
	if again, replayed, err := s.CreateBlocking(ctx, in); err != nil || !replayed || again.ID != b.ID {
		t.Fatalf("replay: %v %v %s", err, replayed, again.ID)
	}
	in.Qty = dec("5")
	if _, _, err := s.CreateBlocking(ctx, in); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key, other body: %v", err)
	}
	in.IdempotencyKey, in.Qty = "q-2", dec("7")
	if _, _, err := s.CreateBlocking(ctx, in); problemCode(err) != domain.CodeBlockExceeds {
		t.Fatalf("blocking more than available: %v", err)
	}
	if _, err := reserve(ctx, s, product, "7", domain.PolicyFIFO, nil, "r"); problemCode(err) != domain.CodeReserveShortfall {
		t.Fatalf("policy reservation reached quarantined stock: %v", err)
	}
	if _, err := s.ReleaseBlocking(ctx, b.ID, b.Version, "", day, postParams()); problemCode(err) != domain.CodeQuarantineDisposition {
		t.Fatalf("quarantine exit without disposition: %v", err)
	}
	if _, err := s.ReleaseBlocking(ctx, b.ID, b.Version+1, domain.DispositionRelease, day, postParams()); problemCode(err) != store.CodeConflictVersion {
		t.Fatalf("stale version: %v", err)
	}
	released, err := s.ReleaseBlocking(ctx, b.ID, b.Version, domain.DispositionRelease, day, postParams())
	if err != nil || released.State != domain.BlockingReleased || released.Disposition != domain.DispositionRelease || released.Version != 2 {
		t.Fatalf("release: %v %+v", err, released)
	}
	if again, err := s.ReleaseBlocking(ctx, b.ID, 1, domain.DispositionRelease, day, postParams()); err != nil || again.Version != 2 {
		t.Fatalf("second release must return the blocking unchanged: %v %+v", err, again)
	}
	avail, blocked = availFor(t, s, ctx, product)
	if phys, value := onHandFor(t, s, ctx, product); !phys.Equal(dec("10")) || !value.Equal(dec("25")) || !avail.Equal(dec("10")) || !blocked.IsZero() {
		t.Fatalf("after release: physical %s value %s avail %s blocked %s", phys, value, avail, blocked)
	}
	var legs int
	if err := admin.QueryRow(context.Background(), `select count(*) from inventory_transaction t join inventory_transaction_origin o
		on o.tenant_id = t.tenant_id and o.id = t.origin_id where o.source_type = 'BLOCKING' and o.reference_number = $1`, b.ID).Scan(&legs); err != nil || legs != 4 {
		t.Fatalf("blocking legs %d (%v), want 4 (block and release pairs)", legs, err)
	}
	txns, err := s.ListTransactions(ctx, store.TransactionFilter{ProductID: product, SourceType: "BLOCKING", Limit: 10})
	if err != nil || len(txns) != 4 || txns[0].Reference != b.ID || txns[0].JournalID != "" {
		t.Fatalf("transaction inquiry of blocking legs: %v %+v", err, txns)
	}
	if c, r := outboxCount(t, admin, "dev.passage.inventory.blocking.created.v1"), outboxCount(t, admin, "dev.passage.inventory.blocking.released.v1"); c != 1 || r != 1 {
		t.Fatalf("blocking events created=%d released=%d", c, r)
	}
	list, err := s.ListBlockings(ctx, store.BlockingFilter{State: domain.BlockingReleased})
	if err != nil || len(list) != 1 || list[0].ID != b.ID {
		t.Fatalf("list released: %v %+v", err, list)
	}
	if list, err := s.ListBlockings(tenantWideCtx(2), store.BlockingFilter{}); err != nil || len(list) != 0 {
		t.Fatalf("tenant 2 sees blockings: %v %d", err, len(list))
	}
}

func countPlan(t *testing.T, s *store.Postgres, ctx context.Context, blind bool) domain.CountPlan {
	t.Helper()
	p, inserted, err := s.CreateCountPlan(ctx, ids.New(), domain.CountPlan{LegalEntityID: legalEntity(ctx), Name: "Weekly W1", Warehouse: "W1",
		Currency: "USD", Unit: "EA", TolerancePct: dec("5"), ToleranceValue: dec("100"), Blind: blind})
	if err != nil || !inserted {
		t.Fatalf("plan: %v", err)
	}
	return p
}

// AC-16.6: a count variance of 8 % against a 5 % tolerance needs an approver;
// the posted COUNT journal line equals counted minus book, valued at the
// running average against the adjustment account.
func TestCountSheetPostsVarianceWithApproval(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product, other := ids.New(), ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "50", "2", dims()), line(other, "7", "1", dims()))
	w2 := dims()
	w2[domain.DimWarehouse] = "W2"
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "3", "2", w2))

	plan := countPlan(t, s, ctx, true)
	key := ids.New()
	sheet, replayed, err := s.GenerateCountSheet(ctx, plan.ID, key)
	if err != nil || replayed || sheet.Status != domain.CountOpen || len(sheet.Lines) != 2 || sheet.LineCount != 2 {
		t.Fatalf("generate: %v replayed=%v %+v (W2 stock must be left out)", err, replayed, sheet)
	}
	if again, replayed, err := s.GenerateCountSheet(ctx, plan.ID, key); err != nil || !replayed || again.ID != sheet.ID {
		t.Fatalf("generate replay: %v %v", err, replayed)
	}
	if _, err := s.PostCountSheet(ctx, sheet.ID, sheet.Version, false, postParams()); problemCode(err) != domain.CodeCountNothing {
		t.Fatalf("post with nothing counted: %v", err)
	}
	var mine domain.CountLine
	for _, l := range sheet.Lines {
		if l.ProductID == product {
			mine = l
		}
	}
	if !mine.Expected.Equal(dec("50")) {
		t.Fatalf("expected %s, want 50", mine.Expected)
	}
	counted, err := s.EnterCount(ctx, sheet.ID, mine.ID, sheet.Version, dec("46"))
	if err != nil || counted.Version != sheet.Version+1 || counted.CountedCount != 1 {
		t.Fatalf("enter: %v %+v", err, counted)
	}
	if _, err := s.EnterCount(ctx, sheet.ID, mine.ID, sheet.Version, dec("45")); problemCode(err) != store.CodeConflictVersion {
		t.Fatalf("stale count entry: %v", err)
	}
	if _, err := s.EnterCount(ctx, sheet.ID, ids.New(), counted.Version, dec("1")); problemCode(err) != domain.CodeCountNotFound {
		t.Fatalf("unknown line: %v", err)
	}
	if _, err := s.PostCountSheet(ctx, sheet.ID, counted.Version, false, postParams()); problemCode(err) != domain.CodeCountApproval {
		t.Fatalf("8%% variance without approval: %v, want %s", err, domain.CodeCountApproval)
	}
	if phys, _ := onHandFor(t, s, ctx, product); !phys.Equal(dec("53")) {
		t.Fatalf("refused post changed stock: %s", phys)
	}
	approver := approverCtx(ctx)
	posted, err := s.PostCountSheet(approver, sheet.ID, counted.Version, true, postParams())
	if err != nil || posted.Status != domain.CountPosted || posted.JournalID == "" || posted.ApprovedBy == "" || posted.PostedAt == nil {
		t.Fatalf("approved post: %v %+v", err, posted)
	}
	j, err := s.GetJournal(ctx, posted.JournalID)
	if err != nil || j.Type != domain.JournalCount || j.Status != domain.JournalPosted || len(j.Lines) != 1 || !j.Lines[0].Qty.Equal(dec("-4")) {
		t.Fatalf("COUNT journal: %v %+v", err, j)
	}
	if !j.GLEntry.Balanced() || !j.GLEntry.Net(inventoryAccount).Equal(dec("-8")) || !j.GLEntry.Net(adjustmentAccount).Equal(dec("8")) {
		t.Fatalf("COUNT GL entry %+v, want Dr %s 8 / Cr %s 8", j.GLEntry, adjustmentAccount, inventoryAccount)
	}
	for _, l := range posted.Lines {
		switch {
		case l.ProductID == product && (l.PostedVariance == nil || !l.PostedVariance.Equal(dec("-4"))):
			t.Fatalf("posted variance %v, want -4", l.PostedVariance)
		case l.ProductID == other && l.PostedVariance != nil:
			t.Fatalf("an uncounted line got a variance: %v", l.PostedVariance)
		}
	}
	if phys, value := onHandFor(t, s, ctx, product); !phys.Equal(dec("49")) || !value.Equal(dec("98")) {
		t.Fatalf("after count: %s / %s, want 49 / 98 (W1 46 + W2 3)", phys, value)
	}
	if again, err := s.PostCountSheet(ctx, sheet.ID, 1, false, postParams()); err != nil || again.JournalID != posted.JournalID {
		t.Fatalf("second post must return the posted sheet: %v", err)
	}
	if _, err := s.EnterCount(ctx, sheet.ID, mine.ID, posted.Version, dec("1")); problemCode(err) != domain.CodeCountSheetState {
		t.Fatalf("count on a posted sheet: %v", err)
	}
	if n := outboxCount(t, admin, "dev.passage.inventory.journal.posted.v1"); n != 3 {
		t.Fatalf("journal.posted events %d, want 3 (two opening balances and the COUNT journal)", n)
	}
}

// BR-16.8: the variance is measured against the book quantity at posting, not
// at generation, and a variance within tolerance posts without approval.
func TestCountVarianceUsesBookAtPosting(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "50", "2", dims()))
	sheet, _, err := s.GenerateCountSheet(ctx, countPlan(t, s, ctx, false).ID, ids.New())
	if err != nil || len(sheet.Lines) != 1 {
		t.Fatalf("generate: %v", err)
	}
	post(t, ctx, s, domain.JournalAdjustment, line(product, "2", "2", dims())) // movement after generation
	counted, err := s.EnterCount(ctx, sheet.ID, sheet.Lines[0].ID, sheet.Version, dec("53"))
	if err != nil {
		t.Fatal(err)
	}
	posted, err := s.PostCountSheet(ctx, counted.ID, counted.Version, false, postParams())
	if err != nil || posted.Lines[0].PostedVariance == nil || !posted.Lines[0].PostedVariance.Equal(dec("1")) {
		t.Fatalf("post: %v %+v; variance must be 53 - 52 = 1", err, posted.Lines)
	}
	if phys, value := onHandFor(t, s, ctx, product); !phys.Equal(dec("53")) || !value.Equal(dec("106")) {
		t.Fatalf("after count: %s / %s, want 53 / 106 (gain valued at the running average 2)", phys, value)
	}
}

// AC2 for slice 2: every projection row equals the transactions behind it,
// and reserved_physical equals the active reservations, after journals,
// blockings, reservations and a count.
func TestProjectionReconcilesAfterSlice2Operations(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	day := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "30", "1.3", dims("batch", "B1")), line(product, "12", "2.2", dims("batch", "B2")))
	if _, _, err := s.CreateBlocking(ctx, store.BlockingInput{IdempotencyKey: "x", LegalEntityID: legalEntity(ctx), ProductID: product,
		Dims: dims("batch", "B1"), Qty: dec("7"), Unit: "EA", Target: domain.StockBlocked, Reason: "DAMAGE", PhysicalDate: day}); err != nil {
		t.Fatal(err)
	}
	r, err := reserve(ctx, s, product, "9", domain.PolicyFIFO, nil, "f")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseReservation(ctx, r.Reservations[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reserve(ctx, s, product, "5", domain.PolicyLIFO, nil, "l"); err != nil {
		t.Fatal(err)
	}
	sheet, _, err := s.GenerateCountSheet(ctx, countPlan(t, s, ctx, false).ID, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	version := sheet.Version
	for _, l := range sheet.Lines {
		next, err := s.EnterCount(ctx, sheet.ID, l.ID, version, l.Expected.Sub(dec("0.5")).Abs())
		if err != nil {
			t.Fatal(err)
		}
		version = next.Version
	}
	if _, err := s.PostCountSheet(ctx, sheet.ID, version, true, postParams()); err != nil {
		t.Fatal(err)
	}
	rows, err := admin.Query(context.Background(), `select o.posted_qty::text, o.posted_value::text, o.reserved_physical::text,
		(select coalesce(sum(t.qty), 0) from inventory_transaction t where t.tenant_id = o.tenant_id and t.legal_entity_id = o.legal_entity_id
		  and t.product_id = o.product_id and t.dimension_set_hash = o.dimension_set_hash)::text,
		(select coalesce(sum(t.cost_amount_posted), 0) from inventory_transaction t where t.tenant_id = o.tenant_id and t.legal_entity_id = o.legal_entity_id
		  and t.product_id = o.product_id and t.dimension_set_hash = o.dimension_set_hash)::text,
		(select coalesce(sum(r.qty), 0) from inventory_reservation r where r.tenant_id = o.tenant_id and r.legal_entity_id = o.legal_entity_id
		  and r.product_id = o.product_id and r.dimension_set_hash = o.dimension_set_hash and r.status = 'ACTIVE')::text
		from on_hand o where o.tenant_id = $1`, testkit.TenantID(1))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var pq, pv, rp, sq, sv, sr string
		if err := rows.Scan(&pq, &pv, &rp, &sq, &sv, &sr); err != nil {
			t.Fatal(err)
		}
		if !dec(pq).Equal(dec(sq)) || !dec(pv).Equal(dec(sv)) || !dec(rp).Equal(dec(sr)) {
			t.Fatalf("projection %s / %s / reserved %s != transactions %s / %s / reservations %s", pq, pv, rp, sq, sv, sr)
		}
		n++
	}
	if n != 3 {
		t.Fatalf("checked %d rows, want 3 (B1, B1 blocked, B2)", n)
	}
}

func TestTransactionInquiryDimensionDrillDown(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance,
		line(product, "1", "1", dims()),
		line(product, "2", "1", dims("batch", "B")),
		line(product, "3", "1", dims("batch", "C", "location", "L1", "inventory_status", "BLOCKED")))
	count := func(f store.TransactionFilter) int {
		t.Helper()
		f.ProductID, f.Limit = product, 10
		rows, err := s.ListTransactions(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}
	for name, tc := range map[string]struct {
		dims map[string]string
		want int
	}{
		"site":               {map[string]string{"site": "S1"}, 3},
		"batch":              {map[string]string{"batch": "B"}, 1},
		"site and batch":     {map[string]string{"site": "S1", "batch": "C"}, 1},
		"batch and location": {map[string]string{"batch": "C", "location": "L1"}, 1},
		"wrong site":         {map[string]string{"site": "S2"}, 0},
		"location mismatch":  {map[string]string{"batch": "B", "location": "L1"}, 0},
		"inventory status":   {map[string]string{"inventory_status": "BLOCKED"}, 1},
		"empty map":          {map[string]string{}, 3},
	} {
		if got := count(store.TransactionFilter{Dims: tc.dims}); got != tc.want {
			t.Errorf("%s: %d rows, want %d", name, got, tc.want)
		}
	}
	if got := count(store.TransactionFilter{LegalEntityID: legalEntity(ctx), Dims: map[string]string{"site": "S1"}}); got != 3 {
		t.Errorf("own legal entity: %d rows, want 3", got)
	}
	if got := count(store.TransactionFilter{LegalEntityID: ids.New()}); got != 0 {
		t.Errorf("other legal entity: %d rows", got)
	}
}

// The legal-entity filter only narrows: a caller scoped to one legal entity
// who names another, which holds stock of the same product, still gets none
// of its rows (row-level security on the application role).
func TestTransactionInquiryLegalEntityFilterDoesNotWidenScope(t *testing.T) {
	s, _, _ := setup(t)
	own, other := ids.New(), ids.New()
	product := ids.New()
	post(t, entityCtx(1, own), s, domain.JournalOpeningBalance, line(product, "1", "1", dims()))
	post(t, entityCtx(1, other), s, domain.JournalOpeningBalance, line(product, "2", "1", dims()), line(product, "3", "1", dims()))
	list := func(ctx context.Context, entity string) []store.TransactionRow {
		t.Helper()
		rows, err := s.ListTransactions(ctx, store.TransactionFilter{ProductID: product, LegalEntityID: entity, Dims: map[string]string{"site": "S1"}, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	if rows := list(entityCtx(1, own), other); len(rows) != 0 {
		t.Fatalf("caller of %s naming %s saw %d rows", own, other, len(rows))
	}
	if rows := list(entityCtx(1, own), ""); len(rows) != 1 || rows[0].LegalEntityID != own {
		t.Fatalf("own scope: %+v", rows)
	}
	if rows := list(tenantWideCtx(1), other); len(rows) != 2 || rows[0].LegalEntityID != other || rows[1].LegalEntityID != other {
		t.Fatalf("tenant-wide caller naming %s: %+v", other, rows)
	}
}

func TestTransactionInquiryPages(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	j := post(t, ctx, s, domain.JournalOpeningBalance, line(product, "1", "1", dims()), line(product, "2", "1", dims("batch", "B")), line(product, "3", "1", dims("batch", "C")))
	post(t, ctx, s, domain.JournalOpeningBalance, line(ids.New(), "1", "1", dims()))
	first, err := s.ListTransactions(ctx, store.TransactionFilter{ProductID: product, Limit: 2})
	if err != nil || len(first) != 2 || first[0].JournalID != j.ID || first[0].SourceType != "INVENT_JOURNAL_LINE" || first[0].ReceiptStatus != "PURCHASED" {
		t.Fatalf("first page: %v %+v", err, first)
	}
	rest, err := s.ListTransactions(ctx, store.TransactionFilter{ProductID: product, Limit: 2, Cursor: first[1].ID})
	if err != nil || len(rest) != 1 || rest[0].ID >= first[1].ID {
		t.Fatalf("second page: %v %+v", err, rest)
	}
	from := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	if none, err := s.ListTransactions(ctx, store.TransactionFilter{ProductID: product, From: &from, Limit: 10}); err != nil || len(none) != 0 {
		t.Fatalf("date filter: %v %d", err, len(none))
	}
	if none, err := s.ListTransactions(ctx, store.TransactionFilter{Status: domain.StatusSold, Limit: 10}); err != nil || len(none) != 0 {
		t.Fatalf("status filter: %v %d", err, len(none))
	}
	if none, err := s.ListTransactions(tenantWideCtx(2), store.TransactionFilter{Limit: 10}); err != nil || len(none) != 0 {
		t.Fatalf("tenant 2 sees transactions: %v %d", err, len(none))
	}
}

// A retry that races the original request waits for it and replays it: one
// reservation, no unique-key failure. An outside transaction holds the on-hand
// row while the requests start, so they all arrive before the first commits.
func TestConcurrentRetriesOfOneReservationReplay(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims()))
	hold, err := admin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hold.Exec(context.Background(), `select 1 from on_hand where product_id = $1 for update`, product); err != nil {
		t.Fatal(err)
	}
	var replays, failures atomic.Int64
	var wg sync.WaitGroup
	defer func() { _ = hold.Rollback(context.Background()) }()
	release := time.AfterFunc(300*time.Millisecond, func() { _ = hold.Commit(context.Background()) })
	defer release.Stop()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := reserve(ctx, s, product, "2", domain.PolicyFIFO, nil, "same-key")
			switch {
			case err != nil:
				failures.Add(1)
				t.Log(err)
			case res.Replayed:
				replays.Add(1)
			}
		}()
	}
	wg.Wait()
	if failures.Load() != 0 || replays.Load() != 7 {
		t.Fatalf("failures=%d replays=%d, want 0 and 7", failures.Load(), replays.Load())
	}
	if avail, _ := availFor(t, s, ctx, product); !avail.Equal(dec("8")) {
		t.Fatalf("avail %s, want 8 (one reservation of 2)", avail)
	}
}
