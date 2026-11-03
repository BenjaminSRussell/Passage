package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const bankPath = "/api/v1/migration/packs/bank_balances/runs"

func bankBody(company, extra string, records ...string) string {
	return fmt.Sprintf(`{"sourceSystem":"SAPECC","sourceCompany":%q,"cutoffTs":"2026-12-31T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z",
		"bankAccounts":["OPER-USD","PAY-EUR","SAV-USD"]%s,"records":[%s]}`, company, extra, strings.Join(records, ","))
}

func bankRec(id, account, cur, bal string) string {
	return fmt.Sprintf(`{"legacyId":%q,"bankAccount":%q,"currency":%q,"balance":%q}`, id, account, cur, bal)
}

var cleanBank = []string{bankRec("BK-1", "OPER-USD", "USD", "-1250.50"), bankRec("BK-2", "PAY-EUR", "EUR", "900"), bankRec("BK-3", "SAV-USD", "USD", "50.25")}

// FR-49.1, FR-49.2, FR-49.10: a bank_balances extract loads as opening balances with legacy
// references, reconciles, and a re-run loads nothing new.
func TestBankBalances_LoadReconcileAndRerunLoadsNothing(t *testing.T) {
	h, admin := dbServer(t)
	first := do(t, h, request{method: "POST", path: bankPath, tenant: tenantA, duties: allDuties, body: bankBody("1000", "", cleanBank...), user: "engineer"})
	if first.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", first.Code, first.Body)
	}
	run := decode[runDetailDTO](t, first)
	if run.Pack != "bank_balances" || run.Status != "RECONCILED" || run.RecordsLoaded != 3 || run.RecordsQuarantined != 0 {
		t.Fatalf("run %+v", run.runDTO)
	}
	names := map[string]int{}
	for _, c := range run.ControlTotals {
		names[c.Name]++
		if c.Status != "MATCH" {
			t.Fatalf("total %+v not MATCH", c)
		}
	}
	if names["bank_balances.balance"] != 3 || names["bank_balances.balance_entity"] != 2 || names["bank_balances.count"] != 1 {
		t.Fatalf("totals %v", names)
	}
	if n := count(t, admin, `select count(*) from bank_opening_balance where run_id = $1 and bank_account = 'OPER-USD' and balance = -1250.50`, run.RunID); n != 1 {
		t.Fatalf("overdrawn account row = %d", n)
	}
	if n := count(t, admin, `select count(*) from legacy_xref x join bank_opening_balance b on b.legacy_xref_id = x.xref_id and b.balance_id = x.passage_id
		where x.source_table = 'bank_balances' and x.passage_entity = 'bank_opening_balance'`); n != 3 {
		t.Fatalf("xref rows tied to their balance = %d, want 3", n)
	}
	again := do(t, h, request{method: "POST", path: bankPath, tenant: tenantA, duties: allDuties, body: bankBody("1000", "", cleanBank...)})
	rerun := decode[runDetailDTO](t, again)
	if again.Code != http.StatusCreated || rerun.RecordsLoaded != 0 || rerun.AlreadyPresent != 3 || rerun.Status != "RECONCILED" || rerun.InputHash != run.InputHash {
		t.Fatalf("re-run: %d %+v", again.Code, rerun.runDTO)
	}
	if n := count(t, admin, `select count(*) from bank_opening_balance`); n != 3 {
		t.Fatalf("balances after the re-run = %d, want 3", n)
	}
	// The same bank account under a new legacy id would open it twice: refused, nothing written.
	dup := do(t, h, request{method: "POST", path: bankPath, tenant: tenantA, duties: allDuties, body: bankBody("1000", "", bankRec("BK-9", "OPER-USD", "USD", "1"))})
	if dup.Code != http.StatusUnprocessableEntity || !strings.Contains(dup.Body.String(), "already has an opening balance") {
		t.Fatalf("account reloaded under a new id: %d %s", dup.Code, dup.Body)
	}
	if n := count(t, admin, `select count(*) from pack_run where pack = 'bank_balances'`); n != 2 {
		t.Fatalf("the refused run must not be stored: %d runs", n)
	}
	// Another tenant sees none of it and may load the same extract.
	if r := do(t, h, request{method: "GET", path: "/api/v1/migration/runs/" + run.RunID, tenant: tenantB, duties: "MigrationView"}); r.Code != http.StatusNotFound {
		t.Fatalf("other tenant reads the run: %d", r.Code)
	}
	if r := do(t, h, request{method: "POST", path: bankPath, tenant: tenantB, duties: allDuties, body: bankBody("1000", "", cleanBank...)}); r.Code != http.StatusCreated || decode[runDetailDTO](t, r).RecordsLoaded != 3 {
		t.Fatalf("tenant B loads its own balances: %d %s", r.Code, r.Body)
	}
}

// FR-49.11: failing lines are quarantined with their rule, the rest load and the run stays LOADED.
func TestBankBalances_QuarantineKeepsRunLoadedAndRequestIsChecked(t *testing.T) {
	h, _ := dbServer(t)
	lines := []string{bankRec("BK-1", "OPER-USD", "USD", "10"), bankRec("BK-2", "GONE", "USD", "5"), bankRec("BK-3", "PAY-EUR", "EU", "5"),
		bankRec("BK-4", "SAV-USD", "USD", "10.255")} // a fraction of a cent
	accounts := `"OPER-USD","PAY-EUR","SAV-USD"`
	for i := 0; i < 32; i++ { // 3 of 36 lines are quarantined, under the 10 % limit
		lines = append(lines, bankRec(fmt.Sprintf("BK-F%d", i), fmt.Sprintf("ACC-%d", i), "USD", "1"))
		accounts += fmt.Sprintf(`,"ACC-%d"`, i)
	}
	body := strings.Replace(bankBody("1000", `,"quarantineMaxPct":"10"`, lines...), `"OPER-USD","PAY-EUR","SAV-USD"`, accounts, 1)
	rec := do(t, h, request{method: "POST", path: bankPath, tenant: tenantA, duties: allDuties, body: body})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	run := decode[runDetailDTO](t, rec)
	rules := map[string]string{}
	for _, q := range run.Quarantined {
		rules[q.LegacyID] = q.Rule
	}
	if run.Status != "LOADED" || run.RecordsLoaded != 33 || rules["BK-2"] != "REFERENTIAL" || rules["BK-3"] != "DOMAIN" || rules["BK-4"] != "DOMAIN" {
		t.Fatalf("run %+v %v", run.runDTO, rules)
	}
	for name, b := range map[string]string{
		"no accounts": `{"sourceSystem":"AX2012","sourceCompany":"X","cutoffTs":"2026-12-31T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z","bankAccounts":[],"records":[` + bankRec("1", "A", "USD", "1") + `]}`,
		"bad balance": bankBody("1000", "", bankRec("1", "OPER-USD", "USD", "1.0000001")),
		"cutoff late": `{"sourceSystem":"AX2012","sourceCompany":"X","cutoffTs":"2026-12-31T09:00:00Z","freezeTs":"2026-12-31T06:00:00Z","bankAccounts":["A"],"records":[` + bankRec("1", "A", "USD", "1") + `]}`,
	} {
		if r := do(t, h, request{method: "POST", path: bankPath, tenant: tenantA, duties: allDuties, body: b}); r.Code < 400 || r.Code >= 500 {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}
	if r := do(t, h, request{method: "POST", path: bankPath, tenant: tenantA, duties: "MigrationView", body: bankBody("1000", "", cleanBank...)}); r.Code != http.StatusForbidden {
		t.Errorf("without MigrationRun: %d", r.Code)
	}
}
