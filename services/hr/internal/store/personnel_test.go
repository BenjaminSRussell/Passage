package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

type personnelFixture struct {
	s      *Postgres
	ctx    context.Context
	entity string
	dept   string
	plan   CompensationPlan
	p1, p2 Position
	worker Worker
	setNow func(time.Time)
}

func newPersonnel(t *testing.T) personnelFixture {
	t.Helper()
	s, ctx := newStore(t)
	f := personnelFixture{s: s, ctx: ctx, entity: ids.New(), dept: ids.New()}
	f.setNow = func(at time.Time) { s.now = func() time.Time { return at } }
	f.setNow(day(time.February, 1))
	var err error
	if f.plan, _, err = s.CreateCompensationPlan(ctx, CompensationPlan{Code: "SAL-USD", Name: "Salary", PayBasis: "ANNUAL", Currency: "USD"}, ""); err != nil {
		t.Fatal(err)
	}
	f.p1 = mustPosition(t, s, ctx, "POS-1", f.dept, f.entity, "1.00", false, day(time.January, 1), nil)
	f.p2 = mustPosition(t, s, ctx, "POS-2", f.dept, f.entity, "1.00", false, day(time.January, 1), nil)
	f.worker = mustWorker(t, s, ctx, "ADA")
	return f
}

func (f personnelFixture) hire(t *testing.T) PersonnelAction {
	t.Helper()
	act, _, err := f.s.SubmitAction(f.ctx, ActionInput{
		ActionType: hrevents.ActionHire, WorkerID: f.worker.ID, EffectiveDate: day(time.January, 5),
		Payload: ActionPayload{LegalEntityID: f.entity, EmploymentType: "FULL_TIME", PositionID: f.p1.ID, CompPlanID: f.plan.ID, Amount: "60000.00", Currency: "USD"},
	}, "")
	if err != nil {
		t.Fatalf("hire: %v", err)
	}
	return act
}

func (f personnelFixture) outboxTypes(t *testing.T) map[string]int {
	t.Helper()
	counts := map[string]int{}
	err := dbx.WithTenantTx(f.ctx, f.s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(f.ctx, `select type from outbox`)
		if err != nil {
			return err
		}
		types, err := pgx.CollectRows(rows, pgx.RowTo[string])
		for _, ty := range types {
			counts[ty]++
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

func TestHireActionCreatesEmploymentAssignmentAndPay(t *testing.T) {
	f := newPersonnel(t)
	act := f.hire(t)
	if act.Status != ActionStatusApplied || act.EmploymentID == nil || act.AppliedBy == nil {
		t.Fatalf("hire = %+v, want APPLIED with an employment", act)
	}
	pay, err := f.s.ListWorkerCompensation(f.ctx, f.worker.ID)
	if err != nil || len(pay) != 1 || pay[0].Amount != "60000.000000" {
		t.Fatalf("pay = %+v err %v", pay, err)
	}
	detail, err := f.s.GetPosition(f.ctx, f.p1.ID)
	if err != nil || len(detail.Assignments) != 1 || !detail.Assignments[0].IsPrimary {
		t.Fatalf("assignments = %+v err %v", detail.Assignments, err)
	}
	types := f.outboxTypes(t)
	for _, want := range []string{hrevents.EmploymentChanged, hrevents.PositionAssignmentChanged, hrevents.CompensationChanged} {
		if types[want] == 0 {
			t.Errorf("no %s event in the outbox: %v", want, types)
		}
	}
}

func TestSubmitActionIsIdempotentAndBounded(t *testing.T) {
	f := newPersonnel(t)
	in := ActionInput{ActionType: hrevents.ActionHire, WorkerID: f.worker.ID, EffectiveDate: day(time.January, 5),
		Payload: ActionPayload{LegalEntityID: f.entity, EmploymentType: "FULL_TIME"}}
	first, inserted, err := f.s.SubmitAction(f.ctx, in, "k1")
	if err != nil || !inserted {
		t.Fatalf("first: %v inserted %v", err, inserted)
	}
	again, inserted, err := f.s.SubmitAction(f.ctx, in, "k1")
	if err != nil || inserted || again.ID != first.ID {
		t.Fatalf("replay: %+v inserted %v err %v", again, inserted, err)
	}
	in.EffectiveDate = day(time.January, 6)
	_, _, err = f.s.SubmitAction(f.ctx, in, "k1")
	requireCode(t, err, CodeIdempotencyReuse)

	far := ActionInput{ActionType: hrevents.ActionHire, WorkerID: f.worker.ID, EffectiveDate: day(time.February, 1).AddDate(0, 0, 366),
		Payload: ActionPayload{LegalEntityID: ids.New(), EmploymentType: "FULL_TIME"}}
	_, _, err = f.s.SubmitAction(f.ctx, far, "")
	requireCode(t, err, CodeActionTooFar)
}

func TestFutureTransferAppliesOnItsDateAC426(t *testing.T) {
	f := newPersonnel(t)
	hire := f.hire(t)
	transfer, _, err := f.s.SubmitAction(f.ctx, ActionInput{
		ActionType: hrevents.ActionTransfer, WorkerID: f.worker.ID, EmploymentID: hire.EmploymentID, EffectiveDate: day(time.December, 1),
		Payload: ActionPayload{PositionID: f.p2.ID},
	}, "")
	if err != nil || transfer.Status != ActionStatusPending {
		t.Fatalf("transfer = %+v err %v, want PENDING", transfer, err)
	}
	f.setNow(day(time.November, 30))
	res, err := f.s.ApplyDueActions(f.ctx, day(time.November, 30))
	if err != nil || res.Applied != 0 {
		t.Fatalf("sweep before the date = %+v err %v", res, err)
	}
	f.setNow(day(time.December, 1))
	res, err = f.s.ApplyDueActions(f.ctx, day(time.December, 1))
	if err != nil || res.Applied != 1 || res.Failed != 0 {
		t.Fatalf("sweep on the date = %+v err %v", res, err)
	}
	old, _ := f.s.GetPosition(f.ctx, f.p1.ID)
	next, _ := f.s.GetPosition(f.ctx, f.p2.ID)
	if len(old.Assignments) != 1 || old.Assignments[0].ValidTo == nil || !old.Assignments[0].ValidTo.Equal(day(time.December, 1)) {
		t.Fatalf("old assignment = %+v, want closed at 2026-12-01", old.Assignments)
	}
	if len(next.Assignments) != 1 || !next.Assignments[0].ValidFrom.Equal(day(time.December, 1)) || !next.Assignments[0].IsPrimary {
		t.Fatalf("new assignment = %+v, want primary from 2026-12-01", next.Assignments)
	}
	again, err := f.s.ApplyDueActions(f.ctx, day(time.December, 1))
	if err != nil || again.Applied != 0 {
		t.Fatalf("second sweep = %+v err %v, want nothing", again, err)
	}
}

func TestSweepMarksAFailingActionFailed(t *testing.T) {
	f := newPersonnel(t)
	hire := f.hire(t)
	bad, _, err := f.s.SubmitAction(f.ctx, ActionInput{
		ActionType: hrevents.ActionTransfer, WorkerID: f.worker.ID, EmploymentID: hire.EmploymentID, EffectiveDate: day(time.March, 1),
		Payload: ActionPayload{PositionID: f.p1.ID},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	f.setNow(day(time.March, 1))
	res, err := f.s.ApplyDueActions(f.ctx, day(time.March, 1))
	if err != nil || res.Failed != 1 {
		t.Fatalf("sweep = %+v err %v", res, err)
	}
	got, _ := f.s.GetAction(f.ctx, bad.ID)
	if got.Status != ActionStatusFailed || got.FailureCode == nil {
		t.Fatalf("action = %+v, want FAILED with a code", got)
	}
}

func TestWithdrawPendingActionOnly(t *testing.T) {
	f := newPersonnel(t)
	hire := f.hire(t)
	pending, _, err := f.s.SubmitAction(f.ctx, ActionInput{
		ActionType: hrevents.ActionTerminate, WorkerID: f.worker.ID, EmploymentID: hire.EmploymentID, EffectiveDate: day(time.June, 30),
		Payload: ActionPayload{TerminationReason: "RESIGN"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	withdrawn, changed, err := f.s.WithdrawAction(f.ctx, pending.ID)
	if err != nil || !changed || withdrawn.Status != ActionStatusWithdrawn {
		t.Fatalf("withdraw = %+v changed %v err %v", withdrawn, changed, err)
	}
	f.setNow(day(time.July, 1))
	if res, _ := f.s.ApplyDueActions(f.ctx, day(time.July, 1)); res.Applied != 0 {
		t.Fatalf("a withdrawn action was applied: %+v", res)
	}
	_, _, err = f.s.WithdrawAction(f.ctx, hire.ID)
	requireCode(t, err, CodeActionState)
}

func TestTerminationEndsEmploymentAssignmentAndPay(t *testing.T) {
	f := newPersonnel(t)
	hire := f.hire(t)
	term, _, err := f.s.SubmitAction(f.ctx, ActionInput{
		ActionType: hrevents.ActionTerminate, WorkerID: f.worker.ID, EmploymentID: hire.EmploymentID, EffectiveDate: day(time.January, 31),
		Payload: ActionPayload{TerminationReason: "RESIGN"},
	}, "")
	if err != nil || term.Status != ActionStatusApplied {
		t.Fatalf("terminate = %+v err %v", term, err)
	}
	emps, _ := f.s.ListEmployments(f.ctx, f.worker.ID)
	if len(emps) != 1 || emps[0].TerminationDate == nil || *emps[0].TerminationDate != "2026-01-31" || emps[0].ValidTo == nil || !emps[0].ValidTo.Equal(day(time.February, 1)) {
		t.Fatalf("employment = %+v, want terminated 2026-01-31 ending 2026-02-01", emps)
	}
	pos, _ := f.s.GetPosition(f.ctx, f.p1.ID)
	pay, _ := f.s.ListWorkerCompensation(f.ctx, f.worker.ID)
	if pos.Assignments[0].ValidTo == nil || !pos.Assignments[0].ValidTo.Equal(day(time.February, 1)) || pay[0].ValidTo == nil || !pay[0].ValidTo.Equal(day(time.February, 1)) {
		t.Fatalf("assignment %+v pay %+v, want both closed at 2026-02-01", pos.Assignments, pay)
	}
	_, _, err = f.s.SubmitAction(f.ctx, ActionInput{
		ActionType: hrevents.ActionTerminate, WorkerID: f.worker.ID, EmploymentID: hire.EmploymentID, EffectiveDate: day(time.January, 2),
		Payload: ActionPayload{TerminationReason: "RESIGN"},
	}, "")
	requireCode(t, err, CodeTerminationDate)
}

func TestCompChangeClosesPriorRowAndChecksCurrency(t *testing.T) {
	f := newPersonnel(t)
	hire := f.hire(t)
	change := ActionInput{
		ActionType: hrevents.ActionCompChange, WorkerID: f.worker.ID, EmploymentID: hire.EmploymentID, EffectiveDate: day(time.January, 20),
		Payload: ActionPayload{CompPlanID: f.plan.ID, Amount: "66000.00", Currency: "USD"},
	}
	if _, _, err := f.s.SubmitAction(f.ctx, change, ""); err != nil {
		t.Fatal(err)
	}
	pay, _ := f.s.ListWorkerCompensation(f.ctx, f.worker.ID)
	if len(pay) != 2 || pay[0].ValidTo == nil || !pay[0].ValidTo.Equal(day(time.January, 20)) || pay[1].Amount != "66000.000000" {
		t.Fatalf("pay = %+v, want the first row closed and a new row", pay)
	}
	change.Payload.Currency = "EUR"
	change.EffectiveDate = day(time.January, 25)
	_, _, err := f.s.SubmitAction(f.ctx, change, "")
	requireCode(t, err, CodeCompCurrency)
}

func TestJobsRejectDuplicatesAndReplay(t *testing.T) {
	s, ctx := newStore(t)
	cur, lo, hi := "USD", "50000", "90000"
	in := Job{Code: "ENG", Title: "Engineer", JobType: "EXEMPT", PayCurrency: &cur, PayMin: &lo, PayMax: &hi}
	first, inserted, err := s.CreateJob(ctx, in, "j1")
	if err != nil || !inserted {
		t.Fatal(err)
	}
	again, inserted, err := s.CreateJob(ctx, in, "j1")
	if err != nil || inserted || again.ID != first.ID {
		t.Fatalf("replay = %+v inserted %v err %v", again, inserted, err)
	}
	_, _, err = s.CreateJob(ctx, in, "j2")
	requireCode(t, err, CodeJobDuplicate)
}
