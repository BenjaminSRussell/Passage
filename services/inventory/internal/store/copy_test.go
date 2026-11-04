package store_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// A copy is a new DRAFT with the source's header and lines; the source, posted
// or not, is untouched, the key replays, and the unrelated journal kinds refuse.
func TestCopyJournalMakesADraftOfAnyJournal(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product, other := ids.New(), ids.New()
	src := post(t, ctx, s, domain.JournalMovement, line(product, "10", "3", dims("batch", "B1")), line(other, "4", "1.25", dims()))
	when := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

	cp, inserted, err := s.CopyJournal(ctx, src.ID, when, "copy-1", false)
	if err != nil || !inserted {
		t.Fatalf("copy: %v inserted=%v", err, inserted)
	}
	if cp.ID == src.ID || cp.Status != domain.JournalDraft || cp.Type != src.Type || cp.OffsetAccountID != src.OffsetAccountID ||
		cp.Currency != src.Currency || !cp.PostingDate.Equal(when) || len(cp.Lines) != 2 || cp.ReversesJournalID != "" {
		t.Fatalf("copy header %+v", cp)
	}
	for i, l := range cp.Lines {
		o := src.Lines[i]
		if l.ID == o.ID || !l.Qty.Equal(o.Qty) || !l.CostPrice.Equal(o.CostPrice) || l.ProductID != o.ProductID || l.Dims[domain.DimBatch] != o.Dims[domain.DimBatch] || l.ReversesLineID != "" {
			t.Fatalf("copy line %d = %+v, want a copy of %+v", i+1, l, o)
		}
	}
	if again, inserted, err := s.CopyJournal(ctx, src.ID, when, "copy-1", false); err != nil || inserted || again.ID != cp.ID {
		t.Fatalf("same key must return the first copy: %v inserted=%v", err, inserted)
	}
	if again, inserted, err := s.CopyJournal(ctx, src.ID, time.Time{}, "copy-1", false); err != nil || inserted || again.ID != cp.ID {
		t.Fatalf("replay without a date: %v inserted=%v", err, inserted)
	}
	if _, _, err := s.CopyJournal(ctx, src.ID, when.AddDate(0, 0, 1), "copy-1", false); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key, other date: %v, want %s", err, store.CodeIdempotencyReuse)
	}
	dated, _, err := s.CopyJournal(ctx, src.ID, time.Time{}, "copy-2", false)
	if err != nil || !dated.PostingDate.Equal(src.PostingDate) || dated.ID == cp.ID {
		t.Fatalf("no date keeps the source date: %v %+v", err, dated)
	}
	if o, err := s.GetJournal(ctx, src.ID); err != nil || o.Status != domain.JournalPosted || o.Version != src.Version || len(o.Lines) != 2 {
		t.Fatalf("source after copy: %v %+v", err, o)
	}

	// The copy is an ordinary draft: it takes lines and posts.
	if _, err := s.AddJournalLines(ctx, cp.ID, []domain.JournalLine{line(product, "1", "1", dims())}); err != nil {
		t.Fatalf("add a line to the copy: %v", err)
	}

	// A reversal and an unknown journal cannot be copied.
	rev, _, err := s.ReverseJournal(ctx, src.ID, when)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CopyJournal(ctx, rev.ID, when, "copy-3", false); problemCode(err) != domain.CodeJournalNotReady {
		t.Fatalf("copying a reversal: %v, want %s", err, domain.CodeJournalNotReady)
	}
	if _, _, err := s.CopyJournal(ctx, ids.New(), when, "copy-4", false); problemCode(err) != store.CodeJournalNotFound {
		t.Fatalf("unknown journal: %v", err)
	}
	// Another tenant cannot see the source.
	if _, _, err := s.CopyJournal(tenantWideCtx(2), src.ID, when, "copy-5", false); problemCode(err) != store.CodeJournalNotFound {
		t.Fatalf("other tenant: %v, want %s", err, store.CodeJournalNotFound)
	}
}

// A TRANSFER journal copies its destination dimensions.
func TestCopyTransferJournalKeepsDestinations(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "2", dims()))
	l := line(product, "4", "0", dims())
	l.ToDims = domain.Dimensions{domain.DimSite: "S2", domain.DimWarehouse: "W9"}
	src, err := newJournal(ctx, s, domain.JournalTransfer, l)
	if err != nil {
		t.Fatal(err)
	}
	cp, inserted, err := s.CopyJournal(ctx, src.ID, time.Time{}, "copy-t", false)
	if err != nil || !inserted || len(cp.Lines) != 1 {
		t.Fatalf("copy: %v %+v", err, cp)
	}
	if cp.Lines[0].ToDims[domain.DimSite] != "S2" || cp.Lines[0].ToDims[domain.DimWarehouse] != "W9" {
		t.Fatalf("destination = %v", cp.Lines[0].ToDims)
	}
}

// An OWNERSHIP_CHANGE journal is ConsignmentMaintain's: copying one without
// that duty is refused, as creating one is, and the source is untouched.
func TestCopyOwnershipChangeNeedsTheConsignmentDuty(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	l := line(ids.New(), "12", "4.00", vendorDims())
	l.LineNo = 1
	src, err := newJournal(ctx, s, domain.JournalOwnershipChange, l)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CopyJournal(ctx, src.ID, time.Time{}, "copy-oc", false); problemCode(err) != store.CodeOutOfScope {
		t.Fatalf("copy without the consignment duty: %v, want %s", err, store.CodeOutOfScope)
	}
	cp, inserted, err := s.CopyJournal(ctx, src.ID, time.Time{}, "copy-oc", true)
	if err != nil || !inserted || cp.Type != domain.JournalOwnershipChange || len(cp.Lines) != 1 || cp.Lines[0].Dims[domain.DimOwner] != src.Lines[0].Dims[domain.DimOwner] {
		t.Fatalf("copy with the duty: %v inserted=%v %+v", err, inserted, cp)
	}
}
