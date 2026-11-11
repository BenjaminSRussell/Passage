package store_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
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
