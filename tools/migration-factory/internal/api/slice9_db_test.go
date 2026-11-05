package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const onhandPath = "/api/v1/migration/packs/onhand/runs"

// onhandBody builds an onhand extract for company with the given lines and options.
func onhandBody(company, extra string, records ...string) string {
	return fmt.Sprintf(`{"sourceSystem":"AX2012","sourceCompany":%q,"cutoffTs":"2026-12-31T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z",
		"items":["BOLT-10","PUMP-1"]%s,"records":[%s]}`, company, extra, strings.Join(records, ","))
}

func onhandRec(id, item, site, serial, qty, value string) string {
	return fmt.Sprintf(`{"legacyId":%q,"item":%q,"site":%q,"warehouse":"W1","location":"A-01","serial":%q,"unit":"EA","currency":"USD","quantity":%q,"value":%q}`,
		id, item, site, serial, qty, value)
}

var cleanOnhand = []string{
	onhandRec("OH-1", "BOLT-10", "S1", "", "100", "250.50"),
	onhandRec("OH-2", "BOLT-10", "S2", "", "40", "100.25"),
	onhandRec("OH-3", "PUMP-1", "S1", "SN-1", "1", "400"),
}

// FR-49.1, FR-49.2, FR-49.10: an onhand extract loads as opening stock with legacy references,
// reconciles, and a re-run loads nothing new.
func TestOnhand_LoadReconcileAndRerunLoadsNothing(t *testing.T) {
	h, admin := dbServer(t)
	first := do(t, h, request{method: "POST", path: onhandPath, tenant: tenantA, duties: allDuties, body: onhandBody("USMF", "", cleanOnhand...), user: "engineer"})
	if first.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", first.Code, first.Body)
	}
	run := decode[runDetailDTO](t, first)
	if run.Pack != "onhand" || run.Status != "RECONCILED" || run.RecordsLoaded != 3 || run.RecordsQuarantined != 0 {
		t.Fatalf("run %+v", run.runDTO)
	}
	names := map[string]int{}
	for _, c := range run.ControlTotals {
		names[c.Name]++
		if c.Status != "MATCH" {
			t.Fatalf("total %+v not MATCH", c)
		}
	}
	if names["onhand.quantity"] != 3 || names["onhand.value"] != 3 || names["onhand.value_entity"] != 1 {
		t.Fatalf("totals %v", names)
	}
	if n := count(t, admin, `select count(*) from onhand_opening_balance where run_id = $1 and item = 'BOLT-10' and site = 'S2' and quantity = 40 and value = 100.25`, run.RunID); n != 1 {
		t.Fatalf("opening stock rows for BOLT-10/S2 = %d", n)
	}
	if n := count(t, admin, `select count(*) from legacy_xref x join onhand_opening_balance b on b.legacy_xref_id = x.xref_id and b.balance_id = x.passage_id
		where x.source_table = 'onhand' and x.passage_entity = 'onhand_opening_balance'`); n != 3 {
		t.Fatalf("xref rows tied to their opening stock = %d, want 3", n)
	}
	xref := do(t, h, request{method: "GET", path: "/api/v1/migration/xref?system=AX2012&company=USMF&table=onhand&id=OH-2", tenant: tenantA})
	if xref.Code != http.StatusOK || !strings.Contains(xref.Body.String(), "onhand_opening_balance") {
		t.Fatalf("FR-49.3 legacy lookup of a line: %d %s", xref.Code, xref.Body)
	}

	again := do(t, h, request{method: "POST", path: onhandPath, tenant: tenantA, duties: allDuties, body: onhandBody("USMF", "", cleanOnhand...)})
	rerun := decode[runDetailDTO](t, again)
	if again.Code != http.StatusCreated || rerun.RunID == run.RunID || rerun.RecordsLoaded != 0 || rerun.AlreadyPresent != 3 || rerun.Status != "RECONCILED" || rerun.InputHash != run.InputHash {
		t.Fatalf("re-run: %d %+v", again.Code, rerun.runDTO)
	}
	if !strings.Contains(rerun.Message, "Re-run loaded 0 new records; all 3") {
		t.Fatalf("MIG-1505 message %q", rerun.Message)
	}
	if n := count(t, admin, `select count(*) from onhand_opening_balance`); n != 3 {
		t.Fatalf("opening stock after the re-run = %d, want 3", n)
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.migration.pack.reconciled.v1'`); n != 2 {
		t.Fatalf("pack.reconciled events = %d, want one per run", n)
	}

	// The same item and dimension under a new legacy id would open the stock twice: refused, nothing written.
	dup := do(t, h, request{method: "POST", path: onhandPath, tenant: tenantA, duties: allDuties, body: onhandBody("USMF", "",
		onhandRec("OH-9", "BOLT-10", "S1", "", "100", "250.50"), onhandRec("OH-10", "BOLT-10", "S7", "", "1", "1"))})
	if dup.Code != http.StatusUnprocessableEntity || !strings.Contains(dup.Body.String(), "already has opening stock") {
		t.Fatalf("natural key reloaded under a new id: %d %s", dup.Code, dup.Body)
	}
	if n := count(t, admin, `select count(*) from pack_run where pack = 'onhand'`); n != 2 {
		t.Fatalf("the refused run must not be stored: %d runs", n)
	}

	// Another tenant sees none of it and may load the same extract for itself.
	if r := do(t, h, request{method: "GET", path: "/api/v1/migration/runs/" + run.RunID, tenant: tenantB, duties: "migration.factory.view"}); r.Code != http.StatusNotFound {
		t.Fatalf("other tenant reads the run: %d", r.Code)
	}
	if r := do(t, h, request{method: "POST", path: onhandPath, tenant: tenantB, duties: allDuties, body: onhandBody("USMF", "", cleanOnhand...)}); r.Code != http.StatusCreated || decode[runDetailDTO](t, r).RecordsLoaded != 3 {
		t.Fatalf("tenant B loads its own stock: %d %s", r.Code, r.Body)
	}
}

// FR-49.11: failing lines are quarantined with their rule, the rest load and the run stays LOADED
// with the held-back quantity visible on its totals.
func TestOnhand_QuarantineKeepsRunLoadedAndShowsOnTotals(t *testing.T) {
	h, _ := dbServer(t)
	lines := []string{
		onhandRec("OH-1", "BOLT-10", "S1", "", "100", "250.50"), onhandRec("OH-2", "NOPE", "S1", "", "5", "5"),
		onhandRec("OH-3", "PUMP-1", "S1", "SN-1", "2", "800"), onhandRec("OH-4", "BOLT-10", "S2", "", "-3", "1"),
	}
	for i := 0; i < 36; i++ { // 3 of 40 lines are quarantined, under the 10 % limit
		lines = append(lines, onhandRec(fmt.Sprintf("OH-F%d", i), "BOLT-10", fmt.Sprintf("F%d", i), "", "1", "1"))
	}
	body := onhandBody("USMF", `,"quarantineMaxPct":"10"`, lines...)
	rec := do(t, h, request{method: "POST", path: onhandPath, tenant: tenantA, duties: allDuties, body: body})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	run := decode[runDetailDTO](t, rec)
	if run.Status != "LOADED" || run.RecordsLoaded != 37 || run.RecordsQuarantined != 3 {
		t.Fatalf("run %+v", run.runDTO)
	}
	rules := map[string]string{}
	for _, q := range run.Quarantined {
		rules[q.LegacyID] = q.Rule
	}
	if rules["OH-2"] != "REFERENTIAL" || rules["OH-3"] != "DOMAIN" || rules["OH-4"] != "DOMAIN" {
		t.Fatalf("quarantine %+v", run.Quarantined)
	}
	if q := run.Quarantined[0]; q.Account != "NOPE" || q.Dimensions != "S1/W1/A-01//" || q.Amount != "5.000000" {
		t.Fatalf("quarantined line shape %+v", q)
	}
	mismatched := map[string]bool{}
	for _, c := range run.ControlTotals {
		if c.Status == "MISMATCH" {
			mismatched[c.Name] = true
		}
	}
	if !mismatched["onhand.quantity"] || !mismatched["onhand.value_entity"] {
		t.Fatalf("held-back lines must show on quantity and entity value: %+v", run.ControlTotals)
	}
	rel := do(t, h, request{method: "POST", path: "/api/v1/migration/runs/" + run.RunID + "/quarantine/1/release", tenant: tenantA, duties: allDuties, body: `{"account":"BOLT-10"}`})
	if rel.Code != http.StatusUnprocessableEntity || problemCode(t, rel) != CodeInvalid {
		t.Fatalf("an open_items field on an onhand line is refused (slice 13 releases by item and dimension): %d %s", rel.Code, rel.Body)
	}
}

// Invalid requests are refused before anything is stored.
func TestOnhand_RefusedRequests(t *testing.T) {
	h, admin := dbServer(t)
	cases := []struct{ name, body, want string }{
		{"quantity not a decimal", onhandBody("USMF", "", onhandRec("OH-1", "BOLT-10", "S1", "", "1e3", "1")), "quantity"},
		{"value with seven decimals", onhandBody("USMF", "", onhandRec("OH-1", "BOLT-10", "S1", "", "1", "1.1234567")), "value"},
		{"no item master", strings.Replace(onhandBody("USMF", "", cleanOnhand...), `"items":["BOLT-10","PUMP-1"],`, "", 1), "items must list"},
		{"glRunId without account", onhandBody("USMF", `,"glRunId":"00000000-0000-7000-8000-0000000000ff"`, cleanOnhand...), "go together"},
		{"account without glRunId", onhandBody("USMF", `,"inventoryAccount":"1400"`, cleanOnhand...), "go together"},
		{"glRunId not a UUID", onhandBody("USMF", `,"glRunId":"run-1","inventoryAccount":"1400"`, cleanOnhand...), "must be a UUID"},
		{"unknown gl run", onhandBody("USMF", `,"glRunId":"00000000-0000-7000-8000-0000000000ff","inventoryAccount":"1400"`, cleanOnhand...), "not a pack run of this tenant"},
		{"cutoff after freeze", strings.Replace(onhandBody("USMF", "", cleanOnhand...), "2026-12-31T00:00:00Z", "2027-01-01T00:00:00Z", 1), "MIG-1501"},
		{"no records", onhandBody("USMF", ""), "at least one line"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := do(t, h, request{method: "POST", path: onhandPath, tenant: tenantA, duties: allDuties, body: c.body})
			if (r.Code != http.StatusUnprocessableEntity && r.Code != http.StatusBadRequest) || !strings.Contains(r.Body.String(), c.want) {
				t.Fatalf("%d %s", r.Code, r.Body)
			}
		})
	}
	if n := count(t, admin, `select count(*) from pack_run`); n != 0 {
		t.Fatalf("refused requests stored %d runs", n)
	}
}

// BR-49.3: the migrated balance of the inventory GL account must equal the loaded on-hand value
// to 0.00 before the onhand run reconciles.
func TestOnhand_InventoryAccountReconcilesWithGLRun(t *testing.T) {
	h, admin := dbServer(t)
	// On-hand value is 750.75 USD. The GL run holds the inventory account 1100 at that amount.
	glOK := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody("USMF", "",
		glRec("GL-1", "1100", "USMF-100", "500.00"), glRec("GL-2", "1100", "USMF-200", "250.75"), glRec("GL-3", "3000", "", "-750.75"))}))
	glOff := decode[runDetailDTO](t, do(t, h, request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody("DEMF", "",
		glRec("GL-1", "1100", "", "750.74"), glRec("GL-3", "3000", "", "-750.74"))}))
	if glOK.Status != "RECONCILED" || glOff.Status != "RECONCILED" {
		t.Fatalf("gl runs %s %s", glOK.Status, glOff.Status)
	}

	match := do(t, h, request{method: "POST", path: onhandPath, tenant: tenantA, duties: allDuties,
		body: onhandBody("USMF", `,"glRunId":"`+glOK.RunID+`","inventoryAccount":"1100"`, cleanOnhand...)})
	if match.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", match.Code, match.Body)
	}
	ok := decode[runDetailDTO](t, match)
	var account *totalDTO
	for i, c := range ok.ControlTotals {
		if c.Name == "onhand.inventory_account" {
			account = &ok.ControlTotals[i]
		}
	}
	if account == nil || account.Status != "MATCH" || account.Key["account"] != "1100" || account.Source != "750.750000" || account.Target != "750.750000" {
		t.Fatalf("inventory account total %+v", account)
	}
	if ok.Status != "RECONCILED" || ok.GLRunID != glOK.RunID || len(ok.MismatchMessages) != 0 {
		t.Fatalf("run %+v", ok)
	}
	if n := count(t, admin, `select count(*) from pack_run where run_id = $1 and gl_run_id = $2`, ok.RunID, glOK.RunID); n != 1 {
		t.Fatal("the gl_balances run reconciled with is stored on the onhand run")
	}

	off := do(t, h, request{method: "POST", path: onhandPath, tenant: tenantA, duties: allDuties,
		body: onhandBody("DEMF", `,"glRunId":"`+glOff.RunID+`","inventoryAccount":"1100"`, cleanOnhand...)})
	bad := decode[runDetailDTO](t, off)
	if off.Code != http.StatusCreated || bad.Status != "LOADED" || len(bad.MismatchMessages) != 1 ||
		!strings.Contains(bad.MismatchMessages[0], "MIG-1502 Inventory account 1100 migrated balance (750.74 USD) differs from loaded on-hand value (750.75 USD)") {
		t.Fatalf("a cent between the GL account and the stock keeps the run LOADED with a message: %d %+v", off.Code, bad)
	}

	refused := []struct{ name, run, company, want string }{
		{"another company", glOff.RunID, "USMF", "is for AX2012 DEMF"},
		{"not a gl_balances run", ok.RunID, "USMF", "is a onhand run, not gl_balances"},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			r := do(t, h, request{method: "POST", path: onhandPath, tenant: tenantA, duties: allDuties,
				body: onhandBody(c.company, `,"glRunId":"`+c.run+`","inventoryAccount":"1100"`, onhandRec("OH-X", "BOLT-10", "SZ", "", "1", "1"))})
			if r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), c.want) {
				t.Fatalf("%d %s", r.Code, r.Body)
			}
		})
	}
	if r := do(t, h, request{method: "POST", path: onhandPath, tenant: tenantB, duties: allDuties,
		body: onhandBody("USMF", `,"glRunId":"`+glOK.RunID+`","inventoryAccount":"1100"`, cleanOnhand...)}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("another tenant's gl_balances run cannot be named: %d %s", r.Code, r.Body)
	}
}

// An Idempotency-Key replays the first run instead of starting a second.
func TestOnhand_IdempotencyKeyReplays(t *testing.T) {
	h, admin := dbServer(t)
	rq := request{method: "POST", path: onhandPath, tenant: tenantA, duties: allDuties, body: onhandBody("USMF", "", cleanOnhand...), key: "oh-usmf-1"}
	first, second := do(t, h, rq), do(t, h, rq)
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated || second.Header().Get("Idempotent-Replayed") != "true" ||
		decode[runDetailDTO](t, first).RunID != decode[runDetailDTO](t, second).RunID {
		t.Fatalf("replay: %d %d %q", first.Code, second.Code, second.Header().Get("Idempotent-Replayed"))
	}
	if n := count(t, admin, `select count(*) from pack_run`); n != 1 {
		t.Fatalf("runs = %d, want 1", n)
	}
	rq.body = onhandBody("DEMF", "", cleanOnhand...)
	if r := do(t, h, rq); r.Code != http.StatusUnprocessableEntity || problemCode(t, r) != CodeIdempotencyReuse {
		t.Fatalf("key reused with another body: %d %s", r.Code, r.Body)
	}
}

// A serial is one unit: once Passage holds it for the company, a later extract that has moved it
// to another site under a new legacy id is refused whole, and the database refuses it even past
// the check. The same serial text on another item, or in another company, is a different unit.
func TestOnhand_SerialAlreadyHeldElsewhereIsRefused(t *testing.T) {
	h, admin := dbServer(t)
	if r := do(t, h, request{method: "POST", path: onhandPath, tenant: tenantA, duties: allDuties, body: onhandBody("USMF", "", cleanOnhand...)}); r.Code != http.StatusCreated {
		t.Fatalf("first load: %d %s", r.Code, r.Body)
	}
	moved := do(t, h, request{method: "POST", path: onhandPath, tenant: tenantA, duties: allDuties, body: onhandBody("USMF", "",
		onhandRec("OH-20", "PUMP-1", "S9", "SN-1", "1", "400"), onhandRec("OH-21", "BOLT-10", "S9", "", "1", "1"))})
	if moved.Code != http.StatusUnprocessableEntity || !strings.Contains(moved.Body.String(), `serial \"SN-1\" of item PUMP-1 already has opening stock at S1/W1/A-01`) {
		t.Fatalf("serial reopened at another site: %d %s", moved.Code, moved.Body)
	}
	if n := count(t, admin, `select count(*) from onhand_opening_balance`); n != 3 {
		t.Fatalf("the refused run wrote stock: %d rows", n)
	}
	if r := do(t, h, request{method: "POST", path: onhandPath, tenant: tenantA, duties: allDuties, body: onhandBody("USMF", "",
		onhandRec("OH-22", "BOLT-10", "S9", "SN-1", "1", "2"))}); r.Code != http.StatusCreated {
		t.Fatalf("the same serial text on another item: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, request{method: "POST", path: onhandPath, tenant: tenantA, duties: allDuties, body: onhandBody("DEMF", "",
		onhandRec("OH-20", "PUMP-1", "S9", "SN-1", "1", "400"))}); r.Code != http.StatusCreated {
		t.Fatalf("the same serial in another company: %d %s", r.Code, r.Body)
	}
	_, err := admin.Exec(context.Background(), `with b as (select * from onhand_opening_balance where legacy_id = 'OH-3'),
		x as (insert into legacy_xref (tenant_id, xref_id, source_system, source_company, source_table, source_id, passage_entity, passage_id, run_id, amount, currency)
			select tenant_id, gen_random_uuid(), source_system, source_company, 'onhand', 'OH-99', 'onhand_opening_balance', gen_random_uuid(), run_id, 1, currency from b
			returning tenant_id, xref_id, passage_id)
		insert into onhand_opening_balance (tenant_id, balance_id, run_id, legacy_xref_id, source_system, source_company,
			legacy_id, item, site, warehouse, serial, unit, quantity, value, currency)
		select b.tenant_id, x.passage_id, b.run_id, x.xref_id, b.source_system, b.source_company, 'OH-99', b.item, 'S8', b.warehouse, b.serial, b.unit, 1, 1, b.currency
		from b join x on x.tenant_id = b.tenant_id`)
	if err == nil || !strings.Contains(err.Error(), "onhand_opening_balance_serial") {
		t.Fatalf("the database must hold a serial once per item and company: %v", err)
	}
}
