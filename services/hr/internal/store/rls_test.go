package store

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/migrations"
)

// TestRowLevelSecurity runs the store as the application role, which cannot
// bypass RLS. Another tenant sees no HR row in any table even without a
// tenant filter, and the legal-entity scope hides employments and positions
// outside the caller's entity. It needs PASSAGE_TEST_APP_DATABASE_URL too.
func TestRowLevelSecurity(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skip("PASSAGE_TEST_DATABASE_URL and PASSAGE_TEST_APP_DATABASE_URL are needed for the RLS test")
	}
	admin, app := testkit.DBWithApp(t)
	bg := context.Background()
	if err := dbx.Migrate(bg, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	sealer, err := domain.NewSealer(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	s := New(app, sealer, 10)
	le, otherLE := ids.New(), ids.New()
	scoped := func(tenantID, entity string) context.Context {
		return tenant.With(bg, tenant.Principal{TenantID: tenantID, LegalEntityID: entity, UserID: "user-test", Duties: map[string]struct{}{}})
	}
	one := scoped(testkit.TenantID(1), "")
	w := mustWorker(t, s, one, "Rls")
	mustEmployment(t, s, one, w.ID, le, "PRIMARY", day(time.January, 1))
	mustEmployment(t, s, one, w.ID, otherLE, "PRIMARY", day(time.January, 1))
	mustPosition(t, s, one, "POS-RLS", ids.New(), le, "1.00", false, day(time.January, 1), nil)

	count := func(ctx context.Context, table string) int {
		var n int
		if err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `select count(*) from `+table).Scan(&n)
		}); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}
	// Absence: an accrual under one legal-entity scope credits only the
	// workers it can see; a later all-entity run credits the rest once.
	at(s, day(time.March, 1))
	plan := mustPlan(t, s, one, usPTO15("US-PTO-RLS"))
	other := mustWorker(t, s, one, "RlsOther")
	mustEmployment(t, s, one, other.ID, otherLE, "PRIMARY", day(time.January, 1))
	if run := mustAccrueMonth(t, s, scoped(testkit.TenantID(1), le), plan.ID, 2026, time.January); run.Credited != 1 {
		t.Fatalf("scoped run credited %d, want only the worker in %s", run.Credited, le)
	}
	if run := mustAccrueMonth(t, s, one, plan.ID, 2026, time.January); run.Credited != 1 || run.AlreadyAccrued != 1 {
		t.Fatalf("all-entity rerun = %+v, want the other worker only", run)
	}
	req, _ := request(t, s, one, w.ID, plan.ID, day(time.February, 2), day(time.February, 2), "1.00")
	if _, _, err := s.DecideAbsenceRequest(one, req.ID, "APPROVE"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// Benefits: a plan, a life event and an enrollment of tenant 1.
	bplan := mustBenefitPlan(t, s, one, medicalPlan("MED-RLS"))
	if _, _, err := s.RecordLifeEvent(one, w.ID, "BIRTH", day(time.December, 1), "", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateBenefitEnrollment(one, BenefitEnrollmentInput{WorkerID: w.ID, PlanID: bplan.ID, OptionCode: "EE",
		EffectiveDate: day(time.November, 15)}, "", false); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	// Skills: a catalog skill, a rating, a certificate and a position
	// requirement of tenant 1.
	mustSkill(t, s, one, "WELDING", 5)
	mustRate(t, s, one, w.ID, "WELDING", 3, day(time.March, 1))
	mustCert(t, s, one, w.ID, "OSHA10", "2026-01-10", nil)
	rlsPos := mustPosition(t, s, one, "POS-RLS-SKL", ids.New(), le, "1.00", false, day(time.January, 1), nil)
	if _, err := s.SetRequirement(one, rlsPos.ID, "WELDING", 2); err != nil {
		t.Fatal(err)
	}

	// Courses: a course with a skill outcome, a session and a registration
	// of tenant 1.
	mustSkill(t, s, one, "FORKLIFT", 5)
	crs := mustCourse(t, s, one, forkliftCourse("FORK-RLS"))
	crsSess := mustSession(t, s, one, crs.ID, day(time.December, 1), 5, 1)
	mustRegister(t, s, one, crsSess.ID, w.ID)

	two := scoped(testkit.TenantID(2), "")
	if _, err := s.GetCourse(two, crs.ID); err == nil {
		t.Error("tenant 2 read tenant 1's course")
	}
	if _, _, _, err := s.RegisterWorker(two, crsSess.ID, w.ID, false); err == nil {
		t.Error("tenant 2 registered into tenant 1's session")
	}
	if found, err := s.SearchWorkers(two, []domain.Requirement{{Skill: "WELDING", MinRating: 1}}, nil, day(time.April, 1)); err != nil || len(found.Items) != 0 {
		t.Errorf("tenant 2 skill search = %+v, err %v", found, err)
	}
	if _, err := s.SkillGapFor(scoped(testkit.TenantID(1), otherLE), w.ID, rlsPos.ID); err == nil {
		t.Error("a caller scoped to another legal entity ran a gap against a position it cannot see")
	}
	if _, err := s.GetBenefitPlan(two, bplan.ID); err == nil {
		t.Error("tenant 2 read tenant 1's benefit plan")
	}
	if _, err := s.Balances(two, w.ID, day(time.January, 31), false); err == nil {
		t.Error("tenant 2 read tenant 1's balances")
	}
	for _, table := range []string{"worker", "worker_pii", "employment", "hr_position", "position_hierarchy", "position_assignment", "audit_event",
		"absence_plan", "absence_balance", "absence_accrual_period", "absence_accrual_line", "absence_close_line", "absence_request",
		"benefit_plan", "benefit_plan_option", "benefit_life_event", "benefit_enrollment",
		"hr_skill", "worker_skill", "hr_certification", "position_skill_requirement",
		"hr_course", "hr_course_skill_outcome", "hr_course_session", "hr_course_registration"} {
		if n := count(two, table); n != 0 {
			t.Errorf("tenant 2 sees %d rows in %s", n, table)
		}
	}
	if n := count(one, "employment"); n != 3 {
		t.Fatalf("tenant 1 with all entities sees %d employments, want 3", n)
	}
	inEntity := scoped(testkit.TenantID(1), le)
	if n := count(inEntity, "employment"); n != 1 {
		t.Errorf("scope %s sees %d employments, want 1", le, n)
	}
	if n := count(scoped(testkit.TenantID(1), otherLE), "hr_position"); n != 0 {
		t.Errorf("scope %s sees %d positions of another entity", otherLE, n)
	}
}
