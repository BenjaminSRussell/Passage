package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
)

type outboxRow struct {
	Type string
	Data map[string]any
}

func outboxFor(t *testing.T, s *Postgres, ctx context.Context, aggregateID string) []outboxRow {
	t.Helper()
	var out []outboxRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select type, payload from outbox where aggregate_id = $1 order by created_at`, aggregateID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var typ string
			var raw []byte
			if err := rows.Scan(&typ, &raw); err != nil {
				return err
			}
			var env struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(raw, &env); err != nil {
				return err
			}
			out = append(out, outboxRow{Type: typ, Data: env.Data})
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	return out
}

// TestTerminationDateIsTheLastDayEmployed covers HCM-EMP-002's boundary: a
// termination date equal to the hire date is a one-day employment, active on
// that day and TERMINATED the next. The employment.changed event carries the
// exclusive end.
func TestTerminationDateIsTheLastDayEmployed(t *testing.T) {
	s, ctx := newStore(t)
	w := mustWorker(t, s, ctx, "OneDay")
	le := ids.New()
	last, end := "2026-03-16", domain.DayAfter(day(time.March, 16))
	e, _, err := s.CreateEmployment(ctx, w.ID, Employment{
		LegalEntityID: le, EmploymentType: "TEMP", AssignmentKind: "PRIMARY", HireDate: last, TerminationDate: &last,
		ValidFrom: day(time.March, 16), ValidTo: &end,
	}, "")
	if err != nil {
		t.Fatalf("one-day employment: %v", err)
	}
	if e.TerminationDate == nil || *e.TerminationDate != last || e.Status != domain.StatusTerminated {
		t.Fatalf("employment = %+v; want termination date %s and TERMINATED today", e, last)
	}
	on, err := s.WorkerAsOf(ctx, w.ID, day(time.March, 16), last)
	if err != nil || len(on.Employments) != 1 || on.Employments[0].Status != domain.StatusActive {
		t.Fatalf("as-of the termination date = %+v err=%v; want one ACTIVE employment", on, err)
	}
	after, err := s.WorkerAsOf(ctx, w.ID, day(time.March, 17), "2026-03-17")
	if err != nil || len(after.Employments) != 0 {
		t.Fatalf("as-of the day after = %+v err=%v; want none", after, err)
	}
	events := outboxFor(t, s, ctx, e.ID)
	if len(events) != 1 || events[0].Type != "dev.passage.hr.employment.changed.v1" || events[0].Data["action"] != "HIRE" ||
		events[0].Data["valid_to"] != "2026-03-17T00:00:00Z" || events[0].Data["worker_id"] != w.ID {
		t.Fatalf("outbox = %+v; want one HIRE employment.changed ending 2026-03-17", events)
	}
	all, err := s.ListEmployments(ctx, w.ID)
	if err != nil || len(all) != 1 || all[0].ID != e.ID {
		t.Fatalf("list employments = %+v err=%v", all, err)
	}
}

// TestAssignmentMustFitEmploymentAndPosition: the assignment range lies inside
// both validities (HCM-ASG-003), the employment belongs to the worker in the
// position's legal entity (HCM-ASG-002), and the event names the reports-to
// position.
func TestAssignmentMustFitEmploymentAndPosition(t *testing.T) {
	s, ctx := newStore(t)
	le, other, dept := ids.New(), ids.New(), ids.New()
	w := mustWorker(t, s, ctx, "Fit")
	stranger := mustWorker(t, s, ctx, "Stranger")
	jun := day(time.June, 1)
	emp, _, err := s.CreateEmployment(ctx, w.ID, Employment{
		LegalEntityID: le, EmploymentType: "FULL_TIME", AssignmentKind: "PRIMARY", HireDate: "2026-02-01", ValidFrom: day(time.February, 1), ValidTo: &jun,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := mustEmployment(t, s, ctx, w.ID, other, "PRIMARY", day(time.January, 1))
	boss := mustPosition(t, s, ctx, "POS-BOSS", dept, le, "1.00", false, day(time.January, 1), nil)
	pos := mustPosition(t, s, ctx, "POS-FIT", dept, le, "1.00", false, day(time.January, 1), &boss.ID)

	cases := []struct {
		name string
		in   PositionAssignment
		code string
	}{
		{"starts before the employment", PositionAssignment{WorkerID: w.ID, EmploymentID: emp.ID, FTEShare: "1.00", ValidFrom: day(time.January, 1), ValidTo: &jun}, CodeAssignmentRange},
		{"open-ended past the employment end", PositionAssignment{WorkerID: w.ID, EmploymentID: emp.ID, FTEShare: "1.00", ValidFrom: day(time.March, 1)}, CodeAssignmentRange},
		{"employment of another worker", PositionAssignment{WorkerID: stranger.ID, EmploymentID: emp.ID, FTEShare: "1.00", ValidFrom: day(time.March, 1), ValidTo: &jun}, CodeAssignmentMismatch},
		{"employment in another legal entity", PositionAssignment{WorkerID: w.ID, EmploymentID: elsewhere.ID, FTEShare: "1.00", ValidFrom: day(time.March, 1)}, CodeAssignmentMismatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := s.CreateAssignment(ctx, pos.ID, c.in, "")
			requireCode(t, err, c.code)
		})
	}

	a := mustAssignment(t, s, ctx, pos.ID, w.ID, emp.ID, "1.00", day(time.March, 1), &jun)
	events := outboxFor(t, s, ctx, a.ID)
	if len(events) != 1 || events[0].Data["manager_position_id"] != boss.ID || events[0].Data["valid_to"] != "2026-06-01T00:00:00Z" {
		t.Fatalf("assignment event = %+v; want manager_position_id %s", events, boss.ID)
	}
	if _, _, err := s.CreateAssignment(ctx, ids.New(), PositionAssignment{WorkerID: w.ID, EmploymentID: emp.ID, FTEShare: "1.00", ValidFrom: day(time.March, 1)}, ""); err == nil {
		t.Fatal("assignment to an unknown position succeeded")
	} else {
		requireCode(t, err, CodePositionNotFound)
	}
}

// TestIdempotentReplaysAndKeyReuse: the same key and body returns the stored
// row without a second insert or event; the same key with another body is
// refused for workers, employments, positions and assignments.
func TestIdempotentReplaysAndKeyReuse(t *testing.T) {
	s, ctx := newStore(t)
	le, dept := ids.New(), ids.New()
	w := mustWorker(t, s, ctx, "Idem")

	empIn := Employment{LegalEntityID: le, EmploymentType: "FULL_TIME", AssignmentKind: "PRIMARY", HireDate: "2026-01-01", ValidFrom: day(time.January, 1)}
	e1, ins1, err := s.CreateEmployment(ctx, w.ID, empIn, "emp-key")
	if err != nil || !ins1 {
		t.Fatalf("first employment: %v", err)
	}
	e2, ins2, err := s.CreateEmployment(ctx, w.ID, empIn, "emp-key")
	if err != nil || ins2 || e2.ID != e1.ID {
		t.Fatalf("employment replay: id=%s inserted=%v err=%v", e2.ID, ins2, err)
	}
	if n := len(outboxFor(t, s, ctx, e1.ID)); n != 1 {
		t.Fatalf("employment events after replay = %d, want 1", n)
	}
	changed := empIn
	changed.EmploymentType = "PART_TIME"
	_, _, err = s.CreateEmployment(ctx, w.ID, changed, "emp-key")
	requireCode(t, err, CodeIdempotencyReuse)

	posIn := Position{PositionNumber: "POS-IDEM", JobCode: "DEV", DepartmentID: dept, LegalEntityID: le, FTE: "1.00", ValidFrom: day(time.January, 1)}
	p1, _, err := s.CreatePosition(ctx, posIn, "pos-key")
	if err != nil {
		t.Fatal(err)
	}
	p2, ins, err := s.CreatePosition(ctx, posIn, "pos-key")
	if err != nil || ins || p2.ID != p1.ID {
		t.Fatalf("position replay: %+v inserted=%v err=%v", p2, ins, err)
	}
	posIn.FTE = "0.50"
	_, _, err = s.CreatePosition(ctx, posIn, "pos-key")
	requireCode(t, err, CodeIdempotencyReuse)

	asIn := PositionAssignment{WorkerID: w.ID, EmploymentID: e1.ID, FTEShare: "1.00", IsPrimary: true, ValidFrom: day(time.January, 1)}
	a1, _, err := s.CreateAssignment(ctx, p1.ID, asIn, "asg-key")
	if err != nil {
		t.Fatal(err)
	}
	a2, ins, err := s.CreateAssignment(ctx, p1.ID, asIn, "asg-key")
	if err != nil || ins || a2.ID != a1.ID {
		t.Fatalf("assignment replay: %+v inserted=%v err=%v", a2, ins, err)
	}
	asIn.FTEShare = "0.50"
	_, _, err = s.CreateAssignment(ctx, p1.ID, asIn, "asg-key")
	requireCode(t, err, CodeIdempotencyReuse)

	_, _, err = s.CreateWorker(ctx, WorkerInput{
		Worker:     Worker{PersonnelNumber: "WRK-Idem", WorkerType: "EMPLOYEE", PartyPersonID: ids.New(), LegalName: "Idem"},
		NationalID: "1234",
	}, "")
	requireCode(t, err, CodeWorkerDuplicate)
}

// TestListsPageAndSearchWithinTheTenant: workers page by personnel number and
// match by name; positions page by number with their reports-to; another
// tenant sees none of them.
func TestListsPageAndSearchWithinTheTenant(t *testing.T) {
	s, ctx := newStore(t)
	for _, name := range []string{"Ada", "Bea", "Cy", "Dee", "Eve"} {
		mustWorker(t, s, ctx, name)
	}
	first, err := s.ListWorkers(ctx, "", "", 2)
	if err != nil || len(first.Items) != 2 || first.Items[0].PersonnelNumber != "WRK-Ada" || first.NextAfter != "WRK-Bea" {
		t.Fatalf("first page = %+v err=%v", first, err)
	}
	rest, err := s.ListWorkers(ctx, "", first.NextAfter, 10)
	if err != nil || len(rest.Items) != 3 || rest.NextAfter != "" || rest.Items[0].PersonnelNumber != "WRK-Cy" {
		t.Fatalf("second page = %+v err=%v", rest, err)
	}
	found, err := s.ListWorkers(ctx, "ee", "", 10)
	if err != nil || len(found.Items) != 1 || found.Items[0].LegalName != "Dee" {
		t.Fatalf("search ee = %+v err=%v", found, err)
	}
	literal, err := s.ListWorkers(ctx, "%", "", 10)
	if err != nil || len(literal.Items) != 0 {
		t.Fatalf("search %% must be literal, got %+v err=%v", literal, err)
	}

	le, dept := ids.New(), ids.New()
	top := mustPosition(t, s, ctx, "POS-A", dept, le, "1.00", false, day(time.January, 1), nil)
	mustPosition(t, s, ctx, "POS-B", dept, le, "0.50", false, day(time.January, 1), &top.ID)
	positions, err := s.ListPositions(ctx, "", 10)
	if err != nil || len(positions.Items) != 2 || positions.Items[1].ReportsToID == nil || *positions.Items[1].ReportsToID != top.ID {
		t.Fatalf("positions = %+v err=%v", positions, err)
	}
	_, _, err = s.CreatePosition(ctx, Position{PositionNumber: "POS-C", JobCode: "X", DepartmentID: dept, LegalEntityID: le, FTE: "1.00",
		ValidFrom: day(time.January, 1), ReportsToID: &top.ID}, "")
	if err != nil {
		t.Fatalf("child on parent's start date: %v", err)
	}
	jan := day(time.January, 1)
	_, _, err = s.CreatePosition(ctx, Position{PositionNumber: "POS-EARLY", JobCode: "X", DepartmentID: dept, LegalEntityID: le, FTE: "1.00",
		ValidFrom: jan.AddDate(-1, 0, 0), ReportsToID: &top.ID}, "")
	requireCode(t, err, CodePositionParent)

	other := testkit.Ctx(testkit.TenantID(2))
	theirs, err := s.ListWorkers(other, "", "", 10)
	if err != nil || len(theirs.Items) != 0 {
		t.Fatalf("other tenant sees %d workers (err=%v)", len(theirs.Items), err)
	}
	if _, err := s.GetWorker(other, first.Items[0].ID); err == nil {
		t.Fatal("other tenant read a worker")
	} else {
		requireCode(t, err, CodeWorkerNotFound)
	}
	if _, err := s.GetPosition(other, top.ID); err == nil {
		t.Fatal("other tenant read a position")
	} else {
		requireCode(t, err, CodePositionNotFound)
	}
}
