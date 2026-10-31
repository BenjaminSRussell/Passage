package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const glPath = "/api/v1/migration/packs/gl_balances/runs"

// glBody builds a gl_balances extract for company with the given balance records and options.
func glBody(company, extra string, records ...string) string {
	return fmt.Sprintf(`{"sourceSystem":"AX2012","sourceCompany":%q,"cutoffTs":"2026-12-31T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z",
		"accounts":["1100","2100","3000"]%s,"records":[%s]}`, company, extra, strings.Join(records, ","))
}

func glRec(id, account, dims, balance string) string {
	return fmt.Sprintf(`{"legacyId":%q,"account":%q,"dimensions":%q,"currency":"USD","balance":%q}`, id, account, dims, balance)
}

var cleanGL = []string{
	glRec("GL-1", "1100", "USMF-100", "800.00"),
	glRec("GL-2", "1100", "USMF-200", "450.00"),
	glRec("GL-3", "3000", "", "-1250.00"),
}

// FR-49.1, FR-49.2, FR-49.10, AC-49.4: a balanced GL extract loads as opening balances with their
// legacy references, reconciles, and a re-run loads nothing new.
func TestGLBalances_LoadReconcileAndRerunLoadsNothing(t *testing.T) {
	h, admin := dbServer(t)
	first := do(t, h, request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody("USMF", "", cleanGL...), user: "engineer"})
	if first.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", first.Code, first.Body)
	}
	run := decode[runDetailDTO](t, first)
	if run.Pack != "gl_balances" || run.Status != "RECONCILED" || run.RecordsLoaded != 3 || run.RecordsQuarantined != 0 {
		t.Fatalf("run %+v", run.runDTO)
	}
	names := map[string]int{}
	for _, c := range run.ControlTotals {
		names[c.Name]++
		if c.Status != "MATCH" {
			t.Fatalf("total %+v not MATCH", c)
		}
	}
	if names["gl_balances.balance"] != 3 || names["gl_balances.nonzero_count"] != 1 || names["gl_balances.trial_balance"] != 1 {
		t.Fatalf("totals %v", names)
	}
	if n := count(t, admin, `select count(*) from gl_opening_balance where run_id = $1 and account = '1100' and dimensions = 'USMF-200' and balance = 450`, run.RunID); n != 1 {
		t.Fatalf("opening balance rows for 1100/USMF-200 = %d", n)
	}
	if n := count(t, admin, `select count(*) from legacy_xref x join gl_opening_balance b on b.legacy_xref_id = x.xref_id and b.balance_id = x.passage_id
		where x.source_table = 'gl_balances' and x.passage_entity = 'gl_opening_balance'`); n != 3 {
		t.Fatalf("xref rows tied to their opening balances = %d, want 3", n)
	}
	xref := do(t, h, request{method: "GET", path: "/api/v1/migration/xref?system=AX2012&company=USMF&table=gl_balances&id=GL-2", tenant: tenantA})
	if xref.Code != http.StatusOK || !strings.Contains(xref.Body.String(), "gl_opening_balance") {
		t.Fatalf("FR-49.3 legacy lookup of a balance: %d %s", xref.Code, xref.Body)
	}

	again := do(t, h, request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody("USMF", "", cleanGL...)})
	rerun := decode[runDetailDTO](t, again)
	if again.Code != http.StatusCreated || rerun.RunID == run.RunID || rerun.RecordsLoaded != 0 || rerun.AlreadyPresent != 3 || rerun.Status != "RECONCILED" || rerun.InputHash != run.InputHash {
		t.Fatalf("AC-49.4 re-run: %d %+v", again.Code, rerun.runDTO)
	}
	if !strings.Contains(rerun.Message, "Re-run loaded 0 new records; all 3") {
		t.Fatalf("MIG-1505 message %q", rerun.Message)
	}
	if n := count(t, admin, `select count(*) from gl_opening_balance`); n != 3 {
		t.Fatalf("opening balances after the re-run = %d, want 3", n)
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.migration.pack.reconciled.v1'`); n != 2 {
		t.Fatalf("pack.reconciled events = %d, want one per run", n)
	}

	// A later extract that names a held account, dimensions and currency under a new legacy id
	// would load the balance twice; it is refused and writes nothing.
	dup := do(t, h, request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody("USMF", "",
		glRec("GL-9", "1100", "USMF-100", "800.00"), glRec("GL-10", "3000", "X", "-800.00"))})
	if dup.Code != http.StatusUnprocessableEntity || !strings.Contains(dup.Body.String(), "already has an opening balance") {
		t.Fatalf("natural key reloaded under a new id: %d %s", dup.Code, dup.Body)
	}
	if n := count(t, admin, `select count(*) from pack_run where pack = 'gl_balances'`); n != 2 {
		t.Fatalf("the refused run must not be stored: %d runs", n)
	}

	// Another tenant sees none of it and may load the same extract for itself.
	if r := do(t, h, request{method: "GET", path: "/api/v1/migration/runs/" + run.RunID, tenant: tenantB, duties: "MigrationView"}); r.Code != http.StatusNotFound {
		t.Fatalf("other tenant reads the run: %d", r.Code)
	}
	if r := do(t, h, request{method: "POST", path: glPath, tenant: tenantB, duties: allDuties, body: glBody("USMF", "", cleanGL...)}); r.Code != http.StatusCreated || decode[runDetailDTO](t, r).RecordsLoaded != 3 {
		t.Fatalf("tenant B loads its own balances: %d %s", r.Code, r.Body)
	}
}

// FR-49.11: an unbalanced extract is refused before anything is stored; a balance on an account
// missing from the chart is quarantined, the rest load and the run stays LOADED.
func TestGLBalances_UnbalancedRefusedAndUnknownAccountQuarantined(t *testing.T) {
	h, admin := dbServer(t)
	unbalanced := do(t, h, request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody("USMF", "",
		glRec("GL-1", "1100", "", "100.00"), glRec("GL-2", "3000", "", "-99.99"))})
	if unbalanced.Code != http.StatusUnprocessableEntity || !strings.Contains(unbalanced.Body.String(), "not balanced in USD") {
		t.Fatalf("unbalanced extract: %d %s", unbalanced.Code, unbalanced.Body)
	}
	if n := count(t, admin, `select count(*) from pack_run`); n != 0 {
		t.Fatalf("a refused extract stores nothing: %d runs", n)
	}

	body := glBody("USMF", `,"quarantineMaxPct":"10"`,
		glRec("GL-1", "1100", "USMF-100", "500.00"), glRec("GL-2", "2100", "", "-300.00"), glRec("GL-3", "9999", "USMF-900", "-200.00"),
		glRec("GL-4", "3000", "", "0"), glRec("GL-5", "3000", "A", "0"), glRec("GL-6", "3000", "B", "0"), glRec("GL-7", "3000", "C", "0"),
		glRec("GL-8", "3000", "D", "0"), glRec("GL-9", "3000", "E", "0"), glRec("GL-10", "3000", "F", "0"))
	rec := do(t, h, request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: body})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	run := decode[runDetailDTO](t, rec)
	if run.Status != "LOADED" || run.RecordsLoaded != 9 || run.RecordsQuarantined != 1 {
		t.Fatalf("run %+v", run.runDTO)
	}
	if len(run.Quarantined) != 1 || run.Quarantined[0].Rule != "REFERENTIAL" || run.Quarantined[0].LegacyID != "GL-3" ||
		run.Quarantined[0].Dimensions != "USMF-900" || run.Quarantined[0].Amount != "-200.000000" {
		t.Fatalf("quarantine %+v", run.Quarantined)
	}
	mismatched := map[string]bool{}
	for _, c := range run.ControlTotals {
		if c.Status == "MISMATCH" {
			mismatched[c.Name] = true
		}
	}
	if !mismatched["gl_balances.balance"] || !mismatched["gl_balances.trial_balance"] {
		t.Fatalf("the held-back balance must show on its total and the trial balance: %+v", run.ControlTotals)
	}
	stored := decode[runDetailDTO](t, do(t, h, request{method: "GET", path: "/api/v1/migration/runs/" + run.RunID, tenant: tenantA, duties: "MigrationView"}))
	if len(stored.Quarantined) != 1 || stored.Quarantined[0].Dimensions != "USMF-900" || len(stored.ControlTotals) != len(run.ControlTotals) {
		t.Fatalf("stored run %+v", stored)
	}
	rel := do(t, h, request{method: "POST", path: "/api/v1/migration/runs/" + run.RunID + "/quarantine/1/release", tenant: tenantA, duties: allDuties, body: `{"account":"2100"}`})
	if rel.Code != http.StatusUnprocessableEntity || problemCode(t, rel) != CodeUnsupported {
		t.Fatalf("gl_balances has no record release yet: %d %s", rel.Code, rel.Body)
	}
}

// BR-49.2: the open items per control account must equal the GL balance migrated for that
// account to 0.00 before the GL run reconciles.
func TestGLBalances_ControlAccountsReconcileWithOpenItems(t *testing.T) {
	h, admin := dbServer(t)
	// Four payables open items of 10.25 on control account 2100 (41.00), for USMF and for DEMF.
	usmf := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: openItemsBody(4)}))
	demf := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: strings.Replace(openItemsBody(4), `"USMF"`, `"DEMF"`, 1)}))
	if usmf.Status != "RECONCILED" || demf.Status != "RECONCILED" {
		t.Fatalf("open items runs %s %s", usmf.Status, demf.Status)
	}

	match := do(t, h, request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody("USMF", `,"openItemsRunId":"`+usmf.RunID+`"`,
		glRec("GL-1", "2100", "USMF-100", "30.00"), glRec("GL-2", "2100", "USMF-200", "11.00"), glRec("GL-3", "3000", "", "-41.00"))})
	if match.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", match.Code, match.Body)
	}
	ok := decode[runDetailDTO](t, match)
	var control *totalDTO
	for i, c := range ok.ControlTotals {
		if c.Name == "gl_balances.control_account" {
			control = &ok.ControlTotals[i]
		}
	}
	if control == nil || control.Status != "MATCH" || control.Key["account"] != "2100" || control.Source != "41.000000" || control.Target != "41.000000" {
		t.Fatalf("control account total %+v", control)
	}
	if ok.Status != "RECONCILED" || ok.OpenItemsRunID != usmf.RunID || len(ok.MismatchMessages) != 0 {
		t.Fatalf("run %+v", ok)
	}
	if n := count(t, admin, `select count(*) from pack_run where run_id = $1 and open_items_run_id = $2`, ok.RunID, usmf.RunID); n != 1 {
		t.Fatal("the open_items run reconciled with is stored on the GL run")
	}

	off := do(t, h, request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody("DEMF", `,"openItemsRunId":"`+demf.RunID+`"`,
		glRec("GL-1", "2100", "", "40.99"), glRec("GL-2", "3000", "", "-40.99"))})
	bad := decode[runDetailDTO](t, off)
	if off.Code != http.StatusCreated || bad.Status != "LOADED" || len(bad.MismatchMessages) != 1 ||
		!strings.Contains(bad.MismatchMessages[0], "MIG-1506 Open items for control account 2100 (41.00 USD) differ from migrated GL balance (40.99 USD)") {
		t.Fatalf("a cent between open items and GL keeps the run LOADED with MIG-1506: %d %+v", off.Code, bad)
	}

	refused := []struct{ name, run, company, want string }{
		{"another company", demf.RunID, "USMF", "is for AX2012 DEMF"},
		{"not an open_items run", ok.RunID, "USMF", "is a gl_balances run"},
		{"unknown run", "00000000-0000-7000-8000-0000000000ff", "USMF", "not a pack run of this tenant"},
		{"not a UUID", "run-1", "USMF", "must be a UUID"},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			r := do(t, h, request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody(c.company, `,"openItemsRunId":"`+c.run+`"`,
				glRec("GL-X", "1100", "Z", "1"), glRec("GL-Y", "3000", "Z", "-1"))})
			if r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), c.want) {
				t.Fatalf("%d %s", r.Code, r.Body)
			}
		})
	}
	if r := do(t, h, request{method: "POST", path: glPath, tenant: tenantB, duties: allDuties, body: glBody("USMF", `,"openItemsRunId":"`+usmf.RunID+`"`, cleanGL...)}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("another tenant's open_items run cannot be named: %d %s", r.Code, r.Body)
	}
}

// An Idempotency-Key replays the first run instead of starting a second.
func TestGLBalances_IdempotencyKeyReplays(t *testing.T) {
	h, admin := dbServer(t)
	rq := request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody("USMF", "", cleanGL...), key: "gl-usmf-1"}
	first, second := do(t, h, rq), do(t, h, rq)
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated || second.Header().Get("Idempotent-Replayed") != "true" ||
		decode[runDetailDTO](t, first).RunID != decode[runDetailDTO](t, second).RunID {
		t.Fatalf("replay: %d %d %q", first.Code, second.Code, second.Header().Get("Idempotent-Replayed"))
	}
	if n := count(t, admin, `select count(*) from pack_run`); n != 1 {
		t.Fatalf("runs = %d, want 1", n)
	}
	rq.body = glBody("DEMF", "", cleanGL...)
	if r := do(t, h, rq); r.Code != http.StatusUnprocessableEntity || problemCode(t, r) != CodeIdempotencyReuse {
		t.Fatalf("key reused with another body: %d %s", r.Code, r.Body)
	}
}
