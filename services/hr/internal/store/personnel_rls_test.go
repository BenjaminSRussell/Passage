package store

import (
	"context"
	"os"
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

func TestPersonnelTablesAreScopedByLegalEntity(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skip("PASSAGE_TEST_DATABASE_URL and PASSAGE_TEST_APP_DATABASE_URL are needed for the RLS test")
	}
	admin, app := testkit.DBWithApp(t)
	bg := context.Background()
	if err := dbx.Migrate(bg, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app, testSealer(t), 10)
	s.now = func() time.Time { return day(time.February, 1) }
	entity, otherEntity, dept := ids.New(), ids.New(), ids.New()
	scoped := func(tenantID, le string) context.Context {
		return tenant.With(bg, tenant.Principal{TenantID: tenantID, LegalEntityID: le, UserID: "user-test", Duties: testkit.ScopeDuties(le)})
	}
	all := scoped(testkit.TenantID(1), "")
	inside, outside := scoped(testkit.TenantID(1), entity), scoped(testkit.TenantID(1), otherEntity)
	otherTenant := scoped(testkit.TenantID(2), "")

	plan, _, err := s.CreateCompensationPlan(all, CompensationPlan{Code: "SAL", Name: "Salary", PayBasis: "ANNUAL", Currency: "USD"}, "")
	if err != nil {
		t.Fatal(err)
	}
	cur, lo, hi := "USD", "50000", "70000"
	if _, _, err := s.CreateJob(all, Job{Code: "MGR", Title: "Manager", JobType: "EXEMPT", PayCurrency: &cur, PayMin: &lo, PayMax: &hi}, ""); err != nil {
		t.Fatal(err)
	}
	pos := mustPosition(t, s, all, "POS-RLS-P", dept, entity, "1.00", false, day(time.January, 1), nil)
	worker := mustWorker(t, s, all, "RlsPersonnel")
	hire, _, err := s.SubmitAction(all, ActionInput{ActionType: hrevents.ActionHire, WorkerID: worker.ID, EffectiveDate: day(time.January, 5),
		Payload: ActionPayload{LegalEntityID: entity, EmploymentType: "FULL_TIME", PositionID: pos.ID, CompPlanID: plan.ID, Amount: "60000.00", Currency: "USD"}}, "")
	if err != nil {
		t.Fatalf("hire: %v", err)
	}
	pending, _, err := s.SubmitAction(all, ActionInput{ActionType: hrevents.ActionTerminate, WorkerID: worker.ID, EmploymentID: hire.EmploymentID,
		EffectiveDate: day(time.March, 31), Payload: ActionPayload{TerminationReason: "RESIGN"}}, "")
	if err != nil || pending.Status != ActionStatusPending {
		t.Fatalf("pending = %+v err %v", pending, err)
	}
	manager := mustWorker(t, s, all, "RlsManager")
	req, _, err := s.CreateRequisition(all, Requisition{RequisitionNumber: "REQ-RLS", PositionID: pos.ID, Headcount: 2, HiringManagerWorkerID: manager.ID,
		PlannedStartDate: "2026-03-01", AllowOverfill: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"APPROVED", "OPEN"} {
		if req, err = s.MoveRequisition(all, req.ID, to); err != nil {
			t.Fatal(err)
		}
	}
	cand, _, err := s.CreateCandidate(all, CandidateInput{RequisitionID: req.ID, FullName: "Rls Candidate", ConsentGranted: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"SCREEN", "INTERVIEW", "OFFER"} {
		if _, err := s.MoveCandidate(all, cand.ID, to); err != nil {
			t.Fatal(err)
		}
	}
	offer, _, err := s.CreateOffer(all, Offer{CandidateID: cand.ID, Amount: "70000.00", Currency: "USD", StartDate: "2026-03-01"}, "")
	if err != nil {
		t.Fatal(err)
	}
	matrix := []domain.GuidelineRow{{Rating: 3, CompaMin: decimal.RequireFromString("0.9"), CompaMax: decimal.RequireFromString("1.1"), PctMin: decimal.NewFromInt(3), PctMax: decimal.NewFromInt(5)}}
	cycle, _, err := s.CreateCompCycle(all, CompCycle{Name: "Merit", Kind: "MERIT", EffectiveDate: "2026-07-01", Currency: "USD", GuidelineMatrix: matrix,
		Budgets: []CycleBudget{{DepartmentID: dept, BudgetAmount: "10000"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdvanceCompCycle(all, cycle.ID, "MANAGER_INPUT"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveWorksheet(all, cycle.ID, manager.ID, []WorksheetRowInput{{WorkerID: worker.ID, Rating: 3, ProposedPct: "3"}}); err != nil {
		t.Fatalf("worksheet: %v", err)
	}

	count := func(ctx context.Context, table string) int {
		var n int
		if err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `select count(*) from `+table).Scan(&n)
		}); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}
	for _, table := range []string{"hr_personnel_action", "hr_fixed_compensation", "hr_requisition", "hr_candidate", "hr_offer", "hr_comp_worksheet_row"} {
		if n := count(inside, table); n == 0 {
			t.Errorf("a user in the owning legal entity sees no %s rows", table)
		}
		if n := count(outside, table); n != 0 {
			t.Errorf("a user of another legal entity sees %d %s rows", n, table)
		}
		if n := count(otherTenant, table); n != 0 {
			t.Errorf("another tenant sees %d %s rows", n, table)
		}
	}

	_, err = s.GetAction(outside, pending.ID)
	requireCode(t, err, CodeActionNotFound)
	_, err = s.GetRequisition(outside, req.ID)
	requireCode(t, err, CodeRequisitionNotFound)
	_, err = s.GetCandidate(outside, cand.ID)
	requireCode(t, err, CodeCandidateNotFound)
	_, err = s.GetOffer(outside, offer.ID)
	requireCode(t, err, CodeOfferNotFound)
	_, err = s.MoveRequisition(outside, req.ID, "CANCELED")
	requireCode(t, err, CodeRequisitionNotFound)
	_, _, err = s.WithdrawAction(outside, pending.ID)
	requireCode(t, err, CodeActionNotFound)
	_, _, err = s.SubmitAction(outside, ActionInput{ActionType: hrevents.ActionTerminate, WorkerID: worker.ID, EmploymentID: hire.EmploymentID,
		EffectiveDate: day(time.February, 10), Payload: ActionPayload{TerminationReason: "RESIGN"}}, "")
	requireCode(t, err, CodeEmploymentMissing)
	if rows, err := s.GetWorksheet(outside, cycle.ID, manager.ID); err != nil || len(rows) != 0 {
		t.Errorf("outside user read worksheet %+v err %v", rows, err)
	}
	if pay, err := s.ListWorkerCompensation(outside, worker.ID); err != nil || len(pay) != 0 {
		t.Errorf("outside user read compensation %+v err %v", pay, err)
	}

	s.now = func() time.Time { return day(time.April, 1) }
	res, err := s.ApplyDueActions(outside, day(time.April, 1))
	if err != nil || res.Applied != 0 || res.Failed != 0 {
		t.Fatalf("outside sweep = %+v err %v, want it to skip the action", res, err)
	}
	if got, err := s.GetAction(all, pending.ID); err != nil || got.Status != ActionStatusPending {
		t.Fatalf("action after an outside sweep = %+v err %v, want still PENDING", got, err)
	}
	if res, err := s.ApplyDueActions(inside, day(time.April, 1)); err != nil || res.Applied != 1 {
		t.Fatalf("inside sweep = %+v err %v", res, err)
	}
}
