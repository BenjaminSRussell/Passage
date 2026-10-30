package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

func transferLine(product, qty string, from, to domain.Dimensions) domain.JournalLine {
	l := line(product, qty, "0", from)
	l.ToDims = to
	return l
}

func rowValue(t *testing.T, s *store.Postgres, ctx context.Context, product string, d domain.Dimensions) (string, string) {
	t.Helper()
	res, err := s.ListOnHand(ctx, store.OnHandFilter{ProductID: product, GroupBy: []string{"product", "location"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Rows {
		if r.Group["location"] == d["location"] {
			return r.PhysicalInvent.String(), r.PostedValue.String()
		}
	}
	return "0", "0"
}

// A TRANSFER journal moves quantity and value between dimension sets: the issue
// is valued at the source's running average, the receipt takes the same value,
// the GL entry is empty and every row still reconciles with its transactions.
func TestTransferJournalMovesQuantityAndValue(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product, other := ids.New(), ids.New()
	a, b := dims("location", "A1"), dims("location", "B7")
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "2.5", a), line(other, "3", "1", a))
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "5", "3", a))

	posted := post(t, ctx, s, domain.JournalTransfer, transferLine(product, "9", a, b), transferLine(product, "1", a, dims("location", "C2")),
		transferLine(other, "3", a, b))
	if posted.Status != domain.JournalPosted || posted.OffsetAccountID != "" {
		t.Fatalf("transfer journal %+v", posted)
	}
	if posted.GLEntry == nil || len(posted.GLEntry.Lines) != 0 || !posted.GLEntry.Balanced() {
		t.Fatalf("a transfer has no GL lines, got %+v", posted.GLEntry)
	}
	// Average of the source: (25 + 15) / 15 = 2.666667; 9 units -> 24.00, 1 unit -> 2.67, remaining 5 -> 13.33.
	for _, c := range []struct {
		d          domain.Dimensions
		qty, value string
	}{{a, "5", "13.33"}, {b, "9", "24"}, {dims("location", "C2"), "1", "2.67"}} {
		if q, v := rowValue(t, s, ctx, product, c.d); q != c.qty || v != c.value {
			t.Fatalf("product at %v = %s / %s, want %s / %s", c.d["location"], q, v, c.qty, c.value)
		}
	}
	if q, v := rowValue(t, s, ctx, other, a); q != "0" || v != "0" {
		t.Fatalf("other at A1 = %s / %s, want empty", q, v)
	}
	if q, v := rowValue(t, s, ctx, other, b); q != "3" || v != "3" {
		t.Fatalf("other at B7 = %s / %s, want 3 / 3", q, v)
	}
	if phys, value := onHandFor(t, s, ctx, product); !phys.Equal(dec("15")) || !value.Equal(dec("40")) {
		t.Fatalf("product total %s / %s must not change, want 15 / 40", phys, value)
	}
	var legs int
	if err := admin.QueryRow(context.Background(), `select count(*) from inventory_transaction_origin where tenant_id = $1 and source_type = 'TRANSFER_IN'`,
		testkit.TenantID(1)).Scan(&legs); err != nil || legs != 3 {
		t.Fatalf("receipt legs = %d (%v), want 3", legs, err)
	}
	reconcile(t, admin)
	got, err := s.GetJournal(ctx, posted.ID)
	if err != nil || len(got.Lines) != 3 || got.Lines[0].ToDims["location"] != "B7" {
		t.Fatalf("reloaded journal %v %+v", err, got)
	}
}

func TestTransferJournalRules(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	a, b := dims("location", "A1"), dims("location", "B7")
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "4", "2", a))

	for name, c := range map[string]struct {
		l    domain.JournalLine
		code string
	}{
		"same dimensions":         {transferLine(product, "1", a, a), domain.CodeTransferLine},
		"negative quantity":       {transferLine(product, "-1", a, b), domain.CodeTransferLine},
		"no destination":          {transferLine(product, "1", a, nil), domain.CodeDimensionMandatory},
		"destination no site":     {transferLine(product, "1", a, domain.Dimensions{domain.DimLocation: "X"}), domain.CodeDimensionMandatory},
		"batch changed":           {transferLine(product, "1", dims("batch", "B1"), dims("location", "B7", "batch", "B2")), domain.CodeTransferLine},
		"product dimension":       {transferLine(product, "1", dims("color", "RED"), dims("location", "B7", "color", "BLUE")), domain.CodeTransferLine},
		"into quarantine":         {transferLine(product, "1", a, dims("location", "B7", "inventory_status", "QUARANTINE")), domain.CodeTransferLine},
		"out of blocked":          {transferLine(product, "1", dims("location", "A1", "inventory_status", "BLOCKED"), b), domain.CodeTransferLine},
		"vendor-owned target":     {transferLine(product, "1", a, dims("location", "B7", "owner", "V1")), domain.CodeTransferLine},
		"vendor-owned source":     {transferLine(product, "1", dims("location", "A1", "owner", "V1"), b), domain.CodeOwnershipValue},
		"unknown destination key": {transferLine(product, "1", a, domain.Dimensions{"site": "S1", "bin": "x"}), domain.CodeTransferLine},
	} {
		if _, err := newJournal(ctx, s, domain.JournalTransfer, c.l); problemCode(err) != c.code {
			t.Fatalf("%s: %v, want %s", name, err, c.code)
		}
	}
	// A row that is a source and a destination in one journal is refused.
	if _, err := newJournal(ctx, s, domain.JournalTransfer, transferLine(product, "1", a, b), transferLine(product, "1", b, dims("location", "C2"))); problemCode(err) != domain.CodeTransferLine {
		t.Fatalf("chain: %v, want %s", err, domain.CodeTransferLine)
	}
	// toDimensions only on TRANSFER journals.
	if _, err := newJournal(ctx, s, domain.JournalAdjustment, transferLine(product, "1", a, b)); err == nil {
		t.Fatal("toDimensions accepted on an ADJUSTMENT")
	}
	// More than is on hand is refused by the negative policy; nothing is written.
	j, err := newJournal(ctx, s, domain.JournalTransfer, transferLine(product, "5", a, b))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, j.ID, j.Version, postParams()); problemCode(err) != domain.CodeNegativePhysical {
		t.Fatalf("over-transfer: %v, want %s", err, domain.CodeNegativePhysical)
	}
	if q, v := rowValue(t, s, ctx, product, a); q != "4" || v != "8" {
		t.Fatalf("source after a refused transfer = %s / %s, want 4 / 8", q, v)
	}
	if _, ok := rowValueExists(t, s, ctx, product, b); ok {
		t.Fatal("destination row written by a refused transfer")
	}
	// Posted transfers cannot be reversed; post a transfer back.
	ok := post(t, ctx, s, domain.JournalTransfer, transferLine(product, "4", a, b))
	if _, _, err := s.ReverseJournal(ctx, ok.ID, reversalDate); problemCode(err) != domain.CodeJournalNotReady {
		t.Fatalf("reverse a transfer: %v, want %s", err, domain.CodeJournalNotReady)
	}
	back := post(t, ctx, s, domain.JournalTransfer, transferLine(product, "4", b, a))
	if back.Status != domain.JournalPosted {
		t.Fatal("transfer back not posted")
	}
	if q, v := rowValue(t, s, ctx, product, a); q != "4" || v != "8" {
		t.Fatalf("after the round trip A1 = %s / %s, want 4 / 8", q, v)
	}
	reconcile(t, admin)
}

func TestTransferJournalValidateDoesNotPost(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	a, b := dims("location", "A1"), dims("location", "B7")
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "4", "2", a))
	j, err := newJournal(ctx, s, domain.JournalTransfer, transferLine(product, "2", a, b))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateJournal(ctx, j.ID, postParams()); err != nil {
		t.Fatal(err)
	}
	if q, _ := rowValue(t, s, ctx, product, b); q != "0" {
		t.Fatalf("validate moved stock: %s", q)
	}
}

func rowValueExists(t *testing.T, s *store.Postgres, ctx context.Context, product string, d domain.Dimensions) (string, bool) {
	t.Helper()
	res, err := s.ListOnHand(ctx, store.OnHandFilter{ProductID: product, GroupBy: []string{"product", "location"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Rows {
		if r.Group["location"] == d["location"] {
			return r.PhysicalInvent.String(), true
		}
	}
	return "", false
}

// A serial moves with its unit: the serial check sees one unit after the
// transfer, and a second receipt of the serial elsewhere is still refused.
func TestTransferJournalMovesASerial(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	a, b := dims("location", "A1", "serial", "SN-1"), dims("location", "B7", "serial", "SN-1")
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "1", "40", a))
	post(t, ctx, s, domain.JournalTransfer, transferLine(product, "1", a, b))
	if q, v := rowValue(t, s, ctx, product, b); q != "1" || v != "40" {
		t.Fatalf("serial at B7 = %s / %s, want 1 / 40", q, v)
	}
	j, err := newJournal(ctx, s, domain.JournalAdjustment, line(product, "1", "40", dims("location", "C2", "serial", "SN-1")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, j.ID, j.Version, postParams()); problemCode(err) != domain.CodeDuplicateSerial {
		t.Fatalf("second receipt of the serial: %v, want %s", err, domain.CodeDuplicateSerial)
	}
	reconcile(t, admin)
}

// A retried line with the same client key is skipped; the same key with another
// destination is idempotency_key_reuse, not a second line.
func TestTransferJournalLineReplayComparesTheDestination(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	a, b := dims("location", "A1"), dims("location", "B7")
	l := transferLine(product, "2", a, b)
	l.ClientKey = "line-1"
	j, err := newJournal(ctx, s, domain.JournalTransfer, l)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.AddJournalLines(ctx, j.ID, []domain.JournalLine{l}); err != nil || len(again.Lines) != 1 {
		t.Fatalf("replay: %v, %d lines", err, len(again.Lines))
	}
	other := l
	other.ToDims = dims("location", "C2")
	if _, err := s.AddJournalLines(ctx, j.ID, []domain.JournalLine{other}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key, other destination: %v, want %s", err, store.CodeIdempotencyReuse)
	}
}
