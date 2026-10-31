package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/migrations"
)

func appStore(t *testing.T) (*Postgres, context.Context) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "user-test", Duties: testkit.ScopeDuties("")})
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sealer := testSealer(t)
	return New(app, sealer, 10), ctx
}

func TestGrants_AppRoleCannotRewriteAppendOnlyTables(t *testing.T) {
	s, ctx := appStore(t)
	for _, stmt := range []string{
		`update audit_event set action = action`,
		`delete from audit_event`,
		`delete from worker`,
		`select count(*) from outbox`,
		`update outbox set type = type`,
		`update hr_certification set expires_on = null`,
		`delete from hr_certification`,
		`delete from hr_skill`,
		`delete from worker_skill`,
	} {
		err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
}

func TestGrants_LockedPathsWorkAsAppRole(t *testing.T) {
	s, ctx := appStore(t)
	at(s, day(time.March, 1))
	le := ids.New()
	w := mustWorker(t, s, ctx, "Grants")
	emp := mustEmployment(t, s, ctx, w.ID, le, "PRIMARY", day(time.January, 1))
	pos := mustPosition(t, s, ctx, "POS-GRANTS", ids.New(), le, "1.00", false, day(time.January, 1), nil)
	mustAssignment(t, s, ctx, pos.ID, w.ID, emp.ID, "1.00", day(time.January, 1), nil)
	plan := mustPlan(t, s, ctx, usPTO15("US-PTO-GRANTS"))
	mustAccrueMonth(t, s, ctx, plan.ID, 2026, time.January)
	req, _ := request(t, s, ctx, w.ID, plan.ID, day(time.February, 2), day(time.February, 2), "1.00")
	if _, _, err := s.DecideAbsenceRequest(ctx, req.ID, "APPROVE"); err != nil {
		t.Fatalf("approve as app role: %v", err)
	}
	bplan := mustBenefitPlan(t, s, ctx, medicalPlan("MED-GRANTS"))
	enr, _, err := s.CreateBenefitEnrollment(ctx, BenefitEnrollmentInput{WorkerID: w.ID, PlanID: bplan.ID, OptionCode: "EE", EffectiveDate: day(time.November, 15)}, "", false)
	if err != nil {
		t.Fatalf("enroll as app role: %v", err)
	}
	if _, _, err := s.EndBenefitEnrollment(ctx, enr.ID, day(time.December, 31), "TERMINATION"); err != nil {
		t.Fatalf("end enrollment as app role: %v", err)
	}
}

// TestGrants_SkillPathsWorkAsAppRole runs every skills write and read as
// passage_app: catalog insert, rating insert then update, certificate insert,
// requirement upsert and delete, gap, search and the expiring inquiry. A
// missing grant on a 0005 table fails here with 42501.
func TestGrants_SkillPathsWorkAsAppRole(t *testing.T) {
	s, ctx := appStore(t)
	at(s, day(time.March, 1))
	le := ids.New()
	w := mustWorker(t, s, ctx, "GrantsSkills")
	emp := mustEmployment(t, s, ctx, w.ID, le, "PRIMARY", day(time.January, 1))
	pos := mustPosition(t, s, ctx, "POS-GRANTS-SKL", ids.New(), le, "1.00", false, day(time.January, 1), nil)
	mustAssignment(t, s, ctx, pos.ID, w.ID, emp.ID, "1.00", day(time.January, 1), nil)

	mustSkill(t, s, ctx, "WELDING", 5)
	mustRate(t, s, ctx, w.ID, "WELDING", 2, day(time.February, 1))
	mustRate(t, s, ctx, w.ID, "WELDING", 4, day(time.February, 2))
	mustCert(t, s, ctx, w.ID, "OSHA10", "2026-01-10", strp("2026-04-10"))
	if _, err := s.SetRequirement(ctx, pos.ID, "WELDING", 3); err != nil {
		t.Fatalf("set requirement as app role: %v", err)
	}
	if _, err := s.SetRequirement(ctx, pos.ID, "WELDING", 5); err != nil {
		t.Fatalf("change requirement as app role: %v", err)
	}
	gap, err := s.SkillGapFor(ctx, w.ID, pos.ID)
	if err != nil || len(gap.Gaps) != 1 || gap.Gaps[0].Delta != -1 {
		t.Fatalf("gap as app role = %+v, err %v", gap, err)
	}
	found, err := s.SearchWorkers(ctx, []domain.Requirement{{Skill: "WELDING", MinRating: 4}}, []string{"OSHA10"}, day(time.March, 1))
	if err != nil || len(found.Items) != 1 {
		t.Fatalf("search as app role = %+v, err %v", found, err)
	}
	expiring, err := s.ListExpiringCertifications(ctx, day(time.March, 1), 60, "", 10)
	if err != nil || len(expiring.Items) != 1 {
		t.Fatalf("expiring as app role = %+v, err %v", expiring, err)
	}
	if err := s.RemoveRequirement(ctx, pos.ID, "WELDING"); err != nil {
		t.Fatalf("remove requirement as app role: %v", err)
	}
}

// TestGrants_CoursePathsWorkAsAppRole runs every course write and read as
// passage_app: catalog and outcome inserts, a session insert and its FOR
// UPDATE seat lock, registration insert and status updates, waitlist
// promotion, approval and a PASS that upserts worker_skill and inserts
// hr_certification. A missing grant on a 0006 table fails here with 42501.
func TestGrants_CoursePathsWorkAsAppRole(t *testing.T) {
	s, ctx := appStore(t)
	at(s, day(time.May, 1))
	mustSkill(t, s, ctx, "FORKLIFT", 5)
	course := mustCourse(t, s, ctx, forkliftCourse("FORK-GRANTS"))
	if _, err := s.ListCourses(ctx, "", 10); err != nil {
		t.Fatalf("list courses as app role: %v", err)
	}
	sess := mustSession(t, s, ctx, course.ID, day(time.June, 1), 1, 1)
	a := mustWorker(t, s, ctx, "GrantsCrsA")
	b := mustWorker(t, s, ctx, "GrantsCrsB")
	first := mustRegister(t, s, ctx, sess.ID, a.ID)
	mustRegister(t, s, ctx, sess.ID, b.ID)
	if _, _, err := s.CancelRegistration(ctx, first.ID, false); err != nil {
		t.Fatalf("cancel and promote as app role: %v", err)
	}
	approval := mustCourse(t, s, ctx, Course{Code: "APR-GRANTS", Name: "Approval", RequiresManagerApproval: true})
	asess := mustSession(t, s, ctx, approval.ID, day(time.June, 1), 1, 0)
	if _, err := s.ApproveRegistration(ctx, mustRegister(t, s, ctx, asess.ID, a.ID).ID); err != nil {
		t.Fatalf("approve as app role: %v", err)
	}
	at(s, day(time.June, 2))
	res, err := s.CompleteSession(ctx, sess.ID, []Outcome{{WorkerID: b.ID, Status: domain.RegAttended, Result: domain.ResultPass, Score: "75.00"}})
	if err != nil || res.Passed != 1 {
		t.Fatalf("complete as app role = %+v, err %v", res, err)
	}
	if regs, err := s.ListRegistrations(ctx, sess.ID); err != nil || len(regs) != 2 {
		t.Fatalf("list registrations as app role = %+v, err %v", regs, err)
	}
	for _, stmt := range []string{`delete from hr_course`, `delete from hr_course_registration`, `update hr_course set name = name`} {
		err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
}
