package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
)

func (f personnelFixture) openRequisition(t *testing.T, number string, headcount int) Requisition {
	t.Helper()
	manager := mustWorker(t, f.s, f.ctx, "MGR"+number)
	req, _, err := f.s.CreateRequisition(f.ctx, Requisition{RequisitionNumber: number, PositionID: f.p1.ID, Headcount: headcount,
		HiringManagerWorkerID: manager.ID, PlannedStartDate: "2026-03-01", AllowOverfill: headcount > 1}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"APPROVED", "OPEN"} {
		if req, err = f.s.MoveRequisition(f.ctx, req.ID, to); err != nil {
			t.Fatalf("requisition to %s: %v", to, err)
		}
	}
	return req
}

func (f personnelFixture) candidateAtOffer(t *testing.T, req Requisition, name string) Candidate {
	t.Helper()
	c, _, err := f.s.CreateCandidate(f.ctx, CandidateInput{RequisitionID: req.ID, FullName: name, Email: name + "@example.com", Phone: "555-0100", ConsentGranted: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"SCREEN", "INTERVIEW", "OFFER"} {
		if c, err = f.s.MoveCandidate(f.ctx, c.ID, to); err != nil {
			t.Fatalf("candidate to %s: %v", to, err)
		}
	}
	return c
}

func TestRequisitionOpensOnlyWithVacancyBR4215(t *testing.T) {
	f := newPersonnel(t)
	f.openRequisition(t, "REQ-1", 1)
	manager := mustWorker(t, f.s, f.ctx, "M2")
	second, _, err := f.s.CreateRequisition(f.ctx, Requisition{RequisitionNumber: "REQ-2", PositionID: f.p1.ID, Headcount: 1,
		HiringManagerWorkerID: manager.ID, PlannedStartDate: "2026-03-01"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if second, err = f.s.MoveRequisition(f.ctx, second.ID, "APPROVED"); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.MoveRequisition(f.ctx, second.ID, "OPEN")
	requireCode(t, err, domain.CodeReqNoVacancy)
	_, err = f.s.MoveRequisition(f.ctx, second.ID, "DRAFT")
	requireCode(t, err, domain.CodeReqState)
}

func TestCandidateNeedsConsentAndIsSealed(t *testing.T) {
	f := newPersonnel(t)
	req := f.openRequisition(t, "REQ-1", 1)
	_, _, err := f.s.CreateCandidate(f.ctx, CandidateInput{RequisitionID: req.ID, FullName: "Grace Hopper"}, "")
	requireCode(t, err, CodeConsentRequired)

	c, _, err := f.s.CreateCandidate(f.ctx, CandidateInput{RequisitionID: req.ID, FullName: "Grace Hopper", Email: "grace@example.com", ConsentGranted: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	var name, email []byte
	err = dbx.WithTenantTx(f.ctx, f.s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(f.ctx, `select full_name_sealed, email_sealed from hr_candidate where id = $1`, c.ID).Scan(&name, &email)
	})
	if err != nil || bytes.Contains(name, []byte("Hopper")) || bytes.Contains(email, []byte("grace@")) {
		t.Fatalf("stored contact data is not sealed: %q %q err %v", name, email, err)
	}
	got, err := f.s.GetCandidate(f.ctx, c.ID)
	if err != nil || got.FullName == nil || *got.FullName != "Grace Hopper" {
		t.Fatalf("read = %+v err %v", got, err)
	}
	var audits int
	_ = dbx.WithTenantTx(f.ctx, f.s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(f.ctx, `select count(*) from audit_event where entity = 'candidate' and entity_id = $1 and action = 'p3_read'`, c.ID).Scan(&audits)
	})
	if audits != 1 {
		t.Fatalf("candidate read audits = %d, want 1", audits)
	}
	if _, err := f.s.MoveCandidate(f.ctx, c.ID, "OFFER"); err == nil {
		t.Fatal("a candidate skipped stages")
	}
}

func TestOfferAcceptedFillsAndConvertsToHireOnce(t *testing.T) {
	f := newPersonnel(t)
	req := f.openRequisition(t, "REQ-1", 1)
	cand := f.candidateAtOffer(t, req, "Ada")
	offer, _, err := f.s.CreateOffer(f.ctx, Offer{CandidateID: cand.ID, Amount: "70000.00", Currency: "USD", StartDate: "2026-01-20"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"SENT", "ACCEPTED"} {
		if offer, err = f.s.MoveOffer(f.ctx, offer.ID, to); err != nil {
			t.Fatalf("offer to %s: %v", to, err)
		}
	}
	filled, _ := f.s.GetRequisition(f.ctx, req.ID)
	if filled.Status != "FILLED" || filled.Filled != 1 {
		t.Fatalf("requisition = %+v, want FILLED", filled)
	}
	in := HireInput{WorkerID: f.worker.ID, LegalEntityID: f.entity, EmploymentType: "FULL_TIME", CompPlanID: f.plan.ID}
	act, err := f.s.HireFromOffer(f.ctx, offer.ID, in)
	if err != nil || act.Status != ActionStatusApplied || act.SourceKind == nil || *act.SourceKind != "OFFER" {
		t.Fatalf("hire = %+v err %v", act, err)
	}
	_, err = f.s.HireFromOffer(f.ctx, offer.ID, in)
	requireCode(t, err, CodeOfferConverted)
	pay, _ := f.s.ListWorkerCompensation(f.ctx, f.worker.ID)
	if len(pay) != 1 || pay[0].Amount != "70000.000000" {
		t.Fatalf("pay = %+v, want the offer amount", pay)
	}
	hired, _ := f.s.GetCandidate(f.ctx, cand.ID)
	if hired.Stage != "HIRED" {
		t.Fatalf("candidate stage = %s", hired.Stage)
	}
}

func TestDeclinedOfferReopensFilledRequisition(t *testing.T) {
	f := newPersonnel(t)
	req := f.openRequisition(t, "REQ-1", 1)
	cand := f.candidateAtOffer(t, req, "Ada")
	offer, _, err := f.s.CreateOffer(f.ctx, Offer{CandidateID: cand.ID, Amount: "70000.00", Currency: "USD", StartDate: "2026-03-01"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"SENT", "ACCEPTED", "WITHDRAWN"} {
		if offer, err = f.s.MoveOffer(f.ctx, offer.ID, to); err != nil {
			t.Fatalf("offer to %s: %v", to, err)
		}
	}
	got, _ := f.s.GetRequisition(f.ctx, req.ID)
	if got.Status != "OPEN" || got.Filled != 0 {
		t.Fatalf("requisition = %+v, want reopened", got)
	}
	_, err = f.s.HireFromOffer(f.ctx, offer.ID, HireInput{WorkerID: f.worker.ID, LegalEntityID: f.entity, EmploymentType: "FULL_TIME"})
	requireCode(t, err, domain.CodeOfferState)
}

func TestPurgeErasesWithdrawnConsentAndExpiredCandidates(t *testing.T) {
	f := newPersonnel(t)
	req := f.openRequisition(t, "REQ-1", 3)
	withdrawn := f.candidateAtOffer(t, req, "Withdrawn")
	rejected := f.candidateAtOffer(t, req, "Rejected")
	kept := f.candidateAtOffer(t, req, "Kept")
	consents, err := f.s.ListConsents(f.ctx, "CANDIDATE", withdrawn.ID)
	if err != nil || len(consents) != 1 {
		t.Fatalf("consents = %+v err %v", consents, err)
	}
	if _, err := f.s.WithdrawConsent(f.ctx, consents[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.MoveCandidate(f.ctx, rejected.ID, "REJECTED"); err != nil {
		t.Fatal(err)
	}
	holds := &fakeHolds{}
	if res, err := f.s.PurgeCandidates(f.ctx, holds); err != nil || res.Erased != 1 {
		t.Fatalf("purge without a policy erased %+v err %v, want only the withdrawn consent", res, err)
	}
	if _, err := f.s.SetRetentionPolicy(f.ctx, ClassCandidate, 6); err != nil {
		t.Fatal(err)
	}
	f.setNow(day(time.February, 1).AddDate(0, 7, 0))
	if res, err := f.s.PurgeCandidates(f.ctx, holds); err != nil || res.Erased != 1 {
		t.Fatalf("purge past retention erased %+v err %v, want the rejected candidate", res, err)
	}
	if len(holds.asked) != 2 || holds.asked[0].legalEntityID != f.entity {
		t.Fatalf("hold checks = %+v, want one per due candidate in its legal entity", holds.asked)
	}
	gone, _ := f.s.GetCandidate(f.ctx, withdrawn.ID)
	if !gone.Erased || gone.FullName != nil || gone.Email != nil {
		t.Fatalf("erased candidate = %+v", gone)
	}
	still, _ := f.s.GetCandidate(f.ctx, kept.ID)
	if still.Erased || still.FullName == nil {
		t.Fatalf("a candidate in an open stage was erased: %+v", still)
	}
	_, err = f.s.MoveCandidate(f.ctx, withdrawn.ID, "OFFER")
	requireCode(t, err, CodeCandidateErased)
}

type fakeHolds struct {
	held  map[string][]string
	err   error
	asked []struct{ legalEntityID, candidateID string }
}

func (h *fakeHolds) CoveringCandidate(_ context.Context, legalEntityID, candidateID string) ([]string, error) {
	h.asked = append(h.asked, struct{ legalEntityID, candidateID string }{legalEntityID, candidateID})
	return h.held[candidateID], h.err
}

func TestPurgeSkipsHeldCandidatesAndStopsOnAnUnansweredCheck(t *testing.T) {
	f := newPersonnel(t)
	req := f.openRequisition(t, "REQ-1", 2)
	held := f.candidateAtOffer(t, req, "Held")
	free := f.candidateAtOffer(t, req, "Free")
	for _, c := range []Candidate{held, free} {
		consents, err := f.s.ListConsents(f.ctx, "CANDIDATE", c.ID)
		if err != nil || len(consents) != 1 {
			t.Fatalf("consents = %+v err %v", consents, err)
		}
		if _, err := f.s.WithdrawConsent(f.ctx, consents[0].ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.PurgeCandidates(f.ctx, nil); !errors.Is(err, ErrNoCandidateHolds) {
		t.Fatalf("purge without a hold check: %v", err)
	}
	down := &fakeHolds{err: errors.New("compliance down")}
	if _, err := f.s.PurgeCandidates(f.ctx, down); err == nil {
		t.Fatal("an unanswered hold check must stop the purge")
	}
	for _, id := range []string{held.ID, free.ID} {
		if c, _ := f.s.GetCandidate(f.ctx, id); c.Erased {
			t.Fatalf("candidate %s erased although the hold check failed", id)
		}
	}
	res, err := f.s.PurgeCandidates(f.ctx, &fakeHolds{held: map[string][]string{held.ID: {"hold-1"}}})
	if err != nil || res.Erased != 1 || len(res.Held) != 1 || res.Held[0].CandidateID != held.ID || res.Held[0].HoldIDs[0] != "hold-1" {
		t.Fatalf("purge = %+v err %v, want the free candidate erased and the held one reported", res, err)
	}
	if c, _ := f.s.GetCandidate(f.ctx, held.ID); c.Erased || c.FullName == nil {
		t.Fatalf("a held candidate was erased: %+v", c)
	}
	if c, _ := f.s.GetCandidate(f.ctx, free.ID); !c.Erased {
		t.Fatal("the candidate without a hold was not erased")
	}
}

func allScope() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "user-test", Duties: testkit.ScopeDuties("")})
}

func TestCompensationCycleBudgetBlocksAndAppliesAC4213(t *testing.T) {
	f := newPersonnel(t)
	cur, lo, hi := "USD", "50000", "70000"
	if _, _, err := f.s.CreateJob(f.ctx, Job{Code: "MGR", Title: "Manager", JobType: "EXEMPT", PayCurrency: &cur, PayMin: &lo, PayMax: &hi}, ""); err != nil {
		t.Fatal(err)
	}
	var workers []string
	for i, name := range []string{"A", "B", "C"} {
		pos := mustPosition(t, f.s, f.ctx, "POS-C"+name, f.dept, f.entity, "1.00", false, day(time.January, 1), nil)
		w := mustWorker(t, f.s, f.ctx, "CYC"+name)
		_, _, err := f.s.SubmitAction(f.ctx, ActionInput{ActionType: "HIRE", WorkerID: w.ID, EffectiveDate: day(time.January, 5),
			Payload: ActionPayload{LegalEntityID: f.entity, EmploymentType: "FULL_TIME", PositionID: pos.ID, CompPlanID: f.plan.ID, Amount: "60000.00", Currency: "USD"}}, "")
		if err != nil {
			t.Fatalf("hire %d: %v", i, err)
		}
		workers = append(workers, w.ID)
	}
	matrix := []domain.GuidelineRow{{Rating: 3, CompaMin: decimal.RequireFromString("0.9"), CompaMax: decimal.RequireFromString("1.1"),
		PctMin: decimal.NewFromInt(3), PctMax: decimal.NewFromInt(5)}}
	cycle, _, err := f.s.CreateCompCycle(f.ctx, CompCycle{Name: "Merit 2026", Kind: "MERIT", EffectiveDate: "2026-07-01", Currency: "USD",
		GuidelineMatrix: matrix, Budgets: []CycleBudget{{DepartmentID: f.dept, BudgetAmount: "6000.00"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	manager := mustWorker(t, f.s, f.ctx, "CYCMGR")
	rows := func(pct string, justification *string) []WorksheetRowInput {
		var in []WorksheetRowInput
		for _, w := range workers {
			in = append(in, WorksheetRowInput{WorkerID: w, Rating: 3, ProposedPct: pct, Justification: justification})
		}
		return in
	}
	_, err = f.s.SaveWorksheet(f.ctx, cycle.ID, manager.ID, rows("3.5", nil))
	requireCode(t, err, CodeCycleState)
	if _, err = f.s.AdvanceCompCycle(f.ctx, cycle.ID, "MANAGER_INPUT"); err != nil {
		t.Fatal(err)
	}
	saved, err := f.s.SaveWorksheet(f.ctx, cycle.ID, manager.ID, rows("9", nil))
	if err != nil || saved[0].CompaRatio == nil || *saved[0].CompaRatio != "1.00" || saved[0].ProposedAmount != "5400.000000" {
		t.Fatalf("worksheet = %+v err %v", saved, err)
	}
	_, err = f.s.SubmitWorksheet(allScope(), cycle.ID, manager.ID, false)
	requireCode(t, err, domain.CodeCompOutsideGuideline)
	if _, err = f.s.SaveWorksheet(f.ctx, cycle.ID, manager.ID, rows("3.5", nil)); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.SubmitWorksheet(allScope(), cycle.ID, manager.ID, false)
	requireCode(t, err, domain.CodeCompOverBudget)
	if _, err = f.s.SaveWorksheet(f.ctx, cycle.ID, manager.ID, rows("3", nil)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SubmitWorksheet(allScope(), cycle.ID, manager.ID, false); err != nil {
		t.Fatalf("submit at 5,400 of 6,000: %v", err)
	}
	if _, err = f.s.AdvanceCompCycle(f.ctx, cycle.ID, "APPROVAL"); err != nil {
		t.Fatal(err)
	}
	_, _, err = f.s.ApplyCompCycle(f.ctx, cycle.ID)
	requireCode(t, err, "AUTH-0003")
	if _, err := f.s.pool.Exec(context.Background(), `update hr_comp_cycle_budget set budget_amount = 1 where cycle_id = $1`, cycle.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err = f.s.ApplyCompCycle(allScope(), cycle.ID)
	requireCode(t, err, domain.CodeCompOverBudget)
	if _, err := f.s.pool.Exec(context.Background(), `update hr_comp_cycle_budget set budget_amount = 6000 where cycle_id = $1`, cycle.ID); err != nil {
		t.Fatal(err)
	}
	applied, actionIDs, err := f.s.ApplyCompCycle(allScope(), cycle.ID)
	if err != nil || applied.Status != "APPLIED" || len(actionIDs) != 3 {
		t.Fatalf("apply = %+v actions %v err %v, want 3 actions", applied, actionIDs, err)
	}
	_, _, err = f.s.ApplyCompCycle(allScope(), cycle.ID)
	requireCode(t, err, CodeCycleState)
	f.setNow(day(time.July, 1))
	if res, err := f.s.ApplyDueActions(f.ctx, day(time.July, 1)); err != nil || res.Applied != 3 {
		t.Fatalf("sweep = %+v err %v, want 3 compensation changes on the effective date", res, err)
	}
	pay, _ := f.s.ListWorkerCompensation(f.ctx, workers[0])
	if len(pay) != 2 || pay[1].Amount != "61800.000000" {
		t.Fatalf("pay = %+v, want 61,800 from 2026-07-01", pay)
	}
}

func TestCompaRatioAndGuidelineBands(t *testing.T) {
	ratio, ok := domain.CompaRatio(decimal.NewFromInt(66000), decimal.NewFromInt(50000), decimal.NewFromInt(70000))
	if !ok || ratio.String() != "1.1" {
		t.Fatalf("compa = %s %v, want 1.1", ratio, ok)
	}
	if _, ok := domain.CompaRatio(decimal.NewFromInt(1), decimal.Zero, decimal.Zero); ok {
		t.Fatal("a zero midpoint produced a ratio")
	}
	_ = ids.New
}
