package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
	"github.com/BenjaminSRussell/passage/services/close/internal/store"
)

func yearEndInput(hash string) domain.YearEndRun {
	return domain.YearEndRun{
		LegalEntityID: entityID, LedgerID: ledgerID, FiscalYear: "FY2026", Currency: "USD", ClosingDate: "2026-12-31", OpeningDate: "2027-01-01",
		Proposals: domain.YearEnd{Closing: []domain.Entry{{Kind: domain.EntryClosing, Account: "4000"}}}, CreatedBy: "u", RequestHash: hash,
	}
}

func outboxRows(t *testing.T, admin *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = $1`, domain.EventYearEndCompleted).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestYearEnd_RunIsStoredIdempotentlyAndCompletionQueuesOneEvent(t *testing.T) {
	admin, app := migrated(t)
	s := store.New(app)
	run, created, err := s.BeginYearEnd(principal(1), yearEndInput("h1"))
	if err != nil || !created || run.Status != domain.RunPending || len(run.Proposals.Closing) != 1 {
		t.Fatalf("begin: %+v created=%v err=%v", run, created, err)
	}
	again, created, err := s.BeginYearEnd(principal(1), yearEndInput("h1"))
	if err != nil || created || again.ID != run.ID {
		t.Fatalf("repeat: %+v created=%v err=%v", again, created, err)
	}
	var conflict *domain.Error
	if _, _, err = s.BeginYearEnd(principal(1), yearEndInput("other")); !errors.As(err, &conflict) || conflict.Code != domain.CodeIdempotencyReuse {
		t.Fatalf("different body: got %v", err)
	}
	done, err := s.CompleteYearEnd(principal(1), run.ID, "je-1", "je-2")
	if err != nil || done.Status != domain.RunCompleted || done.ClosingJournalID != "je-1" || done.OpeningJournalID != "je-2" {
		t.Fatalf("complete: %+v %v", done, err)
	}
	if _, err = s.CompleteYearEnd(principal(1), run.ID, "je-1", "je-2"); err != nil {
		t.Fatal(err)
	}
	if n := outboxRows(t, admin); n != 1 {
		t.Fatalf("outbox rows = %d, want exactly 1", n)
	}
	read, err := s.YearEndRun(principal(1), run.ID)
	if err != nil || read.Status != domain.RunCompleted {
		t.Fatalf("read: %+v %v", read, err)
	}
}

func TestYearEnd_OtherTenantAndOtherEntityCannotSeeTheRun(t *testing.T) {
	_, app := migrated(t)
	s := store.New(app)
	run, _, err := s.BeginYearEnd(principal(1), yearEndInput("h1"))
	if err != nil {
		t.Fatal(err)
	}
	var missing *domain.Error
	if _, err := s.YearEndRun(principal(2), run.ID); !errors.As(err, &missing) || missing.Code != domain.CodeNoRun {
		t.Fatalf("other tenant: got %v", err)
	}
	if got := visibleRows(t, principal(2), app, "year_end_run"); got != 0 {
		t.Fatalf("second tenant sees %d runs", got)
	}
}
