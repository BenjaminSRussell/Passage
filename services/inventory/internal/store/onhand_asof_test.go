package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

func postDated(t *testing.T, ctx context.Context, s *store.Postgres, date time.Time, typ domain.JournalType, lines ...domain.JournalLine) {
	t.Helper()
	h := header(ctx, typ, "USD")
	h.PostingDate = date
	created, _, err := s.CreateJournal(ctx, h, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.AddJournalLines(ctx, created.ID, lines)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, j.ID, j.Version, postParams()); err != nil {
		t.Fatal(err)
	}
}

func asOf(t *testing.T, ctx context.Context, s *store.Postgres, product, date string, group ...string) store.OnHandResult {
	t.Helper()
	res, err := s.ListOnHand(ctx, store.OnHandFilter{ProductID: product, AsOf: date, GroupBy: group})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestOnHandAsOfReplaysTheLedgerByDate(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	day := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }
	postDated(t, ctx, s, day(1), domain.JournalOpeningBalance, line(product, "10", "2", dims()))
	postDated(t, ctx, s, day(10), domain.JournalAdjustment, line(product, "-4", "0", dims()))
	postDated(t, ctx, s, day(20), domain.JournalAdjustment, line(product, "5", "3", dims("batch", "B1")))

	if res := asOf(t, ctx, s, product, "2026-08-31", "product"); len(res.Rows) != 0 {
		t.Fatalf("before the first posting: %+v", res.Rows)
	}
	cases := []struct{ date, qty, value string }{
		{"2026-09-01", "10", "20"},
		{"2026-09-09", "10", "20"},
		{"2026-09-10", "6", "12"},
		{"2026-09-19", "6", "12"},
		{"2026-09-20", "11", "27"},
	}
	for _, c := range cases {
		res := asOf(t, ctx, s, product, c.date, "product")
		if len(res.Rows) != 1 || !res.Rows[0].PhysicalInvent.Equal(dec(c.qty)) || !res.Rows[0].PostedValue.Equal(dec(c.value)) || res.Rows[0].Currency != "USD" || res.AsOf != c.date {
			t.Fatalf("as of %s: %+v, want %s / %s", c.date, res, c.qty, c.value)
		}
		if !res.Rows[0].AvailPhysical.IsZero() || !res.Rows[0].ReservedPhysical.IsZero() {
			t.Fatalf("availability must not be answered for a date: %+v", res.Rows[0])
		}
	}
	// The latest replay equals the live projection; grouping by batch splits it.
	phys, value := onHandFor(t, s, ctx, product)
	if !phys.Equal(dec("11")) || !value.Equal(dec("27")) {
		t.Fatalf("live %s / %s", phys, value)
	}
	byBatch := asOf(t, ctx, s, product, "2026-09-30", "batch")
	if len(byBatch.Rows) != 2 || byBatch.Rows[0].Group["batch"] != "" || !byBatch.Rows[0].PhysicalInvent.Equal(dec("6")) || !byBatch.Rows[1].PhysicalInvent.Equal(dec("5")) {
		t.Fatalf("by batch: %+v", byBatch.Rows)
	}
}

func TestOnHandAsOfIsTenantAndEntityScoped(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	postDated(t, ctx, s, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), domain.JournalOpeningBalance, line(product, "3", "1", dims()))
	if res := asOf(t, entityCtx(1, ids.New()), s, product, "2026-12-31", "product"); len(res.Rows) != 0 {
		t.Fatalf("another legal entity sees %+v", res.Rows)
	}
	if res := asOf(t, entityCtx(2, ids.New()), s, product, "2026-12-31", "product"); len(res.Rows) != 0 {
		t.Fatalf("another tenant sees %+v", res.Rows)
	}
}

// registerOn and stepOn register and move transactions through transactions:apply
// on a given date, so the status change history carries that date.
func registerOn(t *testing.T, ctx context.Context, s *store.Postgres, date time.Time, product, qty, cost string, status domain.Status, d domain.Dimensions) string {
	t.Helper()
	q := dec(qty)
	c, _, err := s.CreateTransaction(ctx, store.CreateTransactionInput{LegalEntityID: legalEntity(ctx), SourceType: "SALES_LINE",
		SourceID: ids.New(), ProductID: product, Dims: d, Qty: q, EnteredQty: q, EnteredUnit: "EA", Status: status,
		Currency: "USD", CostPrice: dec(cost), Date: date, IdempotencyKey: ids.New()})
	if err != nil {
		t.Fatal(err)
	}
	return c.TransactionID
}

func stepOn(t *testing.T, ctx context.Context, s *store.Postgres, date time.Time, txn string, cmd domain.StepCommand, from, to domain.Status) {
	t.Helper()
	if _, _, err := s.ApplyStatusStep(ctx, store.StatusStepInput{TransactionID: txn, Command: cmd, From: from, To: to,
		Date: date, IdempotencyKey: ids.New()}); err != nil {
		t.Fatal(err)
	}
}

// A transaction counts at the status it held on the date, from its status
// changes: a pick later undone still reduced stock while it was picked, a
// receipt counts only from its registration and is valued only from its
// purchase, and an issue registered after the date is left out.
func TestOnHandAsOfReplaysStatusHistory(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	day := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }
	postDated(t, ctx, s, day(1), domain.JournalOpeningBalance, line(product, "10", "2", dims()))

	issue := registerOn(t, ctx, s, day(3), product, "-4", "0", domain.StatusPicked, dims())
	stepOn(t, ctx, s, day(6), issue, domain.CommandUnpick, "", "")
	stepOn(t, ctx, s, day(12), issue, domain.CommandStep, domain.StatusReservedPhysical, domain.StatusSold)

	c, _, err := s.CreateTransaction(ctx, store.CreateTransactionInput{LegalEntityID: legalEntity(ctx), SourceType: "PURCH_LINE",
		SourceID: ids.New(), ProductID: product, Dims: dims(), Qty: dec("5"), EnteredQty: dec("5"), EnteredUnit: "EA",
		Status: domain.StatusOrdered, Currency: "USD", CostPrice: dec("3"), Date: day(2), IdempotencyKey: ids.New()})
	if err != nil {
		t.Fatal(err)
	}
	stepOn(t, ctx, s, day(8), c.TransactionID, domain.CommandStep, domain.StatusOrdered, domain.StatusRegistered)
	stepOn(t, ctx, s, day(14), c.TransactionID, domain.CommandStep, domain.StatusRegistered, domain.StatusPurchased)

	registerOn(t, ctx, s, day(25), product, "-1", "0", domain.StatusPicked, dims())

	cases := []struct{ date, qty, value string }{
		{"2026-09-02", "10", "20"}, // the purchase is only ordered
		{"2026-09-04", "6", "20"},  // 4 picked
		{"2026-09-07", "10", "20"}, // unpicked: reserved again
		{"2026-09-09", "15", "20"}, // 5 registered, not yet valued
		{"2026-09-12", "11", "12"}, // 4 sold at the 2.00 average
		{"2026-09-14", "11", "27"}, // the purchase is valued at 15
		{"2026-09-24", "11", "27"}, // the later pick is not yet registered
		{"2026-09-25", "10", "27"},
	}
	for _, c := range cases {
		res := asOf(t, ctx, s, product, c.date, "product")
		if len(res.Rows) != 1 || !res.Rows[0].PhysicalInvent.Equal(dec(c.qty)) || !res.Rows[0].PostedValue.Equal(dec(c.value)) {
			t.Fatalf("as of %s: %+v, want %s / %s", c.date, res.Rows, c.qty, c.value)
		}
	}
	if phys, value := onHandFor(t, s, ctx, product); !phys.Equal(dec("10")) || !value.Equal(dec("27")) {
		t.Fatalf("live %s / %s, want the latest replay", phys, value)
	}
}

// Blocked stock and the negative flag are replayed per row: blocked stock
// counts in physical and blocked from its posting, and a row driven negative
// (where the policy allows it) is flagged on the dates it was negative only,
// even when the product total is positive.
func TestOnHandAsOfReplaysBlockedAndNegative(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	day := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }
	postDated(t, ctx, s, day(1), domain.JournalOpeningBalance, line(product, "4", "1", dims()))
	postDated(t, ctx, s, day(2), domain.JournalOpeningBalance, line(product, "3", "1", dims("inventory_status", "BLOCKED")))
	allowed := domain.NegativePolicy{PhysicalAllowed: true, FinancialAllowed: true}
	for _, in := range []store.CreateTransactionInput{
		{SourceType: "SALES_LINE", Qty: dec("-6"), Status: domain.StatusPicked, Date: day(5)},
		{SourceType: "PURCH_LINE", Qty: dec("5"), Status: domain.StatusRegistered, Date: day(8), CostPrice: dec("1")},
	} {
		in.LegalEntityID, in.SourceID, in.ProductID, in.Dims, in.EnteredQty, in.EnteredUnit = legalEntity(ctx), ids.New(), product, dims(), in.Qty, "EA"
		in.Currency, in.IdempotencyKey, in.Negative = "USD", ids.New(), allowed
		if _, _, err := s.CreateTransaction(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		date, qty, blocked string
		negative           bool
	}{
		{"2026-09-01", "4", "0", false},
		{"2026-09-02", "7", "3", false},
		{"2026-09-05", "1", "3", true}, // the free row is at -2
		{"2026-09-08", "6", "3", false},
	}
	for _, c := range cases {
		res := asOf(t, ctx, s, product, c.date, "product")
		if len(res.Rows) != 1 || !res.Rows[0].PhysicalInvent.Equal(dec(c.qty)) || !res.Rows[0].Blocked.Equal(dec(c.blocked)) || res.Rows[0].Negative != c.negative {
			t.Fatalf("as of %s: %+v, want physical %s blocked %s negative %v", c.date, res.Rows, c.qty, c.blocked, c.negative)
		}
	}
}
