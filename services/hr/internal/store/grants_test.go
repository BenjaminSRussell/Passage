package store

import (
	"bytes"
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
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "user-test", Duties: map[string]struct{}{}})
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sealer, err := domain.NewSealer(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
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
