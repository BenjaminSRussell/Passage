package store

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

func recurringOf(e env, name string) RecurringInput {
	base := "1000"
	return RecurringInput{
		LedgerID: e.ledger.ID, JournalName: name, Name: "Monthly freight accrual", Description: "Freight accrual",
		Frequency: "monthly", DayOfMonth: 0, StartDate: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), BaseAmount: &base,
		Lines: []RecurringLine{
			{Account: "6010", DebitCredit: "D", AmountKind: "percent", Amount: "60", Currency: "USD", Dimensions: map[string]string{"DEPT": "D100"}},
			{Account: "6010", DebitCredit: "D", AmountKind: "fixed", Amount: "400.00", Currency: "USD"},
			{Account: "2100", DebitCredit: "C", AmountKind: "percent", Amount: "100", Currency: "USD"},
		},
	}
}

func recurringEvents(t *testing.T, e env, id string) int {
	t.Helper()
	var n int
	err := dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select count(*) from outbox where type = $1 and aggregate_id = $2 and aggregate_type = 'recurring_journal'`,
			ledgerevents.RecurringGenerated, id).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostgres_RecurringJournalLifecycle(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "GJ", JournalName{Active: true})
	id := ids.New()
	rj, created, err := e.store.PutRecurring(e.ctx, id, 0, recurringOf(e, "GJ"))
	if err != nil || !created || rj.Version != 1 || rj.Status != RecurringActive || rj.LineCount != 3 {
		t.Fatalf("create: %+v created=%v err=%v", rj, created, err)
	}
	if rj.NextRunDate == nil || *rj.NextRunDate != "2026-04-30" {
		t.Fatalf("first run should be the last day of April, got %v", rj.NextRunDate)
	}
	if rj.Lines[0].AmountKind != "percent" || rj.Lines[0].Amount != "60" || rj.Lines[1].Amount != "400.00" {
		t.Fatalf("lines: %+v", rj.Lines)
	}

	// Generate the next run: a balanced draft batch dated the scheduled date.
	run, isNew, err := e.store.GenerateRecurring(e.ctx, GenerateRecurringInput{ID: id, ScheduledDate: ptrTime(2026, 4, 30)})
	if err != nil || !isNew || run.ScheduledDate != "2026-04-30" {
		t.Fatalf("generate: %+v new=%v err=%v", run, isNew, err)
	}
	b, err := e.store.GetBatch(e.ctx, run.BatchID)
	if err != nil || b.Status != "draft" || b.PostingDate != "2026-04-30" || b.TotalDebit != "1000.00" || b.TotalCredit != "1000.00" ||
		b.Description != "Freight accrual" || len(b.Lines) != 3 || b.Lines[0].Amount != "600.00" {
		t.Fatalf("batch: %+v err=%v", b, err)
	}

	// The same date again replays the run and creates nothing.
	again, isNew, err := e.store.GenerateRecurring(e.ctx, GenerateRecurringInput{ID: id, ScheduledDate: ptrTime(2026, 4, 30)})
	if err != nil || isNew || again.ID != run.ID || again.BatchID != run.BatchID {
		t.Fatalf("replay: %+v new=%v err=%v", again, isNew, err)
	}
	if n := recurringEvents(t, e, id); n != 1 {
		t.Fatalf("generated events %d, want 1", n)
	}
	rj, err = e.store.GetRecurring(e.ctx, id)
	if err != nil || *rj.NextRunDate != "2026-05-31" || *rj.LastRunDate != "2026-04-30" || rj.Version != 2 {
		t.Fatalf("after run: %+v err=%v", rj, err)
	}

	// A base amount override changes the percentage lines.
	over := "200"
	run2, _, err := e.store.GenerateRecurring(e.ctx, GenerateRecurringInput{ID: id, ScheduledDate: ptrTime(2026, 5, 31), BaseAmount: &over})
	if err != nil {
		t.Fatalf("override run: %v", err)
	}
	if b2, _ := e.store.GetBatch(e.ctx, run2.BatchID); b2.Lines[0].Amount != "120.00" {
		t.Fatalf("override batch lines: %+v", b2.Lines)
	}

	// The draft is an ordinary batch: it submits (validates) through the workspace.
	if _, err := e.store.SubmitBatch(e.ctx, run.BatchID, 0, testToday); err != nil {
		t.Fatalf("submit generated batch: %v", err)
	}

	runs, err := e.store.ListRecurringRuns(e.ctx, id)
	if err != nil || len(runs) != 2 || runs[0].ScheduledDate != "2026-05-31" {
		t.Fatalf("runs: %+v err=%v", runs, err)
	}

	// Paused templates generate nothing new but still replay an existing run.
	paused, err := e.store.SetRecurringStatus(e.ctx, id, 0, RecurringPaused, time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC))
	if err != nil || paused.Status != RecurringPaused {
		t.Fatalf("pause: %+v err=%v", paused, err)
	}
	if _, _, err := e.store.GenerateRecurring(e.ctx, GenerateRecurringInput{ID: id, ScheduledDate: ptrTime(2026, 6, 30)}); !isProblem(err, 409, CodeRecurringPaused) {
		t.Fatalf("generate while paused: %v", err)
	}
	if _, isNew, err := e.store.GenerateRecurring(e.ctx, GenerateRecurringInput{ID: id, ScheduledDate: ptrTime(2026, 4, 30)}); err != nil || isNew {
		t.Fatalf("replay while paused: new=%v err=%v", isNew, err)
	}
	// Resume skips the runs missed while paused (next run was 2026-06-30, today is later).
	resumed, err := e.store.SetRecurringStatus(e.ctx, id, 0, RecurringActive, time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC))
	if err != nil || resumed.Status != RecurringActive || *resumed.NextRunDate != "2026-08-31" {
		t.Fatalf("resume: %+v err=%v", resumed, err)
	}

	// A missed date can still be generated by naming it; it does not move the schedule back.
	if _, isNew, err := e.store.GenerateRecurring(e.ctx, GenerateRecurringInput{ID: id, ScheduledDate: ptrTime(2026, 6, 30)}); err != nil || !isNew {
		t.Fatalf("catch-up run: new=%v err=%v", isNew, err)
	}
	if rj, _ := e.store.GetRecurring(e.ctx, id); *rj.NextRunDate != "2026-08-31" || *rj.LastRunDate != "2026-06-30" {
		t.Fatalf("after catch-up: next %v last %v", *rj.NextRunDate, *rj.LastRunDate)
	}
	resumed, _ = e.store.GetRecurring(e.ctx, id)

	// Stale If-Match, ledger change and out-of-range dates are refused.
	if _, _, err := e.store.PutRecurring(e.ctx, id, 1, recurringOf(e, "GJ")); !isProblem(err, 412, CodeVersionConflict) {
		t.Fatalf("stale version: %v", err)
	}
	in := recurringOf(e, "GJ")
	end := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	in.EndDate = &end
	replaced, created, err := e.store.PutRecurring(e.ctx, id, resumed.Version, in)
	if err != nil || created || replaced.EndDate == nil || *replaced.LastRunDate != "2026-06-30" {
		t.Fatalf("replace: %+v created=%v err=%v", replaced, created, err)
	}
	// The schedule did not change, so the runs skipped by the resume stay skipped (July is not brought back).
	if *replaced.NextRunDate != "2026-08-31" {
		t.Fatalf("replace with the same schedule moved the next run to %s", *replaced.NextRunDate)
	}
	// A changed schedule recomputes from the last run: the 15th after June 30 is July 15.
	in.DayOfMonth = 15
	rescheduled, _, err := e.store.PutRecurring(e.ctx, id, replaced.Version, in)
	if err != nil || *rescheduled.NextRunDate != "2026-07-15" {
		t.Fatalf("reschedule: %+v err=%v", rescheduled, err)
	}
	// A run made under the old schedule still replays; an off-schedule date is refused.
	if _, isNew, err := e.store.GenerateRecurring(e.ctx, GenerateRecurringInput{ID: id, ScheduledDate: ptrTime(2026, 5, 31)}); err != nil || isNew {
		t.Fatalf("replay of an old-schedule run: new=%v err=%v", isNew, err)
	}
	if _, _, err := e.store.GenerateRecurring(e.ctx, GenerateRecurringInput{ID: id, ScheduledDate: ptrTime(2026, 7, 31)}); !isProblem(err, 422, CodeRecurringDateInvalid) {
		t.Fatalf("off-schedule date: %v", err)
	}
	if _, _, err := e.store.GenerateRecurring(e.ctx, GenerateRecurringInput{ID: id}); !isProblem(err, 422, CodeRecurringDateInvalid) {
		t.Fatalf("missing date: %v", err)
	}
	// Shortening the end date before the next run finishes the schedule.
	early := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	in.EndDate = &early
	finished, _, err := e.store.PutRecurring(e.ctx, id, rescheduled.Version, in)
	if err != nil || finished.NextRunDate != nil {
		t.Fatalf("finished schedule: %+v err=%v", finished, err)
	}
	if _, _, err := e.store.GenerateRecurring(e.ctx, GenerateRecurringInput{ID: id, ScheduledDate: ptrTime(2026, 10, 15)}); !isProblem(err, 422, CodeRecurringDateInvalid) {
		t.Fatalf("after end: %v", err)
	}
	if _, _, err := e.store.GenerateRecurring(e.ctx, GenerateRecurringInput{ID: id, ScheduledDate: ptrTime(2026, 3, 31)}); !isProblem(err, 422, CodeRecurringDateInvalid) {
		t.Fatalf("before start: %v", err)
	}
	if _, _, err := e.store.GenerateRecurring(e.ctx, GenerateRecurringInput{ID: ids.New(), ScheduledDate: ptrTime(2026, 4, 30)}); !isProblem(err, 404, CodeRecurringNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestPostgres_RecurringJournalRules(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "GJ", JournalName{Active: true})
	putName(t, e, "OFF", JournalName{Active: false})

	if _, _, err := e.store.PutRecurring(e.ctx, ids.New(), 0, recurringOf(e, "OFF")); !isProblem(err, 422, CodeJournalNameInvalid) {
		t.Fatalf("inactive journal name: %v", err)
	}
	// Percent lines with no base amount cannot generate until one is passed.
	in := recurringOf(e, "GJ")
	in.BaseAmount = nil
	id := ids.New()
	if _, _, err := e.store.PutRecurring(e.ctx, id, 0, in); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.store.GenerateRecurring(e.ctx, GenerateRecurringInput{ID: id, ScheduledDate: ptrTime(2026, 4, 30)}); !isProblem(err, 422, CodeRecurringBaseRequired) {
		t.Fatalf("no base: %v", err)
	}
	base := "50"
	if _, isNew, err := e.store.GenerateRecurring(e.ctx, GenerateRecurringInput{ID: id, ScheduledDate: ptrTime(2026, 4, 30), BaseAmount: &base}); err != nil || !isNew {
		t.Fatalf("with base: new=%v err=%v", isNew, err)
	}

	// The ledger of a template is fixed.
	other, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: "L2", Name: "Second", ChartCode: "STD", Currency: "USD",
		DimensionAttributes: []string{"DEPT"}, FuturePostingDays: 366})
	if err != nil {
		t.Fatal(err)
	}
	moved := recurringOf(e, "GJ")
	moved.LedgerID = other.ID
	if _, _, err := e.store.PutRecurring(e.ctx, id, 0, moved); !isProblem(err, 422, CodeRecurringLedgerFixed) {
		t.Fatalf("ledger change: %v", err)
	}

	// A caller scoped to another legal entity sees nothing.
	foreign := principalCtx(e.tenant, ids.New())
	if list, err := e.store.ListRecurring(foreign, "", Page{}); err != nil || len(list) != 0 {
		t.Fatalf("foreign scope list: %+v err=%v", list, err)
	}
	if _, err := e.store.GetRecurring(foreign, id); !isProblem(err, 404, CodeRecurringNotFound) {
		t.Fatalf("foreign scope get: %v", err)
	}
	if _, _, err := e.store.GenerateRecurring(foreign, GenerateRecurringInput{ID: id, ScheduledDate: ptrTime(2026, 5, 31)}); !isProblem(err, 404, CodeRecurringNotFound) {
		t.Fatalf("foreign scope generate: %v", err)
	}
	if _, err := e.store.SetRecurringStatus(foreign, id, 0, RecurringPaused, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)); !isProblem(err, 404, CodeRecurringNotFound) {
		t.Fatalf("foreign scope pause: %v", err)
	}
	// Its id cannot be taken over by a create from the other legal entity.
	if _, _, err := e.store.PutRecurring(foreign, id, 0, recurringOf(e, "GJ")); err == nil {
		t.Fatal("a caller outside the template's legal entity must not write it")
	}
	if list, err := e.store.ListRecurring(e.ctx, RecurringPaused, Page{}); err != nil || len(list) != 0 {
		t.Fatalf("paused filter: %+v err=%v", list, err)
	}
}

func ptrTime(y int, m time.Month, d int) *time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return &t
}

// BR-11.2 with FR-11.10: a template line carries a statistical quantity and unit, kept on replace
// and copied unscaled onto the generated draft batch line, which then posts as a statistical line.
func TestPostgres_RecurringJournalCarriesStatisticalQuantity(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "GJ", JournalName{Active: true})
	id := ids.New()
	in := recurringOf(e, "GJ")
	in.Lines = append(in.Lines,
		RecurringLine{Account: "6010", DebitCredit: "D", AmountKind: "fixed", Amount: "0.00", Currency: "USD", Quantity: "12.500000", Unit: "KG",
			Dimensions: map[string]string{"DEPT": "D100"}},
		RecurringLine{Account: "6010", DebitCredit: "C", AmountKind: "fixed", Amount: "0.00", Currency: "USD", Quantity: "12.500000", Unit: "KG",
			Dimensions: map[string]string{"DEPT": "D200"}})
	rj, _, err := e.store.PutRecurring(e.ctx, id, 0, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if l := rj.Lines[3]; l.Quantity != "12.500000" || l.Unit != "KG" || rj.Lines[0].Quantity != "" || rj.Lines[0].Unit != "" {
		t.Fatalf("stored lines: %+v", rj.Lines)
	}
	run, _, err := e.store.GenerateRecurring(e.ctx, GenerateRecurringInput{ID: id, ScheduledDate: ptrTime(2026, 4, 30)})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	b, err := e.store.GetBatch(e.ctx, run.BatchID)
	if err != nil || len(b.Lines) != 5 || b.Lines[0].Quantity != "" || b.Lines[3].Quantity != "12.500000" || b.Lines[3].Unit != "KG" ||
		b.Lines[3].Amount != "0.00" || b.Lines[4].DebitCredit != "C" {
		t.Fatalf("batch lines: %+v err=%v", b.Lines, err)
	}
	// The generated draft submits and posts like any batch; the quantity reaches the projection.
	submitted, err := e.store.SubmitBatch(e.ctx, run.BatchID, 0, testToday)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := postBatch(e, e.ctx, submitted, "RJ-QTY-POST", true); err != nil {
		t.Fatalf("post: %v", err)
	}
	var n int
	if err := e.admin.QueryRow(e.ctx, `select count(*) from journal_batch_line where batch_id = $1 and quantity = 12.5 and unit = 'KG'`, run.BatchID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("statistical batch lines = %d err=%v", n, err)
	}
	var debitQty, creditQty string
	if err := e.admin.QueryRow(e.ctx, `select coalesce(sum(debit_quantity), 0)::text, coalesce(sum(credit_quantity), 0)::text from balance_quantity
		where ledger_id = $1 and unit = 'KG'`, e.ledger.ID).Scan(&debitQty, &creditQty); err != nil ||
		!decimal.RequireFromString(debitQty).Equal(decimal.RequireFromString("12.5")) || !decimal.RequireFromString(creditQty).Equal(decimal.RequireFromString("12.5")) {
		t.Fatalf("posted quantity totals: debit %s credit %s err=%v", debitQty, creditQty, err)
	}

	// A bad pair is refused by the database even if the API check were bypassed.
	bad := recurringOf(e, "GJ")
	bad.Lines[0].Quantity = "5"
	if _, _, err := e.store.PutRecurring(e.ctx, ids.New(), 0, bad); err == nil {
		t.Fatal("a quantity without a unit must not be stored")
	}
}
