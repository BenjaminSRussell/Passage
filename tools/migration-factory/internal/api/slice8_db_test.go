package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// FR-49.6 "durations from the last rehearsal": a plan generated from a rehearsal plan takes the
// durations measured on its completed steps as its planned durations; explicit durations override.
func TestGeneratePlan_CarriesMeasuredRehearsalDurations(t *testing.T) {
	h, admin := dbServer(t)
	t0 := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	gen := func(body string) *httptest.ResponseRecorder {
		return do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage", body: body, user: "manager"})
	}
	rehearsal := decode[planDTO](t, gen(generateBody(t0, 36)))
	base := "/api/v1/cutover/plans/" + rehearsal.PlanID
	seqOf := map[string]int{}
	for _, s := range rehearsal.Steps {
		seqOf[s.Name] = s.Seq
	}
	complete := func(name, body string) {
		t.Helper()
		if c := do(t, h, request{method: "POST", path: fmt.Sprintf("%s/steps/%d/complete", base, seqOf[name]), tenant: tenantA, duties: "migration.cutover-step.execute", body: body, user: "owner"}); c.Code != http.StatusOK {
			t.Fatalf("complete %s: %d %s", name, c.Code, c.Body)
		}
	}

	// Nothing measured yet.
	next := fmt.Sprintf(`{"t0":%q,"packs":["open_items","gl_balances"],"entities":["USMF"],"rehearsalPlanId":%q}`, t0.AddDate(0, 0, 14).Format(time.RFC3339), rehearsal.PlanID)
	if r := gen(next); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "no measured step durations") {
		t.Fatalf("a plan with nothing measured: %d %s", r.Code, r.Body)
	}
	complete("Reconciliation complete", `{"actualDurationMin":250}`)
	complete("Go/no-go meeting", `{"actualDurationMin":75}`)

	if r := gen(strings.Replace(next, rehearsal.PlanID, "not-a-uuid", 1)); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad id: %d %s", r.Code, r.Body)
	}
	if r := gen(strings.Replace(next, rehearsal.PlanID, "01a19c46-9e7c-72ce-83c7-aa23184b9838", 1)); r.Code != http.StatusNotFound {
		t.Fatalf("unknown rehearsal plan: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantB, duties: "migration.cutover.manage", body: next, user: "manager"}); r.Code != http.StatusNotFound {
		t.Fatalf("another tenant's rehearsal plan: %d %s", r.Code, r.Body)
	}

	rec := gen(next)
	if rec.Code != http.StatusCreated {
		t.Fatalf("generate from the rehearsal: %d %s", rec.Code, rec.Body)
	}
	plan := decode[planDTO](t, rec)
	planned := map[string]int{}
	for _, s := range plan.Steps {
		planned[s.Name] = s.PlannedDurationMin
	}
	if planned["Reconciliation complete"] != 250 || planned["Go/no-go meeting"] != 75 || plan.RehearsalPlanID != rehearsal.PlanID || plan.RehearsalCarried != 2 {
		t.Fatalf("carried durations %+v from %q carried %d", planned, plan.RehearsalPlanID, plan.RehearsalCarried)
	}
	if n := count(t, admin, `select rehearsal_carried from cutover_plan where plan_id = $1 and rehearsal_plan_id = $2`, plan.PlanID, rehearsal.PlanID); n != 2 {
		t.Fatalf("stored link carried = %d", n)
	}
	if stored := decode[planDTO](t, do(t, h, request{method: "GET", path: "/api/v1/cutover/plans/" + plan.PlanID, tenant: tenantA, duties: "migration.cutover.manage"})); stored.RehearsalPlanID != rehearsal.PlanID || stored.RehearsalCarried != 2 {
		t.Fatalf("stored plan %+v", stored)
	}

	// An explicit duration overrides the measured one.
	over := strings.Replace(next, `"rehearsalPlanId"`, `"rehearsalDurationsMin":{"Go/no-go meeting":90},"rehearsalPlanId"`, 1)
	plan = decode[planDTO](t, gen(over))
	for _, s := range plan.Steps {
		if s.Name == "Go/no-go meeting" && s.PlannedDurationMin != 90 {
			t.Fatalf("override ignored: %d", s.PlannedDurationMin)
		}
		if s.Name == "Reconciliation complete" && s.PlannedDurationMin != 250 {
			t.Fatalf("measured duration lost: %d", s.PlannedDurationMin)
		}
	}
}

// CUT-1603, CUT-1605: a rollback approval belongs to the go/no-go decision round it was given in, like
// a GO approval. A decision recorded in between voids it, so the next migration.cutover.manage's request is a
// new first approval, not the confirmation of the stale one.
func TestRollback_DecisionVoidsPendingApproval(t *testing.T) {
	h, admin := dbServer(t)
	plan := decode[planDTO](t, do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage",
		body: generateBody(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), 36), user: "manager"}))
	base := "/api/v1/cutover/plans/" + plan.PlanID
	rollback := func(user string) *httptest.ResponseRecorder {
		return do(t, h, request{method: "POST", path: base + "/rollback", tenant: tenantA, duties: "migration.cutover.manage", body: `{}`, user: user})
	}
	get := func() planDTO {
		return decode[planDTO](t, do(t, h, request{method: "GET", path: base, tenant: tenantA, duties: "migration.cutover.manage", user: "viewer"}))
	}

	if r := rollback("alice"); r.Code != http.StatusAccepted {
		t.Fatalf("alice approves: %d %s", r.Code, r.Body)
	}
	noGo := do(t, h, request{method: "POST", path: base + "/go-no-go", tenant: tenantA, duties: "migration.cutover.manage",
		body: `{"decision":"NO_GO","reason":"Bank smoke test failed"}`, user: "carol"})
	if noGo.Code != http.StatusCreated {
		t.Fatalf("no-go: %d %s", noGo.Code, noGo.Body)
	}
	if p := decode[decisionResponse](t, noGo).Plan; p.PendingRollback != nil {
		t.Fatalf("the decision response still shows the voided approval: %+v", p.PendingRollback)
	}
	if p := get(); p.PendingRollback != nil || p.RolledBack {
		t.Fatalf("after the decision the approval is void: %+v", p)
	}

	// Bob's request is now the first approval of round 2, not the confirmation of Alice's.
	r := rollback("bob")
	if r.Code != http.StatusAccepted {
		t.Fatalf("bob after the decision: %d %s", r.Code, r.Body)
	}
	if p := decode[planDTO](t, r); p.RolledBack || p.PendingRollback == nil || p.PendingRollback.Approver != "bob" {
		t.Fatalf("bob's approval %+v", p)
	}
	// Alice approves again in the new round, which runs the rollback with both as approvers.
	done := rollback("alice")
	if done.Code != http.StatusOK {
		t.Fatalf("alice confirms in round 2: %d %s", done.Code, done.Body)
	}
	if p := decode[planDTO](t, done); !p.RolledBack || strings.Join(p.Approvers, ",") != "bob,alice" {
		t.Fatalf("rolled back %+v", p)
	}
	if n := count(t, admin, `select count(*) from cutover_rollback_approval where plan_id = $1 and (round, approver) in ((1, 'alice'), (2, 'bob'))`, plan.PlanID); n != 2 {
		t.Fatalf("stored approvals by round = %d, want 2", n)
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.cutover.plan.rolledback.v1'`); n != 1 {
		t.Fatalf("rolledback events = %d, want 1", n)
	}
}
