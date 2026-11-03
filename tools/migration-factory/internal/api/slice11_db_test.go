package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const ordersPath = "/api/v1/migration/packs/open_orders/runs"

func ordersBody(extra string, records ...string) string {
	return fmt.Sprintf(`{"sourceSystem":"AX2012","sourceCompany":"USMF","cutoffTs":"2026-12-31T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z",
		"items":["BOLT","NUT"],"customers":["C1","C2"],"vendors":["V1"]%s,"records":[%s]}`, extra, strings.Join(records, ","))
}

func orderRec(id, typ, order, line, item, party, ordered, delivered, price string) string {
	return fmt.Sprintf(`{"legacyId":%q,"orderType":%q,"orderNumber":%q,"lineNumber":%q,"item":%q,"party":%q,"unit":"EA","currency":"USD",
		"orderedQty":%q,"deliveredQty":%q,"unitPrice":%q,"deliveryDate":"2027-01-15"}`, id, typ, order, line, item, party, ordered, delivered, price)
}

var cleanOrders = []string{
	orderRec("SL-1", "SALES", "SO-1", "1", "BOLT", "C1", "10", "4", "2.5"),
	orderRec("SL-2", "SALES", "SO-1", "2", "NUT", "C1", "4", "0", "1.333333"),
	orderRec("PL-1", "PURCHASE", "PO-1", "1", "BOLT", "V1", "7", "0", "3"),
}

// FR-49.1, FR-49.2, FR-49.10: an open_orders extract loads as opening order lines with legacy
// references, reconciles, and a re-run loads nothing new.
func TestOpenOrders_LoadReconcileAndRerunLoadsNothing(t *testing.T) {
	h, admin := dbServer(t)
	first := do(t, h, request{method: "POST", path: ordersPath, tenant: tenantA, duties: allDuties, body: ordersBody("", cleanOrders...), user: "engineer"})
	if first.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", first.Code, first.Body)
	}
	run := decode[runDetailDTO](t, first)
	if run.Pack != "open_orders" || run.Status != "RECONCILED" || run.RecordsLoaded != 3 || run.RecordsQuarantined != 0 {
		t.Fatalf("run %+v", run.runDTO)
	}
	names := map[string]int{}
	for _, c := range run.ControlTotals {
		names[c.Name]++
		if c.Status != "MATCH" {
			t.Fatalf("total %+v not MATCH", c)
		}
	}
	if names["open_orders.open_quantity"] != 3 || names["open_orders.open_value"] != 2 || names["open_orders.line_count"] != 2 || names["open_orders.order_count"] != 2 {
		t.Fatalf("totals %v", names)
	}
	if n := count(t, admin, `select count(*) from open_order_opening_line where run_id = $1 and order_number = 'SO-1' and line_number = '1' and ordered_qty = 10 and delivered_qty = 4
		and delivery_date = '2027-01-15'`, run.RunID); n != 1 {
		t.Fatalf("opening line = %d", n)
	}
	if n := count(t, admin, `select count(*) from legacy_xref x join open_order_opening_line l on l.legacy_xref_id = x.xref_id and l.line_id = x.passage_id
		where x.source_table in ('open_orders.sales', 'open_orders.purchase') and x.passage_entity = 'open_order_opening_line'`); n != 3 {
		t.Fatalf("xref rows tied to their line = %d, want 3", n)
	}
	again := do(t, h, request{method: "POST", path: ordersPath, tenant: tenantA, duties: allDuties, body: ordersBody("", cleanOrders...)})
	rerun := decode[runDetailDTO](t, again)
	if again.Code != http.StatusCreated || rerun.RecordsLoaded != 0 || rerun.AlreadyPresent != 3 || rerun.Status != "RECONCILED" || rerun.InputHash != run.InputHash {
		t.Fatalf("re-run: %d %+v", again.Code, rerun.runDTO)
	}
	if n := count(t, admin, `select count(*) from open_order_opening_line`); n != 3 {
		t.Fatalf("lines after the re-run = %d, want 3", n)
	}
	// The same order line under a new legacy id would open it twice: refused, nothing written.
	dup := do(t, h, request{method: "POST", path: ordersPath, tenant: tenantA, duties: allDuties, body: ordersBody("", orderRec("SL-9", "SALES", "SO-1", "1", "BOLT", "C1", "1", "0", "1"))})
	if dup.Code != http.StatusUnprocessableEntity || !strings.Contains(dup.Body.String(), "already has an opening line") {
		t.Fatalf("order line reloaded under a new id: %d %s", dup.Code, dup.Body)
	}
	if n := count(t, admin, `select count(*) from pack_run where pack = 'open_orders' and tenant_id = $1`, tenantA); n != 2 {
		t.Fatalf("the refused run must not be stored: %d runs", n)
	}
	// A purchase order line with the legacy id and order number of a held sales line is another
	// legacy table's row: it loads under open_orders.purchase, and a re-run holds both.
	twin := orderRec("SL-1", "PURCHASE", "SO-1", "1", "BOLT", "V1", "5", "0", "2")
	if r := do(t, h, request{method: "POST", path: ordersPath, tenant: tenantA, duties: allDuties, body: ordersBody("", append([]string{twin}, cleanOrders...)...)}); r.Code != http.StatusCreated {
		t.Fatalf("purchase twin: %d %s", r.Code, r.Body)
	} else if twinRun := decode[runDetailDTO](t, r); twinRun.RecordsLoaded != 1 || twinRun.AlreadyPresent != 3 || twinRun.Status != "RECONCILED" {
		t.Fatalf("purchase twin run %+v", twinRun.runDTO)
	}
	if n := count(t, admin, `select count(*) from legacy_xref where source_id = 'SL-1' and source_table in ('open_orders.sales', 'open_orders.purchase')`); n != 2 {
		t.Fatalf("xref rows for SL-1 = %d, want one per order type", n)
	}
	// Another tenant sees none of it and may load the same extract.
	if r := do(t, h, request{method: "GET", path: "/api/v1/migration/runs/" + run.RunID, tenant: tenantB, duties: "MigrationView"}); r.Code != http.StatusNotFound {
		t.Fatalf("other tenant reads the run: %d", r.Code)
	}
	if r := do(t, h, request{method: "POST", path: ordersPath, tenant: tenantB, duties: allDuties, body: ordersBody("", cleanOrders...)}); r.Code != http.StatusCreated || decode[runDetailDTO](t, r).RecordsLoaded != 3 {
		t.Fatalf("tenant B loads its own lines: %d %s", r.Code, r.Body)
	}
}

// FR-49.11: failing lines are quarantined with their rule, the rest load and the run stays LOADED.
func TestOpenOrders_QuarantineKeepsRunLoadedAndRequestIsChecked(t *testing.T) {
	h, _ := dbServer(t)
	lines := []string{
		orderRec("SL-1", "SALES", "SO-1", "1", "BOLT", "C1", "10", "4", "2.5"),
		orderRec("SL-2", "SALES", "SO-2", "1", "GONE", "C1", "1", "0", "1"),   // item not in the master
		orderRec("SL-3", "SALES", "SO-3", "1", "NUT", "C1", "2", "2", "1"),    // fully delivered
		orderRec("PL-1", "PURCHASE", "PO-1", "1", "NUT", "C1", "2", "0", "1"), // a customer on a purchase order
	}
	for i := 0; i < 27; i++ { // enough clean lines to keep the quarantine share under the 10% limit
		lines = append(lines, orderRec(fmt.Sprintf("SL-C%d", i), "SALES", fmt.Sprintf("SO-C%d", i), "1", "NUT", "C2", "3", "1", "1"))
	}
	r := do(t, h, request{method: "POST", path: ordersPath, tenant: tenantA, duties: allDuties, body: ordersBody(`,"quarantineMaxPct":"10"`, lines...)})
	if r.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", r.Code, r.Body)
	}
	run := decode[runDetailDTO](t, r)
	if run.Status != "LOADED" || run.RecordsLoaded != 28 || run.RecordsQuarantined != 3 {
		t.Fatalf("run %+v", run.runDTO)
	}
	if !strings.Contains(r.Body.String(), "REFERENTIAL") || !strings.Contains(r.Body.String(), "is not open") {
		t.Fatalf("quarantine rules missing: %s", r.Body)
	}
	for name, body := range map[string]string{
		"no items":         strings.Replace(ordersBody("", cleanOrders...), `"items":["BOLT","NUT"],`, "", 1),
		"no customers":     strings.Replace(ordersBody("", cleanOrders...), `"customers":["C1","C2"],`, "", 1),
		"no vendors":       strings.Replace(ordersBody("", cleanOrders...), `"vendors":["V1"]`, `"vendors":[]`, 1),
		"no records":       ordersBody(""),
		"bad quantity":     ordersBody("", strings.Replace(cleanOrders[0], `"orderedQty":"10"`, `"orderedQty":"ten"`, 1)),
		"too many decimal": ordersBody("", strings.Replace(cleanOrders[0], `"unitPrice":"2.5"`, `"unitPrice":"2.1234567"`, 1)),
		"no freeze":        strings.Replace(ordersBody("", cleanOrders...), `"freezeTs":"2026-12-31T06:00:00Z"`, `"freezeTs":"2026-12-30T06:00:00Z"`, 1),
	} {
		if r := do(t, h, request{method: "POST", path: ordersPath, tenant: tenantA, duties: allDuties, body: body}); r.Code != http.StatusUnprocessableEntity && r.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d %s", name, r.Code, r.Body)
		}
	}
	if r := do(t, h, request{method: "POST", path: ordersPath, tenant: tenantA, duties: "MigrationView", body: ordersBody("", cleanOrders...)}); r.Code != http.StatusForbidden {
		t.Errorf("without the run duty: %d", r.Code)
	}
}
