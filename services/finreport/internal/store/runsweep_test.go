package store

import (
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
)

// adhoc creates a run of the fixture's ledger that started `age` ago and returns its id.
func (f firingFixture) adhoc(t *testing.T, key string, age time.Duration) string {
	t.Helper()
	run, err := f.st.CreateRun(f.ctx, RunSpec{Code: "FIRE-DEF", LedgerID: f.ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 1,
		Calendar: storeFixtureCalendar(), RequestedBy: "user", IdempotencyKey: key, RequestHash: "h-" + key})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `update statement_run set started_at = now() - $2::interval where id=$1::uuid`, run.ID, age.String()); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

func (f firingFixture) runExists(t *testing.T, id string) bool {
	t.Helper()
	var n int
	if err := f.admin.QueryRow(f.ctx, `select count(*) from statement_run where id=$1::uuid`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func (f firingFixture) cellCount(t *testing.T, id string) (n int) {
	t.Helper()
	if err := f.admin.QueryRow(f.ctx, `select count(*) from statement_cell where run_id=$1::uuid`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

const day = 24 * time.Hour

func TestRunSweep_DeletesOldAdhocRunsWithCellsAndKeepsTheRest(t *testing.T) {
	f := firingSetup(t)
	now := time.Now().UTC()
	old := f.adhoc(t, "old", 100*day)
	young := f.adhoc(t, "young", 10*day)
	if f.cellCount(t, old) == 0 {
		t.Fatal("the fixture run has no cells")
	}
	// A scheduled run is not ad hoc however old it is: its firing record refers to it.
	res := f.result(t, apr1, FiringOutput{Format: "csv", FileName: "a.csv", ContentType: "text/csv", Content: []byte("aaaa")})
	if _, err := f.st.RecordFiring(f.ctx, res); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `update statement_run set started_at = now() - interval '400 days' where id=$1::uuid`, res.Run.ID); err != nil {
		t.Fatal(err)
	}
	cells := f.cellCount(t, old)

	passes, err := f.st.SweepRuns(f.ctx, now, 90)
	if err != nil || len(passes) != 1 {
		t.Fatalf("sweep %+v err %v", passes, err)
	}
	w := passes[0]
	if w.DeletedRuns != 1 || w.DeletedCells != int64(cells) || w.HeldRuns != 0 || w.LegalEntityID != f.entity || w.RetentionDays != 90 || w.Actor != SweepActor || w.HoldID != "" {
		t.Fatalf("pass %+v", w)
	}
	if f.runExists(t, old) || f.cellCount(t, old) != 0 {
		t.Fatal("the old ad-hoc run or its cells are still there")
	}
	if !f.runExists(t, young) || f.cellCount(t, young) == 0 {
		t.Fatal("a run younger than the retention was deleted")
	}
	if !f.runExists(t, res.Run.ID) || f.cellCount(t, res.Run.ID) == 0 {
		t.Fatal("a scheduled run was deleted")
	}
	if wantCutoff := now.Add(-90 * day); w.Cutoff.Before(wantCutoff.Add(-time.Minute)) || w.Cutoff.After(wantCutoff.Add(time.Minute)) {
		t.Fatalf("cutoff %s, want about %s", w.Cutoff, wantCutoff)
	}

	sweeps, err := f.st.ListRunSweeps(f.ctx, 10)
	if err != nil || len(sweeps) != 1 || sweeps[0].ID != w.ID || sweeps[0].DeletedRuns != 1 || sweeps[0].SweptAt.IsZero() {
		t.Fatalf("listed %+v err %v", sweeps, err)
	}
	if countType(outboxTypes(t, f.ctx, f.admin), events.RunsSwept) != 1 {
		t.Fatalf("outbox %v: want one runs.swept", outboxTypes(t, f.ctx, f.admin))
	}
	var audited int
	_ = f.admin.QueryRow(f.ctx, `select count(*) from audit_trail_entry where kind=$1`, domain.AuditRunsSwept).Scan(&audited)
	if audited != 1 {
		t.Fatalf("%d runs_swept audit entries, want 1", audited)
	}
	// Nothing more is past retention: no pass, no event.
	if again, err := f.st.SweepRuns(f.ctx, now, 90); err != nil || len(again) != 0 {
		t.Fatalf("second sweep %+v err %v", again, err)
	}
	// A shorter retention reaches the 10-day-old run.
	again, err := f.st.SweepRuns(f.ctx, now, 7)
	if err != nil || len(again) != 1 || again[0].DeletedRuns != 1 || f.runExists(t, young) {
		t.Fatalf("7-day sweep %+v err %v", again, err)
	}
	if _, err := f.st.SweepRuns(f.ctx, now, 6); err == nil {
		t.Fatal("a retention under 7 days was accepted")
	}
}

func TestRunSweep_LegalHoldKeepsTheRunsOfItsEntity(t *testing.T) {
	f := firingSetup(t)
	now := time.Now().UTC()
	single := f.adhoc(t, "single", 120*day)
	consolidated := f.adhoc(t, "consolidated", 120*day)
	other := ids.New()
	// A consolidated run belongs to the first entity listed and is held by any of them.
	if _, err := f.admin.Exec(f.ctx, `update statement_run set legal_entity_id = null, legal_entity_ids = array[$2::uuid, $3::uuid] where id=$1::uuid`,
		consolidated, f.entity, other); err != nil {
		t.Fatal(err)
	}
	hold, err := f.st.PlaceHold(f.ctx, holdSpec(other, "k-other"))
	if err != nil {
		t.Fatal(err)
	}
	passes, err := f.st.SweepRuns(f.ctx, now, 90)
	if err != nil || len(passes) != 1 || passes[0].DeletedRuns != 1 || passes[0].HeldRuns != 1 || passes[0].HoldID != hold.ID {
		t.Fatalf("sweep with a hold on the second entity %+v err %v", passes, err)
	}
	if f.runExists(t, single) || !f.runExists(t, consolidated) || f.cellCount(t, consolidated) == 0 {
		t.Fatal("the consolidated run must stay under the hold of its second entity and the single-entity run go")
	}
	// The hold on the run's own entity keeps it too, and releasing the holds lets the run go.
	mine, err := f.st.PlaceHold(f.ctx, holdSpec(f.entity, "k-mine"))
	if err != nil {
		t.Fatal(err)
	}
	passes, err = f.st.SweepRuns(f.ctx, now, 90)
	if err != nil || len(passes) != 1 || passes[0].DeletedRuns != 0 || passes[0].HeldRuns != 1 {
		t.Fatalf("sweep under two holds %+v err %v", passes, err)
	}
	for _, h := range []LegalHold{hold, mine} {
		rev := h.Revision
		if _, err := f.st.ReleaseHold(f.ctx, h.ID, &rev, "", "legal"); err != nil {
			t.Fatal(err)
		}
	}
	passes, err = f.st.SweepRuns(f.ctx, now, 90)
	if err != nil || len(passes) != 1 || passes[0].DeletedRuns != 1 || passes[0].HeldRuns != 0 || f.runExists(t, consolidated) {
		t.Fatalf("sweep after release %+v err %v", passes, err)
	}
}

func TestRunSweep_LeavesRunsWithoutAnEntityAndOtherScopesAlone(t *testing.T) {
	f := firingSetup(t)
	now := time.Now().UTC()
	run := f.adhoc(t, "r", 120*day)
	if _, err := f.admin.Exec(f.ctx, `update statement_run set legal_entity_id = null where id=$1::uuid`, run); err != nil {
		t.Fatal(err)
	}
	if passes, err := f.st.SweepRuns(f.ctx, now, 90); err != nil || len(passes) != 0 || !f.runExists(t, run) {
		t.Fatalf("a run without an entity: %+v err %v", passes, err)
	}
	if _, err := f.admin.Exec(f.ctx, `update statement_run set legal_entity_id = $2::uuid where id=$1::uuid`, run, f.entity); err != nil {
		t.Fatal(err)
	}
	scoped := scopedCtx(f.tenantID(), ids.New())
	if passes, err := f.st.SweepRuns(scoped, now, 90); err != nil || len(passes) != 0 || !f.runExists(t, run) {
		t.Fatalf("a caller of another entity swept %+v err %v", passes, err)
	}
	if sweeps, _ := f.st.ListRunSweeps(scoped, 10); len(sweeps) != 0 {
		t.Fatalf("another entity sees %+v", sweeps)
	}
}

// The database refuses a run delete the sweep would not make, whoever issues it.
func TestRunSweep_TriggerRefusesYoungScheduledAndHeldDeletes(t *testing.T) {
	f := firingSetup(t)
	young := f.adhoc(t, "young", 3*day)
	if _, err := f.admin.Exec(f.ctx, `delete from statement_run where id=$1::uuid`, young); err == nil || !strings.Contains(err.Error(), "younger") {
		t.Fatalf("young delete = %v, want the retention refusal", err)
	}
	res := f.result(t, apr1, FiringOutput{Format: "csv", FileName: "a.csv", ContentType: "text/csv", Content: []byte("aaaa")})
	if _, err := f.st.RecordFiring(f.ctx, res); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `update statement_run set started_at = now() - interval '400 days'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `delete from statement_run where id=$1::uuid`, res.Run.ID); err == nil || !strings.Contains(err.Error(), "scheduled") {
		t.Fatalf("scheduled delete = %v, want the scheduled refusal", err)
	}
	if _, err := f.st.PlaceHold(f.ctx, holdSpec(f.entity, "k1")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `delete from statement_run where id=$1::uuid`, young); err == nil {
		t.Fatal("a held delete went through")
	}
	if _, err := f.admin.Exec(f.ctx, `update statement_run set started_at = now() - interval '400 days' where id=$1::uuid`, young); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `delete from statement_run where id=$1::uuid`, young); err == nil || !strings.Contains(err.Error(), "legal hold") {
		t.Fatalf("held delete = %v, want the legal hold refusal", err)
	}
}

// A hold being placed on the second entity of a consolidated run while the
// sweep of the first entity starts: the sweep waits for it and keeps the run as
// held, instead of deleting it or failing on the delete guard.
func TestRunSweep_WaitsForAHoldBeingPlacedOnAnotherEntityOfAConsolidatedRun(t *testing.T) {
	f := firingSetup(t)
	consolidated := f.adhoc(t, "consolidated", 120*day)
	other := ids.New()
	if _, err := f.admin.Exec(f.ctx, `update statement_run set legal_entity_id = null, legal_entity_ids = array[$2::uuid, $3::uuid] where id=$1::uuid`,
		consolidated, f.entity, other); err != nil {
		t.Fatal(err)
	}
	placing, err := f.admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = placing.Rollback(f.ctx) }()
	holdID := ids.New()
	if _, err := placing.Exec(f.ctx, `insert into report_legal_hold(tenant_id, id, legal_entity_id, reason, placed_by, idempotency_key, request_hash)
		values($1::uuid, $2::uuid, $3::uuid, 'Audit', 'legal', 'k-race', 'h')`, f.tenantID(), holdID, other); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		passes []RunSweep
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		passes, err := f.st.SweepRuns(f.ctx, time.Now().UTC(), 90)
		done <- outcome{passes, err}
	}()
	select {
	case o := <-done:
		t.Fatalf("the sweep did not wait for the hold being placed: %+v %v", o.passes, o.err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := placing.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case o := <-done:
		if o.err != nil || len(o.passes) != 1 || o.passes[0].DeletedRuns != 0 || o.passes[0].HeldRuns != 1 || o.passes[0].HoldID != holdID {
			t.Fatalf("sweep after the hold committed %+v err %v", o.passes, o.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the sweep did not finish after the hold committed")
	}
	if !f.runExists(t, consolidated) || f.cellCount(t, consolidated) == 0 {
		t.Fatal("the consolidated run under the new hold was deleted")
	}
}
