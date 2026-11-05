package store

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

func workerWithUser(t *testing.T, f personnelFixture, name, user string) Worker {
	t.Helper()
	w, _, err := f.s.CreateWorker(f.ctx, WorkerInput{
		Worker:     Worker{PersonnelNumber: "WRK-" + name, WorkerType: "EMPLOYEE", PartyPersonID: ids.New(), LegalName: name, UserID: &user},
		NationalID: "123-45-6789",
	}, "")
	if err != nil {
		t.Fatalf("create worker %s: %v", name, err)
	}
	return w
}

// TestCompCycleApprovalSegregatesDuties: the creator, a worksheet submitter,
// the worker on a row and the proposing manager may not approve; an
// independent all-entity approver may, once; apply needs the approval and the
// pay change lands on the cycle's effective date.
func TestCompCycleApprovalSegregatesDuties(t *testing.T) {
	f := newPersonnel(t)
	workerUser, managerUser := ids.New(), ids.New()
	pos := mustPosition(t, f.s, f.ctx, "POS-SOD", f.dept, f.entity, "1.00", false, day(time.January, 1), nil)
	w := workerWithUser(t, f, "SODW", workerUser)
	if _, _, err := f.s.SubmitAction(f.ctx, ActionInput{ActionType: "HIRE", WorkerID: w.ID, EffectiveDate: day(time.January, 5),
		Payload: ActionPayload{LegalEntityID: f.entity, EmploymentType: "FULL_TIME", PositionID: pos.ID, CompPlanID: f.plan.ID, Amount: "50000.00", Currency: "USD"}}, ""); err != nil {
		t.Fatal(err)
	}
	manager := workerWithUser(t, f, "SODM", managerUser)
	why := "no pay range on the job"
	matrix := []domain.GuidelineRow{{Rating: 3, CompaMin: decimal.Zero, CompaMax: decimal.NewFromInt(10), PctMin: decimal.NewFromInt(0), PctMax: decimal.NewFromInt(10)}}
	cycle, _, err := f.s.CreateCompCycle(f.ctx, CompCycle{Name: "Merit SoD", Kind: "MERIT", EffectiveDate: "2026-07-01", Currency: "USD",
		GuidelineMatrix: matrix, AllowOverBudget: true, Budgets: []CycleBudget{{DepartmentID: f.dept, BudgetAmount: "0"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cycle.CreatedBy == nil || *cycle.CreatedBy != "user-test" {
		t.Fatalf("createdBy = %v, want user-test", cycle.CreatedBy)
	}
	if _, err := f.s.AdvanceCompCycle(f.ctx, cycle.ID, "MANAGER_INPUT"); err != nil {
		t.Fatal(err)
	}
	_, _, err = f.s.ApproveCompCycle(allScopeAs("approver-1"), cycle.ID)
	requireCode(t, err, CodeCycleState)
	if _, err := f.s.SaveWorksheet(f.ctx, cycle.ID, manager.ID, []WorksheetRowInput{{WorkerID: w.ID, Rating: 3, ProposedPct: "4", Justification: &why}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SubmitWorksheet(allScopeAs("submitter-1"), cycle.ID, manager.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.AdvanceCompCycle(f.ctx, cycle.ID, "APPROVAL"); err != nil {
		t.Fatal(err)
	}

	_, _, err = f.s.ApproveCompCycle(f.ctx, cycle.ID)
	requireCode(t, err, "AUTH-0003")
	for _, user := range []string{"user-test", "submitter-1", workerUser, managerUser, ""} {
		_, _, err = f.s.ApproveCompCycle(allScopeAs(user), cycle.ID)
		requireCode(t, err, domain.CodeCompSoD)
	}
	approved, now, err := f.s.ApproveCompCycle(allScopeAs("approver-1"), cycle.ID)
	if err != nil || !now || approved.ApprovedBy == nil || *approved.ApprovedBy != "approver-1" || approved.ApprovedAt == nil {
		t.Fatalf("approve = %+v now %v err %v", approved, now, err)
	}
	if again, now, err := f.s.ApproveCompCycle(allScopeAs("approver-1"), cycle.ID); err != nil || now || *again.ApprovedBy != "approver-1" {
		t.Fatalf("repeat approve = %+v now %v err %v, want unchanged", again, now, err)
	}
	_, _, err = f.s.ApproveCompCycle(allScopeAs("approver-2"), cycle.ID)
	requireCode(t, err, CodeCycleState)

	if n := f.outboxTypes(t)[hrevents.CompensationCycleApproved]; n != 1 {
		t.Fatalf("compensationcycle.approved events = %d, want 1", n)
	}
	if _, actions, err := f.s.ApplyCompCycle(allScope(), cycle.ID); err != nil || len(actions) != 1 {
		t.Fatalf("apply actions %v err %v", actions, err)
	}
	f.setNow(day(time.July, 1))
	if res, err := f.s.ApplyDueActions(f.ctx, day(time.July, 1)); err != nil || res.Applied != 1 {
		t.Fatalf("sweep = %+v err %v", res, err)
	}
	pay, _ := f.s.ListWorkerCompensation(f.ctx, w.ID)
	if len(pay) != 2 || pay[1].Amount != "52000.000000" {
		t.Fatalf("pay = %+v, want 52,000 from 2026-07-01", pay)
	}

	empty, _, err := f.s.CreateCompCycle(f.ctx, CompCycle{Name: "Empty", Kind: "BONUS", EffectiveDate: "2026-08-01", Currency: "USD",
		GuidelineMatrix: matrix, Budgets: []CycleBudget{{DepartmentID: f.dept, BudgetAmount: "0"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"MANAGER_INPUT", "APPROVAL"} {
		if _, err := f.s.AdvanceCompCycle(f.ctx, empty.ID, to); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err = f.s.ApproveCompCycle(allScopeAs("approver-1"), empty.ID)
	requireCode(t, err, CodeWorksheetEmpty)
}

// TestHireFromOfferCreatesTheWorker: without workerId the hire creates the
// worker from newWorker in the same transaction, named after the candidate,
// with worker.created; a taken personnel number rolls the whole hire back.
func TestHireFromOfferCreatesTheWorker(t *testing.T) {
	f := newPersonnel(t)
	req := f.openRequisition(t, "REQ-NW", 1)
	cand := f.candidateAtOffer(t, req, "Katherine Johnson")
	offer, _, err := f.s.CreateOffer(f.ctx, Offer{CandidateID: cand.ID, Amount: "80000.00", Currency: "USD", StartDate: "2026-01-20"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"SENT", "ACCEPTED"} {
		if offer, err = f.s.MoveOffer(f.ctx, offer.ID, to); err != nil {
			t.Fatal(err)
		}
	}
	base := HireInput{LegalEntityID: f.entity, EmploymentType: "FULL_TIME", CompPlanID: f.plan.ID}
	_, err = f.s.HireFromOffer(f.ctx, offer.ID, base)
	requireCode(t, err, CodeOfferNoWorker)

	before := f.outboxTypes(t)[hrevents.WorkerCreated]
	dup := base
	dup.NewWorker = &WorkerInput{Worker: Worker{PersonnelNumber: f.worker.PersonnelNumber, WorkerType: "EMPLOYEE", PartyPersonID: ids.New()}, NationalID: "999-99-9999"}
	_, err = f.s.HireFromOffer(f.ctx, offer.ID, dup)
	requireCode(t, err, CodeWorkerDuplicate)

	in := base
	in.NewWorker = &WorkerInput{Worker: Worker{PersonnelNumber: "WRK-KJ", WorkerType: "EMPLOYEE", PartyPersonID: ids.New()}, NationalID: "987-65-4321"}
	act, err := f.s.HireFromOffer(f.ctx, offer.ID, in)
	if err != nil || act.Status != ActionStatusApplied {
		t.Fatalf("hire = %+v err %v", act, err)
	}
	created, err := f.s.GetWorker(f.ctx, act.WorkerID)
	if err != nil || created.PersonnelNumber != "WRK-KJ" || created.LegalName != "Katherine Johnson" {
		t.Fatalf("worker = %+v err %v, want WRK-KJ named after the candidate", created, err)
	}
	if n := f.outboxTypes(t)[hrevents.WorkerCreated]; n != before+1 {
		t.Fatalf("worker.created events = %d, want %d", n, before+1)
	}
	pay, _ := f.s.ListWorkerCompensation(f.ctx, created.ID)
	if len(pay) != 1 || pay[0].Amount != "80000.000000" {
		t.Fatalf("pay = %+v, want the offer amount", pay)
	}
	if hired, _ := f.s.GetCandidate(f.ctx, cand.ID); hired.Stage != "HIRED" {
		t.Fatalf("candidate stage = %s", hired.Stage)
	}
	_, err = f.s.HireFromOffer(f.ctx, offer.ID, in)
	requireCode(t, err, CodeOfferConverted)
}
