package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/migrations"
)

// newStore runs against a fresh schema from testkit and skips without
// PASSAGE_TEST_DATABASE_URL.
func newStore(t *testing.T) (*Postgres, context.Context) {
	t.Helper()
	pool := testkit.DB(t)
	ctx := testkit.Ctx(testkit.TenantID(1))
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sealer, err := domain.NewSealer(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return New(pool, sealer, 10), ctx
}

func day(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Code != code {
		t.Fatalf("error = %v, want problem code %s", err, code)
	}
}

func mustWorker(t *testing.T, s *Postgres, ctx context.Context, name string) Worker {
	t.Helper()
	w, _, err := s.CreateWorker(ctx, WorkerInput{
		Worker:     Worker{PersonnelNumber: "WRK-" + name, WorkerType: "EMPLOYEE", PartyPersonID: ids.New(), LegalName: name},
		NationalID: "123-45-6789",
	}, "")
	if err != nil {
		t.Fatalf("create worker %s: %v", name, err)
	}
	return w
}

func mustEmployment(t *testing.T, s *Postgres, ctx context.Context, workerID, legalEntity, kind string, from time.Time) Employment {
	t.Helper()
	e, _, err := s.CreateEmployment(ctx, workerID, Employment{
		LegalEntityID: legalEntity, EmploymentType: "FULL_TIME", AssignmentKind: kind, HireDate: "2026-01-01", ValidFrom: from,
	}, "")
	if err != nil {
		t.Fatalf("create employment: %v", err)
	}
	return e
}

func mustPosition(t *testing.T, s *Postgres, ctx context.Context, number, dept, legalEntity, fte string, overfill bool, from time.Time, reportsTo *string) Position {
	t.Helper()
	p, _, err := s.CreatePosition(ctx, Position{
		PositionNumber: number, JobCode: "MGR", DepartmentID: dept, LegalEntityID: legalEntity,
		FTE: fte, AllowOverfill: overfill, ValidFrom: from, ReportsToID: reportsTo,
	}, "")
	if err != nil {
		t.Fatalf("create position %s: %v", number, err)
	}
	return p
}

func mustAssignment(t *testing.T, s *Postgres, ctx context.Context, positionID, workerID, employmentID, share string, from time.Time, to *time.Time) PositionAssignment {
	t.Helper()
	return mustAssign(t, s, ctx, positionID, workerID, employmentID, share, true, from, to)
}

func mustAssign(t *testing.T, s *Postgres, ctx context.Context, positionID, workerID, employmentID, share string, primary bool, from time.Time, to *time.Time) PositionAssignment {
	t.Helper()
	a, _, err := s.CreateAssignment(ctx, positionID, PositionAssignment{
		WorkerID: workerID, EmploymentID: employmentID, FTEShare: share, IsPrimary: primary, ValidFrom: from, ValidTo: to,
	}, "")
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	return a
}

// TestWorkerPIIMaskedByDefaultAndEveryReadAudited covers FR-42.1 and AC-42.7:
// the national ID is masked without HcmPIIRead, the stored value is not
// plaintext, and every read (masked or revealed) writes an audit row.
func TestWorkerPIIMaskedByDefaultAndEveryReadAudited(t *testing.T) {
	s, ctx := newStore(t)
	in := WorkerInput{
		Worker:      Worker{PersonnelNumber: "WRK-PII", WorkerType: "EMPLOYEE", PartyPersonID: ids.New(), LegalName: "Ada Lovelace"},
		NationalID:  "123-45-6789",
		BankAccount: "GB82WEST12345698765432",
	}
	w, inserted, err := s.CreateWorker(ctx, in, "key-pii")
	if err != nil || !inserted {
		t.Fatalf("create worker: inserted=%v err=%v", inserted, err)
	}

	var sealed []byte
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select national_id_sealed from worker_pii where worker_id = $1`, w.ID).Scan(&sealed)
	})
	if err != nil || bytes.Contains(sealed, []byte("6789")) {
		t.Fatalf("national ID stored in the clear (err=%v)", err)
	}

	masked, err := s.ReadPII(ctx, w.ID, false, "", "req-1")
	if err != nil {
		t.Fatalf("masked read: %v", err)
	}
	if masked.NationalID != "***-**-6789" || !masked.Masked || masked.BankAccount == nil || *masked.BankAccount != "****5432" {
		t.Fatalf("masked view = %+v", masked)
	}

	revealed, err := s.ReadPII(ctx, w.ID, true, "payroll correction approved", "req-2")
	if err != nil {
		t.Fatalf("reveal: %v", err)
	}
	if revealed.NationalID != "123-45-6789" || revealed.Masked {
		t.Fatalf("revealed view = %+v", revealed)
	}

	var reads, reveals int
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) filter (where action = 'p3_read'), count(*) filter (where action = 'p3_reveal')
			from audit_event where entity_id = $1`, w.ID).Scan(&reads, &reveals)
	})
	if err != nil || reads != 1 || reveals != 1 {
		t.Fatalf("audit rows: reads=%d reveals=%d err=%v; want 1 and 1", reads, reveals, err)
	}

	again, inserted, err := s.CreateWorker(ctx, in, "key-pii")
	if err != nil || inserted || again.ID != w.ID {
		t.Fatalf("idempotent replay: id=%s inserted=%v err=%v", again.ID, inserted, err)
	}
}

// TestAsOfNamesManagerAcrossVacancy is AC-42.1 on a small hierarchy: the manager
// is the incumbent of the reports-to position on the date, the range end is
// exclusive, and a vacant top position yields no manager.
func TestAsOfNamesManagerAcrossVacancy(t *testing.T) {
	s, ctx := newStore(t)
	le, dept := ids.New(), ids.New()
	boss := mustWorker(t, s, ctx, "Boss")
	rep := mustWorker(t, s, ctx, "Rep")
	bossEmp := mustEmployment(t, s, ctx, boss.ID, le, "PRIMARY", day(time.January, 1))
	repEmp := mustEmployment(t, s, ctx, rep.ID, le, "PRIMARY", day(time.January, 1))

	top := mustPosition(t, s, ctx, "POS-TOP", dept, le, "1.00", false, day(time.January, 1), nil)
	lower := mustPosition(t, s, ctx, "POS-LOW", dept, le, "1.00", false, day(time.January, 1), &top.ID)
	jun := day(time.June, 1)
	mustAssignment(t, s, ctx, top.ID, boss.ID, bossEmp.ID, "1.00", day(time.January, 1), &jun)
	mustAssignment(t, s, ctx, lower.ID, rep.ID, repEmp.ID, "1.00", day(time.January, 1), nil)

	cases := []struct {
		name    string
		date    time.Time
		manager string
	}{
		{"mid-March, incumbent in place", day(time.March, 15), boss.ID},
		{"last day before the range ends", day(time.May, 31), boss.ID},
		{"range end is exclusive: vacant from 1 June", jun, ""},
		{"long after the vacancy", day(time.December, 1), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			view, err := s.WorkerAsOf(ctx, rep.ID, domain.AsOfDay(c.date), c.date.Format("2006-01-02"))
			if err != nil {
				t.Fatalf("as-of: %v", err)
			}
			if view.PositionNumber == nil || *view.PositionNumber != "POS-LOW" {
				t.Fatalf("position = %v, want POS-LOW", view.PositionNumber)
			}
			if c.manager == "" {
				if view.ManagerWorkerID != nil {
					t.Fatalf("manager = %s, want none", *view.ManagerWorkerID)
				}
				return
			}
			if view.ManagerWorkerID == nil || *view.ManagerWorkerID != c.manager {
				t.Fatalf("manager = %v, want %s", view.ManagerWorkerID, c.manager)
			}
		})
	}
}

// TestAssignmentRejectsOverfillUnlessAllowed is AC-42.2: a second overlapping
// assignment that takes a 1.00 position past its FTE is refused with
// HCM-POS-001, unless the position allows overfill.
func TestAssignmentRejectsOverfillUnlessAllowed(t *testing.T) {
	s, ctx := newStore(t)
	le, dept := ids.New(), ids.New()
	a := mustWorker(t, s, ctx, "A")
	b := mustWorker(t, s, ctx, "B")
	ea := mustEmployment(t, s, ctx, a.ID, le, "PRIMARY", day(time.January, 1))
	eb := mustEmployment(t, s, ctx, b.ID, le, "PRIMARY", day(time.January, 1))

	strict := mustPosition(t, s, ctx, "POS-100", dept, le, "1.00", false, day(time.January, 1), nil)
	mustAssignment(t, s, ctx, strict.ID, a.ID, ea.ID, "1.00", day(time.January, 1), nil)
	_, _, err := s.CreateAssignment(ctx, strict.ID, PositionAssignment{
		WorkerID: b.ID, EmploymentID: eb.ID, FTEShare: "0.50", IsPrimary: true, ValidFrom: day(time.January, 1),
	}, "")
	requireCode(t, err, "HCM-POS-001")

	// Secondary (non-primary) holds, so the primary-once rule does not apply.
	open := mustPosition(t, s, ctx, "POS-101", dept, le, "1.00", true, day(time.January, 1), nil)
	mustAssign(t, s, ctx, open.ID, a.ID, ea.ID, "1.00", false, day(time.January, 1), nil)
	mustAssign(t, s, ctx, open.ID, b.ID, eb.ID, "0.50", false, day(time.January, 1), nil)
	detail, err := s.GetPosition(ctx, open.ID)
	if err != nil || len(detail.Assignments) != 2 || detail.FTE != "1.00" || !detail.AllowOverfill {
		t.Fatalf("overfilled position detail = %+v err=%v; want two assignments on a 1.00 overfill position", detail, err)
	}
}

// TestPrimaryEmploymentOverlapIsRejectedButConcurrentIsAllowed covers FR-42.2
// and BR-42.14: one PRIMARY employment per legal entity at an instant, while
// CONCURRENT and other-entity employments are accepted.
func TestPrimaryEmploymentOverlapIsRejectedButConcurrentIsAllowed(t *testing.T) {
	s, ctx := newStore(t)
	le1, le2 := ids.New(), ids.New()
	w := mustWorker(t, s, ctx, "Concurrent")
	mustEmployment(t, s, ctx, w.ID, le1, "PRIMARY", day(time.January, 1))

	_, _, err := s.CreateEmployment(ctx, w.ID, Employment{
		LegalEntityID: le1, EmploymentType: "PART_TIME", AssignmentKind: "PRIMARY", HireDate: "2026-06-01", ValidFrom: day(time.June, 1),
	}, "")
	requireCode(t, err, "HCM-EMP-001")

	mustEmployment(t, s, ctx, w.ID, le1, "CONCURRENT", day(time.January, 1))
	mustEmployment(t, s, ctx, w.ID, le2, "PRIMARY", day(time.January, 1))
}
