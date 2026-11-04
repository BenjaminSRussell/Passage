package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Database tests for slice 13: releasing quarantined bank_balances and onhand lines (S11-a). They
// skip without PASSAGE_TEST_DATABASE_URL and run as passage_app when PASSAGE_TEST_APP_DATABASE_URL is set.

// bankFiller adds n clean lines on accounts F0..F(n-1) so a single quarantined line stays under the
// 10 % limit; the returned master lists them with the accounts bankBody already knows.
func bankFiller(n int) (lines []string, extra string) {
	var accts []string
	for i := 0; i < n; i++ {
		lines = append(lines, bankRec(fmt.Sprintf("BK-F%d", i), fmt.Sprintf("F%d", i), "USD", "1"))
		accts = append(accts, fmt.Sprintf("%q", fmt.Sprintf("F%d", i)))
	}
	return lines, strings.Join(accts, ",")
}

func bankBodyWith(extra string, records ...string) string {
	return strings.Replace(bankBody("1000", `,"quarantineMaxPct":"10"`, records...), `"bankAccounts":[`, `"bankAccounts":[`+extra+`,`, 1)
}

func TestBankBalances_ReleaseCorrectsAccountAndReconciles(t *testing.T) {
	h, admin := dbServer(t)
	lines := []string{bankRec("BK-1", "OPER-USD", "USD", "100.50"), bankRec("BK-2", "TYPO-USD", "USD", "-20.25"), bankRec("BK-3", "PAY-EUR", "EUR", "900")}
	filler, accts := bankFiller(10)
	body := bankBodyWith(accts, append(lines, filler...)...)
	run := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: bankPath, tenant: tenantA, duties: allDuties, body: body}))
	if run.Status != "LOADED" || run.RecordsQuarantined != 1 || run.Quarantined[0].Account != "TYPO-USD" {
		t.Fatalf("run %+v %+v", run.runDTO, run.Quarantined)
	}
	if n := count(t, admin, `select count(*) from pack_quarantine where run_id = $1 and line is not null`, run.RunID); n != 1 {
		t.Fatalf("quarantine rows holding the whole line = %d", n)
	}

	// An account outside the master still fails, naming the rule; a stale version is refused.
	rec := do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", body: `{"bankAccount":"NOPE"}`})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "REFERENTIAL") {
		t.Fatalf("uncorrected release: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", body: `{"bankAccount":"SAV-USD","version":9}`}); rec.Code != http.StatusConflict {
		t.Fatalf("stale version: %d %s", rec.Code, rec.Body)
	}
	// A field of another pack is refused.
	if rec := do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", body: `{"bankAccount":"SAV-USD","item":"X"}`}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("onhand field on a bank run: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", key: "bank-rel", body: `{"bankAccount":"SAV-USD","version":1}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("release: %d %s", rec.Code, rec.Body)
	}
	after := decode[runDetailDTO](t, rec)
	if after.Status != "RECONCILED" || after.RecordsLoaded != 13 || after.RecordsQuarantined != 0 || after.Version != 2 {
		t.Fatalf("after release %+v", after.runDTO)
	}
	for _, c := range after.ControlTotals {
		if c.Status != "MATCH" {
			t.Fatalf("total %+v after the release", c)
		}
	}
	if r := after.Quarantined[0]; r.Status != "RELEASED" || r.ReleasedBy != "ops" || r.Released == nil || r.Released.Account != "SAV-USD" {
		t.Fatalf("released row %+v", r)
	}
	if n := count(t, admin, `select count(*) from bank_opening_balance b join legacy_xref x on x.xref_id = b.legacy_xref_id and x.passage_id = b.balance_id
		where b.run_id = $1 and b.legacy_id = 'BK-2' and b.bank_account = 'SAV-USD' and b.balance = -20.25 and x.source_table = 'bank_balances'`, run.RunID); n != 1 {
		t.Fatalf("released balance rows tied to their cross-reference = %d", n)
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.migration.pack.reconciled.v1' and aggregate_id = $1`, run.RunID); n != 2 {
		t.Fatalf("pack.reconciled events = %d, want the run's and the release's", n)
	}
	// The release is idempotent by record, and a re-run of the same extract finds the balance held.
	if rec := do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", key: "again", body: `{"bankAccount":"SAV-USD"}`}); rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("re-release: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantB, duties: allDuties, user: "ops", body: `{}`}); rec.Code != http.StatusNotFound {
		t.Fatalf("other tenant: %d", rec.Code)
	}
	rerun := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: bankPath, tenant: tenantA, duties: allDuties, body: strings.Replace(body, `"TYPO-USD"`, `"SAV-USD"`, 1)}))
	if rerun.RecordsLoaded != 0 || rerun.AlreadyPresent != 13 {
		t.Fatalf("re-run with the corrected extract %+v", rerun.runDTO)
	}
}

func TestBankBalances_ReleaseRefusesAnAccountAlreadyHeld(t *testing.T) {
	h, admin := dbServer(t)
	filler, accts := bankFiller(10)
	run := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: bankPath, tenant: tenantA, duties: allDuties, user: "e",
		body: bankBodyWith(accts, append([]string{bankRec("BK-1", "OPER-USD", "USD", "10"), bankRec("BK-2", "OPER-USD", "USD", "5"), bankRec("BK-3", "PAY-EUR", "EUR", "1")}, filler...)...)}))
	if run.RecordsQuarantined != 1 || run.Quarantined[0].LegacyID != "BK-2" || run.Quarantined[0].Rule != "UNIQUENESS" {
		t.Fatalf("quarantine %+v", run.Quarantined)
	}
	// Released as it is, BK-2 would open OPER-USD a second time.
	rec := do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", body: `{}`})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "already has an opening balance") {
		t.Fatalf("duplicate account: %d %s", rec.Code, rec.Body)
	}
	if n := count(t, admin, `select count(*) from bank_opening_balance where run_id = $1`, run.RunID); n != 12 {
		t.Fatalf("balances after the refused release = %d", n)
	}
	// Moved to the savings account it is a different account, and the run reconciles.
	rec = do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", body: `{"bankAccount":"SAV-USD"}`})
	if rec.Code != http.StatusOK || decode[runDetailDTO](t, rec).Status != "RECONCILED" {
		t.Fatalf("corrected release: %d %s", rec.Code, rec.Body)
	}
}

func TestOnhand_ReleaseCorrectsItemAndReconcilesWithTheGLAccount(t *testing.T) {
	h, admin := dbServer(t)
	gl := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties,
		body: glBody("USMF", "", glRec("GL-1", "1100", "", "500.00"), glRec("GL-2", "3000", "", "-500.00"))}))
	if gl.Status != "RECONCILED" {
		t.Fatalf("gl run %+v", gl.runDTO)
	}
	lines := []string{
		onhandRec("OH-1", "BOLT-10", "S1", "", "100", "250.00"),
		onhandRec("OH-2", "BOLT10", "S1", "", "40", "100.00"), // item typo
		onhandRec("OH-3", "PUMP-1", "S1", "SN-1", "1", "150.00"),
	}
	lines = append(lines, onhandFiller(10)...)
	run := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: onhandPath, tenant: tenantA, duties: allDuties,
		body: onhandBody("USMF", `,"quarantineMaxPct":"10","glRunId":"`+gl.RunID+`","inventoryAccount":"1100"`, lines...)}))
	if run.Status != "LOADED" || run.RecordsQuarantined != 1 {
		t.Fatalf("run %+v %+v", run.runDTO, run.Quarantined)
	}
	rec := do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", body: `{"item":"NOPE"}`})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "REFERENTIAL") {
		t.Fatalf("uncorrected release: %d %s", rec.Code, rec.Body)
	}
	// The stored item master is the run's snapshot: the typo is corrected to an item on it, with a new site.
	rec = do(t, h, request{method: "POST", path: releasePath(run.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", body: `{"item":"BOLT-10","site":"S2","version":1}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("release: %d %s", rec.Code, rec.Body)
	}
	after := decode[runDetailDTO](t, rec)
	if after.Status != "RECONCILED" || after.RecordsLoaded != 13 || after.RecordsQuarantined != 0 {
		t.Fatalf("after %+v", after.runDTO)
	}
	acct := 0
	for _, c := range after.ControlTotals {
		if c.Status != "MATCH" {
			t.Fatalf("total %+v after the release", c)
		}
		if c.Name == "onhand.inventory_account" {
			acct++
			if c.Source != "500.000000" || c.Target != "500.000000" {
				t.Fatalf("inventory account total %+v", c)
			}
		}
	}
	if acct != 1 {
		t.Fatalf("inventory account totals = %d", acct)
	}
	if n := count(t, admin, `select count(*) from onhand_opening_balance b join legacy_xref x on x.xref_id = b.legacy_xref_id and x.passage_id = b.balance_id
		where b.run_id = $1 and b.legacy_id = 'OH-2' and b.item = 'BOLT-10' and b.site = 'S2' and b.quantity = 40 and b.value = 100 and x.amount = 100`, run.RunID); n != 1 {
		t.Fatalf("released stock rows = %d", n)
	}
	// A line already held at the corrected dimension under another legacy id is refused.
	run2 := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: onhandPath, tenant: tenantA, duties: allDuties,
		body: onhandBody("USMF", `,"quarantineMaxPct":"10"`, append([]string{onhandRec("OH-1", "BOLT-10", "S1", "", "100", "250.00"), onhandRec("OH-9", "NOPE", "S1", "", "3", "3")}, onhandFiller(10)...)...)}))
	if run2.RecordsQuarantined != 1 {
		t.Fatalf("second run %+v", run2.runDTO)
	}
	rec = do(t, h, request{method: "POST", path: releasePath(run2.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", body: `{"item":"BOLT-10"}`})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "already has opening stock") {
		t.Fatalf("stock opened twice: %d %s", rec.Code, rec.Body)
	}
	// A quarantine row stored before its whole line was kept cannot be released.
	if _, err := admin.Exec(t.Context(), `update pack_quarantine set line = null where run_id = $1`, run2.RunID); err != nil {
		t.Fatal(err)
	}
	rec = do(t, h, request{method: "POST", path: releasePath(run2.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", body: `{"item":"PUMP-1"}`})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "re-extract") {
		t.Fatalf("row without its line: %d %s", rec.Code, rec.Body)
	}
	// Other packs have no release.
	if rec := do(t, h, request{method: "POST", path: releasePath(gl.RunID, 1), tenant: tenantA, duties: allDuties, user: "ops", body: `{}`}); rec.Code == http.StatusOK {
		t.Fatalf("gl release: %d %s", rec.Code, rec.Body)
	}
}

// onhandFiller returns n clean lines at their own sites, with a value that keeps the GL account
// reconciled: each is worth zero.
func onhandFiller(n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, onhandRec(fmt.Sprintf("OH-F%d", i), "PUMP-1", fmt.Sprintf("F%d", i), "", "1", "0"))
	}
	return out
}
