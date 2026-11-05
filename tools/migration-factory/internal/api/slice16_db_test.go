package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Database test for slice 16: the per-entity runbook (D5a, `fx-cutover-plan`). It skips without
// PASSAGE_TEST_DATABASE_URL and runs as passage_app when PASSAGE_TEST_APP_DATABASE_URL is set.

const expandedBody = `{"t0":"2027-01-01T00:00:00Z","entities":["USMF","DEMF","JPMF"],"expandByEntity":true,` +
	`"packs":["open_items","gl_balances","onhand","open_orders","wip_prod","fixed_assets","project_wip","payroll_ytd","bank_balances"]}`

func TestExpandedPlan_StoresEntityStepsAndRunsThemThroughTheGates(t *testing.T) {
	h, _ := dbServer(t)
	r := do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage", user: "manager", key: "exp-1", body: expandedBody})
	if r.Code != http.StatusCreated {
		t.Fatalf("generate: %d %s", r.Code, r.Body.String())
	}
	plan := decode[planDTO](t, r)
	if plan.StepsTotal != 84 || len(plan.Steps) != 84 {
		t.Fatalf("steps = %d/%d, want 84", plan.StepsTotal, len(plan.Steps))
	}
	// The same key replays the same plan.
	again := do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage", user: "manager", key: "exp-1", body: expandedBody})
	if again.Code != http.StatusCreated || again.Header().Get("Idempotent-Replayed") != "true" || decode[planDTO](t, again).PlanID != plan.PlanID {
		t.Fatalf("replay: %d %v", again.Code, again.Header())
	}
	// The same key with the flag turned off is another request, not a replay.
	if r := do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage", user: "manager", key: "exp-1",
		body: strings.Replace(expandedBody, `"expandByEntity":true`, `"expandByEntity":false`, 1)}); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), CodeIdempotencyReuse) {
		t.Fatalf("same key, other body: %d %s", r.Code, r.Body.String())
	}
	// The entity of every per-entity step survives the store; another tenant cannot read the plan.
	got := decode[planDTO](t, do(t, h, request{method: "GET", path: "/api/v1/cutover/plans/" + plan.PlanID, tenant: tenantA, duties: allDuties}))
	perEntity := map[string]int{}
	var load stepDTO
	for _, s := range got.Steps {
		if s.Entity != "" {
			perEntity[s.Entity]++
		}
		if s.Name == "Load pack onhand for DEMF" {
			load = s
		}
	}
	if perEntity["USMF"] != 23 || perEntity["DEMF"] != 23 || perEntity["JPMF"] != 23 || load.Pack != "onhand" || load.Entity != "DEMF" {
		t.Fatalf("entities not stored: %v %+v", perEntity, load)
	}
	if r := do(t, h, request{method: "GET", path: "/api/v1/cutover/plans/" + plan.PlanID, tenant: tenantB, duties: allDuties}); r.Code != http.StatusNotFound {
		t.Fatalf("other tenant: %d", r.Code)
	}
	seqOf := func(name string) string {
		for _, s := range got.Steps {
			if s.Name == name {
				return strconv.Itoa(s.Seq)
			}
		}
		t.Fatalf("no step %q", name)
		return ""
	}
	steps := "/api/v1/cutover/plans/" + plan.PlanID + "/steps/"
	base := steps + strconv.Itoa(load.Seq)
	// The stored plan keeps the per-entity order: DEMF's load waits for DEMF's freeze and extract.
	if r := do(t, h, request{method: "POST", path: base + "/start", tenant: tenantA, duties: "migration.cutover-step.execute", user: "engineer", body: `{"version":1}`}); r.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(r.Body.String(), "CUT-1609") || !strings.Contains(r.Body.String(), "Freeze legacy company DEMF") {
		t.Fatalf("start before the freeze: %d %s", r.Code, r.Body.String())
	}
	for i, name := range []string{"Freeze legacy company DEMF", "Extract entity DEMF"} {
		body := `{"actualDurationMin":20,"version":` + strconv.Itoa(i+1) + `}`
		if r := do(t, h, request{method: "POST", path: steps + seqOf(name) + "/complete", tenant: tenantA, duties: "migration.cutover-step.execute", user: "engineer", body: body}); r.Code != http.StatusOK {
			t.Fatalf("complete %q: %d %s", name, r.Code, r.Body.String())
		}
	}
	// A per-entity step is then started and completed like any other, with the plan version.
	if r := do(t, h, request{method: "POST", path: base + "/start", tenant: tenantA, duties: "migration.cutover-step.execute", user: "engineer", body: `{"version":3}`}); r.Code != http.StatusOK {
		t.Fatalf("start: %d %s", r.Code, r.Body.String())
	}
	done := do(t, h, request{method: "POST", path: base + "/complete", tenant: tenantA, duties: "migration.cutover-step.execute", user: "engineer", body: `{"actualDurationMin":70,"version":4}`})
	if done.Code != http.StatusOK || !strings.Contains(done.Body.String(), `"entity":"DEMF"`) {
		t.Fatalf("complete: %d %s", done.Code, done.Body.String())
	}
	// The plan-wide reconciliation go/no-go step waits for every entity's sign-off.
	if r := do(t, h, request{method: "POST", path: steps + seqOf("Reconciliation complete") + "/complete", tenant: tenantA, duties: "migration.cutover-step.execute", user: "engineer",
		body: `{"actualDurationMin":5}`}); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "Controller sign-off USMF") {
		t.Fatalf("go/no-go before the sign-offs: %d %s", r.Code, r.Body.String())
	}
	// A rehearsal of this plan carries the per-entity durations (freeze, extract, load) into the next one.
	next := decode[planDTO](t, do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage", user: "manager",
		body: strings.Replace(expandedBody, `"expandByEntity":true`, `"expandByEntity":true,"rehearsalPlanId":"`+plan.PlanID+`"`, 1)}))
	if next.RehearsalCarried != 3 || next.PlanID == plan.PlanID {
		t.Fatalf("rehearsalCarried = %d, want 3 on a new plan", next.RehearsalCarried)
	}
	// A plan generated without the flag has no per-entity step.
	plain := decode[planDTO](t, do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage", user: "manager", body: generateBody(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), 36)}))
	for _, s := range plain.Steps {
		if s.Entity != "" {
			t.Fatalf("plain plan step %q has an entity", s.Name)
		}
	}
}
