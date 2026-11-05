package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

// medicalPlan is a November 2026 open-enrollment plan with the AC-42.5 rule.
func medicalPlan(code string) BenefitPlan {
	deduction := "MED"
	return BenefitPlan{
		Code: code, PlanType: "MEDICAL", Currency: "USD",
		EligibilityRule: "employment_type in ['FULL_TIME'] && service_days >= 30",
		EnrollmentStart: "2026-11-01", EnrollmentEnd: "2026-11-30", PreTax: true, PayrollDeductionCode: &deduction,
		Options: []BenefitOption{
			{Code: "EE", CoverageLevel: "Employee only", EmployeeRate: "12.50", EmployerRate: "180.00", RateBasis: "PER_PAY_PERIOD"},
			{Code: "FAM", CoverageLevel: "Family", EmployeeRate: "48.10", EmployerRate: "420.00", RateBasis: "PER_PAY_PERIOD"},
		},
	}
}

func mustBenefitPlan(t *testing.T, s *Postgres, ctx context.Context, plan BenefitPlan) BenefitPlan {
	t.Helper()
	out, _, err := s.CreateBenefitPlan(ctx, plan, "")
	if err != nil {
		t.Fatalf("create benefit plan %s: %v", plan.Code, err)
	}
	return out
}

// hireOn starts a PRIMARY employment of the given type on hire.
func hireOn(t *testing.T, s *Postgres, ctx context.Context, workerID, empType string, hire time.Time) Employment {
	t.Helper()
	e, _, err := s.CreateEmployment(ctx, workerID, Employment{
		LegalEntityID: ids.New(), EmploymentType: empType, AssignmentKind: "PRIMARY",
		HireDate: hire.Format(absDateLayout), ValidFrom: hire,
	}, "")
	if err != nil {
		t.Fatalf("create employment: %v", err)
	}
	return e
}

// TestBenefitPlanReplayAndDuplicateCode: a replay with the same plan returns
// it without inserting, a replay with another body is refused, and a second
// plan with the same code is a 409. Rates read back without padding.
func TestBenefitPlanReplayAndDuplicateCode(t *testing.T) {
	s, ctx := newStore(t)
	first, inserted, err := s.CreateBenefitPlan(ctx, medicalPlan("MED-GOLD"), "plan-key")
	if err != nil || !inserted || len(first.Options) != 2 {
		t.Fatalf("create: inserted=%v options=%d err=%v", inserted, len(first.Options), err)
	}
	if first.Options[0].Code != "EE" || first.Options[0].EmployeeRate != "12.5" || first.Options[1].EmployerRate != "420" {
		t.Fatalf("options = %+v, want EE 12.5 and FAM employer 420", first.Options)
	}
	again, inserted, err := s.CreateBenefitPlan(ctx, medicalPlan("MED-GOLD"), "plan-key")
	if err != nil || inserted || again.ID != first.ID {
		t.Fatalf("replay: inserted=%v same=%v err=%v", inserted, again.ID == first.ID, err)
	}
	changed := medicalPlan("MED-GOLD")
	changed.Options[0].EmployeeRate = "13.00"
	_, _, err = s.CreateBenefitPlan(ctx, changed, "plan-key")
	requireCode(t, err, CodeIdempotencyReuse)
	_, _, err = s.CreateBenefitPlan(ctx, medicalPlan("MED-GOLD"), "other-key")
	requireCode(t, err, CodeBenefitPlanDuplicate)

	got, err := s.GetBenefitPlan(ctx, first.ID)
	if err != nil || got.Code != "MED-GOLD" || got.Currency != "USD" || len(got.Options) != 2 {
		t.Fatalf("get = %+v err=%v", got, err)
	}
	_, err = s.GetBenefitPlan(ctx, ids.New())
	requireCode(t, err, CodeBenefitPlanNotFound)

	mustBenefitPlan(t, s, ctx, medicalPlan("DEN-A"))
	page, err := s.ListBenefitPlans(ctx, "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].Code != "DEN-A" || page.NextAfter != "DEN-A" || len(page.Items[0].Options) != 2 {
		t.Fatalf("first page = %+v err=%v", page, err)
	}
	page, err = s.ListBenefitPlans(ctx, page.NextAfter, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].Code != "MED-GOLD" || page.NextAfter != "" {
		t.Fatalf("second page = %+v err=%v", page, err)
	}
}

// TestBenefitEnrollmentRules covers 8.42.5 (window or life event), BR-42.6
// (eligibility as of the effective date), the overlap rule, the option check,
// idempotent replay and the outbox event in the same transaction.
func TestBenefitEnrollmentRules(t *testing.T) {
	s, ctx := newStore(t)
	plan := mustBenefitPlan(t, s, ctx, medicalPlan("MED-RULES"))
	ann := mustWorker(t, s, ctx, "Ann")
	hireOn(t, s, ctx, ann.ID, "FULL_TIME", day(time.January, 1))

	enroll := func(workerID, option string, eff time.Time, lifeEvent, key string) (BenefitEnrollment, bool, error) {
		return s.CreateBenefitEnrollment(ctx, BenefitEnrollmentInput{
			WorkerID: workerID, PlanID: plan.ID, OptionCode: option, EffectiveDate: eff, LifeEventID: lifeEvent, DependentCount: 2,
		}, key, false)
	}

	enr, inserted, err := enroll(ann.ID, "FAM", day(time.November, 15), "", "enr-ann")
	if err != nil || !inserted {
		t.Fatalf("enroll in window: inserted=%v err=%v", inserted, err)
	}
	if enr.EmployeeRate != "48.1" || enr.Currency != "USD" || enr.RateBasis != "PER_PAY_PERIOD" || enr.PlanCode != "MED-RULES" ||
		!enr.ValidFrom.Equal(day(time.November, 15)) || enr.ValidTo != nil || enr.DependentCount != 2 {
		t.Fatalf("enrollment = %+v", enr)
	}
	events := outboxFor(t, s, ctx, enr.ID)
	if len(events) != 1 || events[0].Type != hrevents.BenefitEnrollmentChanged || events[0].Data["action"] != "START" ||
		events[0].Data["employee_rate"] != "48.1" || events[0].Data["payroll_deduction_code"] != "MED" || events[0].Data["pre_tax"] != true {
		t.Fatalf("outbox = %+v", events)
	}

	again, inserted, err := enroll(ann.ID, "FAM", day(time.November, 15), "", "enr-ann")
	if err != nil || inserted || again.ID != enr.ID {
		t.Fatalf("replay: inserted=%v same=%v err=%v", inserted, again.ID == enr.ID, err)
	}
	_, _, err = enroll(ann.ID, "EE", day(time.November, 15), "", "enr-ann")
	requireCode(t, err, CodeIdempotencyReuse)
	_, _, err = enroll(ann.ID, "EE", day(time.November, 20), "", "enr-ann-2")
	requireCode(t, err, CodeBenefitAlreadyEnrolled)
	_, _, err = enroll(ann.ID, "GOLD", day(time.November, 20), "", "")
	requireCode(t, err, CodeBenefitOptionUnknown)

	// Bob is eligible but misses the window; a life event opens 30 days.
	bob := mustWorker(t, s, ctx, "Bob")
	hireOn(t, s, ctx, bob.ID, "FULL_TIME", day(time.January, 1))
	_, _, err = enroll(bob.ID, "EE", day(time.December, 15), "", "")
	requireCode(t, err, domain.CodeBenefitWindow)
	birth, inserted, err := s.RecordLifeEvent(ctx, bob.ID, "BIRTH", day(time.December, 10), "le-bob", false)
	if err != nil || !inserted || birth.WindowEnd != "2027-01-09" {
		t.Fatalf("life event = %+v inserted=%v err=%v", birth, inserted, err)
	}
	if replay, inserted, err := s.RecordLifeEvent(ctx, bob.ID, "BIRTH", day(time.December, 10), "le-bob", false); err != nil || inserted || replay.ID != birth.ID {
		t.Fatalf("life event replay: inserted=%v err=%v", inserted, err)
	}
	_, _, err = s.RecordLifeEvent(ctx, bob.ID, "MARRIAGE", day(time.December, 10), "le-bob", false)
	requireCode(t, err, CodeIdempotencyReuse)
	_, _, err = enroll(bob.ID, "EE", time.Date(2027, time.January, 10, 0, 0, 0, 0, time.UTC), birth.ID, "")
	requireCode(t, err, domain.CodeBenefitWindow)
	_, _, err = enroll(ann.ID, "EE", day(time.December, 15), birth.ID, "")
	requireCode(t, err, CodeBenefitLifeEventNotFound)
	bobEnr, _, err := enroll(bob.ID, "EE", day(time.December, 15), birth.ID, "")
	if err != nil || bobEnr.LifeEventID == nil || *bobEnr.LifeEventID != birth.ID {
		t.Fatalf("life-event enrollment = %+v err=%v", bobEnr, err)
	}

	// Cy is in the window but not eligible: part time, then too new, then not employed.
	cy := mustWorker(t, s, ctx, "Cy")
	hireOn(t, s, ctx, cy.ID, "PART_TIME", day(time.January, 1))
	_, _, err = enroll(cy.ID, "EE", day(time.November, 15), "", "")
	requireCode(t, err, domain.CodeBenefitIneligible)
	dee := mustWorker(t, s, ctx, "Dee")
	hireOn(t, s, ctx, dee.ID, "FULL_TIME", day(time.November, 1))
	_, _, err = enroll(dee.ID, "EE", day(time.November, 15), "", "")
	requireCode(t, err, domain.CodeBenefitIneligible)
	if _, _, err := enroll(dee.ID, "EE", day(time.December, 1), "", ""); err == nil {
		t.Fatal("Dee enrolled after the window without a life event")
	}
	eve := mustWorker(t, s, ctx, "Eve")
	_, _, err = enroll(eve.ID, "EE", day(time.November, 15), "", "")
	requireCode(t, err, domain.CodeBenefitIneligible)
	_, _, err = enroll(ids.New(), "EE", day(time.November, 15), "", "")
	requireCode(t, err, CodeWorkerNotFound)

	benefits, err := s.ListWorkerBenefits(ctx, bob.ID, false)
	if err != nil || len(benefits.Enrollments) != 1 || len(benefits.LifeEvents) != 1 || benefits.LifeEvents[0].Kind != "BIRTH" {
		t.Fatalf("Bob's benefits = %+v err=%v", benefits, err)
	}
}

// TestEndBenefitEnrollment is BR-42.6: a termination keeps coverage to the
// end of the termination month and marks a COBRA qualifying event on a health
// plan; ending again with the same values is a no-op, otherwise refused.
func TestEndBenefitEnrollment(t *testing.T) {
	s, ctx := newStore(t)
	plan := mustBenefitPlan(t, s, ctx, medicalPlan("MED-END"))
	w := mustWorker(t, s, ctx, "Ender")
	hireOn(t, s, ctx, w.ID, "FULL_TIME", day(time.January, 1))
	enr, _, err := s.CreateBenefitEnrollment(ctx, BenefitEnrollmentInput{WorkerID: w.ID, PlanID: plan.ID, OptionCode: "EE",
		EffectiveDate: day(time.November, 15)}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.EndBenefitEnrollment(ctx, enr.ID, day(time.November, 10), domain.EndLifeEvent)
	requireCode(t, err, domain.CodeBenefitDateOrder)
	// A termination before coverage began is refused too, although the
	// month-end rule alone would leave a valid range to 2026-12-01.
	_, _, err = s.EndBenefitEnrollment(ctx, enr.ID, day(time.November, 10), domain.EndTermination)
	requireCode(t, err, domain.CodeBenefitDateOrder)
	if one, changed, err := s.EndBenefitEnrollment(ctx, enr.ID, day(time.November, 15), domain.EndLifeEvent); err != nil || !changed ||
		!one.ValidTo.Equal(day(time.November, 16)) {
		t.Fatalf("one-day coverage: %+v changed=%v err=%v", one, changed, err)
	}
	enr, _, err = s.CreateBenefitEnrollment(ctx, BenefitEnrollmentInput{WorkerID: w.ID, PlanID: plan.ID, OptionCode: "EE",
		EffectiveDate: day(time.November, 16)}, "", false)
	if err != nil {
		t.Fatalf("enroll again after the one-day coverage: %v", err)
	}

	ended, changed, err := s.EndBenefitEnrollment(ctx, enr.ID, day(time.December, 10), domain.EndTermination)
	if err != nil || !changed {
		t.Fatalf("end: changed=%v err=%v", changed, err)
	}
	if ended.ValidTo == nil || !ended.ValidTo.Equal(time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)) ||
		ended.EndReason == nil || *ended.EndReason != domain.EndTermination || !ended.CobraQualifyingEvent {
		t.Fatalf("ended = %+v, want valid to 2027-01-01, TERMINATION, COBRA", ended)
	}
	events := outboxFor(t, s, ctx, enr.ID)
	if len(events) != 2 || events[1].Data["action"] != "END" || events[1].Data["end_reason"] != "TERMINATION" ||
		events[1].Data["valid_to"] != "2027-01-01T00:00:00Z" {
		t.Fatalf("outbox = %+v", events)
	}
	if _, changed, err := s.EndBenefitEnrollment(ctx, enr.ID, day(time.December, 10), domain.EndTermination); err != nil || changed {
		t.Fatalf("repeat end: changed=%v err=%v", changed, err)
	}
	_, _, err = s.EndBenefitEnrollment(ctx, enr.ID, day(time.December, 20), domain.EndPlanEnd)
	requireCode(t, err, CodeBenefitEnded)
	if n := len(outboxFor(t, s, ctx, enr.ID)); n != 2 {
		t.Fatalf("outbox rows after repeats = %d, want 2", n)
	}
	_, _, err = s.EndBenefitEnrollment(ctx, ids.New(), day(time.December, 10), domain.EndPlanEnd)
	requireCode(t, err, CodeBenefitEnrollmentNotFound)

	// After the end the worker can enroll again from 2027-01-01 (a life event
	// window), and not on a day the old enrollment still covers.
	le, _, err := s.RecordLifeEvent(ctx, w.ID, "MARRIAGE", day(time.December, 20), "", false)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.CreateBenefitEnrollment(ctx, BenefitEnrollmentInput{WorkerID: w.ID, PlanID: plan.ID, OptionCode: "FAM",
		EffectiveDate: day(time.December, 31), LifeEventID: le.ID}, "", false)
	requireCode(t, err, CodeBenefitAlreadyEnrolled)
	if _, _, err := s.CreateBenefitEnrollment(ctx, BenefitEnrollmentInput{WorkerID: w.ID, PlanID: plan.ID, OptionCode: "FAM",
		EffectiveDate: time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC), LifeEventID: le.ID}, "", false); err != nil {
		t.Fatalf("re-enroll after the end: %v", err)
	}
}

// TestSimulateMatchesEnrollment is AC-42.5 through the database: the
// simulation applies the waiting period and the rule exactly as enrollment,
// one row per worker, and a fact the worker lacks (fte without a position
// assignment) is not eligible with a reason.
func TestSimulateMatchesEnrollment(t *testing.T) {
	s, ctx := newStore(t)
	asOf := day(time.November, 15)
	mk := func(name, empType string, hire *time.Time) Worker {
		w := mustWorker(t, s, ctx, name)
		if hire != nil {
			hireOn(t, s, ctx, w.ID, empType, *hire)
		}
		return w
	}
	jan, nov := day(time.January, 1), day(time.November, 1)
	mk("SimA", "FULL_TIME", &jan)
	mk("SimB", "PART_TIME", &jan)
	mk("SimC", "FULL_TIME", &nov)
	mk("SimD", "FULL_TIME", nil)
	med := mustBenefitPlan(t, s, ctx, medicalPlan("MED-SIM"))

	sim, err := s.SimulateBenefitEligibility(ctx, med.ID, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if sim.Total != 3 || sim.Eligible != 1 || sim.Rows[0].PersonnelNumber != "WRK-SimA" || !sim.Rows[0].Eligible ||
		sim.Rows[1].Reason != "rule false" || sim.Rows[2].Eligible {
		t.Fatalf("simulation = %+v", sim)
	}
	// Enrollment agrees with every row of the simulation.
	for _, row := range sim.Rows {
		_, _, err := s.CreateBenefitEnrollment(ctx, BenefitEnrollmentInput{WorkerID: row.WorkerID, PlanID: med.ID, OptionCode: "EE", EffectiveDate: asOf}, "", false)
		if (err == nil) != row.Eligible {
			t.Errorf("%s: simulation eligible=%v, enrollment err=%v", row.PersonnelNumber, row.Eligible, err)
		}
	}

	waiting := medicalPlan("MED-WAIT")
	waiting.EligibilityRule, waiting.WaitingDays = "fte >= 0.5", 300
	wait := mustBenefitPlan(t, s, ctx, waiting)
	sim, err = s.SimulateBenefitEligibility(ctx, wait.ID, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if sim.Eligible != 0 || !strings.Contains(sim.Rows[0].Reason, "unknown identifier: fte") || !strings.Contains(sim.Rows[2].Reason, "waiting period") {
		t.Fatalf("waiting/fte simulation = %+v", sim.Rows)
	}
	_, err = s.SimulateBenefitEligibility(ctx, ids.New(), asOf)
	requireCode(t, err, CodeBenefitPlanNotFound)
}

// TestEmployeesSeeOnlyTheirOwnBenefits covers hr.benefits.self as own data
// (8.42.17): an employee enrolls, records life events and reads benefits only
// for the worker linked to their user.
func TestEmployeesSeeOnlyTheirOwnBenefits(t *testing.T) {
	s, admin := newStore(t)
	me := ids.New()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: me, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	plan := mustBenefitPlan(t, s, admin, medicalPlan("MED-OWN"))
	mine, _, err := s.CreateWorker(admin, WorkerInput{Worker: Worker{PersonnelNumber: "WRK-ME", WorkerType: "EMPLOYEE",
		PartyPersonID: ids.New(), LegalName: "Me", UserID: &me}, NationalID: "123-45-6789"}, "")
	if err != nil {
		t.Fatal(err)
	}
	other := mustWorker(t, s, admin, "other")
	for _, w := range []string{mine.ID, other.ID} {
		hireOn(t, s, admin, w, "FULL_TIME", day(time.January, 1))
	}
	in := func(workerID string) BenefitEnrollmentInput {
		return BenefitEnrollmentInput{WorkerID: workerID, PlanID: plan.ID, OptionCode: "EE", EffectiveDate: day(time.November, 15)}
	}
	if _, _, err := s.CreateBenefitEnrollment(ctx, in(mine.ID), "", true); err != nil {
		t.Fatalf("own enrollment: %v", err)
	}
	_, _, err = s.CreateBenefitEnrollment(ctx, in(other.ID), "", true)
	requireCode(t, err, CodeNotOwnBenefits)
	_, _, err = s.RecordLifeEvent(ctx, other.ID, "BIRTH", day(time.December, 1), "", true)
	requireCode(t, err, CodeNotOwnBenefits)
	_, err = s.ListWorkerBenefits(ctx, other.ID, true)
	requireCode(t, err, CodeNotOwnBenefits)
	own, err := s.ListWorkerBenefits(ctx, mine.ID, true)
	if err != nil || len(own.Enrollments) != 1 || own.Enrollments[0].Status != EnrollmentActive && own.Enrollments[0].Status != EnrollmentPending {
		t.Fatalf("own benefits = %+v err=%v", own, err)
	}
}
