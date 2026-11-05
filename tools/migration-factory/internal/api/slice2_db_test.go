package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Database tests for slice 2: quarantine release, the legacy cross-reference lookup, the
// parallel-run review and project beginning balances. They skip without PASSAGE_TEST_DATABASE_URL
// and run as passage_app when PASSAGE_TEST_APP_DATABASE_URL is set.

func releasePath(runID string, seq int) string {
	return fmt.Sprintf("/api/v1/migration/runs/%s/quarantine/%d/release", runID, seq)
}

func TestRelease_CorrectedRecordLoadsAndReconcilesTheRun(t *testing.T) {
	h, admin := dbServer(t)
	bad := `{"legacyId":"OI-BAD","party":"V999","account":"2100","currency":"USD","amount":"5.00"}`
	noAcct := `{"legacyId":"OI-NOACCT","party":"V001","account":"","currency":"USD","amount":"1.50"}`
	run := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: strings.Replace(openItemsBody(198, bad, noAcct), `"parties"`, `"quarantineMaxPct":"5","parties"`, 1)}))
	if run.Status != "LOADED" || len(run.Quarantined) != 2 || run.Version != 1 {
		t.Fatalf("run %+v quarantine %+v", run.runDTO, run.Quarantined)
	}
	q := run.Quarantined[0]
	if q.Seq != 1 || q.Status != "OPEN" || q.Party != "V999" || q.Amount != "5.000000" {
		t.Fatalf("quarantine row %+v", q)
	}

	// Still failing: the party is not in master data.
	rec := do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", body: `{}`})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "REFERENTIAL") {
		t.Fatalf("uncorrected release: %d %s", rec.Code, rec.Body)
	}
	// A stale version is refused before anything changes.
	rec = do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", body: `{"party":"V001","version":7}`})
	if rec.Code != http.StatusConflict || problemCode(t, rec) != CodeVersion {
		t.Fatalf("stale version: %d %s", rec.Code, rec.Body)
	}
	// A party added to master data since the run releases the record.
	rec = do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", key: "rel-1", body: `{"parties":["V999"],"version":1}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("release: %d %s", rec.Code, rec.Body)
	}
	after := decode[runDetailDTO](t, rec)
	if after.Status != "LOADED" || after.RecordsLoaded != 199 || after.RecordsQuarantined != 1 || after.Version != 2 {
		t.Fatalf("one record still quarantined keeps the run LOADED: %+v", after.runDTO)
	}
	if r := after.Quarantined[0]; r.Status != "RELEASED" || r.ReleasedBy != "ops" || r.ReleasedAt == nil || r.Released == nil || r.Released.Party != "V999" {
		t.Fatalf("released row %+v", r)
	}

	// The second record needs its control account; the release reconciles the run.
	rec = do(t, h, request{method: "POST", path: releasePath(run.RunID, 2), tenant: tenantA, duties: allDuties, user: "ops", body: `{"account":"2100","version":2}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("second release: %d %s", rec.Code, rec.Body)
	}
	final := decode[runDetailDTO](t, rec)
	if final.Status != "RECONCILED" || final.RecordsLoaded != 200 || final.RecordsQuarantined != 0 || final.Version != 3 {
		t.Fatalf("final %+v", final.runDTO)
	}
	for _, c := range final.ControlTotals {
		if c.Status != "MATCH" {
			t.Fatalf("total %+v after both releases", c)
		}
	}
	if n := count(t, admin, `select count(*) from legacy_xref where run_id = $1`, run.RunID); n != 200 {
		t.Fatalf("legacy_xref rows = %d", n)
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.migration.pack.reconciled.v1' and aggregate_id = $1`, run.RunID); n != 2 {
		t.Fatalf("pack.reconciled events = %d, want the run's and the reconciling release's", n)
	}
	// The stored run reads back the same.
	got := decode[runDetailDTO](t, do(t, h, request{method: "GET", path: "/api/v1/migration/runs/" + run.RunID, tenant: tenantA, duties: "migration.factory.view"}))
	if got.Status != "RECONCILED" || got.Version != 3 || got.Quarantined[1].Released == nil || got.Quarantined[1].Released.Account != "2100" {
		t.Fatalf("read back %+v %+v", got.runDTO, got.Quarantined)
	}

	// Releasing a released record replays (idempotent by record), even with a new key.
	rec = do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", key: "rel-again", body: `{"party":"V001"}`})
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" || decode[runDetailDTO](t, rec).Version != 3 {
		t.Fatalf("re-release: %d %q %s", rec.Code, rec.Header().Get("Idempotent-Replayed"), rec.Body)
	}
	if rec := do(t, h, request{method: "POST", path: releasePath(run.RunID, 9), tenant: tenantA, duties: allDuties, user: "ops", body: `{}`}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown record: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantB, duties: allDuties, user: "ops", body: `{}`}); rec.Code != http.StatusNotFound {
		t.Fatalf("other tenant release: %d %s", rec.Code, rec.Body)
	}

	// The released record resolves through the cross-reference (FR-49.3).
	x := decode[struct{ Items []xrefDTO }](t, do(t, h, request{method: "GET", path: "/api/v1/migration/xref?system=AX2012&table=open_items&id=OI-BAD", tenant: tenantA}))
	if len(x.Items) != 1 || x.Items[0].Company != "USMF" || x.Items[0].PassageEntity != "open_item" || x.Items[0].RunID != run.RunID {
		t.Fatalf("xref %+v", x.Items)
	}
	back := decode[struct{ Items []xrefDTO }](t, do(t, h, request{method: "GET", path: "/api/v1/migration/xref?passageEntity=open_item&passageId=" + x.Items[0].PassageID, tenant: tenantA}))
	if len(back.Items) != 1 || back.Items[0].LegacyID != "OI-BAD" {
		t.Fatalf("reverse xref %+v", back.Items)
	}
	if other := decode[struct{ Items []xrefDTO }](t, do(t, h, request{method: "GET", path: "/api/v1/migration/xref?system=AX2012&table=open_items&id=OI-BAD", tenant: tenantB})); len(other.Items) != 0 {
		t.Fatalf("tenant B resolves tenant A ids: %+v", other.Items)
	}
	if none := decode[struct{ Items []xrefDTO }](t, do(t, h, request{method: "GET", path: "/api/v1/migration/xref?system=AX2012&company=DEMF&table=open_items&id=OI-BAD", tenant: tenantA})); len(none.Items) != 0 {
		t.Fatalf("company filter: %+v", none.Items)
	}
}

func parallelBody(period, passage1100 string) string {
	return fmt.Sprintf(`{"period":%q,"sourceSystem":"AX2012","documentsReplayed":42000,"revenue":"1000000",
		"legacy":[{"area":"TB","key":"USMF|4000|USD","amount":"500000.00"},{"area":"TB","key":"USMF|1100|USD","amount":"120.00"},{"area":"AR","key":"CIV-00123","amount":"99.99"}],
		"passage":[{"area":"TB","key":"USMF|4000|USD","amount":"500000.01"},{"area":"TB","key":"USMF|1100|USD","amount":%q},{"area":"AR","key":"CIV-00123","amount":"99.99"}]}`, period, passage1100)
}

func TestParallelRun_DiffExplainSignOff(t *testing.T) {
	h, admin := dbServer(t)
	const base = "/api/v1/migration/parallel-runs"
	rec := do(t, h, request{method: "POST", path: base, tenant: tenantA, duties: allDuties, user: "alice", body: parallelBody("2026-11", "150.00")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	run := decode[parallelDTO](t, rec)
	if run.State != "REVIEW" || run.UnexplainedCount != 1 || run.UnexplainedAmount != "30.000000" || run.ExpectedByRule["RND-001"] != 1 || run.CanSignOff || !strings.Contains(run.SignOffBlocker, "MIG-1509") {
		t.Fatalf("run %+v", run)
	}
	if len(run.Variances) != 2 {
		t.Fatalf("variances %+v", run.Variances)
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.migration.parallelrun.diffed.v1' and aggregate_id = $1`, run.ParallelRunID); n != 1 {
		t.Fatalf("parallelrun.diffed events = %d", n)
	}

	// Idempotent by (period, system): the same input replays, other input conflicts.
	again := do(t, h, request{method: "POST", path: base, tenant: tenantA, duties: allDuties, user: "alice", body: parallelBody("2026-11", "150.00")})
	if again.Code != http.StatusCreated || again.Header().Get("Idempotent-Replayed") != "true" || decode[parallelDTO](t, again).ParallelRunID != run.ParallelRunID {
		t.Fatalf("replay: %d %q %s", again.Code, again.Header().Get("Idempotent-Replayed"), again.Body)
	}
	if rec := do(t, h, request{method: "POST", path: base, tenant: tenantA, duties: allDuties, user: "alice", body: parallelBody("2026-11", "151.00")}); rec.Code != http.StatusConflict || problemCode(t, rec) != CodeExists {
		t.Fatalf("other input for the period: %d %s", rec.Code, rec.Body)
	}
	if n := count(t, admin, `select count(*) from parallel_run`); n != 1 {
		t.Fatalf("parallel_run rows = %d", n)
	}

	var unexplained string
	for _, v := range run.Variances {
		if v.Class == "UNEXPLAINED" {
			unexplained = v.VarianceID
		}
	}
	explain := base + "/" + run.ParallelRunID + "/variances/" + unexplained + "/explain"
	signOff := base + "/" + run.ParallelRunID + "/sign-off"
	if rec := do(t, h, request{method: "POST", path: explain, tenant: tenantA, duties: allDuties, user: "alice", body: `{"reason":"timing"}`}); rec.Code != http.StatusForbidden || problemCode(t, rec) != CodeSoD {
		t.Fatalf("the starter explains (MIG-001): %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, request{method: "POST", path: signOff, tenant: tenantA, duties: allDuties, user: "bob", body: `{}`}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "MIG-1509") {
		t.Fatalf("sign-off with an unexplained variance: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, request{method: "POST", path: explain, tenant: tenantA, duties: allDuties, user: "bob", body: `{"reason":"timing","version":5}`}); rec.Code != http.StatusConflict || problemCode(t, rec) != CodeVersion {
		t.Fatalf("stale version: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, request{method: "POST", path: explain, tenant: tenantA, duties: allDuties, user: "bob", body: `{"reason":"Invoice 7781 posted after the legacy cut-off","version":1}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("explain: %d %s", rec.Code, rec.Body)
	}
	explained := decode[parallelDTO](t, rec)
	if explained.UnexplainedCount != 0 || explained.ExplainedAmount != "30.000000" || !explained.CanSignOff || explained.Version != 2 {
		t.Fatalf("after explain %+v", explained)
	}
	rec = do(t, h, request{method: "POST", path: explain, tenant: tenantA, duties: allDuties, user: "bob", body: `{"reason":"Invoice 7781 posted after the legacy cut-off"}`})
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("explain again: %d %q", rec.Code, rec.Header().Get("Idempotent-Replayed"))
	}
	if rec := do(t, h, request{method: "POST", path: signOff, tenant: tenantA, duties: "migration.factory.run,migration.factory.view", user: "bob", body: `{}`}); rec.Code != http.StatusForbidden {
		t.Fatalf("sign-off needs migration.factory.review: %d", rec.Code)
	}
	rec = do(t, h, request{method: "POST", path: signOff, tenant: tenantA, duties: allDuties, user: "bob", body: `{"version":2}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("sign-off: %d %s", rec.Code, rec.Body)
	}
	if s := decode[parallelDTO](t, rec); s.State != "SIGNED_OFF" || s.SignedOffBy != "bob" || s.SignedOffAt == nil || s.CanSignOff {
		t.Fatalf("signed off %+v", s)
	}
	if n := count(t, admin, `select count(*) from variance where parallel_run_id = $1 and class = 'EXPLAINED' and explained_by = 'bob'`, run.ParallelRunID); n != 1 {
		t.Fatalf("stored explanation rows = %d", n)
	}

	list := decode[struct{ Items []parallelDTO }](t, do(t, h, request{method: "GET", path: base + "?state=SIGNED_OFF", tenant: tenantA, duties: "migration.factory.view"}))
	if len(list.Items) != 1 || list.Items[0].Variances != nil {
		t.Fatalf("list %+v", list.Items)
	}
	if l := decode[struct{ Items []parallelDTO }](t, do(t, h, request{method: "GET", path: base + "?state=REVIEW", tenant: tenantA, duties: "migration.factory.view"})); len(l.Items) != 0 {
		t.Fatalf("state filter %+v", l.Items)
	}
	if rec := do(t, h, request{method: "GET", path: base + "/" + run.ParallelRunID, tenant: tenantB, duties: "migration.factory.view"}); rec.Code != http.StatusNotFound {
		t.Fatalf("other tenant read: %d", rec.Code)
	}
	if l := decode[struct{ Items []parallelDTO }](t, do(t, h, request{method: "GET", path: base, tenant: tenantB, duties: "migration.factory.view"})); len(l.Items) != 0 {
		t.Fatalf("tenant B lists tenant A runs: %+v", l.Items)
	}
}

// projectBody builds a project_wip extract: PRJ-TM with n chargeable unbilled hour lines
// reconciled to its legacy HOUR totals, and PRJ-FP whose legacy EXPENSE total has no lines.
func projectBody(n int, extra ...string) string {
	var lines []string
	for i := 0; i < n; i++ {
		lines = append(lines, fmt.Sprintf(`{"legacyId":"PT-%03d","category":"HOUR","transactionDate":"2026-12-01","qty":"8","costAmount":"400","salesAmount":"800","currency":"USD","workerId":"W-7","billingStatus":"CHARGEABLE_UNBILLED","wipCost":"400","wipSales":"800"}`, i))
	}
	lines = append(lines, extra...)
	cost, sales := 400*n, 800*n
	return fmt.Sprintf(`{"sourceSystem":"AX2012","sourceCompany":"USMF","cutoffTs":"2026-12-31T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z","projects":[
		{"projectId":"PRJ-TM","legacyTotals":[{"category":"HOUR","cost":"%d","sales":"%d","wipCost":"%d","wipSales":"%d"}],"lines":[%s]},
		{"projectId":"PRJ-FP","legacyTotals":[{"category":"EXPENSE","cost":"250"}],"lines":[]}]}`, cost, sales, cost, sales, strings.Join(lines, ","))
}

const projectRunsPath = "/api/v1/migration/packs/project_wip/runs"

func TestProjectWIP_OpeningLinesReconcilePerProject(t *testing.T) {
	h, admin := dbServer(t)
	rec := do(t, h, request{method: "POST", path: projectRunsPath, tenant: tenantA, duties: allDuties, user: "ops", body: projectBody(120)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body)
	}
	run := decode[runDetailDTO](t, rec)
	if run.Pack != "project_wip" || run.RecordsLoaded != 120 || run.RecordsQuarantined != 0 || run.Status != "LOADED" {
		t.Fatalf("PRJ-FP does not reconcile, so the run stays LOADED: %+v", run.runDTO)
	}
	if n := count(t, admin, `select count(*) from project_opening_line where run_id = $1 and beginning_balance`, run.RunID); n != 120 {
		t.Fatalf("AC-49.11: opening lines = %d", n)
	}
	if n := count(t, admin, `select count(*) from legacy_xref where run_id = $1 and passage_entity = 'project_opening_line'`, run.RunID); n != 120 {
		t.Fatalf("xref rows = %d", n)
	}

	opening := func(project, query string) openingDTO {
		t.Helper()
		rec := do(t, h, request{method: "GET", path: "/api/v1/migration/runs/" + run.RunID + "/projects/" + project + "/opening" + query, tenant: tenantA, duties: "migration.factory.view"})
		if rec.Code != http.StatusOK {
			t.Fatalf("opening %s: %d %s", project, rec.Code, rec.Body)
		}
		return decode[openingDTO](t, rec)
	}
	tm := opening("PRJ-TM", "?limit=50")
	if tm.Status != "RECONCILED" || tm.LineCount != 120 || len(tm.Lines) != 50 || len(tm.Totals) != 4 {
		t.Fatalf("PRJ-TM %+v", tm)
	}
	if l := tm.Lines[0]; l.LegacyID != "PT-000" || !l.BeginningBalance || l.BillingStatus != "CHARGEABLE_UNBILLED" || l.SalesAmount != "800.000000" || l.WorkerID != "W-7" {
		t.Fatalf("line %+v", l)
	}
	next := opening("PRJ-TM", "?limit=50&after="+tm.Lines[49].LegacyID)
	if len(next.Lines) != 50 || next.Lines[0].LegacyID != "PT-050" {
		t.Fatalf("second page starts at %+v", next.Lines[0])
	}
	fp := opening("PRJ-FP", "")
	if fp.Status != "OPENING_MISMATCH" || fp.LineCount != 0 {
		t.Fatalf("BR-49.12: PRJ-FP %+v", fp)
	}
	if rec := do(t, h, request{method: "GET", path: "/api/v1/migration/runs/" + run.RunID + "/projects/PRJ-NONE/opening", tenant: tenantA, duties: "migration.factory.view"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project: %d", rec.Code)
	}
	if rec := do(t, h, request{method: "GET", path: "/api/v1/migration/runs/" + run.RunID + "/projects/PRJ-TM/opening", tenant: tenantB, duties: "migration.factory.view"}); rec.Code != http.StatusNotFound {
		t.Fatalf("other tenant: %d", rec.Code)
	}

	// Re-running the extract loads nothing new (FR-49.10) and reconciles the same way.
	rerun := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: projectRunsPath, tenant: tenantA, duties: allDuties, user: "ops", body: projectBody(120)}))
	if rerun.RecordsLoaded != 0 || rerun.AlreadyPresent != 120 || rerun.Message == "" {
		t.Fatalf("rerun %+v", rerun.runDTO)
	}
	if n := count(t, admin, `select count(*) from project_opening_line`); n != 120 {
		t.Fatalf("opening lines after rerun = %d", n)
	}
}

func TestProjectWIP_BadLineIsQuarantinedAndNotReleasable(t *testing.T) {
	h, _ := dbServer(t)
	onAccount := `{"legacyId":"PT-OA","category":"ON_ACCOUNT","transactionDate":"2026-12-02","costAmount":"0","salesAmount":"1000","currency":"USD","billingStatus":"CHARGEABLE_UNBILLED"}`
	rec := do(t, h, request{method: "POST", path: projectRunsPath, tenant: tenantA, duties: allDuties, user: "ops", body: projectBody(99, onAccount)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body)
	}
	run := decode[runDetailDTO](t, rec)
	if run.RecordsLoaded != 99 || run.RecordsQuarantined != 1 || run.Quarantined[0].LegacyID != "PT-OA" || run.Quarantined[0].Rule != "DOMAIN" {
		t.Fatalf("run %+v quarantine %+v", run.runDTO, run.Quarantined)
	}
	if rec := do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", body: `{}`}); rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != CodeUnsupported {
		t.Fatalf("project lines have no release: %d %s", rec.Code, rec.Body)
	}
	// More than the 1 % quarantine limit stops the load.
	if rec := do(t, h, request{method: "POST", path: projectRunsPath, tenant: tenantA, duties: allDuties, user: "ops", body: projectBody(9, onAccount)}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("10 %% quarantined: %d %s", rec.Code, rec.Body)
	}
}
