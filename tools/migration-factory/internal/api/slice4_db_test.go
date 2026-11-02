package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// FR-49.6 / 9.49.6: a step owner starts a runbook step, the plan reports it running with its
// elapsed time, and a completion without a duration is measured from the start. Starting obeys
// CUT-1601, the optimistic version and tenant isolation, and is stored for the next reader.
func TestCutoverStep_StartTimesAndMeasuresTheStep(t *testing.T) {
	h, admin := dbServer(t)
	t0 := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	plan := decode[planDTO](t, do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "CutoverManager", body: generateBody(t0, 36), user: "manager"}))
	if len(plan.RollbackSteps) != 6 || plan.RollbackSteps[0].OffsetMin != 36*60 {
		t.Fatalf("generated plan carries the rollback plan: %+v", plan.RollbackSteps)
	}
	seqOf := map[string]int{}
	for _, s := range plan.Steps {
		seqOf[s.Name] = s.Seq
	}
	base := "/api/v1/cutover/plans/" + plan.PlanID
	step := func(action, name, body, key string) *httptest.ResponseRecorder {
		return (do(t, h, request{method: "POST", path: fmt.Sprintf("%s/steps/%d/%s", base, seqOf[name], action), tenant: tenantA, duties: "CutoverStepOwner", body: body, key: key, user: "owner"}))
	}
	load := "Load pack open_items"

	if r := step("complete", load, `{}`, ""); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "actualDurationMin is required") {
		t.Fatalf("completing a step never started needs a duration: %d %s", r.Code, r.Body)
	}
	if r := step("start", "Open user access", `{}`, ""); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "CUT-1601") {
		t.Fatalf("open access cannot start with go/no-go items open: %d %s", r.Code, r.Body)
	}
	if r := step("start", load, `{"version":7}`, ""); r.Code != http.StatusConflict || problemCode(t, r) != CodeVersion {
		t.Fatalf("stale version: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, request{method: "POST", path: fmt.Sprintf("%s/steps/%d/start", base, seqOf[load]), tenant: tenantB, duties: "CutoverStepOwner", body: `{}`}); r.Code != http.StatusNotFound {
		t.Fatalf("another tenant cannot start the step: %d %s", r.Code, r.Body)
	}

	started := step("start", load, `{"version":1}`, "start-load")
	if started.Code != http.StatusOK {
		t.Fatalf("start: %d %s", started.Code, started.Body)
	}
	p := decode[planDTO](t, started)
	var running stepDTO
	for _, s := range p.Steps {
		if s.Name == load {
			running = s
		}
	}
	if p.Version != 2 || running.Status != "PENDING" || running.StartedAt == nil || running.StartedBy != "owner" || running.ElapsedMin == nil || running.Tone != "ok" {
		t.Fatalf("after start: version %d step %+v", p.Version, running)
	}
	if r := step("start", load, `{"version":1}`, "start-load"); r.Code != http.StatusOK || r.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replayed start: %d %s", r.Code, r.Body)
	}
	if r := step("start", load, `{}`, ""); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "already started") {
		t.Fatalf("second start: %d %s", r.Code, r.Body)
	}
	if n := count(t, admin, `select count(*) from cutover_step where plan_id = $1 and started_at is not null and started_by = 'owner'`, plan.PlanID); n != 1 {
		t.Fatalf("stored starts = %d, want 1", n)
	}
	// The stored start is read back by the next reader; move it 75 minutes into the past to
	// stand in for the step running past red (125 % of its 60 planned minutes).
	if _, err := admin.Exec(t.Context(), `update cutover_step set started_at = now() - interval '75 minutes' where plan_id = $1 and seq = $2`, plan.PlanID, seqOf[load]); err != nil {
		t.Fatal(err)
	}
	got := decode[planDTO](t, do(t, h, request{method: "GET", path: base, tenant: tenantA, duties: "CutoverManager"}))
	for _, s := range got.Steps {
		if s.Name == load && (s.Tone != "red" || s.ElapsedMin == nil || *s.ElapsedMin < 75 || !strings.Contains(s.OverrunWarning, "CUT-1602")) {
			t.Fatalf("stored running step past red: %+v", s)
		}
	}

	done := step("complete", load, `{"version":2}`, "")
	if done.Code != http.StatusOK {
		t.Fatalf("complete without duration: %d %s", done.Code, done.Body)
	}
	for _, s := range decode[planDTO](t, done).Steps {
		if s.Name == load && (s.Status != "DONE" || s.ActualDurationMin < 75 || s.ActualDurationMin > 80 || s.Tone != "red" || s.OverrunWarning != "") {
			t.Fatalf("measured completion: %+v", s)
		}
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.cutover.step.completed.v1' and aggregate_id = $1`, plan.PlanID); n != 1 {
		t.Fatalf("step.completed events = %d, want 1 (starting writes none)", n)
	}

	if r := rollbackBoth(t, h, base+"/rollback", `{}`, ""); r.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", r.Code, r.Body)
	}
	if r := step("start", "First weekly reconciliation", `{}`, ""); r.Code != http.StatusConflict || problemCode(t, r) != CodeTransition {
		t.Fatalf("a rolled-back plan's steps cannot start: %d %s", r.Code, r.Body)
	}
}
