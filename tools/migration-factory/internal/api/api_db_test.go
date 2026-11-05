package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/migrations"
)

// dbServer returns the API over a migrated throwaway schema, and the admin pool for
// assertions. When the application-role URL is set, the server runs as passage_app so row-level
// security is in force for the tenant-isolation checks.
func dbServer(t *testing.T) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	var admin, app *pgxpool.Pool
	if os.Getenv(testkit.EnvAppURL) != "" {
		admin, app = testkit.DBWithApp(t)
	} else {
		admin = testkit.DB(t)
		app = admin
	}
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return New(store.New(app)).Routes(), admin
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return v
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPackRun_CleanExtractReconcilesAndRerunLoadsNothing(t *testing.T) {
	h, admin := dbServer(t)
	body := openItemsBody(200)
	first := do(t, h, request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: body})
	if first.Code != http.StatusCreated {
		t.Fatalf("first run status %d: %s", first.Code, first.Body)
	}
	run := decode[runDetailDTO](t, first)
	if run.Status != "RECONCILED" || run.RecordsLoaded != 200 || run.RecordsQuarantined != 0 || run.AlreadyPresent != 0 {
		t.Fatalf("first run %+v", run.runDTO)
	}
	names := map[string]bool{}
	for _, c := range run.ControlTotals {
		names[c.Name] = true
		if c.Status != "MATCH" {
			t.Fatalf("control total %+v not MATCH", c)
		}
	}
	for _, want := range []string{"open_items.amount", "open_items.amount_by_account", "open_items.count"} {
		if !names[want] {
			t.Fatalf("control total %s missing: %+v", want, run.ControlTotals)
		}
	}
	if len(run.InputHash) != 64 {
		t.Fatalf("input hash should be sha256 hex, got %q", run.InputHash)
	}

	again := do(t, h, request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: body})
	rerun := decode[runDetailDTO](t, again)
	if rerun.RunID == run.RunID || rerun.RecordsLoaded != 0 || rerun.AlreadyPresent != 200 || rerun.Status != "RECONCILED" {
		t.Fatalf("AC-49.4: re-run is a new run that loads 0 and keeps totals: %+v", rerun.runDTO)
	}
	if !strings.Contains(rerun.Message, "Re-run loaded 0 new records") {
		t.Fatalf("expected MIG-1505 message, got %q", rerun.Message)
	}
	if rerun.InputHash != run.InputHash {
		t.Fatal("same extract must hash identically")
	}
	if n := count(t, admin, `select count(*) from legacy_xref`); n != 200 {
		t.Fatalf("legacy_xref rows = %d, want 200 (no duplicates after a re-run)", n)
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.migration.pack.reconciled.v1'`); n != 2 {
		t.Fatalf("pack.reconciled events = %d, want one per run", n)
	}
	var payload struct {
		Data struct {
			RunID          string `json:"run_id"`
			Status         string `json:"status"`
			TotalsMismatch int    `json:"totals_mismatch"`
		} `json:"data"`
	}
	var raw []byte
	if err := admin.QueryRow(context.Background(), `select payload from outbox where aggregate_id = $1`, run.RunID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Data.RunID != run.RunID || payload.Data.Status != "RECONCILED" || payload.Data.TotalsMismatch != 0 {
		t.Fatalf("event payload %s (%v)", raw, err)
	}

	got := do(t, h, request{method: "GET", path: "/api/v1/migration/runs/" + run.RunID, tenant: tenantA, duties: "migration.factory.view"})
	if got.Code != http.StatusOK {
		t.Fatalf("get run: %d %s", got.Code, got.Body)
	}
	if detail := decode[runDetailDTO](t, got); len(detail.ControlTotals) != len(run.ControlTotals) || detail.Status != "RECONCILED" {
		t.Fatalf("stored run %+v", detail)
	}
	list := do(t, h, request{method: "GET", path: "/api/v1/migration/runs?pack=open_items&limit=1", tenant: tenantA, duties: "migration.factory.view"})
	page := decode[struct{ Items []runDTO }](t, list)
	if len(page.Items) != 1 || page.Items[0].RunID != rerun.RunID {
		t.Fatalf("newest run first, limit 1: %+v", page.Items)
	}
	next := do(t, h, request{method: "GET", path: "/api/v1/migration/runs?after=" + rerun.RunID, tenant: tenantA, duties: "migration.factory.view"})
	if page := decode[struct{ Items []runDTO }](t, next); len(page.Items) != 1 || page.Items[0].RunID != run.RunID {
		t.Fatalf("second page: %+v", page.Items)
	}
}

func TestPackRun_QuarantinesBadRecordAndLoadsRest(t *testing.T) {
	h, admin := dbServer(t)
	bad := `{"legacyId":"OI-BAD","party":"V999","account":"2100","currency":"USD","amount":"5.00"}`
	rec := do(t, h, request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: openItemsBody(200, bad)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	run := decode[runDetailDTO](t, rec)
	if run.RecordsLoaded != 200 || run.RecordsQuarantined != 1 {
		t.Fatalf("AC-49.3: load proceeds for the rest: %+v", run.runDTO)
	}
	if len(run.Quarantined) != 1 || run.Quarantined[0].Rule != "REFERENTIAL" || run.Quarantined[0].LegacyID != "OI-BAD" {
		t.Fatalf("quarantine detail %+v", run.Quarantined)
	}
	if run.Status != "LOADED" {
		t.Fatalf("a quarantined record keeps the run out of RECONCILED, got %s", run.Status)
	}
	if n := count(t, admin, `select count(*) from pack_quarantine where run_id = $1 and rule = 'REFERENTIAL'`, run.RunID); n != 1 {
		t.Fatalf("pack_quarantine rows = %d", n)
	}
	totals := do(t, h, request{method: "GET", path: "/api/v1/migration/runs/" + run.RunID + "/control-totals", tenant: tenantA, duties: "migration.factory.view"})
	body := decode[struct {
		Status        string
		ControlTotals []totalDTO
	}](t, totals)
	mismatch := 0
	for _, c := range body.ControlTotals {
		if c.Status == "MISMATCH" {
			mismatch++
			if c.Name == "open_items.count" && (c.Source != "201.000000" || c.Target != "200.000000") {
				t.Fatalf("count total %+v", c)
			}
		}
	}
	if body.Status != "LOADED" || mismatch != 3 {
		t.Fatalf("amount, account subtotal and count must each show the held-back record: %+v", body)
	}
}

func TestPackRun_ChangedAmountOnRerunIsMismatch(t *testing.T) {
	h, _ := dbServer(t)
	if rec := do(t, h, request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: openItemsBody(3)}); rec.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	changed := strings.Replace(openItemsBody(3), `"OI-00001","party":"V001","account":"2100","currency":"USD","amount":"10.25"`, `"OI-00001","party":"V001","account":"2100","currency":"USD","amount":"11.25"`, 1)
	rec := do(t, h, request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: changed})
	run := decode[runDetailDTO](t, rec)
	if run.Status != "LOADED" || run.RecordsLoaded != 0 {
		t.Fatalf("an extract whose amount changed since the load must not reconcile: %+v", run.runDTO)
	}
	for _, c := range run.ControlTotals {
		if c.Name == "open_items.amount" && c.Delta != "-1.000000" {
			t.Fatalf("amount delta %+v", c)
		}
	}
}

func TestPackRun_IdempotencyKeyReplaysAndRejectsReuse(t *testing.T) {
	h, admin := dbServer(t)
	rq := request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: openItemsBody(5), key: "run-1"}
	first := do(t, h, rq)
	second := do(t, h, rq)
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated || second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %d %q", first.Code, second.Code, second.Header().Get("Idempotent-Replayed"))
	}
	if a, b := decode[runDTO](t, first), decode[runDTO](t, second); a.RunID != b.RunID || b.RecordsLoaded != 5 {
		t.Fatalf("replay must return the stored run: %+v vs %+v", a, b)
	}
	if n := count(t, admin, `select count(*) from pack_run`); n != 1 {
		t.Fatalf("pack_run rows = %d after a replay, want 1", n)
	}
	rq.body = openItemsBody(6)
	if rec := do(t, h, rq); rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != CodeIdempotencyReuse {
		t.Fatalf("reuse with another body: %d %s", rec.Code, rec.Body)
	}
	rq.tenant = tenantB
	if rec := do(t, h, rq); rec.Code != http.StatusCreated || rec.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("keys are per tenant: %d %s", rec.Code, rec.Body)
	}
}

func TestTenantIsolation(t *testing.T) {
	h, _ := dbServer(t)
	run := decode[runDTO](t, do(t, h, request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: openItemsBody(3)}))
	path := "/api/v1/migration/runs/" + run.RunID + "/control-totals"
	if rec := do(t, h, request{method: "GET", path: path, tenant: tenantA, duties: "migration.factory.view"}); rec.Code != http.StatusOK {
		t.Fatalf("owner read: %d", rec.Code)
	}
	if rec := do(t, h, request{method: "GET", path: path, tenant: tenantB, duties: "migration.factory.view"}); rec.Code != http.StatusNotFound {
		t.Fatalf("other tenant must not see the run: %d", rec.Code)
	}
	list := decode[struct{ Items []runDTO }](t, do(t, h, request{method: "GET", path: "/api/v1/migration/runs", tenant: tenantB, duties: "migration.factory.view"}))
	if len(list.Items) != 0 {
		t.Fatalf("tenant B lists tenant A runs: %+v", list.Items)
	}
	// The same extract in tenant B loads everything: legacy_xref is per tenant.
	if b := decode[runDTO](t, do(t, h, request{method: "POST", path: runsPath, tenant: tenantB, duties: allDuties, body: openItemsBody(3)})); b.RecordsLoaded != 3 {
		t.Fatalf("tenant B load: %+v", b)
	}
}

func generateBody(t0 time.Time, abortH int) string {
	return fmt.Sprintf(`{"t0":%q,"packs":["open_items","gl_balances"],"entities":["USMF"],"abortOffsetHours":%d}`, t0.Format(time.RFC3339), abortH)
}

func TestCutoverPlan_GenerateCompleteRollback(t *testing.T) {
	h, admin := dbServer(t)
	t0 := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage", body: generateBody(t0, 48), user: "manager"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("generate: %d %s", rec.Code, rec.Body)
	}
	plan := decode[planDTO](t, rec)
	if plan.Version != 1 || plan.StepsTotal != len(plan.Steps) || plan.AbortOffsetMin != 48*60 {
		t.Fatalf("plan %+v", plan)
	}
	seqOf := map[string]int{}
	for _, s := range plan.Steps {
		seqOf[s.Name] = s.Seq
		if s.Name == "Abort deadline" && s.OffsetMin != 48*60 {
			t.Fatalf("abort deadline step at %d min, want the configured T+48h", s.OffsetMin)
		}
	}
	base := "/api/v1/cutover/plans/" + plan.PlanID
	complete := func(name string, body string) *httptest.ResponseRecorder {
		return do(t, h, request{method: "POST", path: fmt.Sprintf("%s/steps/%d/complete", base, seqOf[name]), tenant: tenantA, duties: "migration.cutover-step.execute", body: body, user: "owner"})
	}
	if c := complete("Open user access", `{"actualDurationMin":1}`); c.Code != http.StatusUnprocessableEntity || !strings.Contains(c.Body.String(), "CUT-1601") {
		t.Fatalf("open access before go/no-go must be rejected: %d %s", c.Code, c.Body)
	}
	if c := complete("Go/no-go meeting", `{"actualDurationMin":1}`); c.Code != http.StatusUnprocessableEntity || !strings.Contains(c.Body.String(), "CUT-1601") {
		t.Fatalf("meeting before reconciliation must be rejected: %d %s", c.Code, c.Body)
	}
	if c := complete("Reconciliation complete", `{"actualDurationMin":250,"version":1}`); c.Code != http.StatusOK {
		t.Fatalf("reconcile: %d %s", c.Code, c.Body)
	}
	if c := complete("Go/no-go meeting", `{"actualDurationMin":1,"version":1}`); c.Code != http.StatusConflict || problemCode(t, c) != CodeVersion {
		t.Fatalf("stale version must be 409 %s: %d %s", CodeVersion, c.Code, c.Body)
	}
	if c := complete("Reconciliation complete", `{"actualDurationMin":1}`); c.Code != http.StatusUnprocessableEntity {
		t.Fatalf("completing a done step twice: %d %s", c.Code, c.Body)
	}
	if c := complete("Go/no-go meeting", `{"actualDurationMin":60}`); c.Code != http.StatusOK {
		t.Fatalf("meeting: %d %s", c.Code, c.Body)
	}
	opened := complete("Open user access", `{"actualDurationMin":30}`)
	if opened.Code != http.StatusOK {
		t.Fatalf("open access after go/no-go: %d %s", opened.Code, opened.Body)
	}
	after := decode[planDTO](t, opened)
	if after.Version != 4 || after.StepsDone != 3 || len(after.OpenGoNoGo) != 0 {
		t.Fatalf("after three completions %+v", after)
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.cutover.step.completed.v1' and aggregate_id = $1`, plan.PlanID); n != 3 {
		t.Fatalf("step.completed events = %d, want 3", n)
	}
	if n := count(t, admin, `select count(*) from cutover_step where plan_id = $1 and status = 'DONE' and completed_by = 'owner' and completed_at is not null`, plan.PlanID); n != 3 {
		t.Fatalf("stored completions = %d", n)
	}

	rb := base + "/rollback"
	if r := do(t, h, request{method: "POST", path: rb, tenant: tenantA, duties: "migration.cutover.manage", body: `{"approvers":["alice","bob"]}`, user: "alice"}); r.Code != http.StatusBadRequest {
		t.Fatalf("approvers cannot be named in the request: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, request{method: "POST", path: rb, tenant: tenantB, duties: "migration.cutover.manage", body: `{}`, user: "alice"}); r.Code != http.StatusNotFound {
		t.Fatalf("other tenant must not see the plan: %d", r.Code)
	}
	first := do(t, h, request{method: "POST", path: rb, tenant: tenantA, duties: "migration.cutover.manage", body: `{}`, key: "rb-1", user: "alice"})
	if first.Code != http.StatusAccepted {
		t.Fatalf("first approval must be 202 pending: %d %s", first.Code, first.Body)
	}
	if p := decode[planDTO](t, first); p.RolledBack || p.PendingRollback == nil || p.PendingRollback.Approver != "alice" || p.Version != 5 {
		t.Fatalf("plan after the first approval %+v", p)
	}
	if r := do(t, h, request{method: "POST", path: rb, tenant: tenantA, duties: "migration.cutover.manage", body: `{}`, key: "rb-1", user: "alice"}); r.Code != http.StatusAccepted || r.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replayed first approval: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, request{method: "POST", path: rb, tenant: tenantA, duties: "migration.cutover.manage", body: `{}`, user: "alice"}); r.Code != http.StatusConflict || !strings.Contains(r.Body.String(), "CUT-1603") {
		t.Fatalf("the same user cannot confirm their own approval: %d %s", r.Code, r.Body)
	}
	if got := decode[planDTO](t, do(t, h, request{method: "GET", path: base, tenant: tenantA, duties: "migration.cutover.manage"})); got.PendingRollback == nil || got.RolledBack {
		t.Fatalf("pending approval is shown on the plan: %+v", got)
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.cutover.plan.rolledback.v1'`); n != 0 {
		t.Fatalf("a first approval writes no rolledback event, got %d", n)
	}
	done := do(t, h, request{method: "POST", path: rb, tenant: tenantA, duties: "migration.cutover.manage", body: `{}`, key: "rb-2", user: "bob"})
	if done.Code != http.StatusOK {
		t.Fatalf("second approval: %d %s", done.Code, done.Body)
	}
	if p := decode[planDTO](t, done); !p.RolledBack || strings.Join(p.Approvers, ",") != "alice,bob" || p.PendingRollback != nil {
		t.Fatalf("rolled back plan %+v", p)
	}
	if r := do(t, h, request{method: "POST", path: rb, tenant: tenantA, duties: "migration.cutover.manage", body: `{}`, key: "rb-2", user: "bob"}); r.Code != http.StatusOK || r.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replayed rollback: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, request{method: "POST", path: rb, tenant: tenantA, duties: "migration.cutover.manage", body: `{}`, user: "carol"}); r.Code != http.StatusConflict {
		t.Fatalf("a second rollback must be 409: %d %s", r.Code, r.Body)
	}
	if c := complete("First weekly reconciliation", `{"actualDurationMin":1}`); c.Code != http.StatusConflict {
		t.Fatalf("steps of a rolled-back plan cannot complete: %d %s", c.Code, c.Body)
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.cutover.plan.rolledback.v1'`); n != 1 {
		t.Fatalf("rolledback events = %d, want 1", n)
	}

	got := decode[planDTO](t, do(t, h, request{method: "GET", path: base, tenant: tenantA, duties: "migration.cutover.manage"}))
	if !got.RolledBack || len(got.Steps) != plan.StepsTotal {
		t.Fatalf("stored plan %+v", got)
	}
	list := decode[struct{ Items []planDTO }](t, do(t, h, request{method: "GET", path: "/api/v1/cutover/plans", tenant: tenantA, duties: "migration.cutover.manage"}))
	if len(list.Items) != 1 || list.Items[0].PlanID != plan.PlanID || list.Items[0].Steps != nil || list.Items[0].StepsDone != 3 {
		t.Fatalf("plan list %+v", list.Items)
	}
	if missing := do(t, h, request{method: "GET", path: "/api/v1/cutover/plans/00000000-0000-7000-8000-000000000099", tenant: tenantA, duties: "migration.cutover.manage"}); missing.Code != http.StatusNotFound || problemCode(t, missing) != CodeNotFound {
		t.Fatalf("unknown plan: %d %s", missing.Code, missing.Body)
	}
}
