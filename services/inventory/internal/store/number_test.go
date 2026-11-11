package store_test

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
	"github.com/BenjaminSRussell/passage/services/inventory/migrations"
)

// Every journal gets the next IJ number of its legal entity on insert (migration
// 0042), whichever path creates it, and the list finds a journal by the start of
// its number.
func TestJournalsAreNumberedPerLegalEntity(t *testing.T) {
	s, _, _ := setup(t)
	entityA, entityB := ids.New(), ids.New()
	ctxA, ctxB := entityCtx(1, entityA), entityCtx(1, entityB)
	product := ids.New()
	first, err := newJournal(ctxA, s, domain.JournalAdjustment, line(product, "1", "1", dims()))
	if err != nil {
		t.Fatal(err)
	}
	second, err := newJournal(ctxA, s, domain.JournalAdjustment, line(product, "2", "1", dims()))
	if err != nil {
		t.Fatal(err)
	}
	other, err := newJournal(ctxB, s, domain.JournalAdjustment, line(product, "1", "1", dims()))
	if err != nil {
		t.Fatal(err)
	}
	if first.Number != "IJ-000001" || second.Number != "IJ-000002" || other.Number != "IJ-000001" {
		t.Fatalf("numbers %q %q, other entity %q; want IJ-000001, IJ-000002 and a separate count per legal entity", first.Number, second.Number, other.Number)
	}
	got, err := s.GetJournal(ctxA, second.ID)
	if err != nil || got.Number != "IJ-000002" {
		t.Fatalf("read back %q, %v", got.Number, err)
	}
	page, err := s.ListJournals(ctxA, store.JournalFilter{Number: "ij-000002", Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != second.ID {
		t.Fatalf("by number = %+v, %v", page.Items, err)
	}
	if page, err := s.ListJournals(ctxA, store.JournalFilter{Number: "_J", Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("a LIKE wildcard matched %d journals, %v", len(page.Items), err)
	}
}

// Past 999999 the number grows a digit instead of being cut to six, so it never
// repeats an earlier number.
func TestJournalNumbersGrowPastSixDigits(t *testing.T) {
	s, _, admin := setup(t)
	entity := ids.New()
	ctx := entityCtx(1, entity)
	if _, err := admin.Exec(context.Background(), `insert into inventory_journal_counter (tenant_id, legal_entity_id, last_number) values ($1, $2, 999999)`,
		testkit.TenantID(1), entity); err != nil {
		t.Fatal(err)
	}
	product := ids.New()
	first, err := newJournal(ctx, s, domain.JournalAdjustment, line(product, "1", "1", dims()))
	if err != nil {
		t.Fatal(err)
	}
	second, err := newJournal(ctx, s, domain.JournalAdjustment, line(product, "1", "1", dims()))
	if err != nil {
		t.Fatal(err)
	}
	if first.Number != "IJ-1000000" || second.Number != "IJ-1000001" {
		t.Fatalf("numbers %q and %q; want IJ-1000000 and IJ-1000001", first.Number, second.Number)
	}
}

// backfillBlock is the DO block of migration 0042 that numbers journals without a number.
func backfillBlock(t *testing.T) string {
	t.Helper()
	raw, err := fs.ReadFile(migrations.FS, "0042_journal_number.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	start := strings.Index(sql, "DO $$\nBEGIN\n  IF NOT EXISTS")
	end := strings.Index(sql[start:], "$$;")
	if start < 0 || end < 0 {
		t.Fatal("backfill block not found in 0042")
	}
	return sql[start : start+end+3]
}

// The backfill numbers the journals left without a number after the counter, so
// running it again after the trigger has numbered journals reuses no number, and
// a second run changes nothing.
func TestJournalNumberBackfillContinuesAfterTheCounter(t *testing.T) {
	s, _, admin := setup(t)
	entity := ids.New()
	ctx := entityCtx(1, entity)
	product := ids.New()
	var made []domain.Journal
	for i := 0; i < 3; i++ {
		j, err := newJournal(ctx, s, domain.JournalAdjustment, line(product, "1", "1", dims()))
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, j)
	}
	// A journal written before the trigger existed has no number.
	if _, err := admin.Exec(context.Background(), `update inventory_journal set number = null where tenant_id = $1 and id = $2`, testkit.TenantID(1), made[1].ID); err != nil {
		t.Fatal(err)
	}
	block := backfillBlock(t)
	for run := 0; run < 2; run++ {
		if _, err := admin.Exec(context.Background(), block); err != nil {
			t.Fatalf("backfill run %d: %v", run+1, err)
		}
	}
	got, err := s.GetJournal(ctx, made[1].ID)
	if err != nil || got.Number != "IJ-000004" {
		t.Fatalf("backfilled number %q, %v; want IJ-000004 after the counter's 3", got.Number, err)
	}
	next, err := newJournal(ctx, s, domain.JournalAdjustment, line(product, "1", "1", dims()))
	if err != nil || next.Number != "IJ-000005" {
		t.Fatalf("next number %q, %v; want IJ-000005", next.Number, err)
	}
}
