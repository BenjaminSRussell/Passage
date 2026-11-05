package store_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

func startRun(key, input string) store.PlanRunStart {
	return store.PlanRunStart{PlanCode: "MAIN", Items: 1, IdempotencyKey: key, InputHash: input, RequestedBy: "user-test"}
}

// TestPlanRun_Lifecycle is the asynchronous run record of #479: begin, replay,
// key reuse, one running run per plan, heartbeat progress, the synchronous
// run refused while it is alive, the version stored by the run itself, and
// finish with the version.
func TestPlanRun_Lifecycle(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	run, replayed, err := st.BeginPlanRun(ctx, startRun("async-1", "in-1"))
	if err != nil || replayed {
		t.Fatalf("begin = %v, replayed %v", err, replayed)
	}
	if run.Status != store.RunRunning || run.Phase != store.PhasePlanning || run.PlanCode != "MAIN" || run.Items != 1 || run.RequestedBy != "user-test" || run.FinishedAt != nil {
		t.Fatalf("begun run = %+v", run)
	}
	again, replayed, err := st.BeginPlanRun(ctx, startRun("async-1", "in-1"))
	if err != nil || !replayed || again.ID != run.ID {
		t.Fatalf("replay = %+v, %v, %v; want the same run", again, replayed, err)
	}
	if _, _, err := st.BeginPlanRun(ctx, startRun("async-1", "in-2")); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse = %v; want %s", err, store.CodeIdempotencyReuse)
	}
	if _, _, err := st.BeginPlanRun(ctx, startRun("async-2", "in-1")); problemCode(err) != store.CodeAlreadyRunning {
		t.Fatalf("second run of the plan = %v; want %s", err, store.CodeAlreadyRunning)
	}
	if got, found, err := st.ReplayPlanRun(ctx, "async-1", "in-1"); err != nil || !found || got.ID != run.ID {
		t.Fatalf("ReplayPlanRun = %+v, %v, %v", got, found, err)
	}
	if _, found, err := st.ReplayPlanRun(ctx, "nope", "in-1"); err != nil || found {
		t.Fatalf("ReplayPlanRun of an unknown key = %v, %v", found, err)
	}

	stop, err := st.HeartbeatPlanRun(ctx, run.ID, store.PlanRunProgress{Phase: store.PhasePersisting, RowsWritten: 10, RowsTotal: 40})
	if err != nil || stop {
		t.Fatalf("heartbeat = %v, stop %v", err, stop)
	}
	got, err := st.GetPlanRun(ctx, run.ID)
	if err != nil || got.Phase != store.PhasePersisting || got.RowsWritten != 10 || got.RowsTotal != 40 {
		t.Fatalf("run after heartbeat = %+v, %v", got, err)
	}

	res := oneItemPlan(t)
	if _, _, err := st.RunPlan(ctx, runRequest(res, "sync-1", "in-s")); problemCode(err) != store.CodeAlreadyRunning {
		t.Fatalf("synchronous run while an asynchronous one is alive = %v; want %s", err, store.CodeAlreadyRunning)
	}
	req := runRequest(res, "async-1", "in-1")
	var rows []int64
	req.RunID, req.Progress = run.ID, func(n int64) { rows = append(rows, n) }
	v, _, err := st.RunPlan(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 || rows[len(rows)-1] < int64(len(res.Orders)+len(res.Pegging)) {
		t.Fatalf("progress = %v; want the rows written", rows)
	}
	done, err := st.FinishPlanRun(ctx, run.ID, store.PlanRunFinish{Status: store.RunSucceeded, VersionID: v.ID, RowsWritten: rows[len(rows)-1]})
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != store.RunSucceeded || done.Phase != store.PhaseDone || done.FinishedAt == nil || done.Version == nil || done.Version.ID != v.ID ||
		done.VersionNumber == nil || *done.VersionNumber != 1 || done.Version.Status != "current" {
		t.Fatalf("finished run = %+v", done)
	}
	if stop, err := st.HeartbeatPlanRun(ctx, run.ID, store.PlanRunProgress{Phase: store.PhaseDone}); err != nil || !stop {
		t.Fatalf("heartbeat of a finished run = %v, stop %v; want stop", err, stop)
	}
	if _, err := st.CancelPlanRun(ctx, run.ID); problemCode(err) != store.CodeRunFinished {
		t.Fatalf("cancel of a finished run = %v; want %s", err, store.CodeRunFinished)
	}
	// The plan is free again for a synchronous run and a new asynchronous one.
	if _, _, err := st.RunPlan(ctx, runRequest(res, "sync-1", "in-s")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BeginPlanRun(ctx, startRun("async-2", "in-1")); err != nil {
		t.Fatal(err)
	}
	// A tenant does not see another tenant's run.
	other := testkit.Ctx(testkit.TenantID(2), "planning.read")
	if _, err := st.GetPlanRun(other, run.ID); problemCode(err) != store.CodeRunNotFound {
		t.Fatalf("other tenant = %v; want %s", err, store.CodeRunNotFound)
	}
	var stored int
	if err := admin.QueryRow(context.Background(), `select count(*) from plan_run where status = 'running'`).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("running rows = %d, %v; want 1", stored, err)
	}
}

// TestPlanRun_CancelAndAbandon covers a cancel request reaching the heartbeat,
// the cancelled outcome, and a run whose heartbeat stopped: it reads failed,
// no longer holds the plan, and the next run of the plan closes it.
func TestPlanRun_CancelAndAbandon(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	run, _, err := st.BeginPlanRun(ctx, startRun("c-1", "in"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.CancelPlanRun(ctx, run.ID); err != nil || !got.CancelRequested {
		t.Fatalf("cancel = %+v, %v", got, err)
	}
	if stop, err := st.HeartbeatPlanRun(ctx, run.ID, store.PlanRunProgress{Phase: store.PhasePlanning}); err != nil || !stop {
		t.Fatalf("heartbeat after cancel = %v, stop %v; want stop", err, stop)
	}
	cancelled := httpx.NewProblem(http.StatusConflict, "PLAN-RUN-CANCELLED", "cancelled")
	done, err := st.FinishPlanRun(ctx, run.ID, store.PlanRunFinish{Status: store.RunCancelled, Problem: cancelled})
	if err != nil || done.Status != store.RunCancelled || done.Problem == nil || done.Problem.Code != "PLAN-RUN-CANCELLED" || done.Version != nil {
		t.Fatalf("cancelled run = %+v, %v", done, err)
	}
	if _, err := st.GetPlanRun(ctx, "not-a-uuid"); problemCode(err) != store.CodeRunNotFound {
		t.Fatalf("malformed id = %v; want 404", err)
	}

	stale, _, err := st.BeginPlanRun(ctx, startRun("s-1", "in"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), `update plan_run set heartbeat_at = now() - interval '1 hour' where id = $1`, stale.ID); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetPlanRun(ctx, stale.ID)
	if err != nil || got.Status != store.RunFailed || got.Problem == nil || got.Problem.Code != store.CodeRunAbandoned {
		t.Fatalf("stale run = %+v, %v; want failed with %s", got, err, store.CodeRunAbandoned)
	}
	if _, _, err := st.RunPlan(ctx, runRequest(oneItemPlan(t), "sync-2", "in")); err != nil {
		t.Fatalf("synchronous run beside an abandoned one = %v", err)
	}
	next, _, err := st.BeginPlanRun(ctx, startRun("s-2", "in"))
	if err != nil {
		t.Fatalf("run after an abandoned one = %v", err)
	}
	var status string
	if err := admin.QueryRow(context.Background(), `select status from plan_run where id = $1`, stale.ID).Scan(&status); err != nil || status != store.RunFailed {
		t.Fatalf("abandoned row = %q, %v; want closed as failed", status, err)
	}
	if next.Status != store.RunRunning {
		t.Fatalf("next run = %+v", next)
	}
}
