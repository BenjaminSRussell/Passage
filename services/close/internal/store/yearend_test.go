package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
	"github.com/BenjaminSRussell/passage/services/close/internal/store"
)

const otherEntityID = "00000000-0000-0000-0000-0000000000c2"

func yearEndInput(hash string) domain.YearEndRun {
	return domain.YearEndRun{
		LegalEntityID: entityID, LedgerID: ledgerID, FiscalYear: "2026", Currency: "USD", ClosingDate: "2026-12-31", OpeningDate: "2027-01-01",
		RetainedEarnings: "3000", CreatedBy: "u", RequestHash: hash,
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

func finish(t *testing.T, s *store.Postgres, runID string) domain.YearEndRun {
	t.Helper()
	proposals := domain.YearEnd{Closing: []domain.Entry{{Kind: domain.EntryClosing, Account: "4000", Amount: decimal.NewFromInt(5)}}}
	if _, err := s.DispatchYearEnd(principal(1), runID, &proposals); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetYearEndJournal(principal(1), runID, domain.JournalClosing, ids.New()); err != nil {
		t.Fatal(err)
	}
	done, err := s.CompleteYearEnd(principal(1), runID)
	if err != nil {
		t.Fatal(err)
	}
	return done
}

func TestYearEnd_RunIsStoredIdempotentlyAndCompletionQueuesOneEvent(t *testing.T) {
	admin, app := migrated(t)
	s := store.New(app)
	run, created, err := s.BeginYearEnd(principal(1), yearEndInput("h1"))
	if err != nil || !created || run.Status != domain.RunPending || run.Sequence != 1 || run.Proposals != nil {
		t.Fatalf("begin: %+v created=%v err=%v", run, created, err)
	}
	again, created, err := s.BeginYearEnd(principal(1), yearEndInput("h1"))
	if err != nil || created || again.ID != run.ID {
		t.Fatalf("repeat: %+v created=%v err=%v", again, created, err)
	}
	var conflict *domain.Error
	if _, _, err = s.BeginYearEnd(principal(1), yearEndInput("other")); !errors.As(err, &conflict) || conflict.Code != domain.CodeIdempotencyReuse {
		t.Fatalf("different request while pending: got %v", err)
	}
	done := finish(t, s, run.ID)
	if done.Status != domain.RunCompleted || done.Journals[domain.JournalClosing] == "" || len(done.Proposals.Closing) != 1 {
		t.Fatalf("complete: %+v", done)
	}
	if _, err = s.CompleteYearEnd(principal(1), run.ID); err != nil {
		t.Fatal(err)
	}
	if n := outboxRows(t, admin); n != 1 {
		t.Fatalf("outbox rows = %d, want exactly 1", n)
	}
	replay, created, err := s.BeginYearEnd(principal(1), yearEndInput("h1"))
	if err != nil || created || replay.ID != run.ID || replay.Status != domain.RunCompleted {
		t.Fatalf("replay of the completed run: %+v created=%v err=%v", replay, created, err)
	}
}

func TestYearEnd_ProposalsFreezeAtFirstDispatchAndCompletedRepeatIsFoundByRequest(t *testing.T) {
	_, app := migrated(t)
	s := store.New(app)
	run, _, err := s.BeginYearEnd(principal(1), yearEndInput("h1"))
	if err != nil || run.Dispatched {
		t.Fatalf("a new run has dispatched nothing: %+v %v", run, err)
	}
	proposing := func(amount int64) *domain.YearEnd {
		return &domain.YearEnd{Closing: []domain.Entry{{Kind: domain.EntryClosing, Account: "4000", Amount: decimal.NewFromInt(amount)}}}
	}
	marked, err := s.DispatchYearEnd(principal(1), run.ID, nil)
	if err != nil || !marked.Dispatched || marked.Proposals != nil {
		t.Fatalf("marking before a reversal stores no proposals: %+v %v", marked, err)
	}
	for _, amount := range []int64{5, 9} {
		if _, err := s.DispatchYearEnd(principal(1), run.ID, proposing(amount)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.YearEndRun(principal(1), run.ID)
	if err != nil || !got.Dispatched || !got.Proposals.Closing[0].Amount.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("the first proposals are frozen: %+v %v", got, err)
	}
	if _, found, err := s.CompletedYearEnd(principal(1), entityID, ledgerID, "2026", "h1", ""); err != nil || found {
		t.Fatalf("a pending run is not a completed repeat: %v %v", found, err)
	}
	if _, err := s.SetYearEndJournal(principal(1), run.ID, domain.JournalClosing, ids.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteYearEnd(principal(1), run.ID); err != nil {
		t.Fatal(err)
	}
	for hash, want := range map[string]bool{"h1": true, "other": false} {
		done, found, err := s.CompletedYearEnd(principal(1), entityID, ledgerID, "2026", hash, "")
		if err != nil || found != want || done.ID != run.ID {
			t.Fatalf("hash %s: found=%v want=%v run=%+v err=%v", hash, found, want, done, err)
		}
	}
	if _, found, err := s.CompletedYearEnd(principal(1), entityID, ledgerID, "2027", "h1", ""); err != nil || found {
		t.Fatalf("another year has no run: %v %v", found, err)
	}
}

func TestYearEnd_AbandonedRunFreesTheYearForANewRequestOnlyWhileNothingPosted(t *testing.T) {
	_, app := migrated(t)
	s := store.New(app)
	first, _, err := s.BeginYearEnd(principal(1), yearEndInput("h1"))
	if err != nil {
		t.Fatal(err)
	}
	abandoned, err := s.AbandonYearEnd(principal(1), first.ID)
	if err != nil || abandoned.Status != domain.RunAbandoned {
		t.Fatalf("abandon: %+v %v", abandoned, err)
	}
	second, created, err := s.BeginYearEnd(principal(1), yearEndInput("other"))
	if err != nil || !created || second.ID == first.ID || second.Sequence != 2 {
		t.Fatalf("a new request after an abandoned run: %+v created=%v err=%v", second, created, err)
	}
	if _, err := s.SetYearEndJournal(principal(1), second.ID, domain.JournalClosing, ids.New()); err != nil {
		t.Fatal(err)
	}
	stillPending, err := s.AbandonYearEnd(principal(1), second.ID)
	if err != nil || stillPending.Status != domain.RunPending {
		t.Fatalf("a run with a posted journal must not be abandoned: %+v %v", stillPending, err)
	}
	var conflict *domain.Error
	if _, _, err = s.BeginYearEnd(principal(1), yearEndInput("third")); !errors.As(err, &conflict) || conflict.Code != domain.CodeIdempotencyReuse {
		t.Fatalf("a run with a posted journal still blocks other requests: %v", err)
	}
}

func TestYearEnd_SupersedesChainsRunsAndRefusesAnythingElse(t *testing.T) {
	_, app := migrated(t)
	s := store.New(app)
	var conflict *domain.Error
	redo := yearEndInput("h2")
	redo.SupersedesRunID = ids.New()
	if _, _, err := s.BeginYearEnd(principal(1), redo); !errors.As(err, &conflict) || conflict.Code != domain.CodeIdempotencyReuse {
		t.Fatalf("supersedes with no prior run: got %v", err)
	}
	first, _, err := s.BeginYearEnd(principal(1), yearEndInput("h1"))
	if err != nil {
		t.Fatal(err)
	}
	finish(t, s, first.ID)
	if _, _, err = s.BeginYearEnd(principal(1), yearEndInput("h2")); !errors.As(err, &conflict) || conflict.Code != domain.CodeIdempotencyReuse {
		t.Fatalf("new request against a completed year without supersedes: got %v", err)
	}
	redo.SupersedesRunID = ids.New()
	if _, _, err = s.BeginYearEnd(principal(1), redo); !errors.As(err, &conflict) {
		t.Fatalf("supersedes naming another run: got %v", err)
	}
	redo.SupersedesRunID = first.ID
	second, created, err := s.BeginYearEnd(principal(1), redo)
	if err != nil || !created || second.Sequence != 2 || second.SupersedesRunID != first.ID {
		t.Fatalf("supersede: %+v created=%v err=%v", second, created, err)
	}
	pending, created, err := s.BeginYearEnd(principal(1), redo)
	if err != nil || created || pending.ID != second.ID {
		t.Fatalf("repeat of the pending re-run: %+v created=%v err=%v", pending, created, err)
	}
	finish(t, s, second.ID)
	replay, created, err := s.BeginYearEnd(principal(1), redo)
	if err != nil || created || replay.ID != second.ID || replay.Status != domain.RunCompleted {
		t.Fatalf("repeat of the completed re-run must return it: %+v created=%v err=%v", replay, created, err)
	}
	redone := yearEndInput("h3")
	redone.SupersedesRunID = first.ID
	if _, _, err = s.BeginYearEnd(principal(1), redone); !errors.As(err, &conflict) || conflict.Code != domain.CodeIdempotencyReuse {
		t.Fatalf("a different request re-running a run that already has a successor: got %v", err)
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
	inEntity := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entityID})
	elsewhere := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: otherEntityID})
	if got := visibleRows(t, inEntity, app, "year_end_run"); got != 1 {
		t.Fatalf("scoped to the owning entity sees %d runs, want 1", got)
	}
	if got := visibleRows(t, elsewhere, app, "year_end_run"); got != 0 {
		t.Fatalf("scoped to another legal entity sees %d runs, want 0", got)
	}
	if _, err := s.YearEndRun(elsewhere, run.ID); !errors.As(err, &missing) || missing.Code != domain.CodeNoRun {
		t.Fatalf("other legal entity read: got %v", err)
	}
}

func TestYearPeriods_ReadsOnlyTheYearAndGroupsModules(t *testing.T) {
	admin, app := migrated(t)
	insert := func(period, module, status, start, end string) {
		t.Helper()
		if _, err := admin.Exec(context.Background(),
			`insert into period_status(tenant_id, legal_entity_id, ledger_id, fiscal_period_id, module, status, changed_by, start_date, end_date) values($1,$2,$3,$4,$5,$6,'test',$7::date,$8::date)`,
			testkit.TenantID(1), entityID, ledgerID, period, module, status, start, end); err != nil {
			t.Fatal(err)
		}
	}
	dec, jan, rest := "00000000-0000-0000-0000-0000000000d1", "00000000-0000-0000-0000-0000000000d2", "00000000-0000-0000-0000-0000000000d3"
	insert(rest, "all", "closed", "2026-01-01", "2026-11-30")
	insert(dec, "all", "closed", "2026-12-01", "2026-12-31")
	insert(dec, "gl", "on_hold", "2026-12-01", "2026-12-31")
	insert(jan, "all", "open", "2027-01-01", "2027-01-31")
	periods, err := store.New(app).YearPeriods(principal(1), entityID, ledgerID, "2026")
	if err != nil || len(periods) != 2 || periods[0].FiscalPeriodID != rest || periods[1].FiscalPeriodID != dec || len(periods[1].Rows) != 2 ||
		periods[1].Start.Format("2006-01-02") != "2026-12-01" || periods[1].End.Format("2006-01-02") != "2026-12-31" {
		t.Fatalf("got %+v %v", periods, err)
	}
	plan, err := domain.PlanYear("2026", periods)
	if err != nil || plan.ClosingDate.Format("2006-01-02") != "2026-12-31" || plan.OpeningDate.Format("2006-01-02") != "2027-01-01" {
		t.Fatalf("plan %+v %v", plan, err)
	}
}
