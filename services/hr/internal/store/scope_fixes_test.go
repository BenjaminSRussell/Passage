package store

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
	"github.com/BenjaminSRussell/passage/services/hr/migrations"
)

func TestTerminationEndsBenefitsOnlyWhenNoOtherEmploymentRemains(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skip("PASSAGE_TEST_DATABASE_URL and PASSAGE_TEST_APP_DATABASE_URL are needed for the RLS test")
	}
	admin, app := testkit.DBWithApp(t)
	bg := context.Background()
	if err := dbx.Migrate(bg, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app, testSealer(t), 10)
	entityA, entityB := ids.New(), ids.New()
	scoped := func(le string) context.Context {
		return tenant.With(bg, tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "user-test", Duties: testkit.ScopeDuties(le)})
	}
	all, inA := scoped(""), scoped(entityA)
	at(s, day(time.November, 15))
	plan := mustBenefitPlan(t, s, all, medicalPlan("MED-TERM"))

	enrolled := func(name string) (Worker, Employment, Employment, BenefitEnrollment) {
		w := mustWorker(t, s, all, name)
		a := mustEmployment(t, s, all, w.ID, entityA, "PRIMARY", day(time.January, 1))
		b := mustEmployment(t, s, all, w.ID, entityB, "PRIMARY", day(time.January, 1))
		e, _, err := s.CreateBenefitEnrollment(all, BenefitEnrollmentInput{WorkerID: w.ID, PlanID: plan.ID, OptionCode: "EE", EffectiveDate: day(time.November, 15)}, "", false)
		if err != nil {
			t.Fatalf("enroll %s: %v", name, err)
		}
		return w, a, b, e
	}
	terminate := func(ctx context.Context, w Worker, e Employment) {
		t.Helper()
		at(s, day(time.December, 15))
		_, _, err := s.SubmitAction(ctx, ActionInput{ActionType: hrevents.ActionTerminate, WorkerID: w.ID, EmploymentID: &e.ID,
			EffectiveDate: day(time.December, 10), Payload: ActionPayload{TerminationReason: "RESIGN"}}, "")
		if err != nil {
			t.Fatalf("terminate: %v", err)
		}
	}
	open := func(w Worker) bool {
		got, err := s.ListWorkerBenefits(all, w.ID, false)
		if err != nil || len(got.Enrollments) != 1 {
			t.Fatalf("benefits = %+v err %v", got, err)
		}
		return got.Enrollments[0].ValidTo == nil
	}

	scopedWorker, a1, b1, _ := enrolled("TermScoped")
	terminate(inA, scopedWorker, a1)
	if !open(scopedWorker) {
		t.Fatal("ending one of two employments ended the benefits")
	}
	terminate(scoped(entityB), scopedWorker, b1)
	if open(scopedWorker) {
		t.Fatal("a user scoped to one entity ended the worker's last employment and the benefits stayed open")
	}

	both, a2, _, _ := enrolled("TermBoth")
	at(s, day(time.November, 15))
	terminate(all, both, a2)
	if !open(both) {
		t.Fatal("terminating one of two employments ended the benefits")
	}
}

func TestConsentIsScopedByLegalEntity(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skip("PASSAGE_TEST_DATABASE_URL and PASSAGE_TEST_APP_DATABASE_URL are needed for the RLS test")
	}
	admin, app := testkit.DBWithApp(t)
	bg := context.Background()
	if err := dbx.Migrate(bg, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app, testSealer(t), 10)
	at(s, day(time.February, 1))
	entity, other := ids.New(), ids.New()
	scoped := func(le string) context.Context {
		return tenant.With(bg, tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "user-test", Duties: testkit.ScopeDuties(le)})
	}
	all, inside, outside := scoped(""), scoped(entity), scoped(other)
	pos := mustPosition(t, s, all, "POS-CNS", ids.New(), entity, "1.00", true, day(time.January, 1), nil)
	manager := mustWorker(t, s, all, "CnsMgr")
	req, _, err := s.CreateRequisition(all, Requisition{RequisitionNumber: "REQ-CNS", PositionID: pos.ID, Headcount: 1, HiringManagerWorkerID: manager.ID, PlannedStartDate: "2026-03-01"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"APPROVED", "OPEN"} {
		if req, err = s.MoveRequisition(all, req.ID, to); err != nil {
			t.Fatal(err)
		}
	}
	cand, _, err := s.CreateCandidate(all, CandidateInput{RequisitionID: req.ID, FullName: "Cns Candidate", ConsentGranted: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ListConsents(inside, "CANDIDATE", cand.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("inside consents = %+v err %v", list, err)
	}
	_, err = s.ListConsents(outside, "CANDIDATE", cand.ID)
	requireCode(t, err, CodeSubjectNotFound)
	_, _, err = s.GrantConsent(outside, "CANDIDATE", cand.ID, "TALENT_POOL")
	requireCode(t, err, CodeSubjectNotFound)
	_, err = s.WithdrawConsent(outside, list[0].ID)
	requireCode(t, err, CodeConsentNotFound)
	var n int
	if err := dbx.WithTenantTx(outside, app, func(tx pgx.Tx) error {
		return tx.QueryRow(outside, `select count(*) from hr_consent_record`).Scan(&n)
	}); err != nil || n != 0 {
		t.Fatalf("outside user sees %d consent rows, err %v", n, err)
	}
	if _, err := s.WithdrawConsent(inside, list[0].ID); err != nil {
		t.Fatalf("inside withdraw: %v", err)
	}
}

func (f personnelFixture) acceptedOffer(t *testing.T, number, startDate string) (Offer, Requisition) {
	t.Helper()
	req := f.openRequisition(t, number, 1)
	cand := f.candidateAtOffer(t, req, "Cand"+number)
	offer, _, err := f.s.CreateOffer(f.ctx, Offer{CandidateID: cand.ID, Amount: "70000.00", Currency: "USD", StartDate: startDate}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"SENT", "ACCEPTED"} {
		if offer, err = f.s.MoveOffer(f.ctx, offer.ID, to); err != nil {
			t.Fatal(err)
		}
	}
	return offer, req
}

func TestHireFromOfferChecksEntityPlanOverlapAndCapacity(t *testing.T) {
	f := newPersonnel(t)
	offer, _ := f.acceptedOffer(t, "REQ-1", "2026-03-01")
	_, err := f.s.HireFromOffer(f.ctx, offer.ID, HireInput{WorkerID: f.worker.ID, LegalEntityID: f.entity, EmploymentType: "FULL_TIME"})
	requireCode(t, err, CodeOfferNoPlan)
	_, err = f.s.HireFromOffer(f.ctx, offer.ID, HireInput{WorkerID: f.worker.ID, LegalEntityID: ids.New(), EmploymentType: "FULL_TIME", CompPlanID: f.plan.ID})
	requireCode(t, err, CodeOfferEntity)

	mustEmployment(t, f.s, f.ctx, f.worker.ID, f.entity, "PRIMARY", day(time.January, 1))
	_, err = f.s.HireFromOffer(f.ctx, offer.ID, HireInput{WorkerID: f.worker.ID, LegalEntityID: f.entity, EmploymentType: "FULL_TIME", CompPlanID: f.plan.ID})
	requireCode(t, err, CodeEmploymentOverlap)

	other := mustWorker(t, f.s, f.ctx, "FULLPOS")
	emp := mustEmployment(t, f.s, f.ctx, other.ID, f.entity, "PRIMARY", day(time.January, 1))
	mustAssignment(t, f.s, f.ctx, f.p1.ID, other.ID, emp.ID, "1.00", day(time.January, 1), nil)
	newcomer := mustWorker(t, f.s, f.ctx, "NEWCOMER")
	_, err = f.s.HireFromOffer(f.ctx, offer.ID, HireInput{WorkerID: newcomer.ID, LegalEntityID: f.entity, EmploymentType: "FULL_TIME", CompPlanID: f.plan.ID})
	if err == nil {
		t.Fatal("a hire went into a full position")
	}
}

func TestFailedOrWithdrawnHireReleasesTheOffer(t *testing.T) {
	f := newPersonnel(t)
	offer, _ := f.acceptedOffer(t, "REQ-1", "2026-03-01")
	in := HireInput{WorkerID: f.worker.ID, LegalEntityID: f.entity, EmploymentType: "FULL_TIME", CompPlanID: f.plan.ID}
	act, err := f.s.HireFromOffer(f.ctx, offer.ID, in)
	if err != nil || act.Status != ActionStatusPending {
		t.Fatalf("hire = %+v err %v, want a pending future hire", act, err)
	}
	if _, _, err := f.s.WithdrawAction(f.ctx, act.ID); err != nil {
		t.Fatal(err)
	}
	released, _ := f.s.GetOffer(f.ctx, offer.ID)
	if released.HireActionID != nil {
		t.Fatalf("offer still points at the withdrawn hire: %+v", released)
	}

	act, err = f.s.HireFromOffer(f.ctx, offer.ID, in)
	if err != nil {
		t.Fatalf("second hire after the withdrawal: %v", err)
	}
	mustEmployment(t, f.s, f.ctx, f.worker.ID, f.entity, "PRIMARY", day(time.February, 15))
	f.setNow(day(time.March, 1))
	if res, err := f.s.ApplyDueActions(f.ctx, day(time.March, 1)); err != nil || res.Failed != 1 {
		t.Fatalf("sweep = %+v err %v, want the hire FAILED", res, err)
	}
	if again, _ := f.s.GetOffer(f.ctx, offer.ID); again.HireActionID != nil {
		t.Fatalf("offer still points at the failed hire: %+v", again)
	}
	cand, _ := f.s.GetCandidate(f.ctx, released.CandidateID)
	if cand.Stage != "OFFER" {
		t.Fatalf("candidate stage = %s, want OFFER so the offer can be hired again", cand.Stage)
	}
	other := mustWorker(t, f.s, f.ctx, "REPLACEMENT")
	if _, err := f.s.HireFromOffer(f.ctx, offer.ID, HireInput{WorkerID: other.ID, LegalEntityID: f.entity, EmploymentType: "FULL_TIME", CompPlanID: f.plan.ID}); err != nil {
		t.Fatalf("rehire after a failed hire: %v", err)
	}
}

func TestConcurrentHireFromOfferHappensOnce(t *testing.T) {
	f := newPersonnel(t)
	offer, _ := f.acceptedOffer(t, "REQ-1", "2026-03-01")
	workers := []Worker{f.worker, mustWorker(t, f.s, f.ctx, "RACER")}
	errs := make([]error, len(workers))
	var wg sync.WaitGroup
	for i, w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.s.HireFromOffer(f.ctx, offer.ID, HireInput{WorkerID: w.ID, LegalEntityID: f.entity, EmploymentType: "FULL_TIME", CompPlanID: f.plan.ID})
		}()
	}
	wg.Wait()
	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
		} else {
			requireCode(t, err, CodeOfferConverted)
		}
	}
	if succeeded != 1 {
		t.Fatalf("%d concurrent hires succeeded, want exactly 1", succeeded)
	}
}

func TestOneBadActionDoesNotBlockTheSweep(t *testing.T) {
	f := newPersonnel(t)
	bad, _, err := f.s.SubmitAction(f.ctx, ActionInput{ActionType: hrevents.ActionHire, WorkerID: f.worker.ID, EffectiveDate: day(time.March, 1),
		Payload: ActionPayload{LegalEntityID: f.entity, EmploymentType: "NOT_A_TYPE"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	other := mustWorker(t, f.s, f.ctx, "GOOD")
	good, _, err := f.s.SubmitAction(f.ctx, ActionInput{ActionType: hrevents.ActionHire, WorkerID: other.ID, EffectiveDate: day(time.March, 2),
		Payload: ActionPayload{LegalEntityID: f.entity, EmploymentType: "FULL_TIME"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	f.setNow(day(time.March, 5))
	res, err := f.s.ApplyDueActions(f.ctx, day(time.March, 5))
	if err != nil || res.Failed != 1 || res.Applied != 1 {
		t.Fatalf("sweep = %+v err %v, want one failed and one applied", res, err)
	}
	if got, _ := f.s.GetAction(f.ctx, bad.ID); got.Status != ActionStatusFailed {
		t.Fatalf("bad action = %s", got.Status)
	}
	if got, _ := f.s.GetAction(f.ctx, good.ID); got.Status != ActionStatusApplied {
		t.Fatalf("good action = %s", got.Status)
	}
}

func TestClosingCompensationEmitsAnEvent(t *testing.T) {
	f := newPersonnel(t)
	hire := f.hire(t)
	if _, _, err := f.s.SubmitAction(f.ctx, ActionInput{ActionType: hrevents.ActionCompChange, WorkerID: f.worker.ID, EmploymentID: hire.EmploymentID,
		EffectiveDate: day(time.January, 20), Payload: ActionPayload{CompPlanID: f.plan.ID, Amount: "66000.00", Currency: "USD"}}, ""); err != nil {
		t.Fatal(err)
	}
	if n := f.outboxTypes(t)[hrevents.CompensationChanged]; n != 3 {
		t.Fatalf("compensation events = %d, want open, close and reopen (3)", n)
	}
}

func TestWorksheetSubmitSeesBudgetUsedInOtherEntities(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skip("PASSAGE_TEST_DATABASE_URL and PASSAGE_TEST_APP_DATABASE_URL are needed for the RLS test")
	}
	admin, app := testkit.DBWithApp(t)
	bg := context.Background()
	if err := dbx.Migrate(bg, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app, testSealer(t), 10)
	at(s, day(time.February, 1))
	entityA, entityB, dept := ids.New(), ids.New(), ids.New()
	scoped := func(le string) context.Context {
		return tenant.With(bg, tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "user-test", Duties: testkit.ScopeDuties(le)})
	}
	all, inA := scoped(""), scoped(entityA)
	plan, _, err := s.CreateCompensationPlan(all, CompensationPlan{Code: "SAL", Name: "Salary", PayBasis: "ANNUAL", Currency: "USD"}, "")
	if err != nil {
		t.Fatal(err)
	}
	cur, lo, hi := "USD", "50000", "70000"
	if _, _, err := s.CreateJob(all, Job{Code: "MGR", Title: "Manager", JobType: "EXEMPT", PayCurrency: &cur, PayMin: &lo, PayMax: &hi}, ""); err != nil {
		t.Fatal(err)
	}
	hire := func(entity, number, name string) Worker {
		pos := mustPosition(t, s, all, number, dept, entity, "1.00", false, day(time.January, 1), nil)
		w := mustWorker(t, s, all, name)
		if _, _, err := s.SubmitAction(all, ActionInput{ActionType: hrevents.ActionHire, WorkerID: w.ID, EffectiveDate: day(time.January, 5),
			Payload: ActionPayload{LegalEntityID: entity, EmploymentType: "FULL_TIME", PositionID: pos.ID, CompPlanID: plan.ID, Amount: "60000.00", Currency: "USD"}}, ""); err != nil {
			t.Fatalf("hire %s: %v", name, err)
		}
		return w
	}
	inB, inAWorker := hire(entityB, "POS-B", "BudgetB"), hire(entityA, "POS-A", "BudgetA")
	matrix := []domain.GuidelineRow{{Rating: 3, CompaMin: decimal.RequireFromString("0.9"), CompaMax: decimal.RequireFromString("1.1"), PctMin: decimal.NewFromInt(3), PctMax: decimal.NewFromInt(5)}}
	cycle, _, err := s.CreateCompCycle(all, CompCycle{Name: "Merit", Kind: "MERIT", EffectiveDate: "2026-07-01", Currency: "USD", GuidelineMatrix: matrix,
		Budgets: []CycleBudget{{DepartmentID: dept, BudgetAmount: "3000"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdvanceCompCycle(all, cycle.ID, "MANAGER_INPUT"); err != nil {
		t.Fatal(err)
	}
	managerB, managerA := mustWorker(t, s, all, "MgrB"), mustWorker(t, s, all, "MgrA")
	if _, err := s.SaveWorksheet(all, cycle.ID, managerB.ID, []WorksheetRowInput{{WorkerID: inB.ID, Rating: 3, ProposedPct: "5"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitWorksheet(all, cycle.ID, managerB.ID, false); err != nil {
		t.Fatalf("entity B submit at exactly the budget: %v", err)
	}
	if _, err := s.SaveWorksheet(inA, cycle.ID, managerA.ID, []WorksheetRowInput{{WorkerID: inAWorker.ID, Rating: 3, ProposedPct: "3"}}); err != nil {
		t.Fatal(err)
	}
	_, err = s.SubmitWorksheet(inA, cycle.ID, managerA.ID, false)
	requireCode(t, err, domain.CodeCompOverBudget)
}
