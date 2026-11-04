package store_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// A BOM journal issues the components at their running average and receives
// the parent at the summed component value: the inventory value does not
// change, the GL entry is empty and every row reconciles.
func TestBOMJournalReceivesParentAtComponentValue(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	parent, steel, bolt := ids.New(), ids.New(), ids.New()
	d := dims()
	post(t, ctx, s, domain.JournalOpeningBalance, line(steel, "10", "2.5", d), line(bolt, "100", "0.13", d))

	posted := post(t, ctx, s, domain.JournalBOM, line(steel, "-3", "0", d), line(bolt, "-8", "0", d), line(parent, "1", "0", d))
	if posted.Status != domain.JournalPosted || posted.GLEntry == nil || len(posted.GLEntry.Lines) != 0 {
		t.Fatalf("BOM journal %+v GL %+v", posted, posted.GLEntry)
	}
	// 3 x 2.50 + 8 x 0.13 = 7.50 + 1.04 = 8.54.
	if q, v := onHandFor(t, s, ctx, parent); !q.Equal(dec("1")) || !v.Equal(dec("8.54")) {
		t.Fatalf("parent = %s / %s, want 1 / 8.54", q, v)
	}
	if q, v := onHandFor(t, s, ctx, steel); !q.Equal(dec("7")) || !v.Equal(dec("17.5")) {
		t.Fatalf("steel = %s / %s, want 7 / 17.50", q, v)
	}
	if q, v := onHandFor(t, s, ctx, bolt); !q.Equal(dec("92")) || !v.Equal(dec("11.96")) {
		t.Fatalf("bolt = %s / %s, want 92 / 11.96", q, v)
	}
	reconcile(t, admin)
	if _, _, err := s.ReverseJournal(ctx, posted.ID, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)); problemCode(err) != domain.CodeJournalNotReady {
		t.Fatalf("reversing a BOM journal: %v, want %s", err, domain.CodeJournalNotReady)
	}
}

func TestBOMJournalRefusesBrokenLines(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	parent, comp := ids.New(), ids.New()
	d := dims()
	for name, lines := range map[string][]domain.JournalLine{
		"two parents":       {line(parent, "1", "0", d), line(ids.New(), "1", "0", d)},
		"parent with cost":  {line(parent, "1", "4", d)},
		"parent as a child": {line(parent, "1", "0", d), line(parent, "-1", "0", dims("location", "X"))},
	} {
		if _, err := newJournal(ctx, s, domain.JournalBOM, lines...); problemCode(err) != domain.CodeBOMLine {
			t.Fatalf("%s: %v, want %s", name, err, domain.CodeBOMLine)
		}
	}
	j, err := newJournal(ctx, s, domain.JournalBOM, line(comp, "-1", "0", d))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, j.ID, j.Version, postParams()); problemCode(err) != domain.CodeBOMLine {
		t.Fatalf("posting a BOM journal without a parent: %v, want %s", err, domain.CodeBOMLine)
	}
}

// glSetup returns a store, its owner pool, a principal of a fresh legal entity, and a fake ledger holding one ledger of it.
func glSetup(t *testing.T) (*store.Postgres, *pgxpool.Pool, context.Context, string, string, *ledgerclient.Fake) {
	t.Helper()
	s, _, admin := setup(t)
	entity, ledgerID := ids.New(), ids.New()
	fake := ledgerclient.NewFake()
	fake.AddLedger(ledgerID, entity)
	return s, admin, entityCtx(1, entity), entity, ledgerID, fake
}

// The sweep books each posted journal's stored GL entry once, keyed by the
// journal, skips journals with an empty entry, and a second sweep sends nothing.
func TestPostToGLBooksStoredEntriesOnce(t *testing.T) {
	s, _, ctx, entity, ledgerID, fake := glSetup(t)
	product := ids.New()
	opening := post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "2.5", dims()))
	adjust := post(t, ctx, s, domain.JournalAdjustment, line(product, "-2", "0", dims()))
	post(t, ctx, s, domain.JournalTransfer, transferLine(product, "1", dims(), dims("location", "B")))

	res, err := s.PostToGL(ctx, fake, store.GLPostRequest{LegalEntityID: entity, LedgerID: ledgerID})
	if err != nil || len(res.Booked) != 2 || len(res.Failed) != 0 || res.More || res.LedgerID != ledgerID {
		t.Fatalf("sweep %+v %v", res, err)
	}
	entries := fake.Entries()
	if len(entries) != 2 || entries[0].DocumentID != opening.ID || entries[1].DocumentID != adjust.ID {
		t.Fatalf("ledger entries %+v", entries)
	}
	e := entries[0]
	if e.IdempotencyKey != "inventory-journal:"+opening.ID || e.LedgerID != ledgerID || e.LegalEntityID != entity || e.PostingDate != "2026-09-01" ||
		e.Currency != "USD" || len(e.Lines) != 2 || e.Lines[0].Account != inventoryAccount || e.Lines[0].Credit || !e.Lines[0].Amount.Equal(dec("25")) ||
		e.Lines[1].Account != openingAccount || !e.Lines[1].Credit || !e.Lines[1].Amount.Equal(dec("25")) {
		t.Fatalf("opening entry %+v", e)
	}
	if a := entries[1]; a.Lines[0].Account != adjustmentAccount || a.Lines[0].Credit || !a.Lines[0].Amount.Equal(dec("5")) || !a.Lines[1].Credit {
		t.Fatalf("adjustment entry %+v", a)
	}
	again, err := s.PostToGL(ctx, fake, store.GLPostRequest{LegalEntityID: entity, LedgerID: ledgerID})
	if err != nil || len(again.Booked) != 0 || len(again.Failed) != 0 || len(fake.Entries()) != 2 {
		t.Fatalf("second sweep %+v %v, entries %d", again, err, len(fake.Entries()))
	}
}

// The ledger is pinned to the legal entity: a ledger of another legal entity
// is refused before anything is pinned, and once pinned another ledger is
// refused even when it belongs to the legal entity.
func TestPostToGLPinsTheLegalEntityLedger(t *testing.T) {
	s, _, ctx, entity, ledgerID, fake := glSetup(t)
	post(t, ctx, s, domain.JournalOpeningBalance, line(ids.New(), "1", "1", dims()))
	foreign, second := ids.New(), ids.New()
	fake.AddLedger(foreign, ids.New())
	fake.AddLedger(second, entity)
	if _, err := s.PostToGL(ctx, fake, store.GLPostRequest{LegalEntityID: entity, LedgerID: foreign}); problemCode(err) != store.CodeGLLedgerForeign {
		t.Fatalf("foreign ledger: %v", err)
	}
	if _, err := s.PostToGL(ctx, fake, store.GLPostRequest{LegalEntityID: entity, LedgerID: ids.New()}); problemCode(err) != store.CodeGLLedgerForeign {
		t.Fatalf("unknown ledger: %v", err)
	}
	if _, err := s.PostToGL(ctx, fake, store.GLPostRequest{LegalEntityID: entity, LedgerID: ledgerID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostToGL(ctx, fake, store.GLPostRequest{LegalEntityID: entity, LedgerID: second}); problemCode(err) != store.CodeGLLedgerPinned {
		t.Fatalf("second ledger: %v, want %s", err, store.CodeGLLedgerPinned)
	}
	if n := len(fake.Entries()); n != 1 {
		t.Fatalf("entries = %d, want 1", n)
	}
}

// A lost answer backs the posting off; once due it is resent under the same
// key and the ledger returns the entry it already holds, so nothing is booked
// twice. A refusal the ledger repeats parks at once, and a requeue retries it.
func TestPostToGLBacksOffParksAndRequeues(t *testing.T) {
	s, admin, ctx, entity, ledgerID, fake := glSetup(t)
	post(t, ctx, s, domain.JournalOpeningBalance, line(ids.New(), "4", "1.5", dims()))
	req := store.GLPostRequest{LegalEntityID: entity, LedgerID: ledgerID}

	fake.LoseAnswers(errors.New("timeout"))
	res, err := s.PostToGL(ctx, fake, req)
	if err != nil || len(res.Failed) != 1 || res.Failed[0].Code != store.CodeGLFailed || len(fake.Entries()) != 1 {
		t.Fatalf("lost answer %+v %v", res, err)
	}
	fake.LoseAnswers(nil)
	if res, err := s.PostToGL(ctx, fake, req); err != nil || len(res.Booked)+len(res.Failed) != 0 {
		t.Fatalf("a posting in backoff is not retried: %+v %v", res, err)
	}
	makeDue(t, admin)
	res, err = s.PostToGL(ctx, fake, req)
	if err != nil || len(res.Booked) != 1 || res.Booked[0].EntryID != fake.Entries()[0].ID || len(fake.Entries()) != 1 {
		t.Fatalf("resend %+v %v entries %d", res, err, len(fake.Entries()))
	}

	k := post(t, ctx, s, domain.JournalOpeningBalance, line(ids.New(), "1", "2", dims()))
	fake.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: "LEDGER_ACCOUNT_UNKNOWN"})
	res, err = s.PostToGL(ctx, fake, req)
	if err != nil || len(res.Failed) != 1 || res.Failed[0].JournalID != k.ID || res.Failed[0].Code != "LEDGER_ACCOUNT_UNKNOWN" {
		t.Fatalf("refused %+v %v", res, err)
	}
	makeDue(t, admin)
	if res, err := s.PostToGL(ctx, fake, req); err != nil || res.Parked != 1 || len(res.Failed) != 0 {
		t.Fatalf("a refused posting is parked: %+v %v", res, err)
	}
	fake.FailPosts(nil)
	if n, err := s.RequeueParkedGL(ctx, entity); err != nil || n != 1 {
		t.Fatalf("requeue %d %v", n, err)
	}
	res, err = s.PostToGL(ctx, fake, req)
	if err != nil || len(res.Booked) != 1 || res.Booked[0].JournalID != k.ID || res.Parked != 0 || len(fake.Entries()) != 2 {
		t.Fatalf("after requeue %+v %v", res, err)
	}
	var status, entryID string
	var attempts int
	if err := admin.QueryRow(context.Background(), `select status, journal_entry_id, attempt_count from inventory_gl_posting where journal_id = $1`, k.ID).
		Scan(&status, &entryID, &attempts); err != nil || status != "POSTED" || entryID != res.Booked[0].EntryID || attempts != 0 {
		t.Fatalf("posting row %s %s %d %v", status, entryID, attempts, err)
	}
}

// Transient failures back off and the fifth parks the posting.
func TestPostToGLParksAfterMaxAttempts(t *testing.T) {
	s, admin, ctx, entity, ledgerID, fake := glSetup(t)
	post(t, ctx, s, domain.JournalOpeningBalance, line(ids.New(), "1", "1", dims()))
	req := store.GLPostRequest{LegalEntityID: entity, LedgerID: ledgerID}
	fake.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusServiceUnavailable})
	for i := 0; i < store.MaxGLAttempts; i++ {
		res, err := s.PostToGL(ctx, fake, req)
		if err != nil || len(res.Failed) != 1 || res.Parked != 0 {
			t.Fatalf("attempt %d: %+v %v", i+1, res, err)
		}
		makeDue(t, admin)
	}
	if res, err := s.PostToGL(ctx, fake, req); err != nil || res.Parked != 1 || len(res.Failed) != 0 {
		t.Fatalf("after %d attempts: %+v %v", store.MaxGLAttempts, res, err)
	}
}

// Another legal entity's journals are never swept into this one's ledger.
func TestPostToGLSweepsOnlyTheLegalEntity(t *testing.T) {
	s, _, ctx, entity, ledgerID, fake := glSetup(t)
	other := entityCtx(1, ids.New())
	post(t, ctx, s, domain.JournalOpeningBalance, line(ids.New(), "1", "1", dims()))
	post(t, other, s, domain.JournalOpeningBalance, line(ids.New(), "1", "1", dims()))
	res, err := s.PostToGL(ctx, fake, store.GLPostRequest{LegalEntityID: entity, LedgerID: ledgerID})
	if err != nil || len(res.Booked) != 1 || len(fake.Entries()) != 1 {
		t.Fatalf("sweep %+v %v", res, err)
	}
}

// makeDue ends the backoff of every pending posting.
func makeDue(t *testing.T, admin *pgxpool.Pool) {
	t.Helper()
	if _, err := admin.Exec(context.Background(), `update inventory_gl_posting set next_attempt_at = now() - interval '1 second' where status = 'PENDING'`); err != nil {
		t.Fatal(err)
	}
}
